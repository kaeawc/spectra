package sysinfo

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestComputeImpactReproducible(t *testing.T) {
	in := ImpactInput{
		PID: 91829, Command: "Claude Helper (GPU)",
		Interval: time.Second, BilledEnergyJ: 0.28,
		InterruptWakeups: 9100, GPUTimeNs: 41_000_000, DiskBytes: 0,
	}

	first := ComputeImpact(in, nil, DefaultWeights)
	for i := 0; i < 100; i++ {
		got := ComputeImpact(in, nil, DefaultWeights)
		if got != first {
			t.Fatalf("iter %d differs: %+v vs %+v", i, got, first)
		}
	}
	if first.Total <= 0 {
		t.Fatalf("expected positive total, got %+v", first)
	}
	sum := first.FromEnergy + first.FromWakeups + first.FromGPU + first.FromAssertions + first.FromIO
	if math.Abs(sum-first.Total) > 1e-9 {
		t.Fatalf("Total %g != sum of components %g", first.Total, sum)
	}
}

// `yes > /dev/null` and `stress-ng --cpu 1 -t 10s` both saturate one core
// and should score within ±10% of each other.
func TestComputeImpactCalibration(t *testing.T) {
	yesIn := ImpactInput{PID: 1, Interval: 10 * time.Second, BilledEnergyJ: 30.0, InterruptWakeups: 100}
	stressIn := ImpactInput{PID: 2, Interval: 10 * time.Second, BilledEnergyJ: 31.5, InterruptWakeups: 90}

	a := ComputeImpact(yesIn, nil, DefaultWeights).Total
	b := ComputeImpact(stressIn, nil, DefaultWeights).Total

	diff := math.Abs(a-b) / math.Max(a, b)
	if diff > 0.10 {
		t.Fatalf("calibration drift %.2f%% > 10%% (yes=%g stress=%g)", diff*100, a, b)
	}
}

func TestComputeImpactAssertionOutranksIdle(t *testing.T) {
	caffeinate := ImpactInput{PID: 500, Interval: time.Second}
	idle := ImpactInput{PID: 501, Interval: time.Second}

	assertions := []PowerAssertion{
		{PID: 500, Type: AssertionPreventUserIdleSleep, Name: "caffeinate -i"},
	}

	c := ComputeImpact(caffeinate, assertions, DefaultWeights)
	i := ComputeImpact(idle, assertions, DefaultWeights)

	if !(c.Total > i.Total) {
		t.Fatalf("caffeinate (%g) should outrank idle (%g)", c.Total, i.Total)
	}
	if c.FromAssertions != DefaultWeights.AssertionPenalty {
		t.Fatalf("FromAssertions = %g, want %g", c.FromAssertions, DefaultWeights.AssertionPenalty)
	}
	if i.Total != 0 {
		t.Fatalf("idle Total = %g, want 0", i.Total)
	}
}

func TestComputeImpactAssertionScopedToPID(t *testing.T) {
	in := ImpactInput{PID: 10, Interval: time.Second}
	otherPidAssertions := []PowerAssertion{
		{PID: 99, Type: AssertionPreventUserIdleSleep},
	}
	got := ComputeImpact(in, otherPidAssertions, DefaultWeights)
	if got.FromAssertions != 0 {
		t.Fatalf("FromAssertions = %g, want 0 (assertion held by another pid)", got.FromAssertions)
	}
}

func TestComputeImpactNonBlockingAssertionIgnored(t *testing.T) {
	in := ImpactInput{PID: 10, Interval: time.Second}
	assertions := []PowerAssertion{{PID: 10, Type: "BackgroundTask"}}
	got := ComputeImpact(in, assertions, DefaultWeights)
	if got.FromAssertions != 0 {
		t.Fatalf("FromAssertions = %g, want 0", got.FromAssertions)
	}
}

func TestComputeImpactZeroIntervalUsesRawCount(t *testing.T) {
	in := ImpactInput{PID: 10, InterruptWakeups: 1000}
	got := ComputeImpact(in, nil, DefaultWeights)
	want := 1000.0 * DefaultWeights.InterruptWakeupCost
	if math.Abs(got.FromWakeups-want) > 1e-9 {
		t.Fatalf("FromWakeups = %g, want %g", got.FromWakeups, want)
	}
}

