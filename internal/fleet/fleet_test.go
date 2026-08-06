package fleet

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

func host(name string, apps []detect.Result, jdks []toolchain.JDKInstall) HostSnapshot {
	return HostSnapshot{
		Hostname: name,
		Snap: snapshot.Snapshot{
			Apps:       apps,
			Toolchains: toolchain.Toolchains{JDKs: jdks},
		},
	}
}

func TestRollupSymptom(t *testing.T) {
	hosts := []HostSnapshot{
		host("laptop", []detect.Result{{Path: "/Applications/Unsigned.app", TeamID: ""}}, nil),
		host("work-mac", []detect.Result{{Path: "/Applications/Signed.app", TeamID: "TEAM1"}}, nil),
		{Hostname: "offline", Empty: true},
	}
	r := RollupSymptom(hosts, "app-unsigned", rules.V1Catalog())
	if strings.Join(r.Firing, ",") != "laptop" {
		t.Errorf("firing = %v, want [laptop]", r.Firing)
	}
	if strings.Join(r.Clear, ",") != "work-mac" {
		t.Errorf("clear = %v, want [work-mac]", r.Clear)
	}
	if strings.Join(r.Unknown, ",") != "offline" {
		t.Errorf("unknown = %v, want [offline]", r.Unknown)
	}
}

func TestDriftJDK(t *testing.T) {
	hosts := []HostSnapshot{
		host("laptop", nil, []toolchain.JDKInstall{{VersionMajor: 21, ReleaseString: "21.0.6", Vendor: "Temurin"}}),
		host("ci-mac", nil, []toolchain.JDKInstall{{VersionMajor: 17, ReleaseString: "17.0.10", Vendor: "Zulu"}}),
		host("bare", nil, nil),
		{Hostname: "offline", Empty: true},
	}
	cells := DriftJDK(hosts)
	got := map[string]string{}
	for _, c := range cells {
		got[c.Host] = c.Value
	}
	if got["laptop"] != "21.0.6 (Temurin)" {
		t.Errorf("laptop jdk = %q", got["laptop"])
	}
	if got["ci-mac"] != "17.0.10 (Zulu)" {
		t.Errorf("ci-mac jdk = %q", got["ci-mac"])
	}
	if got["bare"] != "none" {
		t.Errorf("bare jdk = %q, want none", got["bare"])
	}
	if got["offline"] != "unknown" {
		t.Errorf("offline jdk = %q, want unknown", got["offline"])
	}
}

func TestDuplicateHostnamesDisambiguated(t *testing.T) {
	// Two machines share a hostname; the labels must stay distinct.
	hosts := []HostSnapshot{
		{Hostname: "MacBook-Pro", MachineUUID: "AAAAAAAA-1111", Snap: snapshot.Snapshot{Toolchains: toolchain.Toolchains{JDKs: []toolchain.JDKInstall{{VersionMajor: 21, ReleaseString: "21"}}}}},
		{Hostname: "MacBook-Pro", MachineUUID: "BBBBBBBB-2222", Snap: snapshot.Snapshot{Toolchains: toolchain.Toolchains{JDKs: []toolchain.JDKInstall{{VersionMajor: 17, ReleaseString: "17"}}}}},
	}
	cells := DriftJDK(hosts)
	if len(cells) != 2 || cells[0].Host == cells[1].Host {
		t.Fatalf("labels must be distinct for same-hostname machines, got %+v", cells)
	}
	for _, c := range cells {
		if !strings.HasPrefix(c.Host, "MacBook-Pro (") {
			t.Errorf("expected uuid-disambiguated label, got %q", c.Host)
		}
	}
}

func TestDriftApp(t *testing.T) {
	hosts := []HostSnapshot{
		host("laptop", []detect.Result{{BundleID: "com.example.app", AppVersion: "2.1"}}, nil),
		host("ci-mac", []detect.Result{{BundleID: "com.other", AppVersion: "9"}}, nil),
		{Hostname: "offline", Empty: true},
	}
	cells := DriftApp(hosts, "com.example.app")
	got := map[string]string{}
	for _, c := range cells {
		got[c.Host] = c.Value
	}
	if got["laptop"] != "2.1" {
		t.Errorf("laptop app = %q, want 2.1", got["laptop"])
	}
	if got["ci-mac"] != "absent" {
		t.Errorf("ci-mac app = %q, want absent", got["ci-mac"])
	}
	if got["offline"] != "unknown" {
		t.Errorf("offline app = %q, want unknown", got["offline"])
	}
}
