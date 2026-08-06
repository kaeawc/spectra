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
		if f, ok := fdProcessFinding(proc, s.FDLimit.Soft); ok {
			findings = append(findings, f)
		}
	}
	if f, ok := fdSystemFinding(total, s.Sysctls); ok {
		findings = append(findings, f)
	}
	return findings
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
