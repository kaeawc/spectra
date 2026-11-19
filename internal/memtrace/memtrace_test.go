package memtrace

import (
	"context"
	"errors"
	"math"
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

func TestVerdicts(t *testing.T) {
	tests := []struct {
		name string
		rss  []int64
		want string
	}{
		{"steady", []int64{1000, 1001, 1000, 1002, 1000, 1001}, VerdictSteady},
		{"growing", []int64{1000, 1100, 1200, 1300, 1400, 1500}, VerdictGrowing},
		{"shrinking", []int64{1500, 1400, 1300, 1200, 1100, 1000}, VerdictShrinking},
		{"sawtooth", []int64{1000, 1500, 1000, 1500, 1000, 1010}, VerdictSawtooth},
		{"insufficient", []int64{1000, 2000, 3000, 4000}, VerdictInsufficient},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := runSeries(t, tc.rss...).Summary.Verdict; got != tc.want {
				t.Fatalf("verdict = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSlopeLinearSeries(t *testing.T) {
	// 100 KiB per minute, sampled every 30s.
	var ss []Sample
	for i := range 7 {
		ss = append(ss, Sample{ElapsedMS: int64(i) * 30000, RSSKiB: 5000 + int64(i)*50})
	}
	got := Slope(ss, func(s Sample) int64 { return s.RSSKiB })
	if math.Abs(got-100) > 1e-6 {
		t.Fatalf("slope = %v, want 100", got)
	}
	res := runSeries(t, 1000, 1100, 1200, 1300, 1400)
	if math.Abs(res.Summary.GrowthKiBPerMin-100) > 1e-6 || res.Summary.NetChangeKiB != 400 {
		t.Fatalf("summary = %+v", res.Summary)
	}
	if res.Summary.MinKiB != 1000 || res.Summary.MaxKiB != 1400 || res.Summary.Samples != 5 {
		t.Fatalf("summary = %+v", res.Summary)
	}
}

func TestSlopeIgnoresSpikeAtEnd(t *testing.T) {
	// last-minus-first says +50% but the fit is dominated by the flat run.
	res := runSeries(t, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1500)
	if res.Summary.GrowthKiBPerMin >= 500/1.0 {
		t.Fatalf("slope %v should be below the endpoint rate", res.Summary.GrowthKiBPerMin)
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

func TestEmptyCollectionIsNotExit(t *testing.T) {
	tables := [][]process.Info{{target(10)}, nil, {target(20)}}
	h := newHarness(tables...)
	res, err := Run(context.Background(), h.cfg(100, false, time.Minute, 2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Exited || len(res.Samples) != 2 {
		t.Fatalf("res = %+v", res)
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
