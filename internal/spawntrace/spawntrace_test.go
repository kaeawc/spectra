package spawntrace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func p(pid, ppid int, cmd string, start time.Time) process.Info {
	return process.Info{PID: pid, PPID: ppid, Command: cmd, StartTime: start}
}

func base() []process.Info {
	return []process.Info{
		p(1, 0, "launchd", t0.Add(-time.Hour)),
		p(100, 1, "zsh", t0.Add(-time.Minute)),
	}
}

func TestBaselineProducesNoEvents(t *testing.T) {
	tr := NewTracker(0)
	if ev := tr.Observe(base(), t0); len(ev) != 0 {
		t.Fatalf("baseline events = %v", ev)
	}
	if ev := tr.Observe(base(), t0.Add(time.Second)); len(ev) != 0 {
		t.Fatalf("unchanged table events = %v", ev)
	}
}

func TestStartExitAndLifetime(t *testing.T) {
	tr := NewTracker(0)
	tr.Observe(base(), t0)
	with := append(base(), p(200, 100, "make", t0.Add(time.Second)))
	ev := tr.Observe(with, t0.Add(1500*time.Millisecond))
	if len(ev) != 1 || ev[0].Kind != KindStart || ev[0].PID != 200 || ev[0].PPID != 100 || ev[0].Command != "make" {
		t.Fatalf("start events = %+v", ev)
	}
	if got := strings.Join(ev[0].Chain, ">"); got != "zsh>launchd" {
		t.Fatalf("chain = %q", got)
	}
	ev = tr.Observe(base(), t0.Add(3*time.Second))
	if len(ev) != 1 || ev[0].Kind != KindExit || ev[0].PID != 200 {
		t.Fatalf("exit events = %+v", ev)
	}
	if ev[0].Lifetime != 2*time.Second {
		t.Fatalf("lifetime = %v, want 2s", ev[0].Lifetime)
	}
	if got := strings.Join(ev[0].Chain, ">"); got != "zsh>launchd" {
		t.Fatalf("exit chain should survive parent state, got %q", got)
	}
}

func TestExitLifetimeFromFirstSightingWithoutStartTime(t *testing.T) {
	tr := NewTracker(0)
	tr.Observe(base(), t0)
	tr.Observe(append(base(), p(200, 100, "x", time.Time{})), t0.Add(time.Second))
	ev := tr.Observe(base(), t0.Add(4*time.Second))
	if len(ev) != 1 || ev[0].Lifetime != 3*time.Second {
		t.Fatalf("events = %+v", ev)
	}
}

func TestPIDReuseIsNewProcess(t *testing.T) {
	tr := NewTracker(0)
	tr.Observe(append(base(), p(200, 100, "old", t0.Add(-time.Minute))), t0)
	ev := tr.Observe(append(base(), p(200, 100, "new", t0.Add(time.Second))), t0.Add(2*time.Second))
	if len(ev) != 2 || ev[0].Kind != KindExit || ev[0].Command != "old" || ev[1].Kind != KindStart || ev[1].Command != "new" {
		t.Fatalf("events = %+v", ev)
	}
}

func TestScopedToRootAndLateDescendants(t *testing.T) {
	tr := NewTracker(100)
	procs := append(base(), p(300, 1, "unrelated", t0.Add(-time.Minute)))
	tr.Observe(procs, t0)

	procs = append(procs,
		p(400, 1, "other", t0.Add(time.Second)),
		p(200, 100, "child", t0.Add(time.Second)),
	)
	ev := tr.Observe(procs, t0.Add(2*time.Second))
	if len(ev) != 1 || ev[0].PID != 200 {
		t.Fatalf("scoped events = %+v", ev)
	}
	if got := strings.Join(ev[0].Chain, ">"); got != "zsh" {
		t.Fatalf("scoped chain = %q, want to stop at root", got)
	}
	procs = append(procs, p(500, 200, "grandchild", t0.Add(3*time.Second)))
	ev = tr.Observe(procs, t0.Add(4*time.Second))
	if len(ev) != 1 || ev[0].PID != 500 || strings.Join(ev[0].Chain, ">") != "child>zsh" {
		t.Fatalf("late descendant = %+v", ev)
	}
}

func TestEmptyCollectionSkipped(t *testing.T) {
	tr := NewTracker(0)
	tr.Observe(base(), t0)
	if ev := tr.Observe(nil, t0.Add(time.Second)); len(ev) != 0 {
		t.Fatalf("empty collection events = %+v", ev)
	}
	if ev := tr.Observe(base(), t0.Add(2*time.Second)); len(ev) != 0 {
		t.Fatalf("after empty, events = %+v", ev)
	}
}

