package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func emptyEnv(string) string                 { return "" }
func noAssertions() []sysinfo.PowerAssertion { return nil }

func TestRunImpactFormulaFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--formula"}, nil, &stdout, &stderr, emptyEnv, noAssertions)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"score = energy",
		"billed_energy_J",
		"SPECTRA_IMPACT_WEIGHTS",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("formula output missing %q:\n%s", want, out)
		}
	}
}

func TestRunImpactReadsInputsFromStdin(t *testing.T) {
	inputs := []sysinfo.ImpactInput{
		{PID: 91829, Command: "Claude Helper (GPU)", Interval: time.Second, BilledEnergyJ: 0.28, GPUTimeNs: 41_000_000},
		{PID: 407, Command: "WindowServer", Interval: time.Second, BilledEnergyJ: 0.18, GPUTimeNs: 148_000_000},
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--from-json", "-", "--json"}, bytes.NewReader(raw), &stdout, &stderr, emptyEnv, noAssertions)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got []sysinfo.ScoredImpact
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\noutput:\n%s", err, stdout.String())
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Input.PID != 407 {
		t.Fatalf("top PID = %d, want 407 (WindowServer beats helper on GPU); rows=%+v",
			got[0].Input.PID, got)
	}
	if got[0].Breakdown.FromGPU <= 0 {
		t.Fatalf("WindowServer FromGPU = %g, want >0", got[0].Breakdown.FromGPU)
	}
}

func TestRunImpactHumanTableShowsBreakdown(t *testing.T) {
	inputs := []sysinfo.ImpactInput{
		{PID: 100, Command: "yes", Interval: time.Second, BilledEnergyJ: 3.0},
	}
	raw, _ := json.Marshal(inputs)

	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--from-json", "-"}, bytes.NewReader(raw), &stdout, &stderr, emptyEnv, noAssertions)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"PID", "SCORE", "ENERGY", "WAKE", "GPU", "ASRT", "IO", "COMMAND", "yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table missing %q:\n%s", want, out)
		}
	}
}

func TestRunImpactWeightOverrideViaEnv(t *testing.T) {
	inputs := []sysinfo.ImpactInput{{PID: 1, Interval: time.Second, BilledEnergyJ: 1.0}}
	raw, _ := json.Marshal(inputs)

	env := func(k string) string {
		if k == "SPECTRA_IMPACT_WEIGHTS" {
			return "energy=200"
		}
		return ""
	}

	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--from-json", "-", "--json"}, bytes.NewReader(raw), &stdout, &stderr, env, noAssertions)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got []sysinfo.ScoredImpact
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[0].Breakdown.FromEnergy != 200 {
		t.Fatalf("FromEnergy = %g, want 200 (env-override 200 J⁻¹ × 1 J)",
			got[0].Breakdown.FromEnergy)
	}
}

func TestRunImpactBadEnvIsError(t *testing.T) {
	env := func(k string) string {
		if k == "SPECTRA_IMPACT_WEIGHTS" {
			return "garbage=1"
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	code := runImpactWithIO(nil, nil, &stdout, &stderr, env, noAssertions)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stderr.Len() == 0 {
		t.Fatalf("expected error on stderr")
	}
}

func TestRunImpactAssertionRanksAboveIdle(t *testing.T) {
	inputs := []sysinfo.ImpactInput{
		{PID: 500, Command: "caffeinate", Interval: time.Second},
		{PID: 501, Command: "idle-daemon", Interval: time.Second},
	}
	raw, _ := json.Marshal(inputs)
	assertions := func() []sysinfo.PowerAssertion {
		return []sysinfo.PowerAssertion{{PID: 500, Type: sysinfo.AssertionPreventUserIdleSleep}}
	}

	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--from-json", "-", "--json"}, bytes.NewReader(raw), &stdout, &stderr, emptyEnv, assertions)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got []sysinfo.ScoredImpact
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[0].Input.PID != 500 {
		t.Fatalf("top PID = %d, want 500 (caffeinate outranks idle); rows=%+v",
			got[0].Input.PID, got)
	}
}

func TestRunImpactFlagError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--nope"}, nil, &stdout, &stderr, emptyEnv, noAssertions)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
