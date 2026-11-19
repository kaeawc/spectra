package memtrace

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

var t0 = time.Unix(1_700_000_000, 0)

// harness serves tables in order (the last repeats) and advances a fake clock
// on sleep instead of waiting.
type harness struct {
	clk    *clock.Fake
	tables [][]process.Info
	calls  int
	sleeps []time.Duration
	cost   time.Duration // fake time each collection takes
}

func newHarness(tables ...[]process.Info) *harness {
	return &harness{clk: clock.NewFake(t0), tables: tables}
}

func (h *harness) cfg(pid int, tree bool, interval, duration time.Duration) Config {
	return Config{
		PID: pid, Tree: tree, Interval: interval, Duration: duration, Clock: h.clk,
		Collect: func(context.Context) []process.Info {
			i := min(h.calls, len(h.tables)-1)
			h.calls++
			h.clk.Advance(h.cost)
			return h.tables[i]
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			h.sleeps = append(h.sleeps, d)
			h.clk.Advance(d)
			return nil
		},
	}
}

func target(rss int64) process.Info {
	return process.Info{PID: 100, PPID: 1, Command: "app", RSSKiB: rss, StartTime: t0.Add(-time.Hour)}
}

func series(rss ...int64) [][]process.Info {
	out := make([][]process.Info, len(rss))
	for i, r := range rss {
		out[i] = []process.Info{target(r)}
	}
	return out
}

