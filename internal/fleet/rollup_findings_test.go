package fleet

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/rules"
)

func TestRollupFindingsDedupesAcrossHosts(t *testing.T) {
	// laptop and ci-mac both have the same unsigned app -> one rolled-up finding
	// with both hosts; other-mac has a different unsigned app -> separate finding.
	hosts := []HostSnapshot{
		host("laptop", []detect.Result{{Path: "/Applications/Shared.app", TeamID: ""}}, nil),
		host("ci-mac", []detect.Result{{Path: "/Applications/Shared.app", TeamID: ""}}, nil),
		host("other-mac", []detect.Result{{Path: "/Applications/Solo.app", TeamID: ""}}, nil),
	}
	findings := RollupFindings(hosts, rules.V1Catalog())

	var shared, solo *FleetFinding
	for i := range findings {
		if findings[i].RuleID != "app-unsigned" {
			continue
		}
		switch findings[i].Subject {
		case "Shared":
			shared = &findings[i]
		case "Solo":
			solo = &findings[i]
		}
	}
	if shared == nil || solo == nil {
		t.Fatalf("expected Shared and Solo app-unsigned findings; got %+v", findings)
	}
	if strings.Join(shared.Hosts, ",") != "ci-mac,laptop" {
		t.Errorf("Shared hosts = %v, want [ci-mac laptop]", shared.Hosts)
	}
	if len(solo.Hosts) != 1 || solo.Hosts[0] != "other-mac" {
		t.Errorf("Solo hosts = %v, want [other-mac]", solo.Hosts)
	}
	// the more widespread finding must sort ahead of the single-host one
	if indexOfRule(findings, "app-unsigned", "Shared") > indexOfRule(findings, "app-unsigned", "Solo") {
		t.Error("the 2-host finding should rank ahead of the 1-host finding")
	}
}

func TestRollupFindingsSkipsEmptyHosts(t *testing.T) {
	hosts := []HostSnapshot{
		host("laptop", []detect.Result{{Path: "/Applications/X.app", TeamID: ""}}, nil),
		{Hostname: "offline", Empty: true},
	}
	for _, f := range RollupFindings(hosts, rules.V1Catalog()) {
		for _, h := range f.Hosts {
			if h == "offline" {
				t.Error("an empty host must not appear in any finding")
			}
		}
	}
}

func indexOfRule(fs []FleetFinding, rule, subject string) int {
	for i, f := range fs {
		if f.RuleID == rule && f.Subject == subject {
			return i
		}
	}
	return -1
}
