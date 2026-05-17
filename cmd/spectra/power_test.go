package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/helperclient"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
)

func fakeProcs(items map[int]string) func(context.Context) []process.Info {
	return func(context.Context) []process.Info {
		out := make([]process.Info, 0, len(items))
		for pid, cmd := range items {
			out = append(out, process.Info{PID: pid, Command: cmd})
		}
		return out
	}
}

func fakePowerDeps() powerDeps {
	return powerDeps{
		collect: fakePowerState,
		sample: func(time.Duration) (sysinfo.SoCPower, error) {
			return sysinfo.SoCPower{}, sysinfo.ErrUnsupportedHardware
		},
		clock: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		signalCh: func() (<-chan os.Signal, func()) {
			ch := make(chan os.Signal)
			return ch, func() {}
		},
	}
}

func fakePowerState() sysinfo.PowerState {
	return sysinfo.PowerState{
		OnBattery:               true,
		BatteryPct:              85,
		ThermalPressure:         "serious",
		ThermalThrottled:        true,
		CPUSpeedLimitPct:        92,
		LowestCPUSpeedLimitPct:  90,
		AverageCPUSpeedLimitPct: 94,
		PercentThermalThrottled: 66,
		CPUSpeedLimitSamples:    []int{90, 92, 100},
		Assertions: []sysinfo.PowerAssertion{
			{Type: "PreventUserIdleSleep", PID: 412, Name: "playing audio"},
		},
		EnergyTopUsers: []sysinfo.EnergyUser{
			{PID: 501, EnergyImpact: 4.2, Command: "Google Chrome Helper"},
		},
	}
}

func TestRunPowerHumanOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO(nil, &stdout, &stderr, fakePowerDeps())
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"=== Power state ===",
		"source:    battery (85%)",
		"thermal:   serious",
		"cpu limit: 92%",
		"throttled: yes (lowest 90%, average 94%, 66% of samples)",
		"PreventUserIdleSleep",
		"Google Chrome Helper",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// Bare `spectra power --json` now emits the stable unified envelope rather
// than the raw sysinfo.PowerState shape. The envelope is the contract
// described in issue #237.
func TestRunPowerJSONEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--json"}, &stdout, &stderr, fakePowerDeps())
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got PowerReport
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if got.Mode != "baseline" {
		t.Fatalf("mode = %q, want baseline", got.Mode)
	}
	if got.IntervalMS != 1000 {
		t.Fatalf("interval_ms = %d, want 1000", got.IntervalMS)
	}
	if got.Battery == nil || !got.Battery.OnBattery || got.Battery.Pct != 85 {
		t.Fatalf("battery = %+v", got.Battery)
	}
	if got.Thermal == nil || got.Thermal.Pressure != "serious" || !got.Thermal.Throttled {
		t.Fatalf("thermal = %+v", got.Thermal)
	}
	if len(got.Processes) != 1 || got.Processes[0].Source != "top" {
		t.Fatalf("processes = %+v", got.Processes)
	}
	if got.Processes[0].Impact == nil || got.Processes[0].Impact.Total != 4.2 {
		t.Fatalf("first impact = %+v", got.Processes[0].Impact)
	}
}

func TestRunPowerJoulesHumanOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := fakePowerDeps()
	deps.sample = func(d time.Duration) (sysinfo.SoCPower, error) {
		return sysinfo.SoCPower{
			Interval:      d,
			CPUPJoules:    5.2,
			CPUEJoules:    0.6,
			GPUJoules:     2.1,
			ANEJoules:     0.0,
			DRAMJoules:    1.3,
			PackageJoules: 9.2,
		}, nil
	}
	code := runPowerWithIO([]string{"--joules", "--interval=1s"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"=== SoC power (IOReport) ===",
		"Package:   9.20 J over 1s  (9.20 W)",
		"GPU:     2.10 J",
		"DRAM:    1.30 J",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunPowerJoulesJSONUsesEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := fakePowerDeps()
	deps.sample = func(d time.Duration) (sysinfo.SoCPower, error) {
		return sysinfo.SoCPower{Interval: d, PackageJoules: 7.5, GPUJoules: 1.5, CPUPJoules: 4.0}, nil
	}
	code := runPowerWithIO([]string{"--joules", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got PowerReport
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "joules" {
		t.Fatalf("mode = %q, want joules", got.Mode)
	}
	if got.Package == nil || got.Package.Joules != 7.5 || got.Package.GPUJoules != 1.5 {
		t.Fatalf("package = %+v, want joules=7.5 gpu=1.5", got.Package)
	}
	if got.Package.CPUJoules != 4.0 {
		t.Fatalf("cpu_joules = %v, want 4.0 (CPUPJoules+CPUEJoules)", got.Package.CPUJoules)
	}
}

func TestRunPowerJoulesUnsupportedHardware(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := fakePowerDeps()
	code := runPowerWithIO([]string{"--joules"}, &stdout, &stderr, deps)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 for unsupported hardware", code)
	}
	if !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("stderr = %q, want graceful unavailable message", stderr.String())
	}
}

func TestRunPowerTopJSONEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := fakePowerDeps()
	deps.procs = fakeProcs(map[int]string{101: "claude", 202: "WindowServer", 303: "claude-busy"})
	deps.sampleRusage = func(_ context.Context, interval time.Duration, _ []int) ([]sysinfo.EnergyDelta, error) {
		return []sysinfo.EnergyDelta{
			{PID: 101, Interval: interval, BilledEnergyNJ: 200, InterruptWakeups: 5},
			{PID: 303, Interval: interval, BilledEnergyNJ: 1_500_000, InterruptWakeups: 99},
		}, nil
	}
	code := runPowerWithIO([]string{"--top", "10", "--json", "--interval", "100ms"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	var got PowerReport
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if got.Mode != "rusage" {
		t.Fatalf("mode = %q, want rusage", got.Mode)
	}
	if got.IntervalMS != 100 {
		t.Fatalf("interval_ms = %d, want 100", got.IntervalMS)
	}
	if got.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (pid 202 was not sampled)", got.Skipped)
	}
	if len(got.Processes) != 2 || got.Processes[0].PID != 303 {
		t.Fatalf("processes = %+v, want pid 303 first", got.Processes)
	}
	if got.Processes[0].BilledEnergyNJ != 1_500_000 {
		t.Fatalf("first billed_energy_nj = %d, want 1_500_000", got.Processes[0].BilledEnergyNJ)
	}
	if got.Processes[0].Source != "rusage" {
		t.Fatalf("source = %q, want rusage", got.Processes[0].Source)
	}
	if got.Processes[0].Command != "claude-busy" {
		t.Fatalf("command = %q, want claude-busy", got.Processes[0].Command)
	}
	if got.Processes[0].Impact == nil || got.Processes[0].Impact.Total <= 0 {
		t.Fatalf("impact not populated: %+v", got.Processes[0].Impact)
	}
}

func TestRunPowerTopJSONImpactAccessibleByJq(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := fakePowerDeps()
	deps.procs = fakeProcs(map[int]string{42: "busybox"})
	deps.sampleRusage = func(_ context.Context, interval time.Duration, _ []int) ([]sysinfo.EnergyDelta, error) {
		return []sysinfo.EnergyDelta{
			{PID: 42, Interval: interval, BilledEnergyNJ: 250_000_000},
		}, nil
	}
	code := runPowerWithIO([]string{"--top", "1", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	procs := raw["processes"].([]any)
	first := procs[0].(map[string]any)
	impact := first["impact"].(map[string]any)
	if _, ok := impact["total"]; !ok {
		t.Fatalf("processes[0].impact.total missing — breaks `jq .processes[0].impact.total`: %+v", impact)
	}
}

func TestRunPowerTopHumanOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := fakePowerDeps()
	deps.procs = fakeProcs(map[int]string{42: "busybox"})
	deps.sampleRusage = func(_ context.Context, interval time.Duration, _ []int) ([]sysinfo.EnergyDelta, error) {
		return []sysinfo.EnergyDelta{
			{PID: 42, Interval: interval, BilledEnergyNJ: 250_000, InterruptWakeups: 7, UserNs: 12_000_000, SystemNs: 3_000_000},
		}, nil
	}
	code := runPowerWithIO([]string{"--top", "3", "--interval", "500ms"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"=== Energy top users (Δ over 500ms) ===",
		"BILLED(nJ)",
		"250000",
		"busybox",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunPowerTopUnsupported(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := fakePowerDeps()
	deps.procs = fakeProcs(map[int]string{1: "launchd"})
	deps.sampleRusage = func(context.Context, time.Duration, []int) ([]sysinfo.EnergyDelta, error) {
		return nil, sysinfo.ErrRusageUnsupported
	}
	code := runPowerWithIO([]string{"--top", "5"}, &stdout, &stderr, deps)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 for unsupported platform", code)
	}
	if !strings.Contains(stderr.String(), "per-pid energy unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunPowerHelpDocumentsPrivilege(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--help"}, &stdout, &stderr, fakePowerDeps())
	// `--help` returns flag.ErrHelp → exit 2 from flag.Parse, but the usage
	// text printed before that is what we care about.
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (flag.ErrHelp)", code)
	}
	for _, want := range []string{
		"--top", "(no privilege)",
		"--joules", "(no privilege)",
		"--deep", "(requires helper)",
		"--watch", "(inherits)",
		"--json", "(inherits)",
		"--interval",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("usage missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestRunPowerFlagError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--nope"}, &stdout, &stderr, fakePowerDeps())
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunPowerRejectsNonPositiveInterval(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--interval", "0s"}, &stdout, &stderr, fakePowerDeps())
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "interval must be positive") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunPowerWatchJSONEmitsNDJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sigCh := make(chan os.Signal, 1)
	frames := 0
	deps := fakePowerDeps()
	deps.collect = func() sysinfo.PowerState {
		frames++
		if frames >= 3 {
			sigCh <- os.Interrupt
		}
		return fakePowerState()
	}
	deps.signalCh = func() (<-chan os.Signal, func()) { return sigCh, func() {} }
	code := runPowerWithIO([]string{"--watch", "--json", "--interval", "10ms"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	count := 0
	for _, line := range strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line not valid JSON: %q: %v", line, err)
		}
		count++
	}
	if count < 2 {
		t.Fatalf("expected at least 2 NDJSON lines, got %d:\n%s", count, stdout.String())
	}
	// Final line must be the watch_stopped summary so consumers see a clean
	// terminator instead of guessing whether the stream was truncated.
	last := strings.TrimRight(stdout.String(), "\n")
	lines := strings.Split(last, "\n")
	var final map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatalf("final line not JSON: %q", lines[len(lines)-1])
	}
	if _, ok := final["watch_stopped"]; !ok {
		t.Fatalf("final line should be the watch_stopped summary, got %v", final)
	}
}

const deepTasksPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>elapsed_ns</key>
	<integer>500000000</integer>
	<key>tasks</key>
	<array>
		<dict>
			<key>pid</key><integer>501</integer>
			<key>name</key><string>Google Chrome Helper</string>
			<key>energy_impact</key><real>9.9</real>
			<key>cputime_ns</key><integer>123000000</integer>
		</dict>
	</array>
</dict>
</plist>
`

func fakeDeepFetcher(plist string, err error) func(int) ([]byte, error) {
	return func(int) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return []byte(plist), nil
	}
}

func deepFakeDeps(fetch func(int) ([]byte, error)) powerDeps {
	d := fakePowerDeps()
	d.fetchDeep = fetch
	return d
}

func TestRunPowerDeepHumanOutput(t *testing.T) {
	deps := deepFakeDeps(func(d int) ([]byte, error) {
		if d < 100 {
			t.Errorf("duration = %d, want >= 100", d)
		}
		return []byte(deepTasksPlist), nil
	})
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--deep"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"=== Power state ===",
		"=== Deep (powermetrics --samplers tasks) ===",
		"Google Chrome Helper",
		"501",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunPowerDeepJSONUnifiedEnvelope(t *testing.T) {
	deps := deepFakeDeps(fakeDeepFetcher(deepTasksPlist, nil))
	deps.sample = func(d time.Duration) (sysinfo.SoCPower, error) {
		return sysinfo.SoCPower{Interval: d, PackageJoules: 7.5, GPUJoules: 1.5}, nil
	}
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--deep", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	var got PowerReport
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "deep" {
		t.Fatalf("mode = %q, want deep", got.Mode)
	}
	if got.Battery == nil || !got.Battery.OnBattery {
		t.Fatalf("battery = %+v, want PowerState carried through", got.Battery)
	}
	if got.Package == nil || got.Package.Joules != 7.5 {
		t.Fatalf("package = %+v, want joules carried through (--deep implies --joules)", got.Package)
	}
	if len(got.Processes) != 1 || got.Processes[0].PID != 501 || got.Processes[0].Source != "powermetrics" {
		t.Fatalf("processes = %+v, want pid 501 from powermetrics", got.Processes)
	}
}

func TestRunPowerDeepHelperUnavailableFallsBack(t *testing.T) {
	deps := deepFakeDeps(fakeDeepFetcher("", helperclient.ErrHelperUnavailable))
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--deep"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "sudo spectra install-helper") {
		t.Errorf("stderr should hint at install-helper:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "=== Power state ===") {
		t.Errorf("L1+L2 fallback missing:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "=== Deep") {
		t.Errorf("unexpected deep section:\n%s", stdout.String())
	}
}

func TestRunPowerDeepHelperErrorFallsBack(t *testing.T) {
	deps := deepFakeDeps(fakeDeepFetcher("", fmt.Errorf("powermetrics exploded")))
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--deep"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "powermetrics exploded") {
		t.Errorf("stderr should contain helper error:\n%s", stderr.String())
	}
}

func TestRunPowerDeepHelperUnavailableNoteInJSON(t *testing.T) {
	deps := deepFakeDeps(fakeDeepFetcher("", helperclient.ErrHelperUnavailable))
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--deep", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var got PowerReport
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Notes) == 0 || !strings.Contains(got.Notes[0], "helper unavailable") {
		t.Fatalf("notes = %v, want degradation note", got.Notes)
	}
}
