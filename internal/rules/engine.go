package rules

import (
	"sort"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// severityOrder maps severities to sort keys (lower = higher priority).
var severityOrder = map[Severity]int{
	SeverityHigh:   0,
	SeverityMedium: 1,
	SeverityLow:    2,
	SeverityInfo:   3,
}

// Evaluate runs all rules in catalog against snap and returns the findings
// sorted by severity (high first) then rule ID for stable output.
func Evaluate(snap snapshot.Snapshot, catalog []Rule) []Finding {
	var all []Finding
	for _, r := range catalog {
		all = append(all, r.MatchFn(snap)...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		si := severityOrder[all[i].Severity]
		sj := severityOrder[all[j].Severity]
		if si != sj {
			return si < sj
		}
		if all[i].RuleID != all[j].RuleID {
			return all[i].RuleID < all[j].RuleID
		}
		return all[i].Subject < all[j].Subject
	})
	return all
}
