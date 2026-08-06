package rules

import (
	"testing"

	"github.com/kaeawc/spectra/internal/snapshot"
)

func fixedRule(id string, findings ...Finding) Rule {
	return Rule{ID: id, MatchFn: func(snapshot.Snapshot) []Finding { return findings }}
}

func TestEvaluateSortsBySeverityThenRuleIDThenSubject(t *testing.T) {
	catalog := []Rule{
		fixedRule("r-low", Finding{RuleID: "z-low", Severity: SeverityLow}),
		fixedRule("r-high", Finding{RuleID: "m-high", Severity: SeverityHigh}),
		fixedRule("r-high2", Finding{RuleID: "a-high", Severity: SeverityHigh}),
		fixedRule("r-info", Finding{RuleID: "b-info", Severity: SeverityInfo}),
		fixedRule("r-med", Finding{RuleID: "c-med", Severity: SeverityMedium}),
	}
	got := Evaluate(snapshot.Snapshot{}, catalog)
	want := []string{"a-high", "m-high", "c-med", "z-low", "b-info"}
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].RuleID != id {
			t.Fatalf("order[%d] = %q, want %q (full: %v)", i, got[i].RuleID, id, ruleIDs(got))
		}
	}
}

func TestEvaluateStableBySubjectWithinSameRule(t *testing.T) {
	catalog := []Rule{
		fixedRule("r",
			Finding{RuleID: "same", Severity: SeverityHigh, Subject: "b"},
			Finding{RuleID: "same", Severity: SeverityHigh, Subject: "a"},
		),
	}
	got := Evaluate(snapshot.Snapshot{}, catalog)
	if got[0].Subject != "a" || got[1].Subject != "b" {
		t.Fatalf("subject order = %q,%q; want a,b", got[0].Subject, got[1].Subject)
	}
}

func ruleIDs(fs []Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.RuleID
	}
	return out
}
