package rules

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/updates"
)

const staleMajorPreparedAge = 30 * 24 * time.Hour

func ruleUpdatesStaleMajorPrepared() Rule {
	return Rule{
		ID:       "updates.stale_major_prepared",
		Severity: SeverityInfo,
		Message:  "A prepared major macOS update is older than 30 days without a matching OS transition.",
		Fix:      "Run `spectra updates --since 30d`; complete the update or remove the pending update with explicit admin approval.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			now := s.TakenAt
			if now.IsZero() {
				now = time.Now()
			}
			return staleMajorPreparedFindings(s, now)
		},
	}
}

func staleMajorPreparedFindings(s snapshot.Snapshot, now time.Time) []Finding {
	transitions := updateTransitionLabels(s.Updates.Entries)
	var findings []Finding
	for _, entry := range s.Updates.Entries {
		prepared, ok := updates.ParseMajorUpdatePrepared(entry.Message)
		if !ok {
			continue
		}
		prepared.PreparedAt = entry.Timestamp
		if now.Sub(prepared.PreparedAt) <= staleMajorPreparedAge || transitionMatches(transitions, prepared) {
			continue
		}
		findings = append(findings, Finding{
			RuleID:   "updates.stale_major_prepared",
			Severity: SeverityInfo,
			Subject:  prepared.AssetID,
			Message:  fmt.Sprintf("Prepared major macOS update %s %s has been staged since %s with no matching OS transition.", prepared.Title, prepared.Version, prepared.PreparedAt.Format("2006-01-02")),
			Fix:      "Run `spectra updates --since 30d`; complete the update or remove the pending update with explicit admin approval.",
		})
	}
	return findings
}

func updateTransitionLabels(entries []updates.InstallLogEntry) []string {
	var labels []string
	for _, entry := range entries {
		transition, ok := updates.ParseOSControllerTransition(entry.Message)
		if !ok {
			continue
		}
		if transition.CurrentLabel != "" {
			labels = append(labels, transition.CurrentLabel)
		}
	}
	return labels
}

func transitionMatches(labels []string, prepared updates.MajorUpdatePrepared) bool {
	for _, label := range labels {
		if strings.Contains(label, prepared.Version) || strings.Contains(label, prepared.Title) {
			return true
		}
	}
	return false
}
