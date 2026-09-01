package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/timeline"
	"github.com/kaeawc/spectra/internal/updates"
)

func fixedNow() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }

func fakeTimelineProcs() []process.Info {
	return []process.Info{
		{PID: 10, Command: "recent", StartTime: time.Date(2026, 1, 1, 11, 30, 0, 0, time.UTC)},
		{PID: 20, Command: "old", StartTime: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)},
		{PID: 30, Command: "nostart"}, // zero StartTime -> excluded
	}
}

func procSource() timeline.Source { return processStartSource{procs: fakeTimelineProcs} }

func TestRunTimelineWindowsAndSummary(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTimelineWithIO([]string{"--since", "1h"}, &out, &errBuf, fixedNow, procSource())
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "recent [10] started") {
		t.Errorf("expected the recent process event, got:\n%s", s)
	}
	if strings.Contains(s, "old [20]") || strings.Contains(s, "nostart") {
		t.Errorf("out-of-window / zero-start processes should be excluded:\n%s", s)
	}
}

func TestRunTimelineJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runTimelineWithIO([]string{"--since", "6h", "--json"}, &out, &errBuf, fixedNow, procSource()); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"events"`) || !strings.Contains(out.String(), `"source"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunTimelineArgValidation(t *testing.T) {
	for _, args := range [][]string{{"--since", "0"}, {"extra"}, {"--since", "-5m"}} {
		var out, errBuf bytes.Buffer
		if code := runTimelineWithIO(args, &out, &errBuf, fixedNow, procSource()); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestLogSourceMapsEntries(t *testing.T) {
	var gotStart time.Time
	src := logSource{run: func(_ context.Context, q logquery.Query) (logquery.Result, error) {
		gotStart = q.Start
		if q.MinLevel != "error" {
			t.Errorf("expected MinLevel=error, got %q", q.MinLevel)
		}
		return logquery.Result{Entries: []logquery.LogEntry{
			{Timestamp: fixedNow(), Process: "WindowServer", EventMessage: "boom", MessageType: "Fault"},
			{Timestamp: fixedNow(), Process: "kernel", EventMessage: "warn", MessageType: "Default"},
		}}, nil
	}}
	since := fixedNow().Add(-time.Hour)
	evs, err := src.Events(since)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if !gotStart.Equal(since) {
		t.Errorf("query start = %v, want %v", gotStart, since)
	}
	if len(evs) != 2 || evs[0].Summary != "WindowServer: boom" || evs[0].Severity != timeline.SeverityError {
		t.Errorf("fault entry mapping wrong: %+v", evs[0])
	}
	if evs[1].Severity != timeline.SeverityWarn {
		t.Errorf("non-error entry severity = %s, want warn", evs[1].Severity)
	}
}

func TestInstallSourceFiltersAndMaps(t *testing.T) {
	src := installSource{fetch: func(context.Context) ([]updates.InstallEntry, error) {
		return []updates.InstallEntry{
			{Name: "Xcode", Version: "16.2", InstallDate: fixedNow()},
			{Name: "OldApp", Version: "1.0", InstallDate: fixedNow().Add(-48 * time.Hour)},
		}, nil
	}}
	evs, err := src.Events(fixedNow().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 1 || evs[0].Summary != "installed Xcode 16.2" || evs[0].Source != "update" {
		t.Errorf("install mapping/filter wrong: %+v", evs)
	}
}

func TestRunTimelineMergesAllSources(t *testing.T) {
	logs := logSource{run: func(context.Context, logquery.Query) (logquery.Result, error) {
		return logquery.Result{Entries: []logquery.LogEntry{
			{Timestamp: time.Date(2026, 1, 1, 11, 45, 0, 0, time.UTC), Process: "kernel", EventMessage: "oops", MessageType: "Error"},
		}}, nil
	}}
	installs := installSource{fetch: func(context.Context) ([]updates.InstallEntry, error) {
		return []updates.InstallEntry{{Name: "App", InstallDate: time.Date(2026, 1, 1, 11, 50, 0, 0, time.UTC)}}, nil
	}}
	var out, errBuf bytes.Buffer
	code := runTimelineWithIO([]string{"--since", "1h"}, &out, &errBuf, fixedNow, procSource(), logs, installs)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"recent [10] started", "kernel: oops", "installed App"} {
		if !strings.Contains(s, want) {
			t.Errorf("merged timeline missing %q:\n%s", want, s)
		}
	}
	// Chronological order: process(11:30) < log(11:45) < install(11:50).
	if strings.Index(s, "started") >= strings.Index(s, "oops") || strings.Index(s, "oops") >= strings.Index(s, "installed") {
		t.Errorf("events not in chronological order:\n%s", s)
	}
}

func TestRunTimelineBestEffortOnSourceError(t *testing.T) {
	failing := logSource{run: func(context.Context, logquery.Query) (logquery.Result, error) {
		return logquery.Result{}, errors.New("log unavailable")
	}}
	var out, errBuf bytes.Buffer
	code := runTimelineWithIO([]string{"--since", "1h"}, &out, &errBuf, fixedNow, procSource(), failing)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (best effort)", code)
	}
	if !strings.Contains(out.String(), "recent [10] started") {
		t.Errorf("process events should survive a failing log source:\n%s", out.String())
	}
	if !strings.Contains(errBuf.String(), "log:") {
		t.Errorf("expected a per-source error on stderr, got: %q", errBuf.String())
	}
}
