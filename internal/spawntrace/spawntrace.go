// Package spawntrace watches the process table and reports process starts and
// exits as they happen, to catch short-lived children that a one-shot process
// listing misses. It polls, so a process that starts and exits between two
// collections is never seen.
package spawntrace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

// Event kinds.
const (
	KindStart = "start"
	KindExit  = "exit"
)

// PollingNote is shown in help text and the summary.
const PollingNote = "polling misses processes that live for less than the interval"

// maxChain bounds parent-chain walks against pid cycles in a torn table.
const maxChain = 64

// Event is one process start or exit.
type Event struct {
	Kind     string        `json:"event"`
	Time     time.Time     `json:"time"`
	PID      int           `json:"pid"`
	PPID     int           `json:"ppid"`
	Command  string        `json:"command"`
	Chain    []string      `json:"parent_chain"`
	Lifetime time.Duration `json:"-"`
	// LifetimeErr bounds how far Lifetime can be off: the start is known only
	// to one-second resolution or to the poll window it appeared in, and the
	// exit only to the window between the last poll that saw the process and
	// the one that did not.
	LifetimeErr time.Duration `json:"-"`
}

// MarshalJSON exposes lifetime in milliseconds, with its error bound, on exit
// events only.
func (e Event) MarshalJSON() ([]byte, error) {
	type wire struct {
		Kind        string    `json:"event"`
		Time        time.Time `json:"time"`
		PID         int       `json:"pid"`
		PPID        int       `json:"ppid"`
		Command     string    `json:"command"`
		Chain       []string  `json:"parent_chain"`
		LifetimeMS  *int64    `json:"lifetime_ms,omitempty"`
		LifetimeErr *int64    `json:"lifetime_max_error_ms,omitempty"`
	}
	w := wire{Kind: e.Kind, Time: e.Time, PID: e.PID, PPID: e.PPID, Command: e.Command, Chain: e.Chain}
	if w.Chain == nil {
		w.Chain = []string{}
	}
	if e.Kind == KindExit {
		ms, errMS := e.Lifetime.Milliseconds(), e.LifetimeErr.Milliseconds()
		w.LifetimeMS, w.LifetimeErr = &ms, &errMS
	}
	return json.Marshal(w)
}

// identity is pid plus start time, so a recycled pid is a new process. ps
// reports start time at one-second resolution, so a pid reused within the
// same second as its predecessor is not detected.
type identity struct {
	pid   int
	start int64
}

type tracked struct {
	ev       Event // the start-shaped record: pid, ppid, command, chain
	since    time.Time
	startErr time.Duration // uncertainty of since; the exit side is added on exit
}

// Tracker diffs successive process tables into start and exit events.
type Tracker struct {
	root     int
	rootID   identity // the root's pid+start time from the baseline
	rootSeen bool
	baseline bool
	prev     time.Time

	ignoreParent int
	interval     time.Duration
	known        map[identity]tracked
}

// NewTracker returns a Tracker. root > 0 limits it to that process and its
// descendants; 0 traces everything.
func NewTracker(root int) *Tracker {
	return &Tracker{root: root, known: map[identity]tracked{}}
}

// tracking reports whether a live process with this pid is being followed.
func (t *Tracker) tracking(pid int) bool {
	for id := range t.known {
		if id.pid == pid {
			return true
		}
	}
	return false
}

// isRoot reports whether p is the scoped root. Once the baseline has captured
// the root's identity, a later process that reuses its pid does not match.
func (t *Tracker) isRoot(p process.Info) bool {
	if t.baseline {
		return t.rootSeen && idOf(p) == t.rootID
	}
	return p.PID == t.root
}

func idOf(p process.Info) identity {
	id := identity{pid: p.PID}
	if !p.StartTime.IsZero() {
		id.start = p.StartTime.Unix()
	}
	return id
}

// Observe records procs as seen at now and returns the starts and exits since
// the previous call. The first non-empty table is the baseline and yields no
// events. An empty table is treated as a failed collection and ignored.
func (t *Tracker) Observe(procs []process.Info, now time.Time) []Event {
	if len(procs) == 0 {
		return nil
	}
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	if !t.baseline && t.root > 0 {
		if rp, ok := byPID[t.root]; ok {
			t.rootID, t.rootSeen = idOf(rp), true
		}
	}
	current := t.currentSet(procs, byPID)

	events := t.exits(current, now)
	events = append(events, t.starts(current, byPID, now)...)
	t.baseline = true
	t.prev = now

	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Kind != b.Kind {
			return a.Kind == KindExit
		}
		return a.PID < b.PID
	})
	return events
}