func TestSummaryCountsLoopsAndShortest(t *testing.T) {
	s := NewSummary()
	chain := []string{"zsh", "launchd"}
	for i := 0; i < 3; i++ {
		s.Add(Event{Kind: KindStart, PID: 200 + i, PPID: 100, Command: "flaky", Chain: chain})
		s.Add(Event{Kind: KindExit, PID: 200 + i, PPID: 100, Command: "flaky", Chain: chain,
			Lifetime: time.Duration(i+1) * 100 * time.Millisecond})
	}
	s.Add(Event{Kind: KindStart, PID: 300, PPID: 100, Command: "once", Chain: chain})
	s.Add(Event{Kind: KindStart, PID: 301, PPID: 101, Command: "flaky", Chain: []string{"other"}})
	out := s.Result()
	if out.Starts != 5 || out.Exits != 3 {
		t.Fatalf("starts/exits = %d/%d", out.Starts, out.Exits)
	}
	if out.ByCommand[0].Name != "flaky" || out.ByCommand[0].Count != 4 {
		t.Fatalf("by command = %+v", out.ByCommand)
	}
	if out.ByParent[0].Name != "zsh[100]" || out.ByParent[0].Count != 4 {
		t.Fatalf("by parent = %+v", out.ByParent)
	}
	if len(out.Loops) != 1 || out.Loops[0].Command != "flaky" || out.Loops[0].Parent != "zsh[100]" || out.Loops[0].Count != 3 {
		t.Fatalf("loops = %+v", out.Loops)
	}
	if len(out.Shortest) != 3 || out.Shortest[0].Lifetime != 100*time.Millisecond {
		t.Fatalf("shortest = %+v", out.Shortest)
	}
}

