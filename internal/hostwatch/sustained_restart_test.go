package hostwatch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/proc"
)

// A stale sample.At (collection and persistence took longer than the 3s start
// tolerance) must not make an unchanged process look replaced.
func TestSustainedTickUsesFreshTimestamp(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	clk := clock.NewFake(svcT0)
	r := &scriptRunner{sampleOut: "x", ps: func() string {
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n", fmtEtime(clk.Now().Sub(start)))
	}}
	samples := make([]Sample, 30)
	for i := range samples {
		samples[i] = Sample{At: svcT0, DataFreeGB: 100} // stale: never advances
	}
	svc := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, Clock: clk, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{samples: samples}})
	for i := 0; i < 22; i++ {
		clk.Advance(time.Minute)
		if err := svc.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := firingKeys(t, svc); len(got) != 1 {
		t.Fatalf("stale sample.At discarded the process history: %+v", got)
	}
}

// restartedService builds a second service over the same store, as after a
// daemon restart, with a fake clock at now.
func restartedService(t *testing.T, first *Service, r *scriptRunner, now time.Time) (*Service, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(now)
	svc := NewService(ServiceOptions{Store: first.opts.Store, Runner: r, OS: hostos.Darwin, Clock: clk, IDs: idgen.NewSequence("b"), Collector: &sequenceCollector{}})
	if err := svc.restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	return svc, clk
}

func TestSustainedRestoredAlertSurvivesWarmupWhileBusy(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	var now time.Time
	cpu := 90.0
	r := &scriptRunner{sampleOut: "x", ps: func() string {
		return fmt.Sprintf("412 1 %.1f %s /usr/libexec/diagnosticd\n", cpu, fmtEtime(now.Sub(start)))
	}}
	first := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	for m := 0; m <= 20; m++ {
		now = minutes(m)
		first.TickSustained(context.Background(), now)
	}
	before := firingKeys(t, first)
	if len(before) != 1 {
		t.Fatalf("setup: %+v", before)
	}
	second, _ := restartedService(t, first, r, minutes(21))
	for m := 21; m <= 30; m++ {
		now = minutes(m)
		second.TickSustained(context.Background(), now)
	}
	after := firingKeys(t, second)
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("restored alert changed across restart: before %+v after %+v", before, after)
	}
	all, _ := second.Alerts(context.Background(), "all", 10)
	if len(all) != 1 {
		t.Fatalf("duplicate alert after restart: %+v", all)
	}
}

func TestSustainedRestoredAlertResolvesWhenIdleOrGone(t *testing.T) {
	for _, mode := range []string{"idle", "gone"} {
		t.Run(mode, func(t *testing.T) {
			start := svcT0.Add(-5 * time.Hour)
			var now time.Time
			cpu, alive := 90.0, true
			r := &scriptRunner{sampleOut: "x", ps: func() string {
				if !alive {
					return "1 0 0.0 9-00:00:00 /sbin/launchd\n"
				}
				return fmt.Sprintf("412 1 %.1f %s /usr/libexec/diagnosticd\n", cpu, fmtEtime(now.Sub(start)))
			}}
			first := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
			for m := 0; m <= 20; m++ {
				now = minutes(m)
				first.TickSustained(context.Background(), now)
			}
			second, _ := restartedService(t, first, r, minutes(21))
			cpu, alive = 1, mode == "idle"
			for m := 21; m <= 21+sustainedMinClearPoints+3; m++ {
				now = minutes(m)
				second.TickSustained(context.Background(), now)
			}
			if got := firingKeys(t, second); len(got) != 0 {
				t.Fatalf("restored alert never resolved: %+v", got)
			}
			if resolved, _ := second.Alerts(context.Background(), "resolved", 10); len(resolved) != 1 {
				t.Fatalf("resolved alerts: %+v", resolved)
			}
		})
	}
}

func TestSustainedProbeFailureKeepsAlertsAndIDs(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	var now time.Time
	fail := false
	psRun := func() string {
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n", fmtEtime(now.Sub(start)))
	}
	inner := &scriptRunner{sampleOut: "x", ps: psRun}
	r := runnerFunc(func(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
		if fail && cmd.Name == "ps" {
			return proc.Result{ExitCode: 1}, errors.New("ps failed")
		}
		return inner.Run(ctx, cmd)
	})
	svc := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	for m := 0; m <= 20; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	before := firingKeys(t, svc)
	if len(before) != 1 {
		t.Fatalf("setup: %+v", before)
	}
	fail = true
	for m := 21; m <= 26; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	if got := firingKeys(t, svc); len(got) != 1 || got[0].ID != before[0].ID {
		t.Fatalf("ps failure resolved the alert: %+v", got)
	}
	fail = false
	for m := 27; m <= 32; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	got := firingKeys(t, svc)
	all, _ := svc.Alerts(context.Background(), "all", 10)
	if len(got) != 1 || got[0].ID != before[0].ID || len(all) != 1 {
		t.Fatalf("alert refired under a new ID: firing %+v all %+v", got, all)
	}
}

type runnerFunc func(context.Context, proc.Cmd) (proc.Result, error)

func (f runnerFunc) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) { return f(ctx, cmd) }