// currentSet keeps in-scope processes plus any already-tracked one, so a
// descendant orphaned to pid 1 is still followed until it exits.
func (t *Tracker) currentSet(procs []process.Info, byPID map[int]process.Info) map[identity]process.Info {
	current := map[identity]process.Info{}
	for _, p := range procs {
		if t.ignoreParent > 0 && p.PPID == t.ignoreParent {
			continue
		}
		id := idOf(p)
		if _, ok := t.known[id]; ok || t.inScope(p, byPID) {
			current[id] = p
		}
	}
	return current
}

func (t *Tracker) exits(current map[identity]process.Info, now time.Time) []Event {
	var events []Event
	for id, k := range t.known {
		if _, alive := current[id]; alive {
			continue
		}
		ev := k.ev
		ev.Kind, ev.Time = KindExit, now
		// The exit happened somewhere between the last poll that saw the
		// process (t.prev) and this one.
		ev.Lifetime, ev.LifetimeErr = now.Sub(k.since), k.startErr+now.Sub(t.prev)
		if ev.Lifetime < 0 {
			ev.Lifetime = 0
		}
		events = append(events, ev)
		delete(t.known, id)
	}
	return events
}

// starts registers new processes; during the baseline pass they are recorded
// but not reported.
func (t *Tracker) starts(current map[identity]process.Info, byPID map[int]process.Info, now time.Time) []Event {
	var events []Event
	for id, p := range current {
		if _, ok := t.known[id]; ok {
			continue
		}
		since, startErr := t.startEstimate(p, now)
		ev := Event{
			Kind: KindStart, Time: now, PID: p.PID, PPID: p.PPID,
			Command: name(p), Chain: t.chain(p, byPID),
		}
		t.known[id] = tracked{ev: ev, since: since, startErr: startErr}
		if t.baseline {
			events = append(events, ev)
		}
	}
	return events
}

// startEstimate returns the inferred start and how far it can be off. The
// process started after the previous poll missed it (when there was one) and
// no later than now.
func (t *Tracker) startEstimate(p process.Info, now time.Time) (time.Time, time.Duration) {
	since := p.StartTime
	switch {
	case since.IsZero():
		if t.baseline {
			return now, now.Sub(t.prev)
		}
		return now, t.interval
	case t.baseline && since.Before(t.prev):
		// lstart is truncated to the second; the process cannot have been
		// seen before the previous poll missed it.
		return t.prev, now.Sub(t.prev)
	}
	// Start times carry one-second resolution, and the process cannot have
	// started after it was first seen.
	return since, min(time.Second, max(now.Sub(since), 0))
}

func (t *Tracker) inScope(p process.Info, byPID map[int]process.Info) bool {
	if t.root == 0 {
		return true
	}
	cur := p
	for i := 0; i < maxChain; i++ {
		// A tracked ancestor keeps its subtree in scope after the root has
		// exited and its children were reparented to pid 1.
		if _, ok := t.known[idOf(cur)]; ok || t.isRoot(cur) {
			return true
		}
		parent, ok := byPID[cur.PPID]
		if !ok {
			return false
		}
		cur = parent
	}
	return false
}

// chain lists ancestor commands nearest first, ending at pid 1 or the scoped
// root.
func (t *Tracker) chain(p process.Info, byPID map[int]process.Info) []string {
	out := []string{}
	cur := p
	for i := 0; i < maxChain && cur.PID != 1 && cur.PID != t.root; i++ {
		parent, ok := byPID[cur.PPID]
		if !ok {
			break
		}
		out = append(out, name(parent))
		cur = parent
	}
	return out
}

// name returns the display command. ps shows an exiting process as "(cmd)";
// stripping one pair keeps "sh" and "(sh)" together in counts and loop keys.
func name(p process.Info) string {
	n := p.Command
	if n == "" {
		n = p.BSDName
	}
	if len(n) > 2 && n[0] == '(' && n[len(n)-1] == ')' {
		n = n[1 : len(n)-1]
	}
	if n == "" {
		return fmt.Sprintf("pid-%d", p.PID)
	}
	return n
}

