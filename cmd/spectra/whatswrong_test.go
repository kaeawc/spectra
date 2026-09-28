package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
)

type fakeSlowWatch struct {
	alerts  []hostwatch.Alert
	samples []hostwatch.Sample
	err     error
	slow    bool
}

func (f fakeSlowWatch) Current(ctx context.Context) (hostwatch.Sample, []hostwatch.Alert, error) {
	if f.slow {
		<-ctx.Done()
		return hostwatch.Sample{}, nil, ctx.Err()
	}
	return hostwatch.Sample{}, f.alerts, f.err
}
func (f fakeSlowWatch) Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error) {
	return f.samples, f.err
}
func (fakeSlowWatch) Close() error { return nil }

func TestWhatswrongDaemonEnrichmentAndFallback(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	deps := slowDeps{
		load:  func() sysload.Load { return sysload.Load{MemoryPressure: "critical"} },
		power: func() sysinfo.PowerState { return sysinfo.PowerState{} },
		procs: func() []process.Info { return nil },
		now:   func() time.Time { return now },
	}
	deps.connect = func(context.Context) (slowWatchClient, bool) {
		return fakeSlowWatch{alerts: []hostwatch.Alert{{Title: "Memory pressure is critical", Severity: "critical"}, {Title: "High load", Severity: "warning", Detail: "busy"}}, samples: []hostwatch.Sample{{At: now, Load1: 4, MemoryPressure: "warn"}}}, true
	}
	var out, stderr bytes.Buffer
	if code := runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if strings.Count(out.String(), `"title": "Memory pressure is critical"`) != 2 || !strings.Contains(out.String(), `"daemon_alerts"`) || !strings.Contains(out.String(), `"trend"`) {
		t.Fatalf("enriched: %s", out.String())
	}
	deps.connect = func(context.Context) (slowWatchClient, bool) { return nil, false }
	out.Reset()
	runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps)
	if strings.Contains(out.String(), `"daemon_alerts"`) || strings.Contains(out.String(), `"trend"`) {
		t.Fatalf("unexpected daemon fields: %s", out.String())
	}
	deps.connect = func(context.Context) (slowWatchClient, bool) { return fakeSlowWatch{err: errors.New("failed")}, true }
	out.Reset()
	runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps)
	if strings.Contains(out.String(), `"daemon_alerts"`) {
		t.Fatalf("error was not ignored: %s", out.String())
	}
	deps.connect = func(context.Context) (slowWatchClient, bool) { return fakeSlowWatch{slow: true}, true }
	start := time.Now()
	out.Reset()
	runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps)
	if time.Since(start) > 1500*time.Millisecond || strings.Contains(out.String(), `"daemon_alerts"`) {
		t.Fatalf("slow fallback took %s: %s", time.Since(start), out.String())
	}
}

func TestRankCriticalMemoryFirst(t *testing.T) {
	sig := slowSignals{
		Load: sysload.Load{MemoryPressure: "critical", SwapUsedMB: 9200, SwapTotalMB: 10240},
		Procs: []process.Info{
			{PID: 5, Command: "hog", CPUPct: 95},
		},
	}
	causes := rankSlowCauses(sig)
	if len(causes) < 2 {
		t.Fatalf("expected >=2 causes, got %d: %+v", len(causes), causes)
	}
	if causes[0].Severity != "high" || !strings.Contains(causes[0].Title, "Memory pressure is critical") {
		t.Errorf("first cause = %+v, want high memory pressure", causes[0])
	}
}

func TestRankThermalThrottle(t *testing.T) {
	sig := slowSignals{
		Power: sysinfo.PowerState{ThermalThrottled: true, PercentThermalThrottled: 42, ThermalPressure: "serious"},
	}
	causes := rankSlowCauses(sig)
	if len(causes) != 1 || causes[0].Severity != "high" || !strings.Contains(causes[0].Title, "throttled") {
		t.Errorf("causes = %+v, want one high thermal-throttle cause", causes)
	}
}

func TestRankCPUHog(t *testing.T) {
	sig := slowSignals{
		Procs: []process.Info{
			{PID: 1, Command: "idle", CPUPct: 2},
			{PID: 2, Command: "busy", CPUPct: 190},
		},
	}
	causes := rankSlowCauses(sig)
	if len(causes) != 1 || !strings.Contains(causes[0].Detail, "busy (pid 2)") {
		t.Errorf("causes = %+v, want a CPU-hog cause naming busy", causes)
	}
}

func TestRankHealthyIsEmpty(t *testing.T) {
	sig := slowSignals{
		Load:  sysload.Load{MemoryPressure: "normal", SwapUsedMB: 100},
		Power: sysinfo.PowerState{ThermalPressure: "nominal"},
		Procs: []process.Info{{PID: 1, Command: "chill", CPUPct: 3, RSSKiB: 50000}},
	}
	if c := rankSlowCauses(sig); len(c) != 0 {
		t.Errorf("healthy machine should have no causes, got %+v", c)
	}
}

func TestRunWhatswrongRenders(t *testing.T) {
	deps := slowDeps{
		load: func() sysload.Load {
			return sysload.Load{MemoryPressure: "critical", SwapUsedMB: 9000, SwapTotalMB: 10000}
		},
		power: func() sysinfo.PowerState { return sysinfo.PowerState{ThermalPressure: "nominal"} },
		procs: func() []process.Info { return nil },
	}
	var out, errBuf bytes.Buffer
	if code := runWhatswrongWithIO(nil, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "Memory pressure is critical") {
		t.Errorf("output = %q", out.String())
	}
}

func TestRunWhatswrongHealthy(t *testing.T) {
	deps := slowDeps{
		load:  func() sysload.Load { return sysload.Load{MemoryPressure: "normal"} },
		power: func() sysinfo.PowerState { return sysinfo.PowerState{} },
		procs: func() []process.Info { return nil },
	}
	var out, errBuf bytes.Buffer
	runWhatswrongWithIO(nil, &out, &errBuf, deps)
	if !strings.Contains(out.String(), "Nothing obviously wrong") {
		t.Errorf("healthy output = %q", out.String())
	}
}

func TestRunWhatswrongJSON(t *testing.T) {
	deps := slowDeps{
		load:  func() sysload.Load { return sysload.Load{MemoryPressure: "warn", SwapUsedMB: 500} },
		power: func() sysinfo.PowerState { return sysinfo.PowerState{} },
		procs: func() []process.Info { return nil },
	}
	var out, errBuf bytes.Buffer
	runWhatswrongWithIO([]string{"--json"}, &out, &errBuf, deps)
	s := out.String()
	// causes plus the full raw signals (load + power) so the ranking is auditable
	for _, want := range []string{`"severity": "medium"`, `"signals"`, `"load"`, `"power"`} {
		if !strings.Contains(s, want) {
			t.Errorf("json missing %q; got:\n%s", want, s)
		}
	}
}
