package nettop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A reused socket key whose in counter fell but whose out counter grew is a
// new socket: both counters count in full.
func TestSocketReuseResetsBothDirections(t *testing.T) {
	s := newScript(at(c(10, 10000, 1000)), at(c(10, 100, 2000)))
	rep := Summarize(runScript(t, s.config(time.Second, time.Second)), nil, 0)
	p := findProc(rep, 10)
	if p == nil || p.BytesIn != 100 || p.BytesOut != 2000 {
		t.Fatalf("process = %+v, want in 100 out 2000", p)
	}
}

func TestRemotesBasisFor(t *testing.T) {
	if RemotesBasisFor(BasisNettop) != RemotesEndSnapshot || RemotesBasisFor(BasisSSTCP) != RemotesWindowBytes {
		t.Fatal("wrong remotes basis")
	}
}

// A baseline that succeeded followed only by failures measured no interval:
// that is an error naming the tool, not an idle host.
func TestBaselineOnlyIsNoData(t *testing.T) {
	s := newScript(at(c(10, 0, 0)), step{err: errors.New("boom")})
	_, err := Run(context.Background(), s.config(time.Second, 3*time.Second))
	if !errors.Is(err, ErrNoData) || errors.Is(err, ErrInterrupted) || !strings.Contains(err.Error(), "nettop") ||
		!strings.Contains(err.Error(), "no interval was measured") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}

	// Cancelled right after the baseline: interrupted, not a failure.
	ctx, cancel := context.WithCancel(context.Background())
	s = newScript(at(c(10, 0, 0)))
	cfg := s.config(time.Second, 3*time.Second)
	cfg.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	if _, err := Run(ctx, cfg); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("cancelled after baseline: err = %v", err)
	}
}

// A tiny interval jumps past overdue deadlines in one step instead of
// iterating them, and stops at the end of the window.
func TestTinyIntervalSkipsOverdueDeadlinesArithmetically(t *testing.T) {
	s := newScript(step{cs: []Counter{c(1, 0, 0)}, cost: 40 * time.Millisecond}, step{cs: []Counter{c(1, 100, 0)}, cost: 40 * time.Millisecond})
	done := make(chan *Trace, 1)
	go func() {
		tr, err := Run(context.Background(), s.config(time.Nanosecond, 10*time.Second))
		if err != nil {
			t.Error(err)
		}
		done <- tr
	}()
	var tr *Trace
	select {
	case tr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish: overdue deadlines stepped one at a time")
	}
	rep := Summarize(tr, nil, 0)
	// One baseline then one sample per 40ms, the last ending on or past 10s.
	if s.calls != 251 || rep.DurationMS != 10000 {
		t.Fatalf("calls = %d duration = %d", s.calls, rep.DurationMS)
	}
	// Every 1ns deadline but the ones sampled was skipped.
	if want := int64(10*time.Second) - int64(s.calls-1); rep.SamplesSkipped != want {
		t.Errorf("skipped = %d, want %d", rep.SamplesSkipped, want)
	}
}

func TestNextDeadlineClampsToDuration(t *testing.T) {
	r := &run{cfg: Config{Interval: time.Second, Duration: 2500 * time.Millisecond}}
	n := ceilDiv(r.cfg.Duration, r.cfg.Interval)
	if n != 3 || r.deadline(3, n) != 2500*time.Millisecond || r.deadline(2, n) != 2*time.Second {
		t.Fatalf("n = %d", n)
	}
	for now, want := range map[time.Duration]int64{
		0: 1, time.Second: 1, 1500 * time.Millisecond: 2, 2400 * time.Millisecond: 3, 2500 * time.Millisecond: 3, 2600 * time.Millisecond: 4,
	} {
		if got := r.nextDeadline(1, n, now); got != want {
			t.Errorf("nextDeadline(now=%v) = %d, want %d", now, got, want)
		}
	}
}
