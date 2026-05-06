// Package rules implements the Spectra recommendations engine.
//
// Rules are defined as built-in Go functions and optional CEL/YAML catalog
// files. Both sources compile to Rule values so the engine has one evaluation
// path.
package rules

import "github.com/kaeawc/spectra/internal/snapshot"

// Severity classifies how urgent a finding is.
type Severity string

const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
	SeverityInfo   Severity = "info"
)

// Finding is one instance of a rule firing against a snapshot. Multiple
// findings can come from one rule (e.g. one per JDK).
type Finding struct {
	RuleID   string   `json:"rule_id"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix,omitempty"`
	// Subject identifies what the finding is about (e.g. "JDK 11 at /path").
	// Empty for host-level findings.
	Subject string `json:"subject,omitempty"`
}

// Rule is one declarative check. MatchFn receives a snapshot and returns
// zero or more findings. Zero findings means the rule did not fire.
type Rule struct {
	ID       string
	Severity Severity
	MatchFn  func(s snapshot.Snapshot) []Finding
	Source   string

	// Optional metadata used by catalog inspection commands.
	Message string
	Fix     string
}
