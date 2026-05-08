package rules

import "github.com/kaeawc/spectra/internal/jvm"

// Composable predicates over JVM facts. Rules combine these instead of
// re-implementing thresholds, so a fact like "this heap is tight by design"
// is computed once and shared.

// Threshold defaults. Centralized so rules don't drift apart on the
// boundary between "elevated" and "alarming."
const (
	// OldGenHighPct is the level above which old-gen occupancy is reported
	// as a finding *unless* something else explains it (tight-by-design,
	// flat trend, launcher profile).
	OldGenHighPct = 90.0

	// TightHeapMaxFreeRatio: if -XX:MaxHeapFreeRatio is at or below this,
	// the JVM is configured to keep the heap intentionally close to full.
	// JetBrains Toolbox sets this to 10; Gradle daemon defaults to 70.
	TightHeapMaxFreeRatio = 20

	// FullGCBurstCount / Seconds is the bar for considering accumulated
	// full GC time meaningful in a one-shot snapshot.
	FullGCBurstCount   = 5
	FullGCBurstSeconds = 1.0
)

// OldGenUsedPct returns the old-gen occupancy percent for a JVM, or 0 if
// no GC stats are available. Centralized so every rule computes it identically.
func OldGenUsedPct(j jvm.Info) float64 {
	if j.GC == nil || j.GC.OC <= 0 {
		return 0
	}
	return j.GC.OU * 100 / j.GC.OC
}

// OldGenHigh reports whether old-gen occupancy is above OldGenHighPct.
func OldGenHigh(j jvm.Info) bool { return OldGenUsedPct(j) >= OldGenHighPct }

// TightHeapByDesign reports whether the JVM is configured to keep the
// heap intentionally close to full via -XX:MaxHeapFreeRatio. A small
// MaxHeapFreeRatio tells the JVM not to grow free space, which means
// "near-full" is the steady-state target — not pressure.
func TightHeapByDesign(f VMArgsFacts) bool {
	return f.MaxHeapFreeRatio > 0 && f.MaxHeapFreeRatio <= TightHeapMaxFreeRatio
}

// TightMetaspaceByDesign mirrors TightHeapByDesign for metaspace sizing.
func TightMetaspaceByDesign(f VMArgsFacts) bool {
	return f.MaxMetaspaceFreeRatio > 0 && f.MaxMetaspaceFreeRatio <= TightHeapMaxFreeRatio
}

// FullGCBurst reports whether full GC has accumulated meaningful pause
// time in this snapshot (count and wall-time both above thresholds).
func FullGCBurst(j jvm.Info) bool {
	if j.GC == nil {
		return false
	}
	return j.GC.FGC >= FullGCBurstCount && j.GC.FGCT >= FullGCBurstSeconds
}
