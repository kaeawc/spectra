package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// jobStatus values are the MCP Tasks extension's task statuses, so a job
// maps onto a task unchanged once the SDK supports the extension.
type jobStatus string

const (
	jobWorking   jobStatus = "working"
	jobCompleted jobStatus = "completed"
	jobFailed    jobStatus = "failed"
	jobCancelled jobStatus = "cancelled"
	// jobInputRequired is part of the Tasks status set; no wrapped tool asks
	// for input, so no job enters it.
	jobInputRequired jobStatus = "input_required"
)

var jobStatuses = []jobStatus{jobWorking, jobInputRequired, jobCompleted, jobFailed, jobCancelled}

// Job store defaults.
const (
	defaultMaxLiveJobs       = 32
	defaultMaxConcurrentJobs = 2
	defaultMaxFinishedJobs   = 32
	defaultJobTTL            = 15 * time.Minute
	defaultJobPollInterval   = time.Second
	defaultMaxJobResult      = 4 << 20
	defaultMaxRetainedResult = 32 << 20
	defaultJobShutdownWait   = 5 * time.Second
	defaultMaxJobArgs        = 1 << 20
)

var errUnknownJob = errors.New("unknown job_id (jobs are visible only to the session that started them, and expire after their ttl)")

// jobConfig bounds the job store. Zero fields take the defaults.
type jobConfig struct {
	Clock Clock
	// ProgressClock times the progress a job's tool reports (wall time even
	// when Clock is fixed); nil is the system clock.
	ProgressClock Clock
	// MaxLive bounds jobs that are working (queued or running); job_start
	// refuses a job beyond it.
	MaxLive int
	// MaxConcurrent bounds jobs running at once; the rest wait in start order.
	MaxConcurrent int
	// MaxFinished bounds finished jobs kept for job_result; the oldest go first.
	MaxFinished int
	// TTL is how long a finished job stays retrievable.
	TTL          time.Duration
	PollInterval time.Duration
	// MaxResultBytes caps one job's result text; a larger result fails the job.
	MaxResultBytes int
	// MaxRetainedBytes caps the result text kept across finished jobs.
	MaxRetainedBytes int
	// ShutdownWait bounds how long close waits for cancelled jobs to return.
	ShutdownWait time.Duration
	// MaxArgsBytes caps a job's raw arguments, which it holds until it runs.
	MaxArgsBytes int
}

func (c jobConfig) withDefaults(clock Clock) jobConfig {
	if c.Clock == nil {
		c.Clock = clock
	}
	if c.Clock == nil {
		c.Clock = systemClock{}
	}
	if c.ProgressClock == nil {
		c.ProgressClock = systemClock{}
	}
	setDefault(&c.MaxLive, defaultMaxLiveJobs)
	setDefault(&c.MaxConcurrent, defaultMaxConcurrentJobs)
	setDefault(&c.MaxFinished, defaultMaxFinishedJobs)
	setDefault(&c.TTL, defaultJobTTL)
	setDefault(&c.PollInterval, defaultJobPollInterval)
	setDefault(&c.MaxResultBytes, defaultMaxJobResult)
	setDefault(&c.MaxRetainedBytes, defaultMaxRetainedResult)
	setDefault(&c.ShutdownWait, defaultJobShutdownWait)
	setDefault(&c.MaxArgsBytes, defaultMaxJobArgs)
	return c
}

func setDefault[T int | time.Duration](v *T, def T) {
	if *v <= 0 {
		*v = def
	}
}

// jobProgress is the latest progress the wrapped tool reported.
type jobProgress struct {
	Progress float64 `json:"progress"`
	Total    float64 `json:"total,omitempty"`
	Message  string  `json:"message,omitempty"`
}

