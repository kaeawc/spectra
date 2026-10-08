package rules

import (
	"fmt"
	"strings"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// jvm-gc-log-pressure thresholds over the parsed (bounded-tail) GC log.
const (
	// gcLogMaxPauseMs is the single stop-the-world pause length treated as
	// user-visible latency.
	gcLogMaxPauseMs = 500.0
	// gcLogTotalPauseMs is the cumulative pause time across the scanned log
	// window treated as a throughput problem.
	gcLogTotalPauseMs = 10_000.0
)

// ruleJVMGCLogPressure turns a discovered GC log's per-pause evidence into a
// finding: Full GCs, evacuation failures (to-space exhausted), or pause times
// over threshold. It complements the jstat-counter jvm-gc-pressure rule with
// real per-pause data, and only fires in deep mode where GC logs are found.
func ruleJVMGCLogPressure() Rule {
	return Rule{
		ID:       "jvm-gc-log-pressure",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, r := range s.GCLogReports {
				reasons := gcLogPressureReasons(r)
				if len(reasons) == 0 {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   "jvm-gc-log-pressure",
					Severity: SeverityMedium,
					Subject:  fmt.Sprintf("PID %d (%s)", r.PID, r.MainClass),
					Message: fmt.Sprintf("GC log %s shows %s.",
						strings.Join(r.LogPaths, ", "), strings.Join(reasons, "; ")),
					Fix: gcLogPressureFix(r),
				})
			}
			return findings
		},
	}
}

func gcLogPressureReasons(r snapshot.GCLogReport) []string {
	s := r.Summary
	var reasons []string
	if s.FullGCCount > 0 {
		reasons = append(reasons, fmt.Sprintf("%d Full GC pause(s)", s.FullGCCount))
	}
	if s.EvacuationFailures > 0 {
		reasons = append(reasons, fmt.Sprintf("%d evacuation failure(s) (to-space exhausted)", s.EvacuationFailures))
	}
	if s.MaxPauseMs >= gcLogMaxPauseMs {
		reasons = append(reasons, fmt.Sprintf("a %.0fms max pause (>= %.0fms)", s.MaxPauseMs, gcLogMaxPauseMs))
	}
	if s.TotalPauseMs >= gcLogTotalPauseMs {
		reasons = append(reasons, fmt.Sprintf("%.1fs total pause across %d pauses", s.TotalPauseMs/1000, s.Pauses))
	}
	return reasons
}

func gcLogPressureFix(r snapshot.GCLogReport) string {
	s := r.Summary
	if s.FullGCCount > 0 && s.FullGCCount == s.SystemGCCount {
		return "All Full GCs were triggered by System.gc(); remove the explicit calls or add -XX:+DisableExplicitGC (or -XX:+ExplicitGCInvokesConcurrent for G1)."
	}
	if s.EvacuationFailures > 0 {
		return "Evacuation failures mean the heap ran out of space to copy live objects: raise -Xmx or G1ReservePercent, and reduce the live set (heap histogram / JFR allocation profiling)."
	}
	return "Inspect the GC log's longest pauses and causes (`spectra jvm gc-log <file>`); reduce the live set or allocation rate, or size the heap / pause-time goal (-XX:MaxGCPauseMillis) for the workload."
}

// ruleJVMGCLoggingDisabled recommends enabling GC logging on a JVM that shows
// GC-pressure symptoms in jstat (old gen high or a full-GC burst) but has no
// GC logging configured, so the next incident leaves per-pause evidence.
// Suppressed when the heap is tight by design and when a GC log was already
// discovered for the PID.
func ruleJVMGCLoggingDisabled() Rule {
	return Rule{
		ID:       "jvm-gc-logging-disabled",
		Severity: SeverityLow,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			profiles := BuiltinProfiles()
			logged := make(map[int]bool, len(s.GCLogReports))
			for _, r := range s.GCLogReports {
				logged[r.PID] = true
			}
			var findings []Finding
			for _, j := range s.JVMs {
				if logged[j.PID] || !gcLoggingWorthRecommending(j, profiles) {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   "jvm-gc-logging-disabled",
					Severity: SeverityLow,
					Subject:  fmt.Sprintf("PID %d (%s)", j.PID, j.MainClass),
					Message:  fmt.Sprintf("JVM shows GC pressure (old gen %.0f%% full, %d full GCs) but has no GC logging configured; per-pause evidence is being lost.", OldGenUsedPct(j), j.GC.FGC),
					Fix:      "Add -Xlog:gc*:file=<dir>/gc.log:time,uptime,level,tags:filecount=5,filesize=20m (JDK 9+) or -Xloggc:<file> -XX:+PrintGCDetails (JDK 8); logging is cheap and makes GC pauses diagnosable.",
				})
			}
			return findings
		},
	}
}

func gcLoggingWorthRecommending(j jvm.Info, profiles []AppProfile) bool {
	if j.GC == nil || j.VMArgs == "" {
		return false // no symptoms data, or no args to prove logging is off
	}
	if !OldGenHigh(j) && !FullGCBurst(j) {
		return false
	}
	f := FactsFor(j)
	if f.GCLogging || TightHeapByDesign(f) {
		return false
	}
	return !HasTag(MatchProfile(j, profiles), TagTightHeapExpected)
}
