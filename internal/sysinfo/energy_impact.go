package sysinfo

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ImpactInput is the per-pid power signal that ComputeImpact scores. It is
// the union of what rusage (BilledEnergyJ, wakeups, disk) and powermetrics
// (GPU time) can provide; FromRusage and FromTaskSample build it from each
// source so producers don't have to know the formula's shape.
type ImpactInput struct {
	PID              int           `json:"pid"`
	Command          string        `json:"command,omitempty"`
	Interval         time.Duration `json:"interval_ns,omitempty"`
	BilledEnergyJ    float64       `json:"billed_energy_j,omitempty"`
	InterruptWakeups uint64        `json:"interrupt_wakeups,omitempty"`
	GPUTimeNs        uint64        `json:"gpu_time_ns,omitempty"`
	DiskBytes        uint64        `json:"disk_bytes,omitempty"`
}

// FromRusage lifts a rusage delta (nanojoules, separate read/write bytes)
// into the unit-normalised ImpactInput.
func FromRusage(d EnergyDelta) ImpactInput {
	return ImpactInput{
		PID:              d.PID,
		Command:          d.Command,
		Interval:         d.Interval,
		BilledEnergyJ:    float64(d.BilledEnergyNJ) / 1e9,
		InterruptWakeups: d.InterruptWakeups,
		DiskBytes:        d.DiskBytesRead + d.DiskBytesWritten,
	}
}

// FromTaskSample lifts a powermetrics task sample into ImpactInput. The
// caller supplies Interval since powermetrics samples don't carry it.
// Producers with a matching rusage delta should merge the two: rusage
// owns energy + wakeups; powermetrics owns GPU time.
func FromTaskSample(t TaskPowerSample, interval time.Duration) ImpactInput {
	return ImpactInput{
		PID:       t.PID,
		Command:   t.Command,
		Interval:  interval,
		GPUTimeNs: t.GPUNs,
	}
}

// ImpactWeights tunes the contribution of each signal in ComputeImpact.
// Override at runtime via SPECTRA_IMPACT_WEIGHTS instead of recompiling.
type ImpactWeights struct {
	BilledEnergyPerJoule float64 `json:"billed_energy_per_joule"`
	InterruptWakeupCost  float64 `json:"interrupt_wakeup_cost"`
	GPUNsCost            float64 `json:"gpu_ns_cost"`
	AssertionPenalty     float64 `json:"assertion_penalty"`
	DiskByteCost         float64 `json:"disk_byte_cost"`
}

// DefaultWeights are calibrated against `yes > /dev/null` so 1 J of billed
// energy ≈ 100 score units (about one core-second on M-series).
var DefaultWeights = ImpactWeights{
	BilledEnergyPerJoule: 100.0,
	InterruptWakeupCost:  0.001,
	GPUNsCost:            1e-7,
	AssertionPenalty:     5.0,
	DiskByteCost:         1e-9,
}

// ImpactBreakdown attributes a score to each contributing component so the
// caller can show "why this pid scored high" alongside the headline number.
type ImpactBreakdown struct {
	Total          float64 `json:"total"`
	FromEnergy     float64 `json:"from_energy"`
	FromWakeups    float64 `json:"from_wakeups"`
	FromGPU        float64 `json:"from_gpu"`
	FromAssertions float64 `json:"from_assertions"`
	FromIO         float64 `json:"from_io"`
}

// PowerAssertion.Type values that block deep idle. Exported so producers
// (pmset parser) and consumers (impact scorer) can't drift apart silently.
const (
	AssertionPreventUserIdleSystemSleep  = "PreventUserIdleSystemSleep"
	AssertionPreventUserIdleDisplaySleep = "PreventUserIdleDisplaySleep"
	AssertionPreventUserIdleSleep        = "PreventUserIdleSleep"
	AssertionPreventSystemSleep          = "PreventSystemSleep"
	AssertionNoIdleSleep                 = "NoIdleSleepAssertion"
	AssertionNoDisplaySleep              = "NoDisplaySleepAssertion"
	AssertionUserIsActive                = "UserIsActive"
)

var idleBlockingAssertions = map[string]bool{
	AssertionPreventUserIdleSystemSleep:  true,
	AssertionPreventUserIdleDisplaySleep: true,
	AssertionPreventUserIdleSleep:        true,
	AssertionPreventSystemSleep:          true,
	AssertionNoIdleSleep:                 true,
	AssertionNoDisplaySleep:              true,
	AssertionUserIsActive:                true,
}