// jobRecord is a job's public state. Field names follow the Tasks
// extension's task object (taskId becomes jobId); ttl and pollInterval are
// milliseconds.
type jobRecord struct {
	JobID         string       `json:"jobId"`
	Tool          string       `json:"tool"`
	Operation     string       `json:"operation,omitempty"`
	Status        jobStatus    `json:"status"`
	StatusMessage string       `json:"statusMessage,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	LastUpdatedAt time.Time    `json:"lastUpdatedAt"`
	TTL           int64        `json:"ttl"`
	PollInterval  int64        `json:"pollInterval"`
	ExpiresAt     *time.Time   `json:"expiresAt,omitempty"`
	Progress      *jobProgress `json:"progress,omitempty"`
	// ResultAvailable is true once job_result returns the tool's result.
	ResultAvailable bool `json:"resultAvailable"`
}

type jobList struct {
	Jobs []jobRecord `json:"jobs"`
}

// job is one wrapped tool call. Fields below mu's comment are guarded by the
// store's mutex.
type job struct {
	id, tool, op string
	owner        any
	args         json.RawMessage
	run          toolHandler
	ctx          context.Context
	cancel       context.CancelFunc

	status   jobStatus
	msg      string
	created  time.Time
	updated  time.Time
	finished time.Time
	progress *jobProgress
	result   *ToolResult
	size     int
}

// jobStore runs wrapped tool calls in the background, outside the call slot,
// for the sessions that started them.
type jobStore struct {
	cfg  jobConfig
	base context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu      sync.Mutex
	jobs    map[string]*job
	running int
	queue   []*job
	closed  bool
}

func newJobStore(cfg jobConfig, clock Clock) *jobStore {
	cfg = cfg.withDefaults(clock)
	base, stop := context.WithCancel(context.Background())
	return &jobStore{cfg: cfg, base: base, stop: stop, jobs: map[string]*job{}}
}

func (st *jobStore) now() time.Time { return st.cfg.Clock.Now().UTC() }

func newJobID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("job id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// start registers a job and runs it in the background; it returns at once.
func (st *jobStore) start(owner any, tool, op string, args json.RawMessage, run toolHandler) (jobRecord, error) {
	return st.startCtx(context.Background(), owner, tool, op, args, run)
}

// startCtx is start for a request: a request already cancelled registers
// nothing. Once registered, the job runs under the store's context, not ctx.
func (st *jobStore) startCtx(ctx context.Context, owner any, tool, op string, args json.RawMessage, run toolHandler) (jobRecord, error) {
	if len(args) > st.cfg.MaxArgsBytes {
		return jobRecord{}, fmt.Errorf("arguments are %d bytes, over the %d-byte job limit", len(args), st.cfg.MaxArgsBytes)
	}
	id, err := newJobID()
	if err != nil {
		return jobRecord{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return jobRecord{}, err
	}
	if st.closed {
		return jobRecord{}, errors.New("the server is shutting down")
	}
	now := st.now()
	st.sweepLocked(now)
	if live := st.liveLocked(); live >= st.cfg.MaxLive {
		return jobRecord{}, fmt.Errorf("%d jobs are already working (the limit); wait for one to finish or cancel one", live)
	}
	jctx, cancel := context.WithCancel(st.base)
	j := &job{id: id, tool: tool, op: op, owner: owner, args: args, run: run, ctx: jctx, cancel: cancel,
		status: jobWorking, created: now, updated: now}
	st.jobs[id] = j
	if st.running < st.cfg.MaxConcurrent {
		st.launchLocked(j)
	} else {
		st.queue = append(st.queue, j)
		j.msg = fmt.Sprintf("queued behind %d job(s); at most %d run at once", st.running+len(st.queue)-1, st.cfg.MaxConcurrent)
	}
	return st.recordLocked(j), nil
}

// launchLocked hands the job's handler and arguments to its worker and drops
// them from the job, so a finished job retains only its result.
func (st *jobStore) launchLocked(j *job) {
	st.running++
	j.msg = "running"
	run, args := j.run, j.args
	releaseLocked(j)
	st.wg.Add(1)
	go st.execute(j, run, args)
}

// releaseLocked drops what only a job that has yet to run needs.
func releaseLocked(j *job) { j.args, j.run = nil, nil }

// launchNextLocked starts the oldest queued job. cancelJob removes a
// cancelled job from the queue, so every queued job is still working.
func (st *jobStore) launchNextLocked() {
	for len(st.queue) > 0 && st.running < st.cfg.MaxConcurrent && !st.closed {
		j := st.queue[0]
		st.queue[0] = nil
		st.queue = st.queue[1:]
		st.launchLocked(j)
	}
}

// dequeueLocked removes j from the queue, keeping the rest in order, and
// clears the vacated slot so the backing array does not retain it.
func (st *jobStore) dequeueLocked(j *job) {
	for i, q := range st.queue {
		if q == j {
			copy(st.queue[i:], st.queue[i+1:])
			st.queue[len(st.queue)-1] = nil
			st.queue = st.queue[:len(st.queue)-1]
			return
		}
	}
}

func (st *jobStore) liveLocked() int {
	n := 0
	for _, j := range st.jobs {
		if j.status == jobWorking {
			n++
		}
	}
	return n
}

func (st *jobStore) execute(j *job, run toolHandler, args json.RawMessage) {
	defer st.wg.Done()
	defer j.cancel()
	res := st.call(j.ctx, j, run, args)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.running--
	st.finishLocked(j, res)
	st.launchNextLocked()
}

// call runs the wrapped handler with a progress reporter that records into
// the job instead of notifying the client.
func (st *jobStore) call(ctx context.Context, j *job, run toolHandler, args json.RawMessage) (res ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			res = toolError(fmt.Sprintf("%s panicked: %v", j.tool, r))
		}
	}()
	pr := &progressReporter{token: j.id, now: st.cfg.ProgressClock.Now, every: defaultProgressEvery,
		send: func(_ context.Context, p *sdk.ProgressNotificationParams) error {
			st.update(j, func() { j.progress = &jobProgress{Progress: p.Progress, Total: p.Total, Message: p.Message} })
			return nil
		}}
	return run(withProgress(ctx, pr), args)
}

// update applies fn to a working job and stamps lastUpdatedAt.
func (st *jobStore) update(j *job, fn func()) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if j.status != jobWorking {
		return
	}
	fn()
	j.updated = st.now()
}

func resultSize(res ToolResult) int {
	n := 0
	for _, c := range res.Content {
		n += len(c.Text)
	}
	return n
}

// finishLocked moves a job to its terminal status. A job already cancelled
// keeps that status and drops whatever its handler returned.
func (st *jobStore) finishLocked(j *job, res ToolResult) {
	if j.status != jobWorking {
		return
	}
	now := st.now()
	j.updated, j.finished = now, now
	size := resultSize(res)
	switch {
	case j.ctx.Err() != nil:
		j.status, j.msg = jobCancelled, "cancelled: the server shut down"
	case size > st.cfg.MaxResultBytes:
		j.status = jobFailed
		j.msg = fmt.Sprintf("result is %d bytes, over the %d-byte job limit; narrow the arguments or call %s directly", size, st.cfg.MaxResultBytes, j.tool)
	case res.IsError:
		j.status, j.msg, j.result, j.size = jobFailed, "the tool returned an error; job_result has it", &res, size
	default:
		j.status, j.msg, j.result, j.size = jobCompleted, "", &res, size
	}
	st.sweepLocked(now)
}

// sweepLocked drops finished jobs past their ttl, then the oldest finished
// jobs while too many are kept or their results hold too many bytes.
func (st *jobStore) sweepLocked(now time.Time) {
	var finished []*job
	bytes := 0
	for id, j := range st.jobs {
		if j.status == jobWorking {
			continue
		}
		if now.Sub(j.finished) >= st.cfg.TTL {
			delete(st.jobs, id)
			continue
		}
		finished = append(finished, j)
		bytes += j.size
	}
	sort.Slice(finished, func(a, b int) bool { return finished[a].finished.Before(finished[b].finished) })
	for len(finished) > 0 && (len(finished) > st.cfg.MaxFinished || bytes > st.cfg.MaxRetainedBytes) {
		bytes -= finished[0].size
		delete(st.jobs, finished[0].id)
		finished = finished[1:]
	}
}

func (st *jobStore) recordLocked(j *job) jobRecord {
	r := jobRecord{
		JobID: j.id, Tool: j.tool, Operation: j.op, Status: j.status, StatusMessage: j.msg,
		CreatedAt: j.created, LastUpdatedAt: j.updated,
		TTL: st.cfg.TTL.Milliseconds(), PollInterval: st.cfg.PollInterval.Milliseconds(),
		ResultAvailable: j.result != nil,
	}
	if j.progress != nil {
		p := *j.progress
		r.Progress = &p
	}
	if j.status != jobWorking {
		exp := j.finished.Add(st.cfg.TTL)
		r.ExpiresAt = &exp
	}
	return r
}

// lookupLocked returns owner's job id; another session's job is unknown.
func (st *jobStore) lookupLocked(owner any, id string) (*job, error) {
	st.sweepLocked(st.now())
	j, ok := st.jobs[id]
	if !ok || j.owner != owner {
		return nil, errUnknownJob
	}
	return j, nil
}

func (st *jobStore) status(owner any, id string) (jobRecord, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, err := st.lookupLocked(owner, id)
	if err != nil {
		return jobRecord{}, err
	}
	return st.recordLocked(j), nil
}

// result returns the wrapped tool's result once the job has one.
func (st *jobStore) result(owner any, id string) (ToolResult, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, err := st.lookupLocked(owner, id)
	if err != nil {
		return ToolResult{}, err
	}
	switch {
	case j.result != nil:
		return *j.result, nil
	case j.status == jobWorking:
		return ToolResult{}, fmt.Errorf("job %s is still working; poll job_status (every %s)", id, st.cfg.PollInterval)
	default:
		return ToolResult{}, fmt.Errorf("job %s is %s with no result: %s", id, j.status, j.msg)
	}
}

// cancelJob stops a working job: it is cancelled at once and its context
// ends; a queued job never runs.
func (st *jobStore) cancelJob(owner any, id string) (jobRecord, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, err := st.lookupLocked(owner, id)
	if err != nil {
		return jobRecord{}, err
	}
	if j.status != jobWorking {
		return jobRecord{}, fmt.Errorf("job %s is already %s", id, j.status)
	}
	now := st.now()
	j.status, j.msg, j.updated, j.finished = jobCancelled, "cancelled by the client", now, now
	j.cancel()
	st.dequeueLocked(j)
	releaseLocked(j)
	return st.recordLocked(j), nil
}

func (st *jobStore) list(owner any) jobList {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sweepLocked(st.now())
	out := jobList{Jobs: []jobRecord{}}
	for _, j := range st.jobs {
		if j.owner == owner {
			out.Jobs = append(out.Jobs, st.recordLocked(j))
		}
	}
	sort.Slice(out.Jobs, func(a, b int) bool {
		if !out.Jobs[a].CreatedAt.Equal(out.Jobs[b].CreatedAt) {
			return out.Jobs[a].CreatedAt.Before(out.Jobs[b].CreatedAt)
		}
		return out.Jobs[a].JobID < out.Jobs[b].JobID
	})
	return out
}

// close refuses new jobs, cancels every job and waits (bounded) for them.
func (st *jobStore) close() {
	if st == nil {
		return
	}
	st.mu.Lock()
	st.closed = true
	now := st.now()
	for _, j := range st.queue {
		if j.status == jobWorking {
			j.status, j.msg, j.updated, j.finished = jobCancelled, "cancelled: the server shut down", now, now
		}
		releaseLocked(j)
	}
	st.queue = nil
	st.mu.Unlock()
	st.stop()
	done := make(chan struct{})
	go func() {
		st.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(st.cfg.ShutdownWait):
	}
}
