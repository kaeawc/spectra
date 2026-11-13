package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func emptyEnv(string) string                 { return "" }
func noAssertions() []sysinfo.PowerAssertion { return nil }

func TestRunImpactFormulaFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--formula"}, nil, &stdout, &stderr, testImpactDeps(emptyEnv, noAssertions))
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
	code := runImpactWithIO([]string{"--from-json", "-", "--json"}, bytes.NewReader(raw), &stdout, &stderr, testImpactDeps(emptyEnv, noAssertions))
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
	code := runImpactWithIO([]string{"--from-json", "-"}, bytes.NewReader(raw), &stdout, &stderr, testImpactDeps(emptyEnv, noAssertions))
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
	code := runImpactWithIO([]string{"--from-json", "-", "--json"}, bytes.NewReader(raw), &stdout, &stderr, testImpactDeps(env, noAssertions))
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
	code := runImpactWithIO(nil, nil, &stdout, &stderr, testImpactDeps(env, noAssertions))
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
	code := runImpactWithIO([]string{"--from-json", "-", "--json"}, bytes.NewReader(raw), &stdout, &stderr, testImpactDeps(emptyEnv, assertions))
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
	code := runImpactWithIO([]string{"--nope"}, nil, &stdout, &stderr, testImpactDeps(emptyEnv, noAssertions))
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func testImpactDeps(env func(string) string, assertions func() []sysinfo.PowerAssertion) impactDeps {
	deps := fakePowerDeps()
	deps.procs = fakeProcs(nil)
	return impactDeps{getenv: env, assertions: assertions, power: deps}
}

func TestRunImpactSamplesLiveWithoutFromJSON(t *testing.T) {
	deps := testImpactDeps(emptyEnv, noAssertions)
	procs := map[int]string{}
	for pid := 1; pid <= 12; pid++ {
		procs[pid] = fmt.Sprintf("proc%d", pid)
	}
	deps.power.procs = fakeProcs(procs)
	var gotInterval time.Duration
	deps.power.sampleRusage = func(_ context.Context, interval time.Duration, pids []int) ([]sysinfo.EnergyDelta, error) {
		gotInterval = interval
		out := make([]sysinfo.EnergyDelta, 0, len(pids))
		for _, pid := range pids {
			out = append(out, sysinfo.EnergyDelta{PID: pid, Interval: interval, BilledEnergyNJ: uint64(pid) * 1_000_000, InterruptWakeups: 2000})
		}
		return out, nil
	}

	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--json", "--interval", "2s"}, nil, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if gotInterval != 2*time.Second {
		t.Fatalf("sampled over %s, want 2s", gotInterval)
	}
	var got []sysinfo.ScoredImpact
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if len(got) != defaultImpactTop {
		t.Fatalf("rows = %d, want default top %d", len(got), defaultImpactTop)
	}
	if got[0].Input.PID != 12 || got[0].Input.Command != "proc12" {
		t.Fatalf("top row = %+v, want pid 12 (highest billed energy)", got[0].Input)
	}
	if got[0].Breakdown.FromWakeups <= 0 {
		t.Fatalf("FromWakeups = %g, want >0 from sampled interrupt wakeups", got[0].Breakdown.FromWakeups)
	}
}

func TestRunImpactLiveUnsupportedExits3(t *testing.T) {
	deps := testImpactDeps(emptyEnv, noAssertions)
	deps.power.procs = fakeProcs(map[int]string{1: "launchd"})
	// Go through the real EnergySampler so a sampler that swallows the
	// build-wide unsupported error (and returns an empty sample) is caught.
	deps.power.sampleRusage = func(ctx context.Context, interval time.Duration, pids []int) ([]sysinfo.EnergyDelta, error) {
		return sysinfo.EnergySampler{
			Interval: interval,
			Reader:   func(int) (sysinfo.ProcRusage, error) { return sysinfo.ProcRusage{}, sysinfo.ErrRusageUnsupported },
			Sleeper:  func(context.Context, time.Duration) error { return nil },
		}.Sample(ctx, pids)
	}
	var stdout, stderr bytes.Buffer
	if code := runImpactWithIO(nil, nil, &stdout, &stderr, deps); code != 3 {
		t.Fatalf("exit = %d, want 3; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "per-pid energy unavailable") {
		t.Fatalf("stdout=%q stderr=%q, want no table and the unavailable message", stdout.String(), stderr.String())
	}
}

func TestRunImpactAcceptsWakeupsPerSecKey(t *testing.T) {
	raw := `[{"pid":1,"interval_ns":2000000000,"interrupt_wakeups_per_sec":5000}]`
	var stdout, stderr bytes.Buffer
	code := runImpactWithIO([]string{"--from-json", "-", "--json"}, strings.NewReader(raw), &stdout, &stderr, testImpactDeps(emptyEnv, noAssertions))
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var got []sysinfo.ScoredImpact
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[0].Breakdown.FromWakeups != 5 {
		t.Fatalf("FromWakeups = %g, want 5 (5000/s × 0.001)", got[0].Breakdown.FromWakeups)
	}
}
