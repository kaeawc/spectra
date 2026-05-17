package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestRunPowerJSONOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--json"}, &stdout, &stderr, fakePowerDeps())
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got sysinfo.PowerState
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OnBattery || got.BatteryPct != 85 || got.ThermalPressure != "serious" {
		t.Fatalf("state = %+v, want fake power state", got)
	}
	if !got.ThermalThrottled || got.CPUSpeedLimitPct != 92 {
		t.Fatalf("thermal state = %+v, want throttled with 92%% CPU limit", got)
	}
	if len(got.EnergyTopUsers) != 1 || got.EnergyTopUsers[0].Command != "Google Chrome Helper" {
		t.Fatalf("energy users = %+v, want Google Chrome Helper", got.EnergyTopUsers)
	}
}

func TestRunPowerJoulesHumanOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := powerDeps{
		collect: fakePowerState,
		sample: func(d time.Duration) (sysinfo.SoCPower, error) {
			return sysinfo.SoCPower{
				Interval:      d,
				CPUPJoules:    5.2,
				CPUEJoules:    0.6,
				GPUJoules:     2.1,
				ANEJoules:     0.0,
				DRAMJoules:    1.3,
				PackageJoules: 9.2,
			}, nil
		},
	}
	code := runPowerWithIO([]string{"--joules", "--interval=1s"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"=== SoC power (IOReport) ===",
		"Package:   9.20 J over 1s  (9.20 W)",
		"CPU P:   5.20 J",
		"GPU:     2.10 J",
		"DRAM:    1.30 J",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunPowerJoulesJSONOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := powerDeps{
		collect: fakePowerState,
		sample: func(d time.Duration) (sysinfo.SoCPower, error) {
			return sysinfo.SoCPower{Interval: d, PackageJoules: 7.5, GPUJoules: 1.5}, nil
		},
	}
	code := runPowerWithIO([]string{"--joules", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got sysinfo.SoCPower
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PackageJoules != 7.5 || got.GPUJoules != 1.5 {
		t.Fatalf("got %+v, want PackageJoules=7.5 GPUJoules=1.5", got)
	}
}

func TestRunPowerJoulesUnsupportedHardware(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := powerDeps{
		collect: fakePowerState,
		sample: func(time.Duration) (sysinfo.SoCPower, error) {
			return sysinfo.SoCPower{}, sysinfo.ErrUnsupportedHardware
		},
	}
	code := runPowerWithIO([]string{"--joules"}, &stdout, &stderr, deps)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 for unsupported hardware", code)
	}
	if !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("stderr = %q, want graceful unavailable message", stderr.String())
	}
}

func TestRunPowerTopJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := powerDeps{
		collect: fakePowerState,
		procs:   fakeProcs(map[int]string{101: "claude", 202: "WindowServer", 303: "claude-busy"}),
		sampleRusage: func(_ context.Context, interval time.Duration, _ []int) ([]sysinfo.EnergyDelta, error) {
			return []sysinfo.EnergyDelta{
				{PID: 101, Interval: interval, BilledEnergyNJ: 200, InterruptWakeups: 5},
				{PID: 303, Interval: interval, BilledEnergyNJ: 1_500_000, InterruptWakeups: 99},
			}, nil
		},
		clock: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	code := runPowerWithIO([]string{"--top", "10", "--json", "--interval", "100ms"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	var got EnergyTopReport
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if got.IntervalNS != int64(100*time.Millisecond) {
		t.Fatalf("interval_ns = %d", got.IntervalNS)
	}
	if got.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (pid 202 was not sampled)", got.Skipped)
	}
	if len(got.Top) != 2 || got.Top[0].PID != 303 {
		t.Fatalf("top order = %+v, want pid 303 first", got.Top)
	}
	if got.Top[0].BilledEnergyNJ != 1_500_000 {
		t.Fatalf("raw nanojoules = %d, want 1_500_000", got.Top[0].BilledEnergyNJ)
	}
	if got.Top[0].Command != "claude-busy" {
		t.Fatalf("command = %q, want claude-busy", got.Top[0].Command)
	}
}

func TestRunPowerTopHumanOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := powerDeps{
		collect: fakePowerState,
		procs:   fakeProcs(map[int]string{42: "busybox"}),
		sampleRusage: func(_ context.Context, interval time.Duration, _ []int) ([]sysinfo.EnergyDelta, error) {
			return []sysinfo.EnergyDelta{
				{PID: 42, Interval: interval, BilledEnergyNJ: 250_000, InterruptWakeups: 7, UserNs: 12_000_000, SystemNs: 3_000_000},
			}, nil
		},
		clock: time.Now,
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
	deps := powerDeps{
		collect: fakePowerState,
		procs:   fakeProcs(map[int]string{1: "launchd"}),
		sampleRusage: func(context.Context, time.Duration, []int) ([]sysinfo.EnergyDelta, error) {
			return nil, sysinfo.ErrRusageUnsupported
		},
		clock: time.Now,
	}
	code := runPowerWithIO([]string{"--top", "5"}, &stdout, &stderr, deps)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 for unsupported platform", code)
	}
	if !strings.Contains(stderr.String(), "per-pid energy unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
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

func TestRunPowerDeepJSONIsSupersetOfJoulesAndPowerState(t *testing.T) {
	deps := deepFakeDeps(fakeDeepFetcher(deepTasksPlist, nil))
	deps.sample = func(d time.Duration) (sysinfo.SoCPower, error) {
		return sysinfo.SoCPower{Interval: d, PackageJoules: 7.5, GPUJoules: 1.5}, nil
	}
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--deep", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["on_battery"] != true {
		t.Errorf("on_battery = %v, want true (PowerState superset)", got["on_battery"])
	}
	soc, ok := got["soc"].(map[string]any)
	if !ok {
		t.Fatalf("soc = %T, want map (joules superset)", got["soc"])
	}
	if soc["package_joules"] != 7.5 {
		t.Errorf("soc.package_joules = %v, want 7.5", soc["package_joules"])
	}
	deep, ok := got["deep"].(map[string]any)
	if !ok {
		t.Fatalf("deep = %T, want map", got["deep"])
	}
	tasks, ok := deep["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("deep.tasks = %+v", deep["tasks"])
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
