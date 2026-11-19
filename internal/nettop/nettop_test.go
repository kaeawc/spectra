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
	"github.com/kaeawc/spectra/internal/proc"
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
	if p.Started || p.Stopped || len(rep.Started) != 0 || len(rep.Stopped) != 0 {
		t.Errorf("unexpected churn: %+v", rep)
	}
}

func TestStartsMidWindow(t *testing.T) {
	steps := []step{at(c(10, 0, 0)), at(c(10, 0, 10)), at(c(10, 0, 20), c(20, 500, 500)), at(c(10, 0, 30), c(20, 600, 600))}
	rep := Summarize(runScript(t, newScript(steps...).config(time.Second, 3*time.Second)), nil, 0)
	p := findProc(rep, 20)
	if p == nil || p.BytesIn != 600 || p.BytesOut != 600 || p.PeakInRate != 500 || !p.Started || p.Stopped {
		t.Fatalf("newcomer = %+v, want its whole counter (it started at 0)", p)
	}
	if len(rep.Started) != 1 || rep.Started[0].PID != 20 {
		t.Errorf("started = %+v", rep.Started)
	}
}

// Started and stopped come from measured activity, not socket presence: a
// socket open throughout that moved bytes only in the middle is both, and
// a new socket that moved nothing (SYN-SENT) is neither.
func TestStartedStoppedFollowActivity(t *testing.T) {
	sock := func(pid int, name string, in int64) Counter {
		return Counter{PID: pid, Command: fmt.Sprintf("p%d", pid), Socket: name, In: in}
	}
	s := newScript(
		at(sock(1, "a", 100), sock(3, "c", 0)),
		at(sock(1, "a", 100), sock(3, "c", 10)),
		at(sock(1, "a", 900), sock(3, "c", 20), sock(2, "syn", 0)),
		at(sock(1, "a", 900), sock(3, "c", 30), sock(2, "syn", 0)),
	)
	rep := Summarize(runScript(t, s.config(time.Second, 3*time.Second)), nil, 0)
	if p := findProc(rep, 1); p == nil || p.BytesIn != 800 || !p.Started || !p.Stopped {
		t.Fatalf("middle-only mover = %+v, want started and stopped", p)
	}
	if findProc(rep, 2) != nil || len(rep.Started) != 1 || rep.Started[0].PID != 1 || len(rep.Stopped) != 1 {
		t.Errorf("started = %+v stopped = %+v, want only pid 1", rep.Started, rep.Stopped)
	}
	if p := findProc(rep, 3); p == nil || p.Started || p.Stopped {
		t.Errorf("steady process = %+v", p)
	}
}

// ss counters start at 0 when a socket opens: a socket first seen after the
// baseline counts in full, one present in the baseline only by its growth.
func TestLinuxFirstSeenSocketCountsFully(t *testing.T) {
	sock := func(name string, in, out int64) Counter {
		return Counter{PID: 7, Command: "git", Socket: name, Remote: name, In: in, Out: out}
	}
	s := newScript(
		at(sock("old", 10_000, 1_000)),
		at(sock("old", 10_500, 1_100), sock("new", 4_000, 40)),
		at(sock("old", 10_700, 1_100), sock("new", 4_500, 50)),
	)
	rep := Summarize(runScript(t, s.config(time.Second, 2*time.Second)), nil, 0)
	p := findProc(rep, 7)
	if p == nil || p.BytesIn != 700+4_500 || p.BytesOut != 100+50 {
		t.Fatalf("process = %+v, want baseline socket's growth (700/100) plus the new socket's whole counter (4500/50)", p)
	}
	if p.Started {
		t.Errorf("a new socket of a process already moving bytes is not a start: %+v", p)
	}
}

func TestSharedSocketReattributedIsNotDoubleCounted(t *testing.T) {
	s := newScript(
		at(Counter{PID: 5, Socket: "s", In: 1000}),
		at(Counter{PID: 5, Socket: "s", In: 1200}),
		at(Counter{PID: 9, Socket: "s", In: 1500}), // pid 5 exited; ss now names 9
	)
	rep := Summarize(runScript(t, s.config(time.Second, 2*time.Second)), nil, 0)
	if rep.Totals.BytesIn != 500 {
		t.Fatalf("totals = %+v, want 200 + 300", rep.Totals)
	}
	if p := findProc(rep, 9); p == nil || p.BytesIn != 300 {
		t.Errorf("new owner = %+v, want only the growth", p)
	}
}

func TestStopsMidWindow(t *testing.T) {
	s := newScript(at(c(10, 0, 0), c(30, 0, 0)), at(c(10, 0, 5), c(30, 400, 0)), at(c(10, 0, 9)))
	rep := Summarize(runScript(t, s.config(time.Second, 2*time.Second)), nil, 0)
	p := findProc(rep, 30)
	if p == nil || p.BytesIn != 400 || !p.Stopped {
		t.Fatalf("leaver = %+v", p)
	}
	if p.AvgInRate != 200 || p.PeakInRate != 400 {
		t.Errorf("leaver rates avg=%v peak=%v, want 200 over the window and 400 peak", p.AvgInRate, p.PeakInRate)
	}
	if len(rep.Stopped) != 1 || rep.Stopped[0].Command != "p30" {
		t.Errorf("stopped = %+v", rep.Stopped)
	}
}

