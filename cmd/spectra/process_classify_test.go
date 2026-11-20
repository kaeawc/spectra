package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/process"
)

// TestProcessJSONWithoutClassifyIsPinned guards the contract that output
// without --classify is byte-for-byte what it was before classification.
func TestProcessJSONWithoutClassifyIsPinned(t *testing.T) {
	want, err := os.ReadFile("testdata/process_unclassified.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pinnedProcessFixture()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("unclassified JSON changed:\n%s", buf.String())
	}
}

func TestProcessJSONWithClassifyAddsFields(t *testing.T) {
	procs := pinnedProcessFixture()[:1]
	procs[0].Role = classify.RoleAppHelper
	procs[0].Runtime = classify.RuntimeElectron
	procs[0].App = &classify.App{Name: "Slack", BundlePath: "/Applications/Slack.app", BundleID: "com.tinyspeck.slackmacgap"}
	procs[0].Signing = &classify.Signing{Status: classify.SigningDeveloperID, TeamID: "BQR82RBBHL", HardenedRuntime: true, Validated: true}
	procs[0].Evidence = []string{"role: x", "runtime: y"}
	data, err := json.Marshal(procs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"role":"app_helper"`, `"runtime":"electron"`,
		`"app":{"name":"Slack","bundle_path":"/Applications/Slack.app","bundle_id":"com.tinyspeck.slackmacgap"}`,
		`"signing":{"status":"developer_id","team_id":"BQR82RBBHL","hardened_runtime":true,"validated":true}`,
		`"evidence":["role: x","runtime: y"]`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON missing %s:\n%s", want, data)
		}
	}
}

func TestProcessClassColumnsOffAddNothing(t *testing.T) {
	off := newProcessClassColumns(false)
	if off.header() != "" || off.row(process.Info{Role: classify.RoleApp}) != "" || off.width() != 0 {
		t.Fatalf("classification columns leak into the default table: %+v", off)
	}
	on := processClassColumns{on: true}
	if got := on.row(process.Info{Role: classify.RoleCLI, Runtime: classify.RuntimeGo}); got != "cli         go        -"+strings.Repeat(" ", classAppWidth+1) {
		t.Fatalf("row = %q", got)
	}
	if got := (processClassColumns{on: true, sign: true}).row(process.Info{}); !strings.HasPrefix(got, "-           -         -") {
		t.Fatalf("empty row = %q", got)
	}
}

func TestProcessClassColumnsShowBoundedApp(t *testing.T) {
	cols := processClassColumns{on: true, sign: true}
	if !strings.Contains(cols.header(), "APP") {
		t.Fatalf("header = %q", cols.header())
	}
	long := process.Info{App: &classify.App{Name: "Visual Studio Code - Insiders Edition"}}
	row := cols.row(long)
	if !strings.Contains(row, "Visual Studio C…") || strings.Contains(row, "Insiders") {
		t.Fatalf("long name not truncated to %d runes: %q", classAppWidth, row)
	}
	if len([]rune(row)) != cols.width() || len([]rune(cols.header())) != cols.width() {
		t.Fatalf("row/header width %d/%d, want %d", len([]rune(row)), len([]rune(cols.header())), cols.width())
	}
	if got := cols.row(process.Info{App: &classify.App{Name: "Slack"}}); !strings.Contains(got, "Slack ") {
		t.Fatalf("row = %q", got)
	}
}

// pinnedProcessFixture sets every field `spectra process -json` emitted before
// --classify existed, so the pin test catches any change to that output.
func pinnedProcessFixture() []process.Info {
	start := time.Date(2026, 11, 19, 8, 30, 0, 0, time.UTC)
	return []process.Info{{
		PID:                 4242,
		PPID:                1,
		UID:                 501,
		User:                "alice",
		Command:             "Slack Helper",
		BSDName:             "Slack Helper",
		ExecutablePath:      "/Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper",
		FullCommandLine:     "/Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper --type=renderer",
		RSSKiB:              2048,
		VSizeKiB:            4096,
		VSZBytes:            4096 * 1024,
		ThreadCount:         12,
		CPUPct:              1.5,
		CPUPercent:          1.5,
		MemPercent:          0.25,
		StartTime:           start,
		StartedAt:           start,
		Elapsed:             90 * time.Second,
		OpenFDs:             7,
		FDBreakdown:         &process.FDBreakdown{Total: 7, PTY: 1, Socket: 2, Regular: 3, Pipe: 1},
		ListeningPorts:      []int{8080},
		OutboundConnections: []string{"1.2.3.4:443"},
		LogFiles:            []string{"/tmp/x.log"},
		Cwd:                 "/",
		AppPath:             "/Applications/Slack.app",
	}, {PID: 7, Command: "launchd", FullCommandLine: "/sbin/launchd"}}
}
