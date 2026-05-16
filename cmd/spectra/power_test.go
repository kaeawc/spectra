package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
