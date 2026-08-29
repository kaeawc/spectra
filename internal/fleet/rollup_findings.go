package fleet

import (
	"sort"

	"github.com/kaeawc/spectra/internal/rules"
)

// FleetFinding is one rule finding deduplicated across the hosts that trip it.
type FleetFinding struct {
	RuleID   string   `json:"rule_id"`
	Subject  string   `json:"subject,omitempty"`
	Severity string   `json:"severity"`
	Message  string   `json:"message,omitempty"`
	Hosts    []string `json:"hosts"`
}

// RollupFindings evaluates catalog against every host's snapshot and groups the
// resulting findings by (rule id, subject), collecting the distinct hosts that
// trip each. Results are ranked most-widespread first, then by severity, then
// rule id. Hosts with no usable snapshot are skipped.
func RollupFindings(hosts []HostSnapshot, catalog []rules.Rule) []FleetFinding {
	labels := displayLabels(hosts)
	byKey := map[string]*FleetFinding{}
	seen := map[string]map[string]bool{}
	var order []string
	for i, h := range hosts {
		if h.Empty {
			continue
		}
		for _, f := range rules.Evaluate(h.Snap, catalog) {
			key := f.RuleID + "\x00" + f.Subject
			ff, ok := byKey[key]
			if !ok {
				ff = &FleetFinding{RuleID: f.RuleID, Subject: f.Subject, Severity: string(f.Severity), Message: f.Message}
				byKey[key] = ff
				seen[key] = map[string]bool{}
				order = append(order, key)
			}
			if !seen[key][labels[i]] {
				seen[key][labels[i]] = true
				ff.Hosts = append(ff.Hosts, labels[i])
			}
		}
	}
	out := make([]FleetFinding, 0, len(byKey))
	for _, k := range order {
		f := byKey[k]
		sort.Strings(f.Hosts)
		out = append(out, *f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Hosts) != len(out[j].Hosts) {
			return len(out[i].Hosts) > len(out[j].Hosts)
		}
		if si, sj := findingSevRank(out[i].Severity), findingSevRank(out[j].Severity); si != sj {
			return si < sj
		}
		return out[i].RuleID < out[j].RuleID
	})
	return out
}

func findingSevRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}
