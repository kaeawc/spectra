package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/fleet"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func sharedUnsignedFleet() []fleet.HostSnapshot {
	unsigned := []detect.Result{{Path: "/Applications/Shared.app", TeamID: ""}}
	return []fleet.HostSnapshot{
		{Hostname: "laptop", Snap: snapshot.Snapshot{Apps: unsigned}},
		{Hostname: "ci-mac", Snap: snapshot.Snapshot{Apps: unsigned}},
	}
}

func TestRunFleetIssuesRollsUp(t *testing.T) {
	load := func() ([]fleet.HostSnapshot, error) { return sharedUnsignedFleet(), nil }
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"issues"}, &out, &errBuf, load); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "app-unsigned") || !strings.Contains(s, "2 host(s): ci-mac, laptop") {
		t.Errorf("expected one app-unsigned finding across both hosts; got:\n%s", s)
	}
}

func TestRunFleetIssuesEmptyStore(t *testing.T) {
	load := func() ([]fleet.HostSnapshot, error) { return nil, nil }
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"issues"}, &out, &errBuf, load); code != 1 {
		t.Fatalf("exit = %d, want 1 for empty store", code)
	}
}

func TestRunFleetIssuesRejectsPositional(t *testing.T) {
	load := func() ([]fleet.HostSnapshot, error) { return sharedUnsignedFleet(), nil }
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"issues", "extra"}, &out, &errBuf, load); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
