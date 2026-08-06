package rules

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/snapshot"
)

//go:generate ../../scripts/electron/gen_releases.sh

//go:embed electron_releases.json
var electronReleasesJSON []byte

const electronEOLFix = "Update the app to a build on a currently-supported Electron (its latest three majors). " +
	"If you don't ship this app, flag the outdated build to its vendor or limit its exposure to untrusted content."

// electronRelease maps one Electron major to the runtime versions it bundles.
type electronRelease struct {
	Major    int    `json:"major"`
	Chromium int    `json:"chromium"`
	Node     string `json:"node,omitempty"`
	Released string `json:"released"` // stable GA date, YYYY-MM-DD
}

type electronReleaseFile struct {
	TableAsOf string            `json:"table_as_of"`
	Releases  []electronRelease `json:"releases"`
}

// electronReleaseTable is the parsed, indexed form of electron_releases.json.
type electronReleaseTable struct {
	byMajor  map[int]electronRelease
	released map[int]time.Time
	floor    int // lowest tracked Electron major
}

// electronTable is the compiled-in release table. It parses embedded static
// data with no network access at build or runtime.
var electronTable = loadElectronTable(electronReleasesJSON)

func loadElectronTable(raw []byte) electronReleaseTable {
	var f electronReleaseFile
	if err := json.Unmarshal(raw, &f); err != nil {
		panic(fmt.Sprintf("rules: invalid embedded electron_releases.json: %v", err))
	}
	t := electronReleaseTable{
		byMajor:  make(map[int]electronRelease, len(f.Releases)),
		released: make(map[int]time.Time, len(f.Releases)),
	}
	for _, r := range f.Releases {
		ts, err := time.Parse("2006-01-02", r.Released)
		if err != nil {
			panic(fmt.Sprintf("rules: invalid released date %q for electron %d: %v", r.Released, r.Major, err))
		}
		t.byMajor[r.Major] = r
		t.released[r.Major] = ts
		if t.floor == 0 || r.Major < t.floor {
			t.floor = r.Major
		}
	}
	return t
}

// channelsBehind counts tracked stable Electron majors released on or before
// asOf that are newer than major. Electron patches only its latest three
// stable majors, so three-or-more channels behind means the bundled Chromium
// is end-of-life.
func (t electronReleaseTable) channelsBehind(major int, asOf time.Time) int {
	n := 0
	for m, ts := range t.released {
		if m > major && !ts.After(asOf) {
			n++
		}
	}
	return n
}

// electronMajor extracts the leading integer from an Electron version string
// like "22.3.27". Unlike parseMajor it deliberately does not special-case a
// "1." prefix: Electron's own 1.x releases are ancient and read as major 1.
func electronMajor(v string) int {
	first, _, _ := strings.Cut(v, ".")
	return atoi(first)
}

// ruleElectronChromiumEOL fires for each classified Electron app whose bundled
// Chromium is past Electron's three-major support window. It is the web analog
// of jvm-eol-version: the embedded Chromium is the real remotely-exposed
// surface, and nothing else in the stack judges it. Deterministic on
// s.TakenAt, so it never calls time.Now().
func ruleElectronChromiumEOL() Rule {
	return Rule{
		ID:       "electron-chromium-eol",
		Severity: SeverityMedium,
		MatchFn:  matchElectronChromiumEOL,
	}
}

func matchElectronChromiumEOL(s snapshot.Snapshot) []Finding {
	var findings []Finding
	for _, app := range s.Apps {
		if app.UI != "Electron" || app.ElectronVersion == "" {
			continue
		}
		major := electronMajor(app.ElectronVersion)
		if major <= 0 {
			continue
		}
		if f, ok := electronFindingFor(app, major, s.TakenAt); ok {
			findings = append(findings, f)
		}
	}
	return findings
}

// electronFindingFor returns the finding for one Electron app, if any. Majors
// below the tracked floor are certainly EOL; majors newer than the table
// cannot be judged and stay silent (a safe under-report as the table ages).
func electronFindingFor(app detect.Result, major int, asOf time.Time) (Finding, bool) {
	name := appDisplayName(app.Path)
	rel, known := electronTable.byMajor[major]
	if !known {
		if major < electronTable.floor {
			return Finding{
				RuleID:   "electron-chromium-eol",
				Severity: SeverityHigh,
				Subject:  name,
				Message:  fmt.Sprintf("%s bundles Electron %d, which predates tracked Electron releases and is long past end-of-life — its Chromium receives no security patches.", name, major),
				Fix:      electronEOLFix,
			}, true
		}
		return Finding{}, false
	}
	behind := electronTable.channelsBehind(major, asOf)
	if behind <= 2 {
		return Finding{}, false // still within Electron's supported-three window
	}
	severity := SeverityMedium
	if behind >= 6 {
		severity = SeverityHigh
	}
	return Finding{
		RuleID:   "electron-chromium-eol",
		Severity: severity,
		Subject:  name,
		Message: fmt.Sprintf("%s bundles Electron %d (Chromium %d, released %s) — %d stable channels behind. Electron patches only its latest three majors, so this Chromium is end-of-life and no longer receives security fixes.",
			name, major, rel.Chromium, rel.Released, behind),
		Fix: electronEOLFix,
	}, true
}