func TestEventJSONShape(t *testing.T) {
	b, err := json.Marshal(Event{Kind: KindExit, Time: t0, PID: 2, PPID: 1, Command: "c", Chain: []string{"p"}, Lifetime: 1500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"event", "time", "pid", "ppid", "command", "parent_chain", "lifetime_ms", "lifetime_max_error_ms"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if m["event"] != "exit" || m["lifetime_ms"].(float64) != 1500 {
		t.Errorf("bad values: %s", b)
	}
}

func TestLifetimeClampedToPreviousPoll(t *testing.T) {
	tr := NewTracker(0)
	tr.interval = 250 * time.Millisecond
	tr.Observe(base(), t0.Add(600*time.Millisecond))
	// lstart is truncated to the second: the process really started at ~:00.9.
	short := p(200, 100, "blip", t0)
	tr.Observe(append(base(), short), t0.Add(1100*time.Millisecond))
	ev := tr.Observe(base(), t0.Add(1350*time.Millisecond))
	if len(ev) != 1 || ev[0].Lifetime != 750*time.Millisecond {
		t.Fatalf("events = %+v, want 750ms lifetime clamped to the previous poll", ev)
	}
	// start window 600ms..1100ms plus exit window 1100ms..1350ms
	if ev[0].LifetimeErr != 750*time.Millisecond {
		t.Fatalf("error bound = %v, want 750ms", ev[0].LifetimeErr)
	}
}

func TestZombieNameStripped(t *testing.T) {
	tr := NewTracker(0)
	tr.Observe(base(), t0)
	procs := append(base(), p(200, 100, "(sh)", t0.Add(time.Second)), p(201, 100, "sh", t0.Add(time.Second)))
	ev := tr.Observe(procs, t0.Add(2*time.Second))
	if len(ev) != 2 || ev[0].Command != "sh" || ev[1].Command != "sh" {
		t.Fatalf("events = %+v", ev)
	}
}

func TestOrphanedDescendantKeepsScope(t *testing.T) {
	tr := NewTracker(100)
	tr.Observe(base(), t0)
	daemon := p(300, 100, "daemon", t0.Add(time.Second))
	tr.Observe(append(base(), daemon), t0.Add(2*time.Second))
	// Root exits; the daemon is reparented to pid 1 and then forks.
	daemon.PPID = 1
	child := p(400, 300, "worker", t0.Add(3*time.Second))
	procs := []process.Info{base()[0], daemon, child}
	ev := tr.Observe(procs, t0.Add(4*time.Second))
	var started bool
	for _, e := range ev {
		if e.Kind == KindStart && e.PID == 400 {
			started = true
		}
	}
	if !started {
		t.Fatalf("worker under orphaned daemon not reported: %+v", ev)
	}
}

func TestSummaryBoundedAndTotals(t *testing.T) {
	s := NewSummary()
	for i := 0; i < 200; i++ {
		s.Add(Event{Kind: KindStart, PID: i, PPID: i, Command: "c" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Chain: []string{"p"}})
		s.Add(Event{Kind: KindExit, PID: i, Command: "x", Lifetime: time.Duration(200-i) * time.Millisecond})
	}
	if len(s.lived) != shortestKept {
		t.Fatalf("retained %d lifetimes, want %d", len(s.lived), shortestKept)
	}
	out := s.Result()
	if out.Shortest[0].Lifetime != time.Millisecond || out.Shortest[4].Lifetime != 5*time.Millisecond {
		t.Fatalf("shortest = %+v", out.Shortest)
	}
	if len(out.ByCommand) != TopKept || out.ByCommandTotal != 200 || out.ByParentTotal != 200 {
		t.Fatalf("by_command %d total %d/%d", len(out.ByCommand), out.ByCommandTotal, out.ByParentTotal)
	}
}

// runHarness drives Run with scripted tables; tick sends are buffered so the
// loop never blocks and no real time passes.
type harness struct {
	clk    *clock.Fake
	ticks  chan time.Time
	tables [][]process.Info
	calls  int
	step   time.Duration // clock advance per collection

	deadline func(time.Duration) (<-chan time.Time, func())
}

func TestRunDeadlineFiresBeforeFirstTick(t *testing.T) {
	fired := make(chan time.Time, 1)
	fired <- t0
	h := &harness{
		clk:      clock.NewFake(t0),
		ticks:    make(chan time.Time), // never ticks: interval is far longer than the duration
		tables:   [][]process.Info{base(), append(base(), p(200, 100, "x", t0))},
		deadline: func(time.Duration) (<-chan time.Time, func()) { return fired, func() {} },
	}
	var got []Event
	// Baseline has the child; the final collection at the deadline lacks it.
	h.tables = [][]process.Info{append(base(), p(200, 100, "x", t0)), base()}
	sum := Run(context.Background(), h.cfg(30*time.Second, 5*time.Second, func(e Event) { got = append(got, e) }))
	if h.calls != 2 || len(got) != 1 || got[0].Kind != KindExit {
		t.Fatalf("calls=%d events=%+v, want a final collection reporting the exit", h.calls, got)
	}
	if sum.Polls != 2 {
		t.Fatalf("polls = %d", sum.Polls)
	}
}

func TestRunScopedRootMissingAndExiting(t *testing.T) {
	h := &harness{clk: clock.NewFake(t0), ticks: make(chan time.Time, 5), tables: [][]process.Info{base()}}
	cfg := h.cfg(time.Second, time.Hour, func(Event) {})
	cfg.Root = 999
	if sum := Run(context.Background(), cfg); !sum.RootNotFound || h.calls != 1 {
		t.Fatalf("RootNotFound=%v calls=%d, want immediate stop", sum.RootNotFound, h.calls)
	}

	h = &harness{
		clk:    clock.NewFake(t0),
		ticks:  make(chan time.Time, 10),
		step:   time.Second,
		tables: [][]process.Info{base(), {base()[0]}, {base()[0]}},
	}
	for i := 0; i < 10; i++ {
		h.ticks <- t0
	}
	var notes []string
	cfg = h.cfg(time.Second, 3*time.Second, func(Event) {})
	cfg.Root = 100
	cfg.Notify = func(s string) { notes = append(notes, s) }
	sum := Run(context.Background(), cfg)
	if !sum.RootExited || len(notes) != 1 {
		t.Fatalf("RootExited=%v notes=%v, want one notice", sum.RootExited, notes)
	}
}

func (h *harness) collect(context.Context) []process.Info {
	i := h.calls
	h.calls++
	h.clk.Advance(h.step)
	if i >= len(h.tables) {
		i = len(h.tables) - 1
	}
	return h.tables[i]
}

func (h *harness) cfg(interval, dur time.Duration, emit func(Event)) Config {
	return Config{
		Collect:  h.collect,
		Deadline: h.deadline,
		Ticker:   func(time.Duration) (<-chan time.Time, func()) { return h.ticks, func() {} },
		Clock:    h.clk,
		Interval: interval,
		Duration: dur,
		Emit:     emit,
	}
}

func TestRunStopsAtDurationAndEmits(t *testing.T) {
	h := &harness{
		clk:   clock.NewFake(t0),
		ticks: make(chan time.Time, 100),
		step:  time.Second,
		tables: [][]process.Info{
			base(),
			append(base(), p(200, 100, "x", t0.Add(time.Second))),
			base(),
			base(),
		},
	}
	for i := 0; i < 50; i++ {
		h.ticks <- t0
	}
	var got []Event
	sum := Run(context.Background(), h.cfg(time.Second, 3*time.Second, func(e Event) { got = append(got, e) }))
	if len(got) != 2 || got[0].Kind != KindStart || got[1].Kind != KindExit {
		t.Fatalf("events = %+v", got)
	}
	if h.calls != 3 {
		t.Fatalf("collections = %d, want 3 (baseline at 1s, polls at 2s and 3s)", h.calls)
	}
	if sum.Starts != 1 || sum.Exits != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	h := &harness{clk: clock.NewFake(t0), ticks: make(chan time.Time), tables: [][]process.Info{base()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum := Run(ctx, h.cfg(time.Second, time.Hour, func(Event) {}))
	if h.calls != 1 {
		t.Fatalf("collections = %d, want baseline only", h.calls)
	}
	if sum.Polls != 1 {
		t.Fatalf("polls = %d", sum.Polls)
	}
}

func TestRunSlowCollectorSkipsTicks(t *testing.T) {
	h := &harness{
		clk:    clock.NewFake(t0),
		ticks:  make(chan time.Time, 100),
		step:   600 * time.Millisecond,
		tables: [][]process.Info{base()},
	}
	for i := 0; i < 20; i++ {
		h.ticks <- t0
	}
	sum := Run(context.Background(), h.cfg(250*time.Millisecond, 2*time.Second, func(Event) {}))
	if sum.SkippedTicks == 0 {
		t.Fatalf("expected skipped ticks, summary = %+v", sum)
	}
	if sum.EffectiveInterval < 600*time.Millisecond {
		t.Fatalf("effective interval = %v, want >= 600ms", sum.EffectiveInterval)
	}
	if !strings.Contains(sum.Note, "shorter than") {
		t.Fatalf("note = %q", sum.Note)
	}
}

func TestLifetimeErrorCoversBothObservationWindows(t *testing.T) {
	tr := NewTracker(0)
	tr.interval = 250 * time.Millisecond
	tr.Observe(base(), t0.Add(100*time.Millisecond))
	blip := p(200, 100, "blip", t0)
	tr.Observe(append(base(), blip), t0.Add(350*time.Millisecond))
	ev := tr.Observe(base(), t0.Add(600*time.Millisecond))
	if len(ev) != 1 || ev[0].Lifetime != 500*time.Millisecond || ev[0].LifetimeErr != 500*time.Millisecond {
		t.Fatalf("events = %+v, want 500ms lifetime with 500ms error", ev)
	}
}

func TestLifetimeErrorWidensOnSkippedPolls(t *testing.T) {
	tr := NewTracker(0)
	tr.interval = 250 * time.Millisecond
	tr.Observe(base(), t0)
	tr.Observe(append(base(), p(200, 100, "x", t0.Add(time.Second))), t0.Add(1200*time.Millisecond))
	ev := tr.Observe(base(), t0.Add(4200*time.Millisecond))
	if len(ev) != 1 || ev[0].LifetimeErr < 3*time.Second {
		t.Fatalf("events = %+v, want error to include the 3s exit window", ev)
	}
}

func TestScopedRootPIDReuseNotTraced(t *testing.T) {
	tr := NewTracker(300)
	root := p(300, 100, "root", t0.Add(-time.Minute))
	with := append(base(), root, p(301, 300, "child", t0.Add(-time.Minute)))
	tr.Observe(with, t0)
	tr.Observe(base(), t0.Add(time.Second)) // root and child gone
	reused := append(base(), p(300, 100, "stranger", t0.Add(2*time.Second)), p(302, 300, "grandkid", t0.Add(2*time.Second)))
	for _, e := range tr.Observe(reused, t0.Add(3*time.Second)) {
		t.Fatalf("unrelated process entered the trace: %+v", e)
	}
}

func TestRunSummaryDurationIsElapsedOnEarlyStop(t *testing.T) {
	h := &harness{clk: clock.NewFake(t0), ticks: make(chan time.Time, 1), step: 2 * time.Second, tables: [][]process.Info{base()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum := Run(ctx, h.cfg(time.Second, time.Hour, func(Event) {}))
	if sum.Duration != 2*time.Second {
		t.Fatalf("duration = %v, want elapsed 2s", sum.Duration)
	}
}
