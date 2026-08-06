package rules

import (
	"fmt"

	"github.com/kaeawc/spectra/internal/snapshot"
)

const (
	// processMemoryHogRAMPercent is the share of system RAM above which a single
	// process's resident set is flagged for investigation.
	processMemoryHogRAMPercent = 25
	// processMemoryHogFloorKiB is an absolute floor so the rule never fires on
	// small machines where 25% is still a modest amount of memory (2 GiB).
	processMemoryHogFloorKiB int64 = 2 * 1024 * 1024
)

// ruleProcessMemoryHog fires for a process whose resident set is both above an
// absolute floor and a large share of system RAM — the "what is eating my RAM"
// debugging question that Activity Monitor answers only after manual sorting.
func ruleProcessMemoryHog() Rule {
	return Rule{
		ID:       "process-memory-hog",
		Severity: SeverityMedium,
		MatchFn:  matchProcessMemoryHog,
	}
}

func matchProcessMemoryHog(s snapshot.Snapshot) []Finding {
	ramBytes := s.Host.RAMBytes
	if ramBytes == 0 {
		return nil // unknown system RAM — cannot compute a share
	}
	var findings []Finding
	for _, proc := range s.Processes {
		if proc.RSSKiB < processMemoryHogFloorKiB {
			continue
		}
		rssBytes := uint64(proc.RSSKiB) * 1024
		pct := rssBytes * 100 / ramBytes
		if pct < processMemoryHogRAMPercent {
			continue
		}
		name := proc.Command
		if name == "" {
			name = "process"
		}
		findings = append(findings, Finding{
			RuleID:   "process-memory-hog",
			Severity: SeverityMedium,
			Message:  fmt.Sprintf("%s (pid %d) is resident in %d MiB (%d%% of system RAM).", name, proc.PID, proc.RSSKiB/1024, pct),
			Subject:  fmt.Sprintf("%s pid %d", name, proc.PID),
			Fix:      "Investigate the process's memory growth; restart it or cap its heap/cache if this footprint is unexpected.",
		})
	}
	return findings
}
