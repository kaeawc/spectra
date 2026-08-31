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

// MetaspaceNearLimitPct is the used-vs-configured-ceiling occupancy above
// which class-metadata space is reported as pressure. Approaching a hard
// ceiling risks OutOfMemoryError: Metaspace / Compressed class space.
//
// Note this is deliberately measured against the *configured ceiling*
// (-XX:MaxMetaspaceSize / -XX:CompressedClassSpaceSize), not committed
// capacity: committed metaspace (jstat MC) tracks used (MU) by design and
// sits near 100% normally, so a used/committed ratio would be pure noise.
const MetaspaceNearLimitPct = 90.0

// MetaspaceCeilingPct returns used metaspace as a percent of the configured
// -XX:MaxMetaspaceSize ceiling. ok is false when the ceiling is unset or the
// counters are missing, in which case the point-in-time ratio is meaningless
// (unbounded metaspace growth is a trend signal, not a snapshot one).
func MetaspaceCeilingPct(j jvm.Info, f VMArgsFacts) (pct float64, ok bool) {
	if f.MaxMetaspaceSizeBytes <= 0 || j.GC == nil || j.GC.MU <= 0 {
		return 0, false
	}
	usedBytes := j.GC.MU * 1024 // jstat MU is KiB
	return usedBytes * 100 / float64(f.MaxMetaspaceSizeBytes), true
}

// CompressedClassCeilingPct mirrors MetaspaceCeilingPct for the compressed
// class space and its -XX:CompressedClassSpaceSize ceiling.
func CompressedClassCeilingPct(j jvm.Info, f VMArgsFacts) (pct float64, ok bool) {
	if f.CompressedClassSpaceSizeBytes <= 0 || j.GC == nil || j.GC.CCSU <= 0 {
		return 0, false
	}
	usedBytes := j.GC.CCSU * 1024 // jstat CCSU is KiB
	return usedBytes * 100 / float64(f.CompressedClassSpaceSizeBytes), true
}

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
