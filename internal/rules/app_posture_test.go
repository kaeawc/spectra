package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestAppDebuggablePostureFires(t *testing.T) {
	s := snapshot.Snapshot{Apps: []detect.Result{
		{Path: "/Applications/Debuggable.app", TeamID: "TEAM1", Entitlements: []string{"app-sandbox", "get-task-allow"}},
	}}
	findings := ruleAppDebuggablePosture().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.RuleID != "app-debuggable-posture" || f.Severity != SeverityHigh {
		t.Errorf("rule/severity = %s/%s", f.RuleID, f.Severity)
	}
	if f.Subject != "Debuggable" {
		t.Errorf("subject = %q, want Debuggable", f.Subject)
	}
	if !strings.Contains(f.Message, "get-task-allow") || f.Fix == "" {
		t.Errorf("message/fix incomplete: %q / %q", f.Message, f.Fix)
	}
}

func TestAppDebuggablePostureSilentWithoutEntitlement(t *testing.T) {
	s := snapshot.Snapshot{Apps: []detect.Result{
		{Path: "/Applications/Clean.app", TeamID: "TEAM1", Entitlements: []string{"app-sandbox", "network.client"}},
		{Path: "/Applications/NoEnts.app", TeamID: "TEAM2"},
	}}
	if findings := ruleAppDebuggablePosture().MatchFn(s); len(findings) != 0 {
		t.Errorf("expected no findings, got %v", findings)
	}
}

func TestAppDebuggablePostureInCatalog(t *testing.T) {
	found := false
	for _, r := range V1Catalog() {
		if r.ID == "app-debuggable-posture" {
			found = true
		}
	}
	if !found {
		t.Error("app-debuggable-posture is not registered in V1Catalog")
	}
}

func TestHasEntitlement(t *testing.T) {
	ents := []string{"app-sandbox", "get-task-allow"}
	if !hasEntitlement(ents, "get-task-allow") {
		t.Error("expected get-task-allow to be found")
	}
	if hasEntitlement(ents, "network.client") {
		t.Error("did not expect network.client")
	}
	if hasEntitlement(nil, "get-task-allow") {
		t.Error("nil entitlements should not match")
	}
}