func TestCounterReset(t *testing.T) {
	s := newScript(at(c(10, 1000, 0)), at(c(10, 2000, 0)), at(c(10, 100, 0)), at(c(10, 300, 0)))
	rep := Summarize(runScript(t, s.config(time.Second, 3*time.Second)), nil, 0)
	if p := findProc(rep, 10); p == nil || p.BytesIn != 1300 {
		t.Fatalf("reset process = %+v, want 1000 + 100 (counted from 0) + 200", p)
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
	if p := findProc(rep, 5); p == nil || p.BytesIn != 9150 {
		t.Fatalf("process = %+v, want 50 (a) + 9000 (b first seen) + 100 (b)", p)
	}
}

func TestStartTimeDistinguishesReusedPID(t *testing.T) {
	s := newScript(at(Counter{PID: 7, Start: 100, In: 500}), at(Counter{PID: 7, Start: 200, In: 900}))
	rep := Summarize(runScript(t, s.config(time.Second, time.Second)), nil, 0)
	if rep.Totals.BytesIn != 900 {
		t.Fatalf("totals = %+v, want the new process's own 900, not 400 diffed against its predecessor", rep.Totals)
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

func TestIdleHostIsNotAnError(t *testing.T) {
	s := newScript(step{})
	tr, err := Run(context.Background(), s.config(time.Second, 3*time.Second))
	if err != nil {
		t.Fatalf("empty samples are a valid idle observation: %v", err)
	}
	rep := Summarize(tr, nil, 0)
	if rep.Samples != 4 || rep.SamplesFailed != 0 || len(rep.Processes) != 0 || rep.DurationMS != 3000 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestEverySampleFailed(t *testing.T) {
	s := newScript(step{err: errors.New("boom")})
	_, err := Run(context.Background(), s.config(time.Second, 2*time.Second))
	if !errors.Is(err, ErrNoData) || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "3 attempts") {
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
	both := s.config(time.Second, time.Second)
	both.Measure = func(context.Context, time.Duration) ([]Counter, error) { return nil, nil }
	if _, err := Run(context.Background(), both); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("both sources: err = %v", err)
	}
	both.Sample, both.Measure = nil, nil
	if _, err := Run(context.Background(), both); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("no source: err = %v", err)
	}
}

// Remotes are ranked by the bytes they moved inside the window: a socket
// that moved 8 MB and closed before the last poll is kept, and one whose
// large counter predates the window but stayed idle is dropped.
func TestRemotesRankedByWindowBytes(t *testing.T) {
	r := func(remote string, in int64) Counter {
		return Counter{PID: 3, Socket: remote, Remote: remote, In: in}
	}
	s := newScript(
		at(r("idle:443", 10_000_000), r("bulk:443", 0), r("small:443", 0)),
		at(r("idle:443", 10_000_000), r("bulk:443", 8_000_000), r("small:443", 100)),
		at(r("idle:443", 10_000_000), r("small:443", 300), r("late:443", 50)),
	)
	rep := Summarize(runScript(t, s.config(time.Second, 2*time.Second)), nil, 0)
	p := findProc(rep, 3)
	want := []string{"bulk:443", "small:443", "late:443"}
	if p == nil || strings.Join(p.Remotes, ",") != strings.Join(want, ",") {
		t.Fatalf("remotes = %+v, want %v by window bytes", p, want)
	}
}

const ssSample = `ESTAB 0 0 192.168.1.10:51000 140.82.112.3:443 users:(("git-remote-http",pid=4242,fd=5)) uid:1000 ino:58925 sk:3c cgroup:/user.slice <->
	 cubic bytes_acked:1200 bytes_received:98000
ESTAB 0 0 127.0.0.1:5432 127.0.0.1:41000 users:(("psql",pid=50,fd=3)) uid:1000 ino:58926 sk:3d <->
	 cubic bytes_acked:10 bytes_received:20
ESTAB 0 0 [::1]:8080 [::1]:40000 users:(("node",pid=60,fd=3)) uid:1000 ino:58927 sk:3e <->
	 cubic bytes_acked:10 bytes_received:20
ESTAB 0 0 [2001:db8::5]:40112 [2606:4700::1]:443 users:(("curl",pid=70,fd=3))
	 cubic bytes_acked:5 bytes_received:7
`

// seqRunner returns its outputs in turn (the last repeats); during, if set,
// runs inside each call and may replace its result.
type seqRunner struct {
	outputs []string
	calls   int
	during  func(ctx context.Context, call int) error
}

func (s *seqRunner) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	call := s.calls
	s.calls++
	if cmd.Name != "ss" || strings.Join(cmd.Args, " ") != "-tinpeH" {
		return proc.Result{ExitCode: -1}, errors.New("unexpected command")
	}
	if s.during != nil {
		if err := s.during(ctx, call); err != nil {
			return proc.Result{ExitCode: -1}, err
		}
	}
	return proc.Result{Stdout: []byte(s.outputs[min(call, len(s.outputs)-1)])}, nil
}

func TestSamplerForLinux(t *testing.T) {
	src, err := SourceFor(hostos.Linux, &seqRunner{outputs: []string{ssSample}})
	if err != nil || src.Basis != BasisSSTCP || src.Tool != "ss" || src.Sample == nil || src.Measure != nil || src.Step != 0 || src.MinInterval != MinSSInterval {
		t.Fatalf("source = %+v err = %v", src, err)
	}
	cs, err := src.Sample(context.Background())
	if err != nil || len(cs) != 2 {
		t.Fatalf("counters = %+v err = %v, want loopback sockets dropped", cs, err)
	}
	if cs[0].PID != 4242 || cs[0].In != 98000 || cs[0].Out != 1200 || cs[0].Remote != "140.82.112.3:443" || cs[0].Socket != "sk:3c" {
		t.Errorf("counter = %+v, want the sk: cookie as socket identity", cs[0])
	}
	if cs[1].PID != 70 || cs[1].Remote != "[2606:4700::1]:443" || cs[1].Socket != "[2001:db8::5]:40112->[2606:4700::1]:443" {
		t.Errorf("ipv6 counter without sk: = %+v, want the endpoint key", cs[1])
	}
}

// A new connection reusing a closed one's endpoints has a new cookie, so
// its whole counter counts even when it is higher than the old one's; a
// socket that moves to another pid keeps its cookie and counts once.
func TestSSCookieIdentifiesSocket(t *testing.T) {
	line := func(pid int, sk string, in int64) string {
		return fmt.Sprintf("ESTAB 0 0 10.0.0.5:51000 140.82.112.3:443 users:((\"git\",pid=%d,fd=5)) uid:1000 ino:1 sk:%s <->\n\t cubic bytes_acked:0 bytes_received:%d\n", pid, sk, in)
	}
	sr := &seqRunner{outputs: []string{line(5, "a1", 1000), line(5, "a2", 1500), line(9, "a2", 1700)}}
	src, _ := SourceFor(hostos.Linux, sr)
	s := newScript()
	cfg := s.config(time.Second, 2*time.Second)
	src.Configure(&cfg)
	rep := Summarize(runScript(t, cfg), nil, 0)
	if p := findProc(rep, 5); p == nil || p.BytesIn != 1500 {
		t.Fatalf("pid 5 = %+v, want the replacement socket's whole 1500", p)
	}
	if p := findProc(rep, 9); p == nil || p.BytesIn != 200 || rep.Totals.BytesIn != 1700 {
		t.Fatalf("pid 9 = %+v totals = %+v, want only the shared socket's growth", p, rep.Totals)
	}
}

// Ctrl-C during a slow ss ends the poll at once: the completed interval is
// kept and the interrupted poll is neither a sample nor a failure. Each
// poll also runs under its own timeout.
func TestSSPollFollowsContext(t *testing.T) {
	ss := func(in int64) string {
		return fmt.Sprintf("ESTAB 0 0 10.0.0.5:51000 140.82.112.3:443 users:((\"git\",pid=5,fd=5))\n\t cubic bytes_received:%d\n", in)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sr := &seqRunner{outputs: []string{ss(0), ss(100)}}
	sr.during = func(ctx context.Context, call int) error {
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > ssTimeout {
			t.Errorf("poll %d deadline = %v %v, want within %v", call, dl, ok, ssTimeout)
		}
		if call == 2 {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	src, _ := SourceFor(hostos.Linux, sr)
	s := newScript()
	cfg := s.config(time.Second, 10*time.Second)
	src.Configure(&cfg)
	tr, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep := Summarize(tr, nil, 0)
	if rep.Samples != 2 || rep.SamplesFailed != 0 || rep.Totals.BytesIn != 100 || sr.calls != 3 {
		t.Fatalf("report = %+v calls = %d", rep, sr.calls)
	}
}

func TestSamplerMissingTool(t *testing.T) {
	missing := proc.NewFake().On(proc.MatchPrefix("ss"), proc.Response{Err: &exec.Error{Name: "ss", Err: exec.ErrNotFound}})
	src, _ := SourceFor(hostos.Linux, missing)
	if _, err := src.Sample(context.Background()); !errors.Is(err, ErrToolMissing) || !strings.Contains(err.Error(), "ss") {
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
	if l := strings.Join(Limitations(BasisNettop), " "); !strings.Contains(l, "whole seconds") || !strings.Contains(l, "between runs") {
		t.Errorf("nettop limitations = %v", l)
	}
	if l := strings.Join(Limitations(BasisSSTCP), " "); !strings.Contains(l, "first seen after the baseline sample counts its whole counter") {
		t.Errorf("ss limitations = %v", l)
	}
}
