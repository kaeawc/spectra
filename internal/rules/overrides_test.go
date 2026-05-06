package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestParseOverrides(t *testing.T) {
	cfg := []byte(`
rules:
  disabled:
    - app-unsigned
    - brew-stale-pinned # local exception
  severity:
    jvm-eol-version: high
    library-storage-footprint: low
`)
	got, err := ParseOverrides(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Disabled["app-unsigned"]; !ok {
		t.Fatalf("missing disabled app-unsigned: %+v", got.Disabled)
	}
	if got.Severity["jvm-eol-version"] != SeverityHigh {
		t.Fatalf("severity = %q, want high", got.Severity["jvm-eol-version"])
	}
	if got.Severity["library-storage-footprint"] != SeverityLow {
		t.Fatalf("severity = %q, want low", got.Severity["library-storage-footprint"])
	}
}

func TestParseOverridesRejectsInvalidSeverity(t *testing.T) {
	_, err := ParseOverrides([]byte(`
rules:
  severity:
    jvm-eol-version: urgent
`))
	if err == nil || !strings.Contains(err.Error(), "invalid severity") {
		t.Fatalf("err = %v, want invalid severity", err)
	}
}

func TestApplyOverrides(t *testing.T) {
	catalog := []Rule{
		{
			ID:       "keep",
			Severity: SeverityLow,
			MatchFn: func(snapshot.Snapshot) []Finding {
				return []Finding{{RuleID: "keep", Severity: SeverityLow}}
			},
		},
		{
			ID:       "drop",
			Severity: SeverityHigh,
			MatchFn: func(snapshot.Snapshot) []Finding {
				return []Finding{{RuleID: "drop", Severity: SeverityHigh}}
			},
		},
	}
	got := ApplyOverrides(catalog, Overrides{
		Disabled: map[string]struct{}{"drop": {}},
		Severity: map[string]Severity{"keep": SeverityMedium},
	})
	if len(got) != 1 || got[0].ID != "keep" {
		t.Fatalf("catalog = %+v, want only keep", got)
	}
	if got[0].Severity != SeverityMedium {
		t.Fatalf("rule severity = %q, want medium", got[0].Severity)
	}
	findings := Evaluate(snapshot.Snapshot{}, got)
	if len(findings) != 1 || findings[0].Severity != SeverityMedium {
		t.Fatalf("findings = %+v, want severity medium", findings)
	}
}

func TestOverrideWarnings(t *testing.T) {
	warnings := OverrideWarnings(
		Overrides{
			Disabled: map[string]struct{}{"known": {}, "unknown-a": {}},
			Severity: map[string]Severity{"unknown-b": SeverityHigh},
		},
		[]Rule{{ID: "known"}},
	)
	want := []string{
		`rules override references unknown rule "unknown-a"`,
		`rules override references unknown rule "unknown-b"`,
	}
	if len(warnings) != len(want) {
		t.Fatalf("warnings = %+v, want %+v", warnings, want)
	}
	for i := range want {
		if warnings[i] != want[i] {
			t.Fatalf("warnings = %+v, want %+v", warnings, want)
		}
	}
}
