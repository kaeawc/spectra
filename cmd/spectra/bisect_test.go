package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

func bsnap(id string, day int, teamID string) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:      id,
		TakenAt: time.Date(2026, 8, day, 9, 0, 0, 0, time.UTC),
		Apps:    []detect.Result{{Path: "/Applications/Foo.app", BundleID: "com.x.foo", TeamID: teamID, AppVersion: "2." + id}},
	}
}

func bisectLoader() ([]snapshot.Snapshot, string, error) {
	return []snapshot.Snapshot{bsnap("1", 1, "TEAM1"), bsnap("2", 2, ""), bsnap("3", 3, "")}, "work-mac", nil
}

func TestRunBisectFound(t *testing.T) {
	load := func(string) ([]snapshot.Snapshot, string, error) { return bisectLoader() }
	var out, errBuf bytes.Buffer
	if code := runBisectWithIO([]string{"app-unsigned"}, &out, &errBuf, load); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "first tripped in 2") {
		t.Errorf("output = %q", s)
	}
	if !strings.Contains(s, "correlated") || !strings.Contains(s, "capture cadence") {
		t.Errorf("output should carry the correlation + cadence caveats; got:\n%s", s)
	}
}

func TestRunBisectUnknownRule(t *testing.T) {
	load := func(string) ([]snapshot.Snapshot, string, error) { return bisectLoader() }
	var out, errBuf bytes.Buffer
	if code := runBisectWithIO([]string{"no-such-rule"}, &out, &errBuf, load); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown rule id") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestRunBisectNoArgs(t *testing.T) {
	load := func(string) ([]snapshot.Snapshot, string, error) { return bisectLoader() }
	var out, errBuf bytes.Buffer
	if code := runBisectWithIO(nil, &out, &errBuf, load); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunBisectLoadError(t *testing.T) {
	load := func(string) ([]snapshot.Snapshot, string, error) { return nil, "", errors.New("db locked") }
	var out, errBuf bytes.Buffer
	if code := runBisectWithIO([]string{"app-unsigned"}, &out, &errBuf, load); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRunBisectEmptySeries(t *testing.T) {
	load := func(string) ([]snapshot.Snapshot, string, error) { return nil, "work-mac", nil }
	var out, errBuf bytes.Buffer
	if code := runBisectWithIO([]string{"app-unsigned"}, &out, &errBuf, load); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestResolveBisectHost(t *testing.T) {
	rows := []store.HostRow{
		{Hostname: "laptop", MachineUUID: "A"},
		{Hostname: "work-mac", MachineUUID: "B"},
	}
	// explicit hostname selects it
	if hr, err := resolveBisectHost(rows, "work-mac"); err != nil || hr.MachineUUID != "B" {
		t.Errorf("resolve work-mac = %+v, %v", hr, err)
	}
	// unknown hostname errors
	if _, err := resolveBisectHost(rows, "nope"); err == nil {
		t.Error("expected error for unknown host")
	}
	// ambiguous default (>1 host, no --host) errors
	if _, err := resolveBisectHost(rows, ""); err == nil {
		t.Error("expected error when multiple hosts and no --host")
	}
	// single host defaults
	if hr, err := resolveBisectHost(rows[:1], ""); err != nil || hr.MachineUUID != "A" {
		t.Errorf("single-host default = %+v, %v", hr, err)
	}
	// empty store errors
	if _, err := resolveBisectHost(nil, ""); err == nil {
		t.Error("expected error for empty store")
	}
}
