package nettop

import (
	"context"
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

// measured is one scripted measuring run: the bytes moved in it (or an
// error) and how far past its interval it runs on the fake clock.
type measured struct {
	cs   []Counter
	err  error
	over time.Duration
}

type measureScript struct {
	clk   *clock.Fake
	runs  []measured
	asked []time.Duration
}

func newMeasureScript(runs ...measured) *measureScript {
	return &measureScript{clk: clock.NewFake(time.Unix(1_700_000_000, 0)), runs: runs}
}

func (m *measureScript) measure(_ context.Context, d time.Duration) ([]Counter, error) {
	r := m.runs[min(len(m.asked), len(m.runs)-1)]
	m.asked = append(m.asked, d)
	if r.err == nil {
		m.clk.Advance(d + r.over)
	}
	return r.cs, r.err
}

func (m *measureScript) config(interval, duration time.Duration) Config {
	return Config{
		Measure: m.measure, Tool: "nettop", Clock: m.clk, Interval: interval, Duration: duration,
		Sleep: func(_ context.Context, d time.Duration) error { m.clk.Advance(d); return nil },
	}
}

func moved(cs ...Counter) measured { return measured{cs: cs} }

// A long-lived process whose connections close mid-window: nettop's delta
// block still reports what every flow moved in the interval, including the
// flows that closed, so nothing is discarded (the old cumulative rows
// dropped when a flow closed and the interval counted 0).
func TestMeasureFlowsClosingMidWindowStillCount(t *testing.T) {
	m := newMeasureScript(
		moved(c(10, 500, 50), c(11, 70, 0)),
		moved(c(10, 800, 80)), // three short HTTPS requests opened and closed here
		moved(c(10, 300, 30), c(20, 4000, 10)),
	)
	var ticks []Tick
	cfg := m.config(time.Second, 3*time.Second)
	cfg.Emit = func(tk Tick) { ticks = append(ticks, tk) }
	rep := Summarize(runScript(t, cfg), nil, 0)
	p := findProc(rep, 10)
	if p == nil || p.BytesIn != 1600 || p.BytesOut != 160 || p.PeakInRate != 800 {
		t.Fatalf("long-lived process = %+v, want every interval counted", p)
	}
	if p.Started || p.Stopped {
		t.Errorf("active in every interval: %+v", p)
	}
	if q := findProc(rep, 20); q == nil || q.BytesIn != 4000 || !q.Started {
		t.Errorf("newcomer = %+v, want its first interval counted in full", q)
	}
	if rep.Samples != 4 || rep.DurationMS != 3000 || len(ticks) != 3 || ticks[1].InRate != 800 || ticks[2].Active != 2 {
		t.Errorf("samples=%d duration=%d ticks=%+v", rep.Samples, rep.DurationMS, ticks)
	}
	if len(rep.Stopped) != 1 || rep.Stopped[0].PID != 11 {
		t.Errorf("stopped = %+v", rep.Stopped)
	}
}

// A failed last run is not evidence that anything stopped; a successful
// empty run is.
func TestMeasureFailedLastRunIsNotAbsence(t *testing.T) {
	m := newMeasureScript(moved(c(1, 100, 0), c(2, 50, 0)), moved(c(1, 100, 0), c(2, 50, 0)), measured{err: errors.New("flaky")})
	rep := Summarize(runScript(t, m.config(time.Second, 3*time.Second)), nil, 0)
	if rep.SamplesFailed != 1 || rep.DurationMS != 3000 || len(rep.Stopped) != 0 || len(rep.Started) != 0 {
		t.Fatalf("failed last run: report = %+v, want no process marked stopped", rep)
	}

	m = newMeasureScript(moved(c(1, 100, 0), c(2, 50, 0)), moved(c(1, 100, 0)), moved())
	rep = Summarize(runScript(t, m.config(time.Second, 3*time.Second)), nil, 0)
	if len(rep.Stopped) != 2 || rep.Stopped[0].PID != 1 || rep.Stopped[1].PID != 2 {
		t.Fatalf("empty last run: stopped = %+v, want both", rep.Stopped)
	}

	// A failed first run does not make the first real activity a start.
	m = newMeasureScript(measured{err: errors.New("flaky")}, moved(c(1, 100, 0)), moved(c(1, 100, 0)))
	rep = Summarize(runScript(t, m.config(time.Second, 3*time.Second)), nil, 0)
	if len(rep.Started) != 0 || rep.Totals.BytesIn != 200 {
		t.Fatalf("failed first run: report = %+v", rep)
	}
}

func TestMeasureClampsLastInterval(t *testing.T) {
	m := newMeasureScript(moved(c(1, 10, 0)))
	runScript(t, m.config(time.Second, 2500*time.Millisecond))
	if fmt.Sprint(m.asked) != "[1s 1s 500ms]" {
		t.Fatalf("intervals = %v", m.asked)
	}
}

func TestMeasureOverrunUsesActualElapsed(t *testing.T) {
	m := newMeasureScript(measured{cs: []Counter{c(1, 1100, 0)}, over: 100 * time.Millisecond}, moved(c(1, 1000, 0)))
	rep := Summarize(runScript(t, m.config(time.Second, 2*time.Second)), nil, 0)
	if len(m.asked) != 2 || rep.DurationMS != 2100 {
		t.Fatalf("runs=%d duration=%d, want 2 runs over 2.1s", len(m.asked), rep.DurationMS)
	}
	if p := findProc(rep, 1); p == nil || p.PeakInRate != 1000 {
		t.Fatalf("process = %+v, want 1100 B over 1.1s = 1000 B/s", p)
	}
}

func TestMeasureIdleIsNotAnError(t *testing.T) {
	m := newMeasureScript(moved())
	tr, err := Run(context.Background(), m.config(time.Second, 2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if rep := Summarize(tr, nil, 0); rep.Samples != 3 || len(rep.Processes) != 0 || rep.SamplesFailed != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestMeasureFailureKeepsCadence(t *testing.T) {
	m := newMeasureScript(moved(c(1, 100, 0)), measured{err: errors.New("flaky")}, moved(c(1, 300, 0)))
	rep := Summarize(runScript(t, m.config(time.Second, 3*time.Second)), nil, 0)
	if rep.SamplesFailed != 1 || rep.Samples != 4 || rep.DurationMS != 3000 {
		t.Fatalf("failed=%d samples=%d duration=%d", rep.SamplesFailed, rep.Samples, rep.DurationMS)
	}
	if p := findProc(rep, 1); p == nil || p.BytesIn != 400 || p.PeakInRate != 300 {
		t.Fatalf("process = %+v", p)
	}
}

func TestMeasureEveryRunFailed(t *testing.T) {
	m := newMeasureScript(measured{err: errors.New("boom")})
	_, err := Run(context.Background(), m.config(time.Second, 3*time.Second))
	if !errors.Is(err, ErrNoData) || !strings.Contains(err.Error(), "boom") || len(m.asked) != 3 {
		t.Fatalf("err = %v runs = %d", err, len(m.asked))
	}
}

func TestMeasureToolMissingFailsFast(t *testing.T) {
	m := newMeasureScript(measured{err: toolErr(&exec.Error{Name: "nettop", Err: exec.ErrNotFound})})
	_, err := Run(context.Background(), m.config(time.Second, 10*time.Second))
	if !errors.Is(err, ErrToolMissing) || !strings.Contains(err.Error(), "nettop") || len(m.asked) != 1 {
		t.Fatalf("err = %v runs = %d", err, len(m.asked))
	}
}

func TestMeasureCancelDropsInterruptedInterval(t *testing.T) {
	m := newMeasureScript(moved(c(1, 100, 0)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := m.config(time.Second, 10*time.Second)
	runs := 0
	cfg.Measure = func(ctx context.Context, d time.Duration) ([]Counter, error) {
		runs++
		if runs == 3 {
			m.clk.Advance(d / 2)
			cancel()
			return nil, ctx.Err()
		}
		return m.measure(ctx, d)
	}
	tr, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep := Summarize(tr, nil, 0)
	if rep.Samples != 3 || rep.DurationMS != 2000 || rep.Totals.BytesIn != 200 || rep.SamplesFailed != 0 {
		t.Fatalf("report = %+v", rep)
	}

	early := newMeasureScript(moved())
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := Run(ctx2, early.config(time.Second, time.Second)); !errors.Is(err, ErrNoData) || !errors.Is(err, ErrInterrupted) || !strings.Contains(err.Error(), "before the first interval") {
		t.Fatalf("cancelled before any interval: err = %v", err)
	}
}

// nettopRun is real `nettop -P -x -d -L 2 -s 1 -J bytes_in,bytes_out -t
// external` output (trimmed): the first block sums each process's open
// flows, the second is what moved during the delay.
const nettopRun = `,bytes_in,bytes_out,
apsd.376,5694,23492,
mDNSResponder.519,52868346,11865217,
ssh.8112,10190,4406,
curl.8183,2776036,519,
,bytes_in,bytes_out,
apsd.376,0,0,
mDNSResponder.519,3023,576,
ssh.8112,124,96,
curl.8183,233783,0,
`

func TestSourceForDarwinMeasuresWithNettop(t *testing.T) {
	f := proc.NewFake().On(proc.MatchPrefix("nettop", "-P", "-x", "-d", "-L", "2", "-s", "2"),
		proc.Response{Result: proc.Result{Stdout: []byte(nettopRun)}})
	src, err := SourceFor(hostos.Darwin, f)
	if err != nil || src.Basis != BasisNettop || src.Tool != "nettop" || src.Measure == nil || src.Sample != nil || src.Step != time.Second {
		t.Fatalf("source = %+v err = %v", src, err)
	}
	cs, err := src.Measure(context.Background(), 2*time.Second)
	if err != nil || len(cs) != 4 {
		t.Fatalf("counters = %+v err = %v", cs, err)
	}
	if cs[3].PID != 8183 || cs[3].Command != "curl" || cs[3].In != 233783 || cs[3].Socket != "" {
		t.Errorf("curl = %+v, want the interval's delta, not the open-flow total", cs[3])
	}

	missing := proc.NewFake().On(proc.MatchPrefix("nettop"), proc.Response{Err: &exec.Error{Name: "nettop", Err: exec.ErrNotFound}})
	src, _ = SourceFor(hostos.Darwin, missing)
	if _, err := src.Measure(context.Background(), time.Second); !errors.Is(err, ErrToolMissing) {
		t.Errorf("missing nettop: err = %v", err)
	}
}

func TestSourceRoundAndConfigure(t *testing.T) {
	sec := Source{Step: time.Second}
	for in, want := range map[time.Duration]time.Duration{
		500 * time.Millisecond: time.Second, time.Second: time.Second, 1500 * time.Millisecond: 2 * time.Second, 0: 0,
	} {
		if got := sec.Round(in); got != want {
			t.Errorf("Round(%v) = %v, want %v", in, got, want)
		}
	}
	if got := (Source{}).Round(1500 * time.Millisecond); got != 1500*time.Millisecond {
		t.Errorf("no step: Round = %v", got)
	}
	var cfg Config
	m := func(context.Context, time.Duration) ([]Counter, error) { return nil, nil }
	Source{Tool: "nettop", Measure: m}.Configure(&cfg)
	if cfg.Tool != "nettop" || cfg.Measure == nil || cfg.Sample != nil {
		t.Errorf("configured = %+v", cfg)
	}
}