// ComputeImpact derives a per-pid impact score from one ImpactInput plus
// the assertions held by the process. Pure: no time, env, or random terms.
func ComputeImpact(in ImpactInput, assertions []PowerAssertion, w ImpactWeights) ImpactBreakdown {
	var b ImpactBreakdown

	b.FromEnergy = in.BilledEnergyJ * w.BilledEnergyPerJoule

	wakeRate := float64(in.InterruptWakeups)
	if seconds := in.Interval.Seconds(); seconds > 0 {
		wakeRate /= seconds
	}
	b.FromWakeups = wakeRate * w.InterruptWakeupCost

	b.FromGPU = float64(in.GPUTimeNs) * w.GPUNsCost
	b.FromIO = float64(in.DiskBytes) * w.DiskByteCost

	for _, a := range assertions {
		if a.PID != in.PID {
			continue
		}
		if idleBlockingAssertions[a.Type] {
			b.FromAssertions += w.AssertionPenalty
		}
	}

	b.Total = b.FromEnergy + b.FromWakeups + b.FromGPU + b.FromAssertions + b.FromIO
	return b
}

type ScoredImpact struct {
	Input     ImpactInput     `json:"input"`
	Breakdown ImpactBreakdown `json:"breakdown"`
}

// ScoreImpacts ranks rows by Total descending; ties break by PID ascending
// so the output is fully reproducible.
func ScoreImpacts(inputs []ImpactInput, assertions []PowerAssertion, w ImpactWeights) []ScoredImpact {
	out := make([]ScoredImpact, len(inputs))
	for i, in := range inputs {
		out[i] = ScoredImpact{Input: in, Breakdown: ComputeImpact(in, assertions, w)}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Breakdown.Total != out[j].Breakdown.Total {
			return out[i].Breakdown.Total > out[j].Breakdown.Total
		}
		return out[i].Input.PID < out[j].Input.PID
	})
	return out
}

// weightKey is one tunable knob: the env-spec short name, a pointer into
// an ImpactWeights, and the help-line that documents it. Keeping all three
// in one place stops the override parser, the help text, and the defaults
// from drifting out of sync.
type weightKey struct {
	name string
	ptr  func(*ImpactWeights) *float64
	help string
}

var weightKeyTable = []weightKey{
	{"energy", func(w *ImpactWeights) *float64 { return &w.BilledEnergyPerJoule },
		"energy     = billed_energy_J            × %g  (per joule)"},
	{"wake", func(w *ImpactWeights) *float64 { return &w.InterruptWakeupCost },
		"wakeups    = interrupt_wakeups_per_sec  × %g"},
	{"gpu", func(w *ImpactWeights) *float64 { return &w.GPUNsCost },
		"gpu        = gpu_time_ns                × %g"},
	{"assert", func(w *ImpactWeights) *float64 { return &w.AssertionPenalty },
		"assertions = idle_blocking_count        × %g  (additive)"},
	{"io", func(w *ImpactWeights) *float64 { return &w.DiskByteCost },
		"io         = disk_bytes                 × %g"},
}

// ParseWeightOverrides applies "key=value,key=value" overrides on top of
// base. base is copied; the original is not mutated.
func ParseWeightOverrides(spec string, base ImpactWeights) (ImpactWeights, error) {
	out := base
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return out, nil
	}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, raw, ok := strings.Cut(pair, "=")
		if !ok {
			return base, fmt.Errorf("impact weight %q: expected key=value", pair)
		}
		key = strings.TrimSpace(key)
		ptr := lookupWeight(&out, key)
		if ptr == nil {
			return base, fmt.Errorf("impact weight %q: unknown key (want energy|wake|gpu|assert|io)", key)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return base, fmt.Errorf("impact weight %q: %w", key, err)
		}
		*ptr = v
	}
	return out, nil
}

func lookupWeight(w *ImpactWeights, name string) *float64 {
	for _, k := range weightKeyTable {
		if k.name == name {
			return k.ptr(w)
		}
	}
	return nil
}

// LoadWeights returns DefaultWeights overlaid with any SPECTRA_IMPACT_WEIGHTS
// overrides read via the injected getenv (so tests don't touch os.Getenv).
func LoadWeights(getenv func(string) string) (ImpactWeights, error) {
	return ParseWeightOverrides(getenv("SPECTRA_IMPACT_WEIGHTS"), DefaultWeights)
}

// ImpactFormulaHelp renders the formula and current weight values for
// inclusion in --help output.
func ImpactFormulaHelp(w ImpactWeights) string {
	var b strings.Builder
	b.WriteString("Energy impact formula (per pid, per sample):\n")
	b.WriteString("  score = energy + wakeups + gpu + assertions + io\n")
	b.WriteString("\nComponents:\n")
	for _, k := range weightKeyTable {
		b.WriteString("  ")
		fmt.Fprintf(&b, k.help, *k.ptr(&w))
		b.WriteByte('\n')
	}
	b.WriteString("\nOverride weights without recompiling via env, e.g.\n")
	b.WriteString("  SPECTRA_IMPACT_WEIGHTS=energy=120,wake=0.002,gpu=2e-7,assert=10,io=1e-9\n")
	return b.String()
}
