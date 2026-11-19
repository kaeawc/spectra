// Package memtrace samples one process's resident memory over time and
// classifies the trend (steady, growing, shrinking, sawtooth) so a slow leak
// shows up without a heap dump.
package memtrace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/process"
)

// Verdicts.
const (
	VerdictSteady       = "steady"
	VerdictGrowing      = "growing"
	VerdictShrinking    = "shrinking"
	VerdictSawtooth     = "sawtooth"
	VerdictInsufficient = "insufficient"
	VerdictUnreadable   = "unreadable"
)

const (
	// MinSamples is the fewest samples a verdict is given for.
	MinSamples = 5
	// MaxGrowers bounds the descendant growth list.
	MaxGrowers = 5
)

// ErrNotFound reports that the target pid was not in the first process table.
var ErrNotFound = errors.New("no such process")

// ErrBadConfig reports a non-positive interval or duration.
var ErrBadConfig = errors.New("interval and duration must be positive")

// Sample is one poll of the target.
type Sample struct {
	ElapsedMS     int64 `json:"elapsed_ms"`
	RSSKiB        int64 `json:"rss_kib"`
	SubtreeRSSKiB int64 `json:"subtree_rss_kib,omitempty"`
	Processes     int   `json:"processes,omitempty"`
}

// Summary describes the trend over all samples.
type Summary struct {
	Samples          int     `json:"samples"`
	MinKiB           int64   `json:"min_kib"`
	MaxKiB           int64   `json:"max_kib"`
	FirstKiB         int64   `json:"first_kib"`
	LastKiB          int64   `json:"last_kib"`
	NetChangeKiB     int64   `json:"net_change_kib"`
	GrowthKiBPerMin  float64 `json:"growth_kib_per_min"`
	Verdict          string  `json:"verdict"`
	Pattern          string  `json:"pattern,omitempty"`
	Exited           bool    `json:"exited,omitempty"`
	SubtreeFirstKiB  int64   `json:"subtree_first_kib,omitempty"`
	SubtreeLastKiB   int64   `json:"subtree_last_kib,omitempty"`
	SubtreeGrowthMin float64 `json:"subtree_growth_kib_per_min,omitempty"`
	SubtreeVerdict   string  `json:"subtree_verdict,omitempty"`
	SubtreePattern   string  `json:"subtree_pattern,omitempty"`
}

// Grower is a descendant whose RSS grew over the trace.
type Grower struct {
	PID      int    `json:"pid"`
	Command  string `json:"command"`
	FirstKiB int64  `json:"first_kib"`
	LastKiB  int64  `json:"last_kib"`
	GrowKiB  int64  `json:"growth_kib"`
}

// Result is the outcome of Run.
type Result struct {
	PID        int      `json:"pid"`
	Command    string   `json:"command"`
	Tree       bool     `json:"tree"`
	DurationMS int64    `json:"duration_ms"`
	IntervalMS int64    `json:"interval_ms"`
	Samples    []Sample `json:"samples"`
	// PollsFailed counts collections that returned no process table.
	PollsFailed int `json:"polls_failed"`
	// PollsSkipped counts deadlines missed because a collection overran them.
	PollsSkipped int      `json:"polls_skipped"`
	Summary      Summary  `json:"summary"`
	Growers      []Grower `json:"growers"`
}

// Config drives Run.
type Config struct {
	// Collect returns the current process table; empty means a failed poll.
	Collect func(ctx context.Context) []process.Info
	// Sleep waits d or until ctx ends, returning the context error in that case.
	Sleep    func(ctx context.Context, d time.Duration) error
	Clock    clock.Clock
	PID      int
	Tree     bool
	Interval time.Duration
	Duration time.Duration
	// Emit, if set, receives each sample as it is taken.
	Emit func(Sample)
}

// identity is pid plus start time, so a recycled pid is a different process.
type identity struct {
	pid   int
	start int64
}

