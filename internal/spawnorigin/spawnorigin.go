// Package spawnorigin is the daemon's bounded in-memory record of process
// lineage: for every process of the daemon's user it saw, the parent it had
// when first seen, that parent's command and cwd, and when each exited. A
// process is identified by (pid, start time) so a reused pid never inherits
// another lifetime's origin.
package spawnorigin

import (
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// StartTolerance is how far two start times of one pid may differ and still
// name the same process: ps lstart has 1s resolution and Linux start times
// derive from a btime that can drift by about a second.
const StartTolerance = 2 * time.Second

// Defaults bound the store to a few MB: argv, cwd and comm are truncated to
// these byte limits, so a record's strings total under 2 KiB.
const (
	DefaultMaxRecords = 4096
	DefaultExitedTTL  = 24 * time.Hour
	maxAncestors      = 32
	maxArgvBytes      = 512
	maxCwdBytes       = 1024
	maxCommBytes      = 64
)

// Proc is one process-table row as the daemon's spawn probe sees it.
type Proc struct {
	PID, PPID, UID int
	Comm           string
	Start          time.Time
}

// Record is what the store knows about one process lifetime. PPID and
// ParentStart are the parent when the process was first seen, kept after the
// process is reparented. ExitedAt is the first probe that no longer saw it,
// so the exit happened at most one probe interval earlier.
type Record struct {
	PID         int       `json:"pid"`
	StartTime   time.Time `json:"start_time"`
	PPID        int       `json:"ppid"`
	ParentStart time.Time `json:"parent_start_time,omitzero"`
	ParentComm  string    `json:"parent_comm,omitempty"`
	Comm        string    `json:"comm"`
	Argv        string    `json:"argv,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	ExitedAt    time.Time `json:"exited_at,omitzero"`
	// Baseline marks a process already running at the daemon's first probe.
	Baseline bool `json:"baseline,omitempty"`

	detailed bool
}

// Key identifies one process lifetime.
type Key struct {
	PID   int
	Start int64 // unix nanoseconds
}

// KeyOf returns the key of pid started at start.
func KeyOf(pid int, start time.Time) Key { return Key{PID: pid, Start: start.UnixNano()} }

func (r *Record) key() Key { return KeyOf(r.PID, r.StartTime) }

func (r *Record) parentKey() (Key, bool) {
	if r.PPID <= 1 || r.ParentStart.IsZero() {
		return Key{}, false
	}
	return KeyOf(r.PPID, r.ParentStart), true
}

// Options configures a Store. Zero values pick the defaults; UID < 0 records
// every user's processes.
type Options struct {
	MaxRecords int
	ExitedTTL  time.Duration
	UID        int
}

// Store is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	opts     Options
	recs     map[Key]*Record
	byPID    map[int][]Key
	since    time.Time
	observed bool
}

// NewStore returns an empty store.
func NewStore(opts Options) *Store {
	if opts.MaxRecords <= 0 {
		opts.MaxRecords = DefaultMaxRecords
	}
	if opts.ExitedTTL <= 0 {
		opts.ExitedTTL = DefaultExitedTTL
	}
	return &Store{opts: opts, recs: map[Key]*Record{}, byPID: map[int][]Key{}}
}

// Observe folds one process-table snapshot taken at at into the store.
// Rows without a start time cannot be keyed and are ignored.
func (s *Store) Observe(at time.Time, procs []Proc) {
	at = at.UTC()
	byPID := make(map[int]Proc, len(procs))
	for _, p := range procs {
		if p.PID > 0 && !p.Start.IsZero() {
			byPID[p.PID] = p
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.observed {
		s.since = at
	}
	present := make(map[Key]bool, len(procs))
	for _, p := range byPID {
		if s.opts.UID >= 0 && p.UID != s.opts.UID {
			continue
		}
		if r := s.recs[KeyOf(p.PID, p.Start)]; r != nil {
			// A probe that missed this process marked it exited; seeing
			// the same lifetime again revives it rather than duplicating it.
			r.LastSeen, r.ExitedAt = at, time.Time{}
			present[r.key()] = true
			continue
		}
		r := &Record{
			PID: p.PID, StartTime: p.Start.UTC(), PPID: p.PPID, Comm: truncate(p.Comm, maxCommBytes),
			FirstSeen: at, LastSeen: at, Baseline: !s.observed,
		}
		if parent, ok := byPID[p.PPID]; ok && p.PPID > 1 {
			r.ParentStart = parent.Start.UTC()
			r.ParentComm = truncate(parent.Comm, maxCommBytes)
		}
		k := r.key()
		s.recs[k] = r
		s.byPID[r.PID] = append(s.byPID[r.PID], k)
		present[k] = true
	}
	for k, r := range s.recs {
		if r.ExitedAt.IsZero() && !present[k] {
			r.ExitedAt = at
		}
	}
	s.observed = true
	s.pruneLocked(at)
}

func within(a, b time.Time) bool {
	d := a.Sub(b)
	return d <= StartTolerance && d >= -StartTolerance
}

// PendingDetail returns up to limit live processes that are some recorded
// process's parent and whose argv and cwd have not been read yet. The argv
// and cwd must be read while the process lives, so this is polled per probe.
func (s *Store) PendingDetail(limit int) []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[Key]bool{}
	var out []*Record
	for _, r := range s.recs {
		pk, ok := r.parentKey()
		if !ok || seen[pk] {
			continue
		}
		seen[pk] = true
		if p := s.recs[pk]; p != nil && p.ExitedAt.IsZero() && !p.detailed {
			out = append(out, p)
		}
	}
	// Newest parents first: they are the ones about to spawn work.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.After(out[j].FirstSeen)
		}
		return out[i].PID < out[j].PID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	keys := make([]Key, len(out))
	for i, r := range out {
		keys[i] = r.key()
	}
	return keys
}

// SetDetail stores the argv and cwd read for k. It marks k read even when
// both are empty, so an unreadable process is not retried every probe.
func (s *Store) SetDetail(k Key, argv, cwd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.recs[k]; r != nil {
		r.Argv, r.Cwd, r.detailed = truncate(argv, maxArgvBytes), truncate(cwd, maxCwdBytes), true
	}
}

// Lookup returns the record of pid started at start (within StartTolerance)
// and its recorded ancestors, nearest first.
func (s *Store) Lookup(pid int, start time.Time) (Record, []Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Record
	for _, k := range s.byPID[pid] {
		r := s.recs[k]
		if r == nil || !within(r.StartTime, start) {
			continue
		}
		if best == nil || absDur(r.StartTime.Sub(start)) < absDur(best.StartTime.Sub(start)) {
			best = r
		}
	}
	if best == nil {
		return Record{}, nil, false
	}
	var ancestors []Record
	seen := map[Key]bool{best.key(): true}
	for cur := best; len(ancestors) < maxAncestors; {
		pk, ok := cur.parentKey()
		if !ok || seen[pk] {
			break
		}
		p := s.recs[pk]
		if p == nil {
			break
		}
		seen[pk] = true
		ancestors = append(ancestors, *p)
		cur = p
	}
	return *best, ancestors, true
}

// Len is the number of records held.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

// Since is the time of the first probe, zero before it.
func (s *Store) Since() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.since
}

// MaxRecords is the store's record bound.
func (s *Store) MaxRecords() int { return s.opts.MaxRecords }

// pruneLocked drops exited records past the TTL unless a live process
// descends from them, then evicts down to MaxRecords: unneeded exited
// records first, then exited ancestors of live processes, then live records
// nothing live descends from, and last live ancestors, so long-lived agent and
// shell ancestors outlast their short-lived children; oldest first within each
// tier.
func (s *Store) pruneLocked(at time.Time) {
	keep := s.liveAncestryLocked()
	type cand struct {
		k    Key
		tier int
		at   time.Time
	}
	var cands []cand
	for k, r := range s.recs {
		switch {
		case !r.ExitedAt.IsZero() && !keep[k]:
			if at.Sub(r.ExitedAt) > s.opts.ExitedTTL {
				s.deleteLocked(k)
				continue
			}
			cands = append(cands, cand{k, 0, r.ExitedAt})
		case !r.ExitedAt.IsZero():
			cands = append(cands, cand{k, 1, r.ExitedAt})
		case keep[k]:
			cands = append(cands, cand{k, 3, r.FirstSeen})
		default:
			cands = append(cands, cand{k, 2, r.FirstSeen})
		}
	}
	excess := len(s.recs) - s.opts.MaxRecords
	if excess <= 0 {
		return
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		if a.k.PID != b.k.PID {
			return a.k.PID < b.k.PID
		}
		return a.k.Start < b.k.Start
	})
	for _, c := range cands[:excess] {
		s.deleteLocked(c.k)
	}
}

// liveAncestryLocked is every record on the recorded ancestor chain of a live
// process: the records an orphan's origin is built from.
func (s *Store) liveAncestryLocked() map[Key]bool {
	keep := map[Key]bool{}
	for _, r := range s.recs {
		if !r.ExitedAt.IsZero() {
			continue
		}
		for cur, n := r, 0; n < maxAncestors; n++ {
			pk, ok := cur.parentKey()
			if !ok || keep[pk] {
				break
			}
			p := s.recs[pk]
			if p == nil {
				break
			}
			keep[pk] = true
			cur = p
		}
	}
	return keep
}

func (s *Store) deleteLocked(k Key) {
	delete(s.recs, k)
	keys := s.byPID[k.PID]
	for i, v := range keys {
		if v == k {
			keys = append(keys[:i], keys[i+1:]...)
			break
		}
	}
	if len(keys) == 0 {
		delete(s.byPID, k.PID)
	} else {
		s.byPID[k.PID] = keys
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Query names one process lifetime to look up.
type Query struct {
	PID       int       `json:"pid"`
	StartTime time.Time `json:"start_time"`
}

// MaxQueries bounds one lookup request.
const MaxQueries = 4096

// Ref names one entry of Response.Ancestors.
type Ref struct {
	PID       int       `json:"pid"`
	StartTime time.Time `json:"start_time"`
}

// Answer is the lookup result for one Query. On the wire Ancestors are Refs
// into Response.Ancestors, so an ancestor shared by many answers is sent
// once; Response.Resolve fills Chain from them.
type Answer struct {
	PID       int       `json:"pid"`
	StartTime time.Time `json:"start_time"`
	Found     bool      `json:"found"`
	Record    *Record   `json:"record,omitempty"`
	Ancestors []Ref     `json:"ancestors,omitempty"`
	// Chain is the recorded ancestors, nearest first, rebuilt by Resolve.
	Chain []Record `json:"-"`
}

// Response is the reply to a lookup. Truncated reports that the queries were
// cut to MaxQueries; answers cover the leading unique ones.
type Response struct {
	RecordingSince time.Time `json:"recording_since,omitzero"`
	Records        int       `json:"records"`
	MaxRecords     int       `json:"max_records"`
	Answers        []Answer  `json:"answers"`
	Ancestors      []Record  `json:"ancestors,omitempty"`
	Truncated      bool      `json:"truncated,omitempty"`
}

// Resolve rebuilds each answer's Chain from the shared ancestor table.
func (r *Response) Resolve() {
	table := make(map[Key]Record, len(r.Ancestors))
	for _, rec := range r.Ancestors {
		table[rec.key()] = rec
	}
	for i := range r.Answers {
		a := &r.Answers[i]
		a.Chain = a.Chain[:0]
		for _, ref := range a.Ancestors {
			if rec, ok := table[KeyOf(ref.PID, ref.StartTime)]; ok {
				a.Chain = append(a.Chain, rec)
			}
		}
	}
}

// Answer looks up every query. Duplicate queries (same pid and start) are
// answered once and each ancestor is returned once, so the response is
// bounded by the store size rather than by the request. Queries beyond
// MaxQueries are dropped and Truncated is set.
func (s *Store) Answer(queries []Query) Response {
	resp := Response{RecordingSince: s.Since(), Records: s.Len(), MaxRecords: s.MaxRecords(), Answers: make([]Answer, 0, min(len(queries), MaxQueries))}
	seenQuery := map[Key]bool{}
	seenAnc := map[Key]bool{}
	for _, q := range queries {
		qk := KeyOf(q.PID, q.StartTime)
		if seenQuery[qk] {
			continue
		}
		if len(resp.Answers) >= MaxQueries {
			resp.Truncated = true
			break
		}
		seenQuery[qk] = true
		a := Answer{PID: q.PID, StartTime: q.StartTime}
		if !q.StartTime.IsZero() {
			if rec, anc, ok := s.Lookup(q.PID, q.StartTime); ok {
				a.Found, a.Record = true, &rec
				for _, r := range anc {
					k := r.key()
					a.Ancestors = append(a.Ancestors, Ref{PID: r.PID, StartTime: r.StartTime})
					if !seenAnc[k] {
						seenAnc[k] = true
						resp.Ancestors = append(resp.Ancestors, r)
					}
				}
			}
		}
		resp.Answers = append(resp.Answers, a)
	}
	return resp
}
