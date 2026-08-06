package rules

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// fd-pressure thresholds, expressed as a percentage of the relevant limit.
const (
	// fdWarnPct is the per-process open-fd usage (of the soft limit) at or
	// above which a medium finding is emitted.
	fdWarnPct = 80
	// fdCriticalPct is the per-process usage at or above which the finding is
	// escalated to high severity.
	fdCriticalPct = 95
	// fdSystemPct is the system-wide open-fd usage (of kern.maxfiles) at or
	// above which a single high finding is emitted.
	fdSystemPct = 80
)

// ruleFDPressure fires when a process is approaching its file-descriptor soft
// limit, or when the aggregate open-fd count across all processes approaches
// the system-wide file table limit (kern.maxfiles). Both signals depend on
// per-process OpenFDs, which is only populated in --deep mode; in shallow mode
// OpenFDs is 0 and the rule stays silent.
func ruleFDPressure() Rule {
	return Rule{
		ID:       "fd-pressure",
		Severity: SeverityMedium,
		MatchFn:  matchFDPressure,
	}
}

func matchFDPressure(s snapshot.Snapshot) []Finding {
	var findings []Finding
	var total int
	for _, proc := range s.Processes {
		if proc.OpenFDs > 0 {
			total += proc.OpenFDs
		}
		if f, ok := fdProcessFindingWithHistory(proc, s.FDLimit.Soft, s.FDHistory); ok {
			findings = append(findings, f)
		}
	}
	if f, ok := fdSystemFinding(total, s.Sysctls); ok {
		findings = append(findings, f)
	}
	return findings
}

// fdProcessFindingWithHistory layers trend awareness over the point-in-time
// fdProcessFinding. It consults snapshot.FDHistory exactly the way
// ruleJVMGCPressure consults JVMHistory:
//
//   - On the existing >=80% path: if fd usage is rising across the window
//     (>=3 samples, non-decreasing, net climb) the finding is re-worded as a
//     probable leak and escalated to High. If history exists but is steady
//     (not rising) the original finding is kept without escalation.
//   - Below 80%: a rising window still emits a distinct early-leak finding.
//
// When FDHistory is empty/nil, HasFDTrendFor is false, so this collapses to
// exactly fdProcessFinding — the no-history one-shot case is byte-for-byte
// unchanged.
func fdProcessFindingWithHistory(proc process.Info, soft int, h snapshot.FDHistory) (Finding, bool) {
	rising := HasFDTrendFor(h, proc.PID) && RisingFDsFor(h, proc.PID)
	if f, ok := fdProcessFinding(proc, soft); ok {
		if rising {
			return escalateFDLeak(f, proc, soft), true
		}
		return f, true
	}
	if rising {
		return fdEarlyLeakFinding(proc, soft)
	}
	return Finding{}, false
}

// escalateFDLeak re-words an at-threshold finding as a confirmed leak and
// raises it to High severity. The rising slope removes the ambiguity between
// a steady high-water mark and descriptor exhaustion in progress.
func escalateFDLeak(base Finding, proc process.Info, soft int) Finding {
	pct := proc.OpenFDs * 100 / soft
	base.Severity = SeverityHigh
	base.Message = fmt.Sprintf("open_fds=%d is %d%% of the default fd soft limit (%d) and rising across recent samples; this is a probable file-descriptor leak.",
		proc.OpenFDs, pct, soft)
	return base
}

// fdEarlyLeakFinding emits a Medium finding for a process whose open-fd count
// is climbing across the sample window but has not yet reached fdWarnPct. It
// catches a leak early — before descriptor exhaustion — and is only ever
// reached behind HasFDTrendFor, so no-history snapshots never see it.
func fdEarlyLeakFinding(proc process.Info, soft int) (Finding, bool) {
	if proc.OpenFDs <= 0 {
		return Finding{}, false
	}
	limitClause := "the fd soft limit"
	if soft > 0 {
		limitClause = fmt.Sprintf("the fd soft limit (%d)", soft)
	}
	return Finding{
		RuleID:   "fd-pressure",
		Severity: SeverityMedium,
		Subject:  fmt.Sprintf("PID %d (%s)", proc.PID, fdProcessName(proc)),
		Message: fmt.Sprintf("open_fds=%d is rising across recent samples while still below %s; this looks like an early file-descriptor leak.",
			proc.OpenFDs, limitClause),
		Fix: "Investigate a possible file-descriptor leak before the process approaches its limit (`lsof -p <pid>` to see what is accumulating).",
	}, true
}

// fdProcessFinding evaluates one process against its soft fd limit. It returns
// no finding when the process reports no open descriptors, when the soft limit
// is unknown (<= 0), or when usage is below fdWarnPct.
func fdProcessFinding(proc process.Info, soft int) (Finding, bool) {
	if proc.OpenFDs <= 0 || soft <= 0 {
		return Finding{}, false
	}
	pct := proc.OpenFDs * 100 / soft
	if pct < fdWarnPct {
		return Finding{}, false
	}
	severity := SeverityMedium
	if pct >= fdCriticalPct {
		severity = SeverityHigh
	}
	return Finding{
		RuleID:   "fd-pressure",
		Severity: severity,
		Subject:  fmt.Sprintf("PID %d (%s)", proc.PID, fdProcessName(proc)),
		Message: fmt.Sprintf("open_fds=%d is %d%% of the default fd soft limit (%d); process may be approaching descriptor exhaustion.",
			proc.OpenFDs, pct, soft),
		Fix: "Raise the descriptor limit for this process (`launchctl limit maxfiles` or `ulimit -n`), or investigate a possible file-descriptor leak.",
	}, true
}

// fdSystemFinding evaluates the aggregate open-fd count against the system-wide
// file table limit (kern.maxfiles). It stays silent when nothing has open
// descriptors, or when kern.maxfiles is absent or unparseable.
func fdSystemFinding(total int, sysctls map[string]string) (Finding, bool) {
	if total <= 0 {
		return Finding{}, false
	}
	maxfiles, ok := parseMaxfiles(sysctls)
	if !ok {
		return Finding{}, false
	}
	pct := total * 100 / maxfiles
	if pct < fdSystemPct {
		return Finding{}, false
	}
	return Finding{
		RuleID:   "fd-pressure",
		Severity: SeverityHigh,
		Subject:  "system file table",
		Message: fmt.Sprintf("open file descriptors across all processes total %d, which is %d%% of kern.maxfiles (%d); the system file table may be approaching exhaustion.",
			total, pct, maxfiles),
		Fix: "Raise kern.maxfiles (`sudo sysctl -w kern.maxfiles=...` and a matching /etc/sysctl.conf entry), or investigate processes leaking descriptors with `spectra process --deep`.",
	}, true
}

// parseMaxfiles returns the positive integer value of kern.maxfiles from the
// sysctl map, or ok=false when it is missing, empty, or not a positive int.
func parseMaxfiles(sysctls map[string]string) (int, bool) {
	raw, present := sysctls["kern.maxfiles"]
	if !present {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// fdProcessName returns the best available human-readable name for a process,
// matching the "PID <pid> (<name>)" subject style used by neighboring rules.
func fdProcessName(proc process.Info) string {
	if proc.Command != "" {
		return proc.Command
	}
	if proc.BSDName != "" {
		return proc.BSDName
	}
	return "unknown"
}