func idOf(p process.Info) identity {
	id := identity{pid: p.PID}
	if !p.StartTime.IsZero() {
		id.start = p.StartTime.Unix()
	}
	return id
}

// same tolerates 1s of start-time drift: Linux derives start time from
// /proc/stat btime, which shifts by a second after a clock step.
func (a identity) same(b identity) bool {
	if a.pid != b.pid || (a.start == 0) != (b.start == 0) {
		return false
	}
	d := a.start - b.start
	return d >= -1 && d <= 1
}

// MaxSamples is the most samples a trace of duration at interval can take:
// the first poll plus one per deadline, the last clamped to duration.
func MaxSamples(duration, interval time.Duration) int64 {
	if duration <= 0 || interval <= 0 {
		return 1
	}
	n := int64(duration / interval)
	if duration%interval != 0 {
		n++
	}
	return n + 1
}

type growth struct {
	command     string
	first, last int64
	seq         int
}

type tracer struct {
	cfg     Config
	target  identity
	began   time.Time
	res     *Result
	desc    map[identity]*growth
	exited  bool
	nextSeq int
}

// Run samples the target until Duration elapses on cfg.Clock, the target
// exits, or ctx ends. It returns ErrNotFound if the target is absent from the
// first process table.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Interval <= 0 || cfg.Duration <= 0 {
		return nil, fmt.Errorf("memtrace: %w", ErrBadConfig)
	}
	t := &tracer{
		cfg: cfg, desc: map[identity]*growth{}, began: cfg.Clock.Now(),
		res: &Result{PID: cfg.PID, Tree: cfg.Tree, IntervalMS: cfg.Interval.Milliseconds(), Samples: []Sample{}, Growers: []Grower{}},
	}
	if err := t.start(ctx); err != nil {
		return nil, err
	}
	t.loop(ctx)
	t.finish()
	return t.res, nil
}

// loop polls at fixed deadlines (start + k×interval, the last clamped to
// Duration) until the end, the target's exit, or cancellation, sleeping only
// the remainder so a slow collection does not stretch the cadence. Deadlines a
// collection overran are skipped and counted. A cancelled sleep ends the trace
// normally so the summary still prints.
func (t *tracer) loop(ctx context.Context) {
	for k := time.Duration(1); !t.exited; k++ {
		due := min(k*t.cfg.Interval, t.cfg.Duration)
		if wait := due - t.elapsed(); wait < 0 {
			t.res.PollsSkipped++
		} else {
			if wait > 0 && (t.cfg.Sleep(ctx, wait) != nil || ctx.Err() != nil) {
				return
			}
			t.poll(ctx)
		}
		if due >= t.cfg.Duration {
			return
		}
	}
}

func (t *tracer) elapsed() time.Duration { return t.cfg.Clock.Now().Sub(t.began) }

func (t *tracer) start(ctx context.Context) error {
	procs := t.cfg.Collect(ctx)
	found := false
	for _, p := range procs {
		if p.PID == t.cfg.PID {
			t.target, t.res.Command, found = idOf(p), p.Command, true
		}
	}
	if !found {
		return fmt.Errorf("pid %d: %w", t.cfg.PID, ErrNotFound)
	}
	t.observe(procs, t.elapsed())
	return nil
}

// poll stamps the sample with the time the collection finished.
func (t *tracer) poll(ctx context.Context) {
	procs := t.cfg.Collect(ctx)
	if len(procs) == 0 {
		t.res.PollsFailed++ // failed collection, not an exit
		return
	}
	t.observe(procs, t.elapsed())
}

