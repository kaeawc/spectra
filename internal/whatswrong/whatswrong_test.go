package whatswrong

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
)

func TestRankCriticalMemoryFirst(t *testing.T) {
	sig := Signals{
		Load: sysload.Load{MemoryPressure: "critical", SwapUsedMB: 9200, SwapTotalMB: 10240},
		Procs: []process.Info{
			{PID: 5, Command: "hog", CPUPct: 95},
		},
	}
	causes := RankCauses(sig)
	if len(causes) < 2 {
		t.Fatalf("expected >=2 causes, got %d: %+v", len(causes), causes)
	}
	if causes[0].Severity != "high" || !strings.Contains(causes[0].Title, "Memory pressure is critical") {
		t.Errorf("first cause = %+v, want high memory pressure", causes[0])
	}
}

func TestRankThermalThrottle(t *testing.T) {
	sig := Signals{
		Power: sysinfo.PowerState{ThermalThrottled: true, PercentThermalThrottled: 42, ThermalPressure: "serious"},
	}
	causes := RankCauses(sig)
	if len(causes) != 1 || causes[0].Severity != "high" || !strings.Contains(causes[0].Title, "throttled") {
		t.Errorf("causes = %+v, want one high thermal-throttle cause", causes)
	}
}

func TestRankCPUHog(t *testing.T) {
	sig := Signals{
		Procs: []process.Info{
			{PID: 1, Command: "idle", CPUPct: 2},
			{PID: 2, Command: "busy", CPUPct: 190},
		},
	}
	causes := RankCauses(sig)
	if len(causes) != 1 || !strings.Contains(causes[0].Detail, "busy (pid 2)") {
		t.Errorf("causes = %+v, want a CPU-hog cause naming busy", causes)
	}
}

func TestRankHealthyIsEmpty(t *testing.T) {
	sig := Signals{
		Load:  sysload.Load{MemoryPressure: "normal", SwapUsedMB: 100},
		Power: sysinfo.PowerState{ThermalPressure: "nominal"},
		Procs: []process.Info{{PID: 1, Command: "chill", CPUPct: 3, RSSKiB: 50000}},
	}
	if c := RankCauses(sig); len(c) != 0 {
		t.Errorf("healthy machine should have no causes, got %+v", c)
	}
}

func TestCollectRunsProbesConcurrently(t *testing.T) {
	var started sync.WaitGroup
	started.Add(3)
	all := make(chan struct{})
	go func() { started.Wait(); close(all) }()
	wait := func() {
		started.Done()
		select {
		case <-all:
		case <-time.After(5 * time.Second):
			t.Error("probes ran sequentially")
		}
	}
	deps := Deps{
		Load:  func(context.Context) sysload.Load { wait(); return sysload.Load{MemoryPressure: "critical"} },
		Power: func(context.Context) sysinfo.PowerState { wait(); return sysinfo.PowerState{} },
		Procs: func(context.Context) []process.Info { wait(); return nil },
	}
	r := Collect(context.Background(), deps)
	if len(r.Causes) != 1 || r.Signals.Load.MemoryPressure != "critical" {
		t.Fatalf("report = %+v", r)
	}
}

type localSrc struct{}

func (localSrc) Current() (hostwatch.Sample, []hostwatch.Alert) {
	return hostwatch.Sample{}, []hostwatch.Alert{{Title: "Disk nearly full", Severity: "warning"}}
}
func (localSrc) Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error) {
	return []hostwatch.Sample{{At: time.Unix(10, 0), Load1: 1.5}}, nil
}

func TestLocalWatchEnriches(t *testing.T) {
	deps := Deps{
		Load:    func(context.Context) sysload.Load { return sysload.Load{} },
		Power:   func(context.Context) sysinfo.PowerState { return sysinfo.PowerState{} },
		Procs:   func(context.Context) []process.Info { return nil },
		Connect: LocalWatch(localSrc{}),
	}
	r := Collect(context.Background(), deps)
	if len(r.DaemonAlerts) != 1 || len(r.Causes) != 1 || !strings.HasPrefix(r.Trend, "load 1.50/") {
		t.Fatalf("report = %+v", r)
	}
}
