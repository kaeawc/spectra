package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
	"github.com/kaeawc/spectra/internal/whatswrong"
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
	deps := whatswrong.Deps{
		Load:  func(context.Context) sysload.Load { return sysload.Load{MemoryPressure: "critical"} },
		Power: func(context.Context) sysinfo.PowerState { return sysinfo.PowerState{} },
		Procs: func(context.Context) []process.Info { return nil },
		Now:   func() time.Time { return now },
	}
	deps.Connect = func(context.Context) (whatswrong.WatchClient, bool) {
		return fakeSlowWatch{alerts: []hostwatch.Alert{{Title: "Memory pressure is critical", Severity: "critical"}, {Title: "High load", Severity: "warning", Detail: "busy"}}, samples: []hostwatch.Sample{{At: now, Load1: 4, MemoryPressure: "warn"}}}, true
	}
	var out, stderr bytes.Buffer
	if code := runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if strings.Count(out.String(), `"title": "Memory pressure is critical"`) != 2 || !strings.Contains(out.String(), `"daemon_alerts"`) || !strings.Contains(out.String(), `"trend"`) {
		t.Fatalf("enriched: %s", out.String())
	}
	deps.Connect = func(context.Context) (whatswrong.WatchClient, bool) { return nil, false }
	out.Reset()
	runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps)
	if strings.Contains(out.String(), `"daemon_alerts"`) || strings.Contains(out.String(), `"trend"`) {
		t.Fatalf("unexpected daemon fields: %s", out.String())
	}
	deps.Connect = func(context.Context) (whatswrong.WatchClient, bool) {
		return fakeSlowWatch{err: errors.New("failed")}, true
	}
	out.Reset()
	runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps)
	if strings.Contains(out.String(), `"daemon_alerts"`) {
		t.Fatalf("error was not ignored: %s", out.String())
	}
	deps.Connect = func(context.Context) (whatswrong.WatchClient, bool) { return fakeSlowWatch{slow: true}, true }
	start := time.Now()
	out.Reset()
	runWhatswrongWithIO([]string{"--json"}, &out, &stderr, deps)
	if time.Since(start) > 1500*time.Millisecond || strings.Contains(out.String(), `"daemon_alerts"`) {
		t.Fatalf("slow fallback took %s: %s", time.Since(start), out.String())
	}
}

func TestRunWhatswrongRenders(t *testing.T) {
	deps := whatswrong.Deps{
		Load: func(context.Context) sysload.Load {
			return sysload.Load{MemoryPressure: "critical", SwapUsedMB: 9000, SwapTotalMB: 10000}
		},
		Power: func(context.Context) sysinfo.PowerState { return sysinfo.PowerState{ThermalPressure: "nominal"} },
		Procs: func(context.Context) []process.Info { return nil },
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
	deps := whatswrong.Deps{
		Load:  func(context.Context) sysload.Load { return sysload.Load{MemoryPressure: "normal"} },
		Power: func(context.Context) sysinfo.PowerState { return sysinfo.PowerState{} },
		Procs: func(context.Context) []process.Info { return nil },
	}
	var out, errBuf bytes.Buffer
	runWhatswrongWithIO(nil, &out, &errBuf, deps)
	if !strings.Contains(out.String(), "Nothing obviously wrong") {
		t.Errorf("healthy output = %q", out.String())
	}
}

func TestRunWhatswrongJSON(t *testing.T) {
	deps := whatswrong.Deps{
		Load:  func(context.Context) sysload.Load { return sysload.Load{MemoryPressure: "warn", SwapUsedMB: 500} },
		Power: func(context.Context) sysinfo.PowerState { return sysinfo.PowerState{} },
		Procs: func(context.Context) []process.Info { return nil },
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

// pinDeps is a fixed input touching every report field; its output was
// captured from the pre-extraction CLI and must not change.
func pinDeps() whatswrong.Deps {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	procs := familyTestProcs([]string{"AAAAAAAA-1111-2222-3333-444444444444"}, 4, 40)
	procs = append(procs, process.Info{PID: 77, PPID: 1, Command: "hog", CPUPct: 120, RSSKiB: 5 * 1024 * 1024, Elapsed: time.Hour})
	return whatswrong.Deps{
		Load: func(context.Context) sysload.Load {
			return sysload.Load{MemoryPressure: "warn", PressureLevel: 2, SwapUsedMB: 3000, SwapTotalMB: 4096}
		},
		Power: func(context.Context) sysinfo.PowerState {
			return sysinfo.PowerState{BatteryPct: 80, ThermalPressure: "serious", ThermalThrottled: true, PercentThermalThrottled: 30,
				Assertions:     []sysinfo.PowerAssertion{{Type: "PreventUserIdleSystemSleep", PID: 5, Name: "caffeinate"}},
				EnergyTopUsers: []sysinfo.EnergyUser{{PID: 77, EnergyImpact: 12.5, Command: "hog"}}}
		},
		Procs: func(context.Context) []process.Info { return procs },
		Now:   func() time.Time { return now },
		CPUs:  func() int { return 16 },
		Connect: func(context.Context) (whatswrong.WatchClient, bool) {
			return fakeSlowWatch{alerts: []hostwatch.Alert{{Title: "CPU is thermally throttled", Severity: "critical"}, {Title: "Disk nearly full", Severity: "warning", Detail: "95% used"}},
				samples: []hostwatch.Sample{{At: now, Load1: 4, Load5: 3, Load15: 2, MemoryPressure: "warn"}}}, true
		},
	}
}

func TestWhatswrongJSONPinned(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "whatswrong.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runWhatswrongWithIO([]string{"--json"}, &out, &stderr, pinDeps()); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("whatswrong --json changed:\n%s", out.String())
	}
}