func TestFromRusageConvertsUnits(t *testing.T) {
	d := EnergyDelta{
		PID: 42, Command: "yes", Interval: time.Second,
		BilledEnergyNJ:   3_000_000_000,
		InterruptWakeups: 50,
		DiskBytesRead:    1000, DiskBytesWritten: 500,
	}
	got := FromRusage(d)
	if got.PID != 42 || got.Command != "yes" || got.Interval != time.Second {
		t.Fatalf("identity fields wrong: %+v", got)
	}
	if got.BilledEnergyJ != 3.0 {
		t.Fatalf("BilledEnergyJ = %g, want 3.0 (from 3e9 nJ)", got.BilledEnergyJ)
	}
	if got.DiskBytes != 1500 {
		t.Fatalf("DiskBytes = %d, want 1500 (read+write)", got.DiskBytes)
	}
}

func TestFromTaskSampleCarriesGPU(t *testing.T) {
	got := FromTaskSample(TaskPowerSample{PID: 7, Command: "WindowServer", GPUNs: 12_000_000}, 2*time.Second)
	if got.PID != 7 || got.Interval != 2*time.Second {
		t.Fatalf("identity fields wrong: %+v", got)
	}
	if got.GPUTimeNs != 12_000_000 {
		t.Fatalf("GPUTimeNs = %d, want 12_000_000", got.GPUTimeNs)
	}
}

func TestParseWeightOverrides(t *testing.T) {
	w, err := ParseWeightOverrides("energy=120,wake=0.002,gpu=2e-7,assert=10,io=5e-9", DefaultWeights)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.BilledEnergyPerJoule != 120 || w.InterruptWakeupCost != 0.002 ||
		w.GPUNsCost != 2e-7 || w.AssertionPenalty != 10 || w.DiskByteCost != 5e-9 {
		t.Fatalf("weights = %+v, want full override", w)
	}
}

func TestParseWeightOverridesPartial(t *testing.T) {
	w, err := ParseWeightOverrides("energy=200", DefaultWeights)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.BilledEnergyPerJoule != 200 {
		t.Fatalf("BilledEnergyPerJoule = %g, want 200", w.BilledEnergyPerJoule)
	}
	if w.InterruptWakeupCost != DefaultWeights.InterruptWakeupCost {
		t.Fatalf("wake cost mutated to %g, expected default %g",
			w.InterruptWakeupCost, DefaultWeights.InterruptWakeupCost)
	}
}

func TestParseWeightOverridesEmpty(t *testing.T) {
	w, err := ParseWeightOverrides("", DefaultWeights)
	if err != nil || w != DefaultWeights {
		t.Fatalf("got (%+v, %v), want defaults, nil", w, err)
	}
}

func TestParseWeightOverridesErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":     "garbage=1.0",
		"missing equals":  "energy",
		"non-numeric val": "energy=banana",
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseWeightOverrides(spec, DefaultWeights); err == nil {
				t.Fatalf("expected error for %q", spec)
			}
		})
	}
}

func TestLoadWeightsFromEnv(t *testing.T) {
	w, err := LoadWeights(func(k string) string {
		if k == "SPECTRA_IMPACT_WEIGHTS" {
			return "energy=150"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.BilledEnergyPerJoule != 150 {
		t.Fatalf("BilledEnergyPerJoule = %g, want 150", w.BilledEnergyPerJoule)
	}
}

func TestImpactFormulaHelpMentionsWeights(t *testing.T) {
	help := ImpactFormulaHelp(DefaultWeights)
	for _, want := range []string{"score = energy", "billed_energy_J", "100", "SPECTRA_IMPACT_WEIGHTS"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}

func TestScoreImpactsSortedAndStable(t *testing.T) {
	inputs := []ImpactInput{
		{PID: 100, Interval: time.Second, BilledEnergyJ: 0.1},
		{PID: 200, Interval: time.Second, BilledEnergyJ: 0.5},
		{PID: 300, Interval: time.Second, BilledEnergyJ: 0.1},
	}
	got := ScoreImpacts(inputs, nil, DefaultWeights)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Input.PID != 200 {
		t.Fatalf("top PID = %d, want 200", got[0].Input.PID)
	}
	if got[1].Input.PID != 100 || got[2].Input.PID != 300 {
		t.Fatalf("tie-break order = [%d,%d], want [100,300]",
			got[1].Input.PID, got[2].Input.PID)
	}
}
