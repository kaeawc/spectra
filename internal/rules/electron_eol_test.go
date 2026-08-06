package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// elAsOf is after Electron 36's GA (2025-05-27), so every tracked major
// (10..36) counts as released for channels-behind math.
var elAsOf = time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

func electronSnap(asOf time.Time, apps ...detect.Result) snapshot.Snapshot {
	s := baseSnap()
	s.TakenAt = asOf
	s.Apps = apps
	return s
}

func elApp(name, version string) detect.Result {
	return detect.Result{Path: "/Applications/" + name + ".app", UI: "Electron", ElectronVersion: version}
}

func TestElectronMajor(t *testing.T) {
	cases := map[string]int{"22.3.27": 22, "36.0.0": 36, "1.8.4": 1, "9": 9, "": 0, "abc": 0}
	for in, want := range cases {
		if got := electronMajor(in); got != want {
			t.Errorf("electronMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestElectronReleaseTableEmbeds(t *testing.T) {
	if electronTable.floor != 10 {
		t.Errorf("floor = %d, want 10", electronTable.floor)
	}
	r, ok := electronTable.byMajor[22]
	if !ok || r.Chromium != 108 {
		t.Errorf("electron 22 -> chromium %d (ok=%v), want 108", r.Chromium, ok)
	}
}

func TestElectronChromiumEOLSeverity(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		want     int
		severity Severity
	}{
		{"Ancient", "22.3.27", 1, SeverityHigh},      // 14 channels behind
		{"WellBehind", "30.0.0", 1, SeverityHigh},    // 6 behind → high
		{"OutOfWindow", "33.0.0", 1, SeverityMedium}, // 3 behind → medium
		{"OldestSupported", "34.0.0", 0, ""},         // 2 behind → supported
		{"Supported", "35.1.0", 0, ""},               // 1 behind → supported
		{"BelowFloor", "9.0.0", 1, SeverityHigh},     // predates the table
		{"NewerThanTable", "99.0.0", 0, ""},          // can't assess → silent
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := electronSnap(elAsOf, elApp(c.name, c.version))
			f := ruleElectronChromiumEOL().MatchFn(s)
			if len(f) != c.want {
				t.Fatalf("findings = %d, want %d: %+v", len(f), c.want, f)
			}
			if c.want == 0 {
				return
			}
			if f[0].Severity != c.severity {
				t.Errorf("severity = %q, want %q", f[0].Severity, c.severity)
			}
			if f[0].RuleID != "electron-chromium-eol" {
				t.Errorf("rule id = %q", f[0].RuleID)
			}
			if f[0].Subject != c.name {
				t.Errorf("subject = %q, want %q", f[0].Subject, c.name)
			}
		})
	}
}

func TestElectronChromiumEOLMessageNamesChromium(t *testing.T) {
	s := electronSnap(elAsOf, elApp("LegacyChatApp", "22.3.27"))
	f := ruleElectronChromiumEOL().MatchFn(s)
	if len(f) != 1 {
		t.Fatalf("want 1 finding, got %d", len(f))
	}
	if !strings.Contains(f[0].Message, "Chromium 108") {
		t.Errorf("message should name bundled Chromium 108: %q", f[0].Message)
	}
	if f[0].Fix == "" {
		t.Error("fix should be non-empty")
	}
}

func TestElectronChromiumEOLSkipsIrrelevant(t *testing.T) {
	s := electronSnap(elAsOf,
		detect.Result{Path: "/Applications/Native.app", UI: "AppKit", ElectronVersion: "22.0.0"}, // not an Electron UI
		elApp("NoVersion", ""),  // no version string
		elApp("Garbage", "abc"), // unparseable version
	)
	if f := ruleElectronChromiumEOL().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no findings, got %d: %+v", len(f), f)
	}
}

func TestElectronChromiumEOLInCatalog(t *testing.T) {
	var found bool
	for _, r := range V1Catalog() {
		if r.ID == "electron-chromium-eol" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("V1Catalog missing electron-chromium-eol")
	}
}
