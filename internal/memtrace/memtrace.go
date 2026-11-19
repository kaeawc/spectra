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
)

const (
	// MinSamples is the fewest samples a verdict is given for.
	MinSamples = 5
	// MaxGrowers bounds the descendant growth list.
	MaxGrowers = 5

	changeFrac = 0.05 // net change that counts as growth or shrinkage
	rangeFrac  = 0.20 // min-to-max range, relative to the mean, that counts as sawtooth
)

// ErrNotFound reports that the target pid was not in the first process table.
var ErrNotFound = errors.New("no such process")

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
	Exited           bool    `json:"exited,omitempty"`
	SubtreeFirstKiB  int64   `json:"subtree_first_kib,omitempty"`
	SubtreeLastKiB   int64   `json:"subtree_last_kib,omitempty"`
	SubtreeGrowthMin float64 `json:"subtree_growth_kib_per_min,omitempty"`
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
	Summary    Summary  `json:"summary"`
	Growers    []Grower `json:"growers"`
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

// loop polls until the deadline, the target's exit, or cancellation; a
// cancelled sleep ends the trace normally so the summary still prints.
func (t *tracer) loop(ctx context.Context) {
	for !t.exited && t.elapsed() < t.cfg.Duration {
		remaining := t.cfg.Duration - t.elapsed()
		if t.cfg.Sleep(ctx, min(t.cfg.Interval, remaining)) != nil || ctx.Err() != nil {
			return
		}
		t.poll(ctx)
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
	t.observe(procs, 0)
	return nil
}

func (t *tracer) poll(ctx context.Context) {
	at := t.elapsed()
	procs := t.cfg.Collect(ctx)
	if len(procs) == 0 {
		return // failed collection, not an exit
	}
	t.observe(procs, at)
}

func (t *tracer) observe(procs []process.Info, at time.Duration) {
	var target *process.Info
	for i := range procs {
		if procs[i].PID == t.cfg.PID {
			target = &procs[i]
		}
	}
	if target == nil || idOf(*target) != t.target {
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
		g, ok := t.desc[id]
		if !ok {
			g = &growth{command: c.Command, first: c.RSSKiB, seq: t.nextSeq}
			t.nextSeq++
			t.desc[id] = g
		}
		g.last = c.RSSKiB
		t.noteDescendants(c)
	}
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

// Summarize computes the trend statistics and verdict for samples.
func Summarize(samples []Sample) Summary {
	s := Summary{Samples: len(samples), Verdict: VerdictInsufficient}
	if len(samples) == 0 {
		return s
	}
	last := samples[len(samples)-1]
	s.FirstKiB, s.LastKiB = samples[0].RSSKiB, last.RSSKiB
	s.MinKiB, s.MaxKiB = s.FirstKiB, s.FirstKiB
	var sum float64
	for _, x := range samples {
		s.MinKiB, s.MaxKiB = min(s.MinKiB, x.RSSKiB), max(s.MaxKiB, x.RSSKiB)
		sum += float64(x.RSSKiB)
	}
	s.NetChangeKiB = s.LastKiB - s.FirstKiB
	s.GrowthKiBPerMin = Slope(samples, func(x Sample) int64 { return x.RSSKiB })
	if samples[0].Processes > 0 {
		s.SubtreeFirstKiB, s.SubtreeLastKiB = samples[0].SubtreeRSSKiB, last.SubtreeRSSKiB
		s.SubtreeGrowthMin = Slope(samples, func(x Sample) int64 { return x.SubtreeRSSKiB })
	}
	if len(samples) >= MinSamples {
		span := float64(last.ElapsedMS-samples[0].ElapsedMS) / 60000
		s.Verdict = verdict(s, sum/float64(len(samples)), s.GrowthKiBPerMin*span)
	}
	return s
}

// verdict also checks the slope-projected change over the trace, so "steady"
// requires the fitted trend to be flat and not only the two endpoints.
func verdict(s Summary, mean, projected float64) string {
	net, limit := float64(s.NetChangeKiB), changeFrac*float64(s.FirstKiB)
	small := net > -limit && net < limit
	switch {
	case small && float64(s.MaxKiB-s.MinKiB) >= rangeFrac*mean:
		return VerdictSawtooth
	case small && projected > -limit && projected < limit:
		return VerdictSteady
	case net >= limit && s.GrowthKiBPerMin > 0:
		return VerdictGrowing
	case net <= -limit && s.GrowthKiBPerMin < 0:
		return VerdictShrinking
	}
	// Endpoints and fitted trend disagree: the series is not monotonic.
	return VerdictSawtooth
}

// Slope is the least-squares slope of val over elapsed time, in KiB/minute.
func Slope(samples []Sample, val func(Sample) int64) float64 {
	if len(samples) < 2 {
		return 0
	}
	n := float64(len(samples))
	var sx, sy, sxx, sxy float64
	for _, p := range samples {
		x, y := float64(p.ElapsedMS)/60000, float64(val(p))
		sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}
