package serve

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Detached collector jobs decouple a client connection from collector
// execution: a client starts a job, disconnects, and any later connection
// fetches the result by id. Jobs run the same registered RPC handlers as
// direct calls, so method-level policy (such as confirm_sensitive gating on
// artifact captures) applies identically. Job state is in-memory: it survives
// reconnects but not a daemon restart.

const (
	jobStateRunning = "running"
	jobStateDone    = "done"
	jobStateFailed  = "failed"

	defaultJobRetention = time.Hour
	defaultJobCapacity  = 200
	maxJobWait          = 60 * time.Second
)

// JobRecord is the full job view returned by job.get.
type JobRecord struct {
	ID         string          `json:"id"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params,omitempty"`
	State      string          `json:"state"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Result     any             `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// JobSummary is the compact job view returned by job.list.
type JobSummary struct {
	ID         string     `json:"id"`
	Method     string     `json:"method"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type jobRecord struct {
	JobRecord
	done chan struct{}
}

type jobDispatchFunc func(method string, params json.RawMessage) (any, error)

type jobManager struct {
	mu        sync.Mutex
	jobs      map[string]*jobRecord
	seq       uint64
	dispatch  jobDispatchFunc
	retention time.Duration
	capacity  int
	now       func() time.Time
}

func newJobManager(dispatch jobDispatchFunc) *jobManager {
	return &jobManager{
		jobs:      make(map[string]*jobRecord),
		dispatch:  dispatch,
		retention: defaultJobRetention,
		capacity:  defaultJobCapacity,
		now:       time.Now,
	}
}

// Start launches method with params in the background and returns the job id.
func (m *jobManager) Start(method string, params json.RawMessage) (string, error) {
	method = strings.TrimSpace(method)
	if method == "" {
		return "", fmt.Errorf("job.start: method is required")
	}
	if strings.HasPrefix(method, "job.") {
		return "", fmt.Errorf("job.start: cannot run %s as a job", method)
	}

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("job.start: id generation: %w", err)
	}

	m.mu.Lock()
	m.pruneLocked()
	if m.runningCountLocked() >= m.capacity {
		m.mu.Unlock()
		return "", fmt.Errorf("job.start: %d jobs already running", m.capacity)
	}
	m.seq++
	id := fmt.Sprintf("job-%d-%s", m.seq, hex.EncodeToString(suffix[:]))
	rec := &jobRecord{
		JobRecord: JobRecord{
			ID:        id,
			Method:    method,
			Params:    params,
			State:     jobStateRunning,
			StartedAt: m.now().UTC(),
		},
		done: make(chan struct{}),
	}
	m.jobs[id] = rec
	m.mu.Unlock()

	go m.run(rec)
	return id, nil
}

func (m *jobManager) run(rec *jobRecord) {
	defer func() {
		if r := recover(); r != nil {
			m.finish(rec, nil, fmt.Errorf("handler panic: %v", r))
		}
	}()
	result, err := m.dispatch(rec.Method, rec.Params)
	m.finish(rec, result, err)
}

func (m *jobManager) finish(rec *jobRecord, result any, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec.State != jobStateRunning {
		return
	}
	at := m.now().UTC()
	rec.FinishedAt = &at
	if err != nil {
		rec.State = jobStateFailed
		rec.Error = err.Error()
	} else {
		rec.State = jobStateDone
		rec.Result = result
	}
	close(rec.done)
}

// Get returns a copy of the job record. With wait > 0 it blocks until the job
// finishes or the wait elapses, whichever is first, then returns the current
// state either way.
func (m *jobManager) Get(id string, wait time.Duration) (JobRecord, bool) {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return JobRecord{}, false
	}
	if wait > 0 {
		if wait > maxJobWait {
			wait = maxJobWait
		}
		select {
		case <-rec.done:
		case <-time.After(wait):
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return rec.JobRecord, true
}

// List returns job summaries, newest first, pruning expired finished jobs.
func (m *jobManager) List() []JobSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	out := make([]JobSummary, 0, len(m.jobs))
	for _, rec := range m.jobs {
		out = append(out, JobSummary{
			ID:         rec.ID,
			Method:     rec.Method,
			State:      rec.State,
			StartedAt:  rec.StartedAt,
			FinishedAt: rec.FinishedAt,
			Error:      rec.Error,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

func (m *jobManager) runningCountLocked() int {
	n := 0
	for _, rec := range m.jobs {
		if rec.State == jobStateRunning {
			n++
		}
	}
	return n
}

// pruneLocked drops finished jobs past retention, and if the map still
// exceeds capacity, the oldest finished jobs. Running jobs are never dropped.
func (m *jobManager) pruneLocked() {
	cutoff := m.now().UTC().Add(-m.retention)
	for id, rec := range m.jobs {
		if rec.State != jobStateRunning && rec.FinishedAt != nil && rec.FinishedAt.Before(cutoff) {
			delete(m.jobs, id)
		}
	}
	if len(m.jobs) <= m.capacity {
		return
	}
	finished := make([]*jobRecord, 0, len(m.jobs))
	for _, rec := range m.jobs {
		if rec.State != jobStateRunning {
			finished = append(finished, rec)
		}
	}
	sort.Slice(finished, func(i, j int) bool {
		return finished[i].StartedAt.Before(finished[j].StartedAt)
	})
	for _, rec := range finished {
		if len(m.jobs) <= m.capacity {
			return
		}
		delete(m.jobs, rec.ID)
	}
}