// Config drives Run.
type Config struct {
	// Collect returns the current process table; empty means failure.
	Collect func(ctx context.Context) []process.Info
	// Ticker returns a tick channel for the interval and a stop function.
	Ticker func(interval time.Duration) (<-chan time.Time, func())
	// Deadline returns a channel that fires once after d, plus a stop
	// function. It ends the trace on time even when ticks are far apart. Nil
	// disables it and the clock check after each tick applies alone.
	Deadline func(d time.Duration) (<-chan time.Time, func())
	Clock    clock.Clock
	Interval time.Duration
	Duration time.Duration
	Root     int
	// IgnoreChildrenOf hides direct children of this pid, so the tracer's own
	// ps collections do not show up as spawns. 0 disables.
	IgnoreChildrenOf int
	// Emit receives each event as it happens.
	Emit func(Event)
	// Notify receives one-off notices, such as the scoped root exiting.
	Notify func(string)
}

// Run polls until Duration elapses on cfg.Clock or ctx is cancelled and
// returns the summary. Collections run one at a time on the calling goroutine;
// ticks that fall due while a collection is running are dropped.
func Run(ctx context.Context, cfg Config) Summary {
	tracker := NewTracker(cfg.Root)
	tracker.ignoreParent = cfg.IgnoreChildrenOf
	tracker.interval = cfg.Interval
	sum := NewSummary()
	sum.Duration = cfg.Duration
	sum.Interval = cfg.Interval
	sum.Note = PollingNote

	ticks, stop := cfg.Ticker(cfg.Interval)
	defer stop()

	var deadline <-chan time.Time
	if cfg.Deadline != nil {
		var stopDeadline func()
		deadline, stopDeadline = cfg.Deadline(cfg.Duration)
		defer stopDeadline()
	}
	began := cfg.Clock.Now()
	end := began.Add(cfg.Duration)
	r := &runner{cfg: cfg, tracker: tracker, sum: sum, ticks: ticks, began: began}

	r.poll(ctx)
	for !sum.RootNotFound && cfg.Clock.Now().Before(end) {
		select {
		case <-ctx.Done():
			return r.finish()
		case <-ticks:
			r.poll(ctx)
		case <-deadline:
			// One last look so exits in the final window are reported.
			r.poll(ctx)
			return r.finish()
		}
	}
	return r.finish()
}

type runner struct {
	cfg         Config
	tracker     *Tracker
	sum         *Summary
	ticks       <-chan time.Time
	first, last time.Time
	rootChecked bool
	began       time.Time
}

func (r *runner) poll(ctx context.Context) {
	began := r.cfg.Clock.Now()
	if r.first.IsZero() {
		r.first = began
	}
	r.last = began
	r.sum.Polls++
	procs := r.cfg.Collect(ctx)
	now := r.cfg.Clock.Now()
	for _, ev := range r.tracker.Observe(procs, now) {
		r.sum.Add(ev)
		r.cfg.Emit(ev)
		r.noteRootExit(ev)
	}
	if !r.rootChecked && r.tracker.baseline {
		r.rootChecked = true
		r.sum.RootNotFound = r.cfg.Root > 0 && !r.tracker.tracking(r.cfg.Root)
	}
	r.dropOverrunTicks(now.Sub(began))
}

func (r *runner) noteRootExit(ev Event) {
	if ev.Kind != KindExit || r.cfg.Root == 0 || ev.PID != r.cfg.Root || r.sum.RootExited {
		return
	}
	r.sum.RootExited = true
	if r.cfg.Notify != nil {
		r.cfg.Notify(fmt.Sprintf("pid %d exited; still tracing its tracked descendants", r.cfg.Root))
	}
}

// dropOverrunTicks discards the ticks that fell due while a slow collection
// ran, so collections never run back to back.
func (r *runner) dropOverrunTicks(elapsed time.Duration) {
	if elapsed <= r.cfg.Interval {
		return
	}
	skipped := int(elapsed / r.cfg.Interval)
	r.sum.SkippedTicks += skipped
	for i := 0; i < skipped; i++ {
		select {
		case <-r.ticks:
		default:
		}
	}
}

func (r *runner) finish() Summary {
	sum, first, last, cfg := r.sum, r.first, r.last, r.cfg
	sum.Duration = cfg.Clock.Now().Sub(r.began).Round(time.Millisecond)
	sum.EffectiveInterval = cfg.Interval
	if sum.Polls > 1 {
		sum.EffectiveInterval = last.Sub(first) / time.Duration(sum.Polls-1)
	}
	if sum.EffectiveInterval > cfg.Interval+cfg.Interval/10 {
		sum.Note = fmt.Sprintf("the %s interval was shorter than one collection takes; effective interval %s; %s",
			cfg.Interval, sum.EffectiveInterval.Round(time.Millisecond), PollingNote)
	}
	return sum.Result()
}
