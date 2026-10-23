package rules

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// NMTGrowthConfig holds the thresholds for jvm-native-memory-growth.
type NMTGrowthConfig struct {
	// MinGrowthKiB and MinGrowthPct must both be met by the committed growth
	// between the first and last sample of the evaluated window.
	MinGrowthKiB int64
	MinGrowthPct float64
	// MinSamples and MinSpan bound how much history is needed before the rule
	// has an opinion.
	MinSamples int
	MinSpan    time.Duration
	// NoiseFraction is the largest single-step decrease tolerated, as a
	// fraction of the total growth, for the series to still count as monotonic.
	NoiseFraction float64
	// SettleAge is the process age before which warmup categories are ignored;
	// growth must then persist in samples taken after it.
	SettleAge time.Duration
	// WarmupCategories grow legitimately while the JIT and class loader warm up.
	WarmupCategories []string
}

// DefaultNMTGrowthConfig returns the shipped thresholds: >= 64 MiB and >= 25%
// committed growth over >= 4 samples spanning >= 15 minutes.
func DefaultNMTGrowthConfig() NMTGrowthConfig {
	return NMTGrowthConfig{
		MinGrowthKiB:     64 * 1024,
		MinGrowthPct:     25,
		MinSamples:       4,
		MinSpan:          15 * time.Minute,
		NoiseFraction:    0.10,
		SettleAge:        30 * time.Minute,
		WarmupCategories: []string{"Class", "Code", "Compiler"},
	}
}

// nmtIgnoredCategories are never evaluated: heap growth is governed by the
// collector and surfaced by the GC rules, not a native-memory leak signal.
var nmtIgnoredCategories = map[string]bool{"java heap": true, "total": true}

type nmtPoint struct {
	at  time.Time
	kib int64
}

// ruleJVMNativeMemoryGrowth fires, once per category, when a JVM's committed
// NMT category grows steadily across the sampled history. It stays silent when
// NMT is not enabled (no samples exist; NMT is never enabled implicitly).
func ruleJVMNativeMemoryGrowth() Rule {
	return ruleJVMNativeMemoryGrowthWith(DefaultNMTGrowthConfig())
}

func ruleJVMNativeMemoryGrowthWith(cfg NMTGrowthConfig) Rule {
	return Rule{
		ID:       "jvm-native-memory-growth",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, j := range s.JVMs {
				if !FactsFor(j).NMTEnabled {
					continue
				}
				samples := nmtSamplesFor(s.JVMHistory, j.PID)
				for _, cat := range nmtCategoryNames(samples) {
					g, ok := evalNMTCategory(samples, cat, cfg)
					if !ok {
						continue
					}
					findings = append(findings, Finding{
						RuleID:   "jvm-native-memory-growth",
						Severity: SeverityMedium,
						Subject:  fmt.Sprintf("PID %d NMT %s", j.PID, cat),
						Message: fmt.Sprintf(
							"%s: NMT category %q committed memory grew %d MiB (%d -> %d MiB, +%.0f%%) over %s across %d samples without shrinking.",
							jvmLabel(j), cat, g.growthKiB/1024, g.firstKiB/1024, g.lastKiB/1024, g.pct, g.span.Round(time.Minute), g.samples),
						Fix: fmt.Sprintf("Attribute the growth to call sites: run `spectra jvm nmt baseline %d`, let it grow further, then `spectra jvm nmt diff %d`.", j.PID, j.PID),
					})
				}
			}
			return findings
		},
	}
}

// nmtSamplesFor returns pid's samples that carry NMT data, oldest first. It
// returns nil when the newest sample has none (probe failed, timed out, or NMT
// went away): an older series must not keep firing without a current reading.
func nmtSamplesFor(h snapshot.JVMHistory, pid int) []snapshot.JVMSample {
	all := h.NMTSamplesFor(pid)
	if len(all) == 0 {
		return nil
	}
	newest := all[0]
	for _, sm := range all {
		if sm.At.After(newest.At) {
			newest = sm
		}
	}
	if len(newest.NMTCommittedKiB) == 0 {
		return nil
	}
	var out []snapshot.JVMSample
	for _, sm := range all {
		if len(sm.NMTCommittedKiB) > 0 {
			out = append(out, sm)
		}
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].At.Before(out[k].At) })
	return out
}

// nmtCategoryNames lists categories in the newest sample, sorted for stable
// finding order, minus ignored ones.
func nmtCategoryNames(samples []snapshot.JVMSample) []string {
	if len(samples) == 0 {
		return nil
	}
	var names []string
	for name := range samples[len(samples)-1].NMTCommittedKiB {
		if !nmtIgnoredCategories[strings.ToLower(name)] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

type nmtGrowth struct {
	growthKiB, firstKiB, lastKiB int64
	pct                          float64
	span                         time.Duration
	samples                      int
}

// evalNMTCategory applies the thresholds to one category's series.
func evalNMTCategory(samples []snapshot.JVMSample, cat string, cfg NMTGrowthConfig) (nmtGrowth, bool) {
	var notBefore time.Time
	if isNMTWarmupCategory(cat, cfg) {
		start := samples[len(samples)-1].ProcStart
		if start.IsZero() {
			return nmtGrowth{}, false // age unknown: cannot prove the settle period passed
		}
		notBefore = start.Add(cfg.SettleAge)
	}
	var pts []nmtPoint
	for _, sm := range samples {
		kib, ok := sm.NMTCommittedKiB[cat]
		if !ok || sm.At.Before(notBefore) {
			continue
		}
		pts = append(pts, nmtPoint{sm.At, kib})
	}
	return growthOver(pts, cfg)
}

func isNMTWarmupCategory(cat string, cfg NMTGrowthConfig) bool {
	for _, w := range cfg.WarmupCategories {
		if strings.EqualFold(w, cat) {
			return true
		}
	}
	return false
}

func growthOver(pts []nmtPoint, cfg NMTGrowthConfig) (nmtGrowth, bool) {
	if len(pts) < cfg.MinSamples {
		return nmtGrowth{}, false
	}
	first, last := pts[0], pts[len(pts)-1]
	span := last.at.Sub(first.at)
	growth := last.kib - first.kib
	if span < cfg.MinSpan || growth < cfg.MinGrowthKiB {
		return nmtGrowth{}, false
	}
	pct := 100.0
	if first.kib > 0 {
		pct = float64(growth) * 100 / float64(first.kib)
	}
	if pct < cfg.MinGrowthPct {
		return nmtGrowth{}, false
	}
	tolerance := float64(growth) * cfg.NoiseFraction
	for i := 1; i < len(pts); i++ {
		if drop := pts[i-1].kib - pts[i].kib; drop > 0 && float64(drop) > tolerance {
			return nmtGrowth{}, false
		}
	}
	return nmtGrowth{growth, first.kib, last.kib, pct, span, len(pts)}, true
}

// jvmLabel names a JVM in message text; unlike the finding Subject it may
// include the main class, which jps can omit between scans.
func jvmLabel(j jvm.Info) string {
	if j.MainClass == "" {
		return fmt.Sprintf("PID %d", j.PID)
	}
	return fmt.Sprintf("PID %d (%s)", j.PID, j.MainClass)
}