func runSeries(t *testing.T, rss ...int64) *Result {
	t.Helper()
	h := newHarness(series(rss...)...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, time.Duration(len(rss)-1)*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRunSummaryFacts(t *testing.T) {
	res := runSeries(t, 1000, 1100, 1200, 1300, 1400)
	if math.Abs(res.Summary.GrowthKiBPerMin-100) > 1e-6 || res.Summary.NetChangeKiB != 400 {
		t.Fatalf("summary = %+v", res.Summary)
	}
	if res.Summary.MinKiB != 1000 || res.Summary.MaxKiB != 1400 || res.Summary.Samples != 5 {
		t.Fatalf("summary = %+v", res.Summary)
	}
}

func treeTable(a, b, c int64) []process.Info {
	return []process.Info{
		{PID: 1, PPID: 0, Command: "launchd"},
		target(a),
		{PID: 101, PPID: 100, Command: "child-a", RSSKiB: b, StartTime: t0},
		{PID: 102, PPID: 101, Command: "grandchild", RSSKiB: c, StartTime: t0},
		{PID: 103, PPID: 1, Command: "unrelated", RSSKiB: 9999, StartTime: t0},
	}
}

func TestTreeSumsAndGrowers(t *testing.T) {
	h := newHarness(treeTable(100, 50, 10), treeTable(100, 80, 10), treeTable(100, 90, 40))
	res, err := Run(context.Background(), h.cfg(100, true, time.Minute, 2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.Samples[0].SubtreeRSSKiB != 160 || res.Samples[0].Processes != 3 || res.Samples[2].SubtreeRSSKiB != 230 {
		t.Fatalf("samples = %+v", res.Samples)
	}
	if len(res.Growers) != 2 || res.Growers[0].PID != 101 || res.Growers[0].GrowKiB != 40 || res.Growers[1].PID != 102 || res.Growers[1].LastKiB != 40 {
		t.Fatalf("growers = %+v", res.Growers)
	}
	if res.Summary.SubtreeFirstKiB != 160 || res.Summary.SubtreeLastKiB != 230 {
		t.Fatalf("summary = %+v", res.Summary)
	}
}

func TestGrowersCappedAtFive(t *testing.T) {
	mk := func(extra int64) []process.Info {
		p := []process.Info{target(10)}
		for i := range 7 {
			p = append(p, process.Info{PID: 200 + i, PPID: 100, Command: "c", RSSKiB: 10 + extra*int64(i+1), StartTime: t0})
		}
		return p
	}
	h := newHarness(mk(0), mk(1))
	res, err := Run(context.Background(), h.cfg(100, true, time.Minute, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Growers) != MaxGrowers || res.Growers[0].PID != 206 {
		t.Fatalf("growers = %+v", res.Growers)
	}
}

func TestTargetExitsMidTrace(t *testing.T) {
	tables := append(series(1000, 1100, 1200), []process.Info{{PID: 1, PPID: 0, Command: "launchd"}})
	h := newHarness(tables...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Exited || len(res.Samples) != 3 || res.DurationMS != 3*60000 {
		t.Fatalf("res = %+v", res)
	}
}

func TestPIDReuseIsExit(t *testing.T) {
	reused := []process.Info{{PID: 100, PPID: 1, Command: "other", RSSKiB: 5, StartTime: t0.Add(30 * time.Second)}}
	h := newHarness(append(series(1000, 1100), reused)...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Exited || len(res.Samples) != 2 || res.Command != "app" {
		t.Fatalf("res = %+v", res)
	}
}

func zombieTarget() process.Info {
	return process.Info{PID: 100, PPID: 1, Command: "app <defunct>", StartTime: t0.Add(-time.Hour)}
}

func TestZombieTargetIsExit(t *testing.T) {
	tables := append(series(100000, 150000, 200000, 250000, 300000), []process.Info{zombieTarget()})
	h := newHarness(tables...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if !s.Exited || s.ExitReason != ExitZombie || len(res.Samples) != 5 || s.LastKiB != 300000 || s.Verdict != VerdictGrowing {
		t.Fatalf("res = %+v", res)
	}
}

func TestGoneTargetExitReason(t *testing.T) {
	tables := append(series(1000, 1100), []process.Info{{PID: 1, Command: "launchd"}})
	h := newHarness(tables...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.ExitReason != ExitGone || !res.Summary.Exited {
		t.Fatalf("res = %+v", res.Summary)
	}
	live := runSeries(t, 1, 2, 3)
	if live.Summary.ExitReason != "" {
		t.Fatalf("exit_reason = %q", live.Summary.ExitReason)
	}
}

func TestZeroRSSLiveProcessStillSampled(t *testing.T) {
	res := runSeries(t, 0, 0, 0, 0, 0)
	if res.Summary.Exited || len(res.Samples) != 5 || res.Summary.Verdict != VerdictUnreadable {
		t.Fatalf("res = %+v", res.Summary)
	}
}

func TestDescendantThatBecomesZombieIsNotAGrower(t *testing.T) {
	last := treeTable(100, 0, 10)
	last[2] = process.Info{PID: 101, PPID: 100, Command: "child-a <defunct>", StartTime: t0}
	h := newHarness(treeTable(100, 50, 10), treeTable(100, 200, 10), last)
	res, err := Run(context.Background(), h.cfg(100, true, time.Minute, 2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range res.Growers {
		if g.PID == 101 {
			t.Fatalf("zombie still a grower: %+v", res.Growers)
		}
	}
}

func TestZombieDescendantExcludedFromTree(t *testing.T) {
	table := treeTable(1000, 200, 50)
	table = append(table, process.Info{PID: 104, PPID: 100, Command: "dead <defunct>", StartTime: t0})
	h := newHarness(table)
	res, err := Run(context.Background(), h.cfg(100, true, time.Minute, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Samples[0]; got.Processes != 3 || got.SubtreeRSSKiB != 1250 {
		t.Fatalf("sample = %+v", got)
	}
	for _, g := range res.Growers {
		if g.PID == 104 {
			t.Fatalf("zombie in growers: %+v", res.Growers)
		}
	}
}

func TestEmptyCollectionIsNotExit(t *testing.T) {
	tables := [][]process.Info{{target(10)}, nil, {target(20)}}
	h := newHarness(tables...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Exited || len(res.Samples) != 2 || res.PollsFailed != 1 || res.PollsSkipped != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestSlowCollectionSkipsDeadlines(t *testing.T) {
	h := newHarness(series(1000)...)
	h.cost = 2500 * time.Millisecond
	res, err := Run(context.Background(), h.cfg(100, false, time.Second, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// Collections finish at 2.5s, 5.5s, 8.5s, 11.5s; the deadlines at 1, 2,
	// 4, 5, 7, 8 and 10s were overrun.
	var at []int64
	for _, s := range res.Samples {
		at = append(at, s.ElapsedMS)
	}
	if !slices.Equal(at, []int64{2500, 5500, 8500, 11500}) || res.PollsSkipped != 7 || res.PollsFailed != 0 {
		t.Fatalf("elapsed=%v skipped=%d failed=%d", at, res.PollsSkipped, res.PollsFailed)
	}
	for _, d := range h.sleeps {
		if d != 500*time.Millisecond {
			t.Fatalf("sleeps = %v, want only the remainder to each deadline", h.sleeps)
		}
	}
}

func TestFixedDeadlinesWithFastCollection(t *testing.T) {
	h := newHarness(series(1000)...)
	h.cost = 300 * time.Millisecond
	res, err := Run(context.Background(), h.cfg(100, false, time.Second, 4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 5 || res.Samples[4].ElapsedMS != 4300 || res.PollsSkipped != 0 || h.sleeps[0] != 700*time.Millisecond {
		t.Fatalf("res=%+v sleeps=%v", res, h.sleeps)
	}
}

func TestStartTimeJitter(t *testing.T) {
	for _, tc := range []struct {
		drift  time.Duration
		exited bool
	}{{time.Second, false}, {-time.Second, false}, {2 * time.Second, true}} {
		moved := target(1000)
		moved.StartTime = moved.StartTime.Add(tc.drift)
		h := newHarness([]process.Info{target(1000)}, []process.Info{moved})
		res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if res.Summary.Exited != tc.exited {
			t.Errorf("drift %v: exited = %v, want %v", tc.drift, res.Summary.Exited, tc.exited)
		}
	}
}

func TestDescendantStartJitterIsSameProcess(t *testing.T) {
	kid := func(rss int64, drift time.Duration) []process.Info {
		return []process.Info{target(10), {PID: 101, PPID: 100, Command: "kid", RSSKiB: rss, StartTime: t0.Add(drift)}}
	}
	h := newHarness(kid(10, 0), kid(20, time.Second), kid(30, 0))
	res, err := Run(context.Background(), h.cfg(100, true, time.Minute, 2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Growers) != 1 || res.Growers[0].FirstKiB != 10 || res.Growers[0].LastKiB != 30 {
		t.Fatalf("growers = %+v", res.Growers)
	}
}

func TestRunRejectsNonPositiveInterval(t *testing.T) {
	h := newHarness(series(1)...)
	if _, err := Run(context.Background(), h.cfg(100, false, 0, time.Minute)); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("err = %v", err)
	}
}

func TestMaxSamples(t *testing.T) {
	for _, tc := range []struct {
		d, i time.Duration
		want int64
	}{{4 * time.Second, time.Second, 5}, {3 * time.Second, time.Second, 4}, {4500 * time.Millisecond, time.Second, 6}, {time.Second, time.Minute, 2}} {
		if got := MaxSamples(tc.d, tc.i); got != tc.want {
			t.Errorf("MaxSamples(%v, %v) = %d, want %d", tc.d, tc.i, got, tc.want)
		}
	}
}

func TestIntervalLargerThanDurationDoesNotOverrun(t *testing.T) {
	h := newHarness(series(1, 2)...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.sleeps) != 1 || h.sleeps[0] != 10*time.Second || res.DurationMS != 10000 || len(res.Samples) != 2 {
		t.Fatalf("sleeps=%v res=%+v", h.sleeps, res)
	}
	if res.IntervalMS != 60000 {
		t.Fatalf("interval_ms = %d", res.IntervalMS)
	}
}

func TestUnknownPID(t *testing.T) {
	h := newHarness([]process.Info{{PID: 1, Command: "launchd"}})
	_, err := Run(context.Background(), h.cfg(100, false, time.Second, time.Minute))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelledContextStopsAndSummarizes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newHarness(series(1, 2, 3)...)
	cfg := h.cfg(100, false, time.Minute, time.Hour)
	cfg.Sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	res, err := Run(ctx, cfg)
	if err != nil || len(res.Samples) != 1 || res.Summary.Verdict != VerdictInsufficient {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