func (t *tracer) observe(procs []process.Info, at time.Duration) {
	var target *process.Info
	for i := range procs {
		if procs[i].PID == t.cfg.PID {
			target = &procs[i]
		}
	}
	if target == nil || !idOf(*target).same(t.target) {
		t.exited = true
		return
	}
	s := Sample{ElapsedMS: at.Milliseconds(), RSSKiB: target.RSSKiB}
	if t.cfg.Tree {
		if res, err := lineage.Build(procs, t.cfg.PID); err == nil {
			s.SubtreeRSSKiB, s.Processes = res.Tree.SubtreeRSSKiB, res.Tree.Count
			t.noteDescendants(res.Tree)
		}
	}
	t.res.Samples = append(t.res.Samples, s)
	if t.cfg.Emit != nil {
		t.cfg.Emit(s)
	}
}

func (t *tracer) noteDescendants(n *lineage.Node) {
	for _, c := range n.Children {
		id := identity{pid: c.PID}
		if !c.StartTime.IsZero() {
			id.start = c.StartTime.Unix()
		}
		g, ok := t.lookup(id)
		if !ok {
			g = &growth{command: c.Command, first: c.RSSKiB, seq: t.nextSeq}
			t.nextSeq++
			t.desc[id] = g
		}
		g.last = c.RSSKiB
		t.noteDescendants(c)
	}
}

// lookup finds a tracked descendant whose start time is within the same
// 1s drift tolerance as the target.
func (t *tracer) lookup(id identity) (*growth, bool) {
	for _, d := range []int64{0, -1, 1} {
		k := identity{pid: id.pid, start: id.start + d}
		if g, ok := t.desc[k]; ok && k.same(id) {
			return g, true
		}
	}
	return nil, false
}

func (t *tracer) finish() {
	t.res.DurationMS = t.elapsed().Milliseconds()
	t.res.Summary = Summarize(t.res.Samples)
	t.res.Summary.Exited = t.exited
	if t.cfg.Tree {
		t.res.Growers = t.growers()
	}
}

func (t *tracer) growers() []Grower {
	type keyed struct {
		pid int
		g   *growth
	}
	var all []keyed
	for id, g := range t.desc {
		if g.last > g.first {
			all = append(all, keyed{id.pid, g})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		di, dj := all[i].g.last-all[i].g.first, all[j].g.last-all[j].g.first
		if di != dj {
			return di > dj
		}
		return all[i].g.seq < all[j].g.seq
	})
	out := []Grower{}
	for _, k := range all[:min(len(all), MaxGrowers)] {
		out = append(out, Grower{PID: k.pid, Command: k.g.command, FirstKiB: k.g.first, LastKiB: k.g.last, GrowKiB: k.g.last - k.g.first})
	}
	return out
}

// Summarize computes the facts and verdict for samples. First, last and net
// change are reported as facts only; the verdict comes from classify, which
// does not depend on where the two endpoints land.
func Summarize(samples []Sample) Summary {
	s := Summary{Samples: len(samples)}
	rss := classify(pointsOf(samples, func(x Sample) int64 { return x.RSSKiB }))
	s.Verdict, s.Pattern, s.GrowthKiBPerMin = rss.verdict, rss.pattern, rss.slope
	if len(samples) == 0 {
		return s
	}
	last := samples[len(samples)-1]
	s.FirstKiB, s.LastKiB = samples[0].RSSKiB, last.RSSKiB
	s.MinKiB, s.MaxKiB = s.FirstKiB, s.FirstKiB
	for _, x := range samples {
		s.MinKiB, s.MaxKiB = min(s.MinKiB, x.RSSKiB), max(s.MaxKiB, x.RSSKiB)
	}
	s.NetChangeKiB = s.LastKiB - s.FirstKiB
	if samples[0].Processes > 0 {
		s.SubtreeFirstKiB, s.SubtreeLastKiB = samples[0].SubtreeRSSKiB, last.SubtreeRSSKiB
		sub := classify(pointsOf(samples, func(x Sample) int64 { return x.SubtreeRSSKiB }))
		s.SubtreeVerdict, s.SubtreePattern, s.SubtreeGrowthMin = sub.verdict, sub.pattern, sub.slope
	}
	return s
}
