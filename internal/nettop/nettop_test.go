package nettop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
)

// step is one scripted sample: its counters (or error) and how long the
// collection takes on the fake clock.
type step struct {
	cs   []Counter
	err  error
	cost time.Duration
}

type script struct {
	clk   *clock.Fake
	steps []step
	calls int
}

func (s *script) sample(context.Context) ([]Counter, error) {
	st := s.steps[min(s.calls, len(s.steps)-1)]
	s.calls++
	s.clk.Advance(st.cost)
	return st.cs, st.err
}

func newScript(steps ...step) *script {
	return &script{clk: clock.NewFake(time.Unix(1_700_000_000, 0)), steps: steps}
}

func (s *script) config(interval, duration time.Duration) Config {
	return Config{
		Sample: s.sample, Tool: "nettop", Clock: s.clk, Interval: interval, Duration: duration,
		Sleep: func(_ context.Context, d time.Duration) error { s.clk.Advance(d); return nil },
	}
}

func c(pid int, in, out int64) Counter {
	return Counter{PID: pid, Command: fmt.Sprintf("p%d", pid), In: in, Out: out}
}

func at(cs ...Counter) step { return step{cs: cs} }

func runScript(t *testing.T, cfg Config) *Trace {
	t.Helper()
	tr, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func findProc(rep Report, pid int) *Process {
	for i := range rep.Processes {
		if rep.Processes[i].PID == pid {
			return &rep.Processes[i]
		}
	}
	return nil
}

func TestSteadySender(t *testing.T) {
	s := newScript(at(c(10, 0, 0)), at(c(10, 100, 1000)), at(c(10, 200, 2000)), at(c(10, 300, 3000)))
	rep := Summarize(runScript(t, s.config(time.Second, 3*time.Second)), nil, 0)
	if rep.Samples != 4 || rep.DurationMS != 3000 || rep.IntervalMS != 1000 {
		t.Fatalf("samples=%d duration=%d interval=%d", rep.Samples, rep.DurationMS, rep.IntervalMS)
	}
	p := findProc(rep, 10)
	if p == nil || p.BytesOut != 3000 || p.BytesIn != 300 {
		t.Fatalf("process = %+v", p)
	}
	if p.AvgOutRate != 1000 || p.PeakOutRate != 1000 || p.AvgInRate != 100 || p.PeakInRate != 100 {
		t.Errorf("rates = %+v", p)
	}
	if rep.Totals.PeakTotalRate != 1100 || rep.Totals.BytesOut != 3000 || rep.Totals.ActiveProcesses != 1 {
		t.Errorf("totals = %+v", rep.Totals)
	}
	if p.Appeared || p.Disappeared || len(rep.Appeared) != 0 || len(rep.Disappeared) != 0 {
		t.Errorf("unexpected churn: %+v", rep)
	}
}

func TestAppearsMidWindow(t *testing.T) {
	steps := []step{at(c(10, 0, 0)), at(c(10, 0, 10), c(20, 500, 500)), at(c(10, 0, 20), c(20, 600, 600))}

	rep := Summarize(runScript(t, newScript(steps...).config(time.Second, 2*time.Second)), nil, 0)
	p := findProc(rep, 20)
	if p == nil || p.BytesIn != 100 || p.BytesOut != 100 || !p.Appeared {
		t.Fatalf("unknown-start newcomer = %+v, want only its second interval counted", p)
	}
	if len(rep.Appeared) != 1 || rep.Appeared[0].PID != 20 {
		t.Errorf("appeared = %+v", rep.Appeared)
	}

	s := newScript(steps...)
	cfg := s.config(time.Second, 2*time.Second)
	cfg.Preexisting = map[int]bool{10: true}
	rep = Summarize(runScript(t, cfg), nil, 0)
	if p := findProc(rep, 20); p == nil || p.BytesIn != 600 || p.PeakInRate != 500 {
		t.Fatalf("process born in window = %+v, want its whole counter", p)
	}
}

func TestDisappearsMidWindow(t *testing.T) {
	s := newScript(at(c(10, 0, 0), c(30, 0, 0)), at(c(10, 0, 5), c(30, 400, 0)), at(c(10, 0, 9)))
	rep := Summarize(runScript(t, s.config(time.Second, 2*time.Second)), nil, 0)
	p := findProc(rep, 30)
	if p == nil || p.BytesIn != 400 || !p.Disappeared {
		t.Fatalf("leaver = %+v", p)
	}
	if p.AvgInRate != 200 || p.PeakInRate != 400 {
		t.Errorf("leaver rates avg=%v peak=%v, want 200 over the window and 400 peak", p.AvgInRate, p.PeakInRate)
	}
	if len(rep.Disappeared) != 1 || rep.Disappeared[0].Command != "p30" {
		t.Errorf("disappeared = %+v", rep.Disappeared)
	}
}

func TestCounterReset(t *testing.T) {
	s := newScript(at(c(10, 1000, 0)), at(c(10, 2000, 0)), at(c(10, 100, 0)), at(c(10, 300, 0)))
	rep := Summarize(runScript(t, s.config(time.Second, 3*time.Second)), nil, 0)
	if p := findProc(rep, 10); p == nil || p.BytesIn != 1200 {
		t.Fatalf("reset process = %+v, want 1000 + 0 + 200", p)
	}
}

func TestNegativeCounterIsZero(t *testing.T) {
	s := newScript(at(c(10, -5, 0)), at(c(10, 50, 0)))
	rep := Summarize(runScript(t, s.config(time.Second, time.Second)), nil, 0)
	if p := findProc(rep, 10); p == nil || p.BytesIn != 50 {
		t.Fatalf("process = %+v", p)
	}
}

func TestZeroElapsed(t *testing.T) {
	s := newScript(at(c(10, 0, 0)), at(c(10, 100, 100)), at(c(10, 200, 200)))
	cfg := s.config(time.Second, 2*time.Second)
	cfg.Sleep = func(context.Context, time.Duration) error { return nil } // clock never moves
	rep := Summarize(runScript(t, cfg), nil, 0)
	if rep.DurationMS != 0 || rep.Samples != 3 {
		t.Fatalf("duration=%d samples=%d", rep.DurationMS, rep.Samples)
	}
	p := findProc(rep, 10)
	if p == nil || p.BytesIn != 200 || p.AvgInRate != 0 || p.PeakInRate != 0 || rep.Totals.PeakTotalRate != 0 {
		t.Fatalf("process = %+v totals = %+v", p, rep.Totals)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatalf("report does not marshal (NaN/Inf?): %v", err)
	}
}

func TestUnevenSpacingUsesActualElapsed(t *testing.T) {
	// The second sample takes 2.5s, so it is stamped at 3.5s and the 2s and
	// 3s deadlines are skipped; rates must use 3.5s and 0.5s, not 1s.
	s := newScript(at(c(10, 0, 0)), step{cs: []Counter{c(10, 3500, 0)}, cost: 2500 * time.Millisecond}, at(c(10, 4000, 0)))
	var ticks []Tick
	cfg := s.config(time.Second, 4*time.Second)
	cfg.Emit = func(tk Tick) { ticks = append(ticks, tk) }
	tr := runScript(t, cfg)
	rep := Summarize(tr, nil, 0)
	if rep.Samples != 3 || rep.SamplesSkipped != 2 || rep.DurationMS != 4000 {
		t.Fatalf("samples=%d skipped=%d duration=%d", rep.Samples, rep.SamplesSkipped, rep.DurationMS)
	}
	p := findProc(rep, 10)
	if p == nil || p.PeakInRate != 1000 || p.AvgInRate != 1000 {
		t.Fatalf("rates = %+v, want 1000 B/s throughout", p)
	}
	if len(ticks) != 2 || ticks[0].SpanMS != 3500 || ticks[1].SpanMS != 500 || ticks[0].InRate != 1000 || ticks[1].ElapsedMS != 4000 {
		t.Errorf("ticks = %+v", ticks)
	}
}

func TestRankingAndTop(t *testing.T) {
	s := newScript(at(c(1, 0, 0), c(2, 0, 0), c(3, 0, 0), c(4, 0, 0)),
		at(c(1, 10, 0), c(2, 500, 500), c(3, 300, 0), c(4, 0, 0)))
	rep := Summarize(runScript(t, s.config(time.Second, time.Second)), nil, 2)
	if len(rep.Processes) != 2 || rep.Processes[0].PID != 2 || rep.Processes[1].PID != 3 {
		t.Fatalf("top = %+v", rep.Processes)
	}
	if rep.Totals.ActiveProcesses != 3 || rep.Totals.BytesIn != 810 {
		t.Errorf("totals = %+v, want all active processes counted beyond --top", rep.Totals)
	}
}

func TestIncludeFilter(t *testing.T) {
	s := newScript(at(c(1, 0, 0), c(2, 0, 0)), at(c(1, 10, 0), c(2, 500, 0)))
	var ticks []Tick
	cfg := s.config(time.Second, time.Second)
	cfg.Emit = func(tk Tick) { ticks = append(ticks, tk) }
	cfg.EmitInclude = func(pid int) bool { return pid == 1 }
	rep := Summarize(runScript(t, cfg), func(pid int) bool { return pid == 1 }, 0)
	if len(rep.Processes) != 1 || rep.Processes[0].PID != 1 || rep.Totals.BytesIn != 10 {
		t.Fatalf("filtered = %+v", rep)
	}
	if len(ticks) != 1 || ticks[0].InRate != 10 || ticks[0].Active != 1 {
		t.Errorf("ticks = %+v", ticks)
	}
}

func TestSocketKeysNotDiffedAcrossSockets(t *testing.T) {
	sock := func(name string, in int64) Counter { return Counter{PID: 5, Command: "curl", Socket: name, In: in} }
	s := newScript(at(sock("a", 100)), at(sock("a", 150)), at(sock("b", 9000)), at(sock("b", 9100)))
	rep := Summarize(runScript(t, s.config(time.Second, 3*time.Second)), nil, 0)
	if p := findProc(rep, 5); p == nil || p.BytesIn != 150 {
		t.Fatalf("process = %+v, want 50 (a) + 0 (b first seen) + 100 (b)", p)
	}
}

func TestStartTimeDistinguishesReusedPID(t *testing.T) {
	s := newScript(at(Counter{PID: 7, Start: 100, In: 500}), at(Counter{PID: 7, Start: 200, In: 900}))
	rep := Summarize(runScript(t, s.config(time.Second, time.Second)), nil, 0)
	if rep.Totals.BytesIn != 0 {
		t.Fatalf("reused pid diffed against its predecessor: %+v", rep.Totals)
	}
}

func TestCancelEndsEarly(t *testing.T) {
	s := newScript(at(c(10, 0, 0)), at(c(10, 100, 0)), at(c(10, 200, 0)))
	ctx, cancel := context.WithCancel(context.Background())
	cfg := s.config(time.Second, 10*time.Second)
	sleeps := 0
	cfg.Sleep = func(ctx context.Context, d time.Duration) error {
		sleeps++
		if sleeps == 3 {
			s.clk.Advance(d / 2)
			cancel()
			return ctx.Err()
		}
		s.clk.Advance(d)
		return nil
	}
	tr, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep := Summarize(tr, nil, 0)
	if rep.Samples != 3 || rep.DurationMS != 2000 {
		t.Fatalf("samples=%d duration=%d", rep.Samples, rep.DurationMS)
	}
}

func TestToolMissingFailsFast(t *testing.T) {
	missing := fmt.Errorf("nettop: %w", &exec.Error{Name: "nettop", Err: exec.ErrNotFound})
	s := newScript(step{err: toolErr(missing)})
	_, err := Run(context.Background(), s.config(time.Second, 10*time.Second))
	if !errors.Is(err, ErrToolMissing) || !strings.Contains(err.Error(), "nettop") {
		t.Fatalf("err = %v", err)
	}
	if s.calls != 1 {
		t.Errorf("sampler called %d times, want 1", s.calls)
	}
}

func TestEverySampleEmpty(t *testing.T) {
	s := newScript(step{})
	_, err := Run(context.Background(), s.config(time.Second, 3*time.Second))
	if !errors.Is(err, ErrNoData) || !strings.Contains(err.Error(), "nettop") || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
	s = newScript(step{err: errors.New("boom")})
	_, err = Run(context.Background(), s.config(time.Second, 2*time.Second))
	if !errors.Is(err, ErrNoData) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestFailedSampleIsSkipped(t *testing.T) {
	s := newScript(at(c(10, 0, 0)), step{err: errors.New("flaky")}, at(c(10, 200, 0)))
	rep := Summarize(runScript(t, s.config(time.Second, 2*time.Second)), nil, 0)
	if rep.SamplesFailed != 1 || rep.Samples != 2 {
		t.Fatalf("failed=%d samples=%d", rep.SamplesFailed, rep.Samples)
	}
	if p := findProc(rep, 10); p == nil || p.BytesIn != 200 || p.PeakInRate != 100 {
		t.Fatalf("process = %+v, want 200 over the 2s span", p)
	}
}

func TestBadConfig(t *testing.T) {
	s := newScript(at(c(1, 0, 0)))
	if _, err := Run(context.Background(), s.config(0, time.Second)); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("err = %v", err)
	}
}

func TestRemotesKeptFromLatestSample(t *testing.T) {
	r := func(remote string, in int64) Counter {
		return Counter{PID: 3, Socket: remote, Remote: remote, In: in}
	}
	s := newScript(at(r("1.1.1.1:443", 0), r("old:80", 0)),
		at(r("1.1.1.1:443", 10), r("4.4.4.4:443", 50), r("3.3.3.3:443", 0), r("2.2.2.2:443", 0)))
	rep := Summarize(runScript(t, s.config(time.Second, time.Second)), nil, 0)
	p := findProc(rep, 3)
	want := []string{"4.4.4.4:443", "1.1.1.1:443", "2.2.2.2:443"}
	if p == nil || strings.Join(p.Remotes, ",") != strings.Join(want, ",") {
		t.Fatalf("remotes = %+v, want busiest first %v", p, want)
	}
	for _, x := range p.Remotes {
		if x == "old:80" {
			t.Errorf("stale remote kept: %v", p.Remotes)
		}
	}
}

const nettopSample = `,bytes_in,bytes_out,
apsd.376,5670,23348,
mDNSResponder.519,52768938,11854128,
`

func TestSamplerForDarwin(t *testing.T) {
	run := func(name string, _ ...string) ([]byte, error) {
		if name != "nettop" {
			return nil, errors.New("unexpected " + name)
		}
		return []byte(nettopSample), nil
	}
	sample, basis, tool, err := SamplerFor(hostos.Darwin, run)
	if err != nil || basis != BasisNettop || tool != "nettop" {
		t.Fatalf("basis=%q tool=%q err=%v", basis, tool, err)
	}
	cs, err := sample(context.Background())
	if err != nil || len(cs) != 2 || cs[1].PID != 519 || cs[1].In != 52768938 || cs[1].Socket != "" {
		t.Fatalf("counters = %+v err = %v", cs, err)
	}
}

const ssSample = `ESTAB 0 0 192.168.1.10:51000 140.82.112.3:443 users:(("git-remote-http",pid=4242,fd=5))
	 cubic bytes_acked:1200 bytes_received:98000
ESTAB 0 0 127.0.0.1:5432 127.0.0.1:41000 users:(("psql",pid=50,fd=3))
	 cubic bytes_acked:10 bytes_received:20
ESTAB 0 0 [::1]:8080 [::1]:40000 users:(("node",pid=60,fd=3))
	 cubic bytes_acked:10 bytes_received:20
ESTAB 0 0 [2001:db8::5]:40112 [2606:4700::1]:443 users:(("curl",pid=70,fd=3))
	 cubic bytes_acked:5 bytes_received:7
`

func TestSamplerForLinux(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if name != "ss" || len(args) != 1 || args[0] != "-tinpH" {
			return nil, errors.New("unexpected command")
		}
		return []byte(ssSample), nil
	}
	sample, basis, tool, err := SamplerFor(hostos.Linux, run)
	if err != nil || basis != BasisSSTCP || tool != "ss" {
		t.Fatalf("basis=%q tool=%q err=%v", basis, tool, err)
	}
	cs, err := sample(context.Background())
	if err != nil || len(cs) != 2 {
		t.Fatalf("counters = %+v err = %v, want loopback sockets dropped", cs, err)
	}
	if cs[0].PID != 4242 || cs[0].In != 98000 || cs[0].Out != 1200 || cs[0].Remote != "140.82.112.3:443" || cs[0].Socket == "" {
		t.Errorf("counter = %+v", cs[0])
	}
	if cs[1].PID != 70 || cs[1].Remote != "[2606:4700::1]:443" {
		t.Errorf("ipv6 counter = %+v", cs[1])
	}
}

func TestSamplerMissingTool(t *testing.T) {
	run := func(name string, _ ...string) ([]byte, error) {
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	sample, _, _, _ := SamplerFor(hostos.Linux, run)
	if _, err := sample(context.Background()); !errors.Is(err, ErrToolMissing) || !strings.Contains(err.Error(), "ss") {
		t.Fatalf("err = %v", err)
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:80": true, "[::1]:443": true, "[::ffff:127.0.0.1]:22": true,
		"10.0.0.1:443": false, "[fe80::1]%eth0:22": false, "*:*": false,
	} {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestLimitations(t *testing.T) {
	if l := Limitations(BasisSSTCP); len(l) == 0 || !strings.Contains(strings.Join(l, " "), "TCP only") || !strings.Contains(strings.Join(l, " "), "root") {
		t.Errorf("ss limitations = %v", l)
	}
	if l := Limitations(BasisNettop); len(l) == 0 {
		t.Error("nettop limitations empty")
	}
}
