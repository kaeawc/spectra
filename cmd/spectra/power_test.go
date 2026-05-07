package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func fakePowerState() sysinfo.PowerState {
	return sysinfo.PowerState{
		OnBattery:       true,
		BatteryPct:      85,
		ThermalPressure: "serious",
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
	code := runPowerWithIO(nil, &stdout, &stderr, fakePowerState)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"=== Power state ===",
		"source:    battery (85%)",
		"thermal:   serious",
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
	code := runPowerWithIO([]string{"--json"}, &stdout, &stderr, fakePowerState)
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
	if len(got.EnergyTopUsers) != 1 || got.EnergyTopUsers[0].Command != "Google Chrome Helper" {
		t.Fatalf("energy users = %+v, want Google Chrome Helper", got.EnergyTopUsers)
	}
}

func TestRunPowerFlagError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPowerWithIO([]string{"--nope"}, &stdout, &stderr, fakePowerState)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
