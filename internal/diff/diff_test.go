package diff

import (
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// base returns a minimal snapshot for tests.
func base() snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:   "snap-a",
		Kind: snapshot.KindLive,
		Host: snapshot.HostInfo{
			Hostname:    "mac-a",
			OSVersion:   "14.4",
			OSBuild:     "23E214",
			CPUBrand:    "Apple M3",
			CPUCores:    10,
			RAMBytes:    17179869184,
			Architecture: "arm64",
		},
	}
}

func TestCompareIdentical(t *testing.T) {
	a := base()
	r := Compare(a, a)
	if r.HasChanges() {
		for _, s := range r.Sections {
			for _, c := range s.Changes {
				t.Errorf("[%s] unexpected change: %+v", s.Name, c)
			}
		}
	}
}

func TestDiffHostChanged(t *testing.T) {
	a := base()
	b := base()
	b.Host.OSVersion = "15.0"
	b.Host.OSBuild = "24A336"

	r := Compare(a, b)
	sec := findSection(r, "host")
	if sec == nil {
		t.Fatal("host section missing")
	}
	if !hasChange(sec.Changes, Changed, "os_version") {
		t.Error("expected os_version changed")
	}
	if !hasChange(sec.Changes, Changed, "os_build") {
		t.Error("expected os_build changed")
	}
	if hasChange(sec.Changes, Changed, "hostname") {
		t.Error("unexpected hostname change")
	}
}

func TestDiffAppsAddedRemoved(t *testing.T) {
	a := base()
	b := base()
	a.Apps = []detect.Result{
		{BundleID: "com.example.old", AppVersion: "1.0", Path: "/Applications/Old.app"},
	}
	b.Apps = []detect.Result{
		{BundleID: "com.example.new", AppVersion: "2.0", Path: "/Applications/New.app"},
	}

	r := Compare(a, b)
	sec := findSection(r, "apps")
	if sec == nil {
		t.Fatal("apps section missing")
	}
	if !hasChange(sec.Changes, Removed, "com.example.old") {
		t.Error("expected old app removed")
	}
	if !hasChange(sec.Changes, Added, "com.example.new") {
		t.Error("expected new app added")
	}
}

func TestDiffAppsVersionChanged(t *testing.T) {
	a := base()
	b := base()
	a.Apps = []detect.Result{{BundleID: "com.example.app", AppVersion: "1.0"}}
	b.Apps = []detect.Result{{BundleID: "com.example.app", AppVersion: "2.0"}}

	r := Compare(a, b)
	sec := findSection(r, "apps")
	if !hasChange(sec.Changes, Changed, "com.example.app") {
		t.Error("expected version change")
	}
}

func TestDiffJDKsAddedRemoved(t *testing.T) {
	a := base()
	b := base()
	a.Toolchains.JDKs = []toolchain.JDKInstall{
		{VersionMajor: 17, VersionMinor: 0, VersionPatch: 5, Vendor: "Temurin", Source: "brew"},
	}
	b.Toolchains.JDKs = []toolchain.JDKInstall{
		{VersionMajor: 21, VersionMinor: 0, VersionPatch: 1, Vendor: "Temurin", Source: "brew"},
	}

	r := Compare(a, b)
	sec := findSection(r, "jdks")
	if sec == nil {
		t.Fatal("jdks section missing")
	}
	if len(sec.Changes) != 2 {
		t.Errorf("expected 2 jdk changes (removed+added), got %d", len(sec.Changes))
	}
}

func TestDiffBrewFormulae(t *testing.T) {
	a := base()
	b := base()
	a.Toolchains.Brew.Formulae = []toolchain.BrewFormula{
		{Name: "wget", Version: "1.21"},
		{Name: "curl", Version: "8.1"},
	}
	b.Toolchains.Brew.Formulae = []toolchain.BrewFormula{
		{Name: "wget", Version: "1.22"},
		{Name: "jq", Version: "1.7"},
	}

	r := Compare(a, b)
	sec := findSection(r, "brew_formulae")
	if !hasChange(sec.Changes, Changed, "wget") {
		t.Error("expected wget version changed")
	}
	if !hasChange(sec.Changes, Removed, "curl") {
		t.Error("expected curl removed")
	}
	if !hasChange(sec.Changes, Added, "jq") {
		t.Error("expected jq added")
	}
}

func TestDiffListeningPorts(t *testing.T) {
	a := base()
	b := base()
	a.Network.ListeningPorts = []netstate.ListeningPort{{Port: 8080, Proto: "tcp"}}
	b.Network.ListeningPorts = []netstate.ListeningPort{{Port: 9090, Proto: "tcp"}}

	r := Compare(a, b)
	sec := findSection(r, "listening_ports")
	if !hasChange(sec.Changes, Removed, "tcp:8080") {
		t.Error("expected tcp:8080 removed")
	}
	if !hasChange(sec.Changes, Added, "tcp:9090") {
		t.Error("expected tcp:9090 added")
	}
}

func TestDiffPathDirs(t *testing.T) {
	a := base()
	b := base()
	a.Toolchains.Env.PathDirs = []string{"/usr/local/bin", "/usr/bin", "/bin"}
	b.Toolchains.Env.PathDirs = []string{"/opt/homebrew/bin", "/usr/bin", "/bin"}

	r := Compare(a, b)
	sec := findSection(r, "path_dirs")
	if !hasChange(sec.Changes, Removed, "/usr/local/bin") {
		t.Error("expected /usr/local/bin removed")
	}
	if !hasChange(sec.Changes, Added, "/opt/homebrew/bin") {
		t.Error("expected /opt/homebrew/bin added")
	}
}

func TestDiffPathDirsReordered(t *testing.T) {
	a := base()
	b := base()
	a.Toolchains.Env.PathDirs = []string{"/usr/bin", "/usr/local/bin"}
	b.Toolchains.Env.PathDirs = []string{"/usr/local/bin", "/usr/bin"}

	r := Compare(a, b)
	sec := findSection(r, "path_dirs")
	// Both dirs present but at different positions → Changed.
	if !hasChange(sec.Changes, Changed, "/usr/bin") && !hasChange(sec.Changes, Changed, "/usr/local/bin") {
		t.Error("expected reorder to show as Changed")
	}
}

func TestDiffSysctls(t *testing.T) {
	a := base()
	b := base()
	a.Sysctls = map[string]string{
		"hw.ncpu":     "10",
		"hw.memsize":  "17179869184",
	}
	b.Sysctls = map[string]string{
		"hw.ncpu":      "10",
		"kern.maxfiles": "12288",
	}

	r := Compare(a, b)
	sec := findSection(r, "sysctls")
	if !hasChange(sec.Changes, Removed, "hw.memsize") {
		t.Error("expected hw.memsize removed")
	}
	if !hasChange(sec.Changes, Added, "kern.maxfiles") {
		t.Error("expected kern.maxfiles added")
	}
	if hasChange(sec.Changes, Changed, "hw.ncpu") {
		t.Error("hw.ncpu unchanged, should not appear")
	}
}

// --- helpers ---

func findSection(r Result, name string) *Section {
	for i := range r.Sections {
		if r.Sections[i].Name == name {
			return &r.Sections[i]
		}
	}
	return nil
}

func hasChange(changes []Change, kind ChangeKind, key string) bool {
	for _, c := range changes {
		if c.Kind == kind && c.Key == key {
			return true
		}
	}
	return false
}
