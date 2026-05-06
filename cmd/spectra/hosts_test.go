package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/store"
)

func TestRunHostsWithTable(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith(nil, &stdout, &stderr, func(context.Context, bool) ([]store.HostRow, error) {
		return []store.HostRow{
			{
				MachineUUID:   "UUID-1234567890",
				Hostname:      "work-mac.local",
				OSName:        "macOS",
				OSVersion:     "15.6.1",
				LastSeen:      time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
				SnapshotCount: 3,
			},
		}, nil
	}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "UUID-1234567890") || !strings.Contains(out, "work-mac.local") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunHostsWithJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith([]string{"--json"}, &stdout, &stderr, func(context.Context, bool) ([]store.HostRow, error) {
		return []store.HostRow{{MachineUUID: "UUID-A", Hostname: "a.local"}}, nil
	}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	var rows []store.HostRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].MachineUUID != "UUID-A" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRunHostsWithEmptyList(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith(nil, &stdout, &stderr, func(context.Context, bool) ([]store.HostRow, error) {
		return nil, nil
	}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "no hosts stored") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHostsWithListError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith(nil, &stdout, &stderr, func(context.Context, bool) ([]store.HostRow, error) {
		return nil, errors.New("db unavailable")
	}, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "db unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHostsWithProbe(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith([]string{"--probe"}, &stdout, &stderr, func(context.Context, bool) ([]store.HostRow, error) {
		return []store.HostRow{
			{MachineUUID: "UUID-A", Hostname: "work-mac.local", OSName: "macOS", OSVersion: "15.6.1"},
			{MachineUUID: "UUID-B", Hostname: "deadbox.local", OSName: "macOS", OSVersion: "15.6.1"},
		}, nil
	}, func(_ context.Context, host string) error {
		if host == "work-mac.local" {
			return nil
		}
		return errors.New("timeout")
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "UUID-A") || !strings.Contains(out, "UUID-B") {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(out, "yes") || !strings.Contains(out, "no") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunHostsWithProbeJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith([]string{"--probe", "--json"}, &stdout, &stderr, func(context.Context, bool) ([]store.HostRow, error) {
		return []store.HostRow{
			{MachineUUID: "UUID-A", Hostname: "work-mac.local", OSName: "macOS", OSVersion: "15.6.1"},
		}, nil
	}, func(_ context.Context, host string) error {
		if host == "work-mac.local" {
			return nil
		}
		return errors.New("timeout")
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	var rows []struct {
		store.HostRow
		Reachable bool   `json:"reachable"`
		Error     string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Reachable {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRunHostsWithDiscover(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith([]string{"--discover"}, &stdout, &stderr, func(_ context.Context, discover bool) ([]store.HostRow, error) {
		if !discover {
			t.Fatal("discover flag was not passed")
		}
		return []store.HostRow{
			{MachineUUID: "UUID-A", Hostname: "work-mac.local", OSName: "macOS", OSVersion: "15.6.1"},
		}, nil
	}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "work-mac.local") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestListHostRowsWithDiscoveredPeers(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbPath := filepath.Join(home, ".spectra", "spectra.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	takenAt := time.Date(2026, time.May, 5, 12, 0, 0, 0, time.UTC)
	if err := db.SaveSnapshot(ctx, store.SnapshotInput{
		ID:          "snap-work-mac",
		MachineUUID: "uuid-work-mac",
		TakenAt:     takenAt,
		Kind:        "live",
		Hostname:    "work-mac",
		OSName:      "macOS",
		OSVersion:   "15.0",
	}); err != nil {
		t.Fatal(err)
	}

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return []string{"work-mac", "alice-laptop"}, nil
	}
	origNow := discoverHostRowsNow
	discoverHostRowsNow = func() time.Time { return time.Date(2026, time.May, 5, 13, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		fanDiscoverPeers = origDiscoverPeers
		discoverHostRowsNow = origNow
	})

	rows, err := listHostRows(ctx, db, true)
	if err != nil {
		t.Fatalf("listHostRows = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].Hostname != "alice-laptop" || rows[0].MachineUUID != "alice-laptop" {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	if rows[1].Hostname != "work-mac" || rows[1].MachineUUID != "uuid-work-mac" {
		t.Fatalf("rows[1] = %+v", rows[1])
	}
}

func TestListHostRowsDiscoveryErrorFallback(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbPath := filepath.Join(home, ".spectra", "spectra.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	takenAt := time.Date(2026, time.May, 5, 12, 0, 0, 0, time.UTC)
	if err := db.SaveSnapshot(ctx, store.SnapshotInput{
		ID:          "snap-work-mac",
		MachineUUID: "uuid-work-mac",
		TakenAt:     takenAt,
		Kind:        "live",
		Hostname:    "work-mac",
		OSName:      "macOS",
		OSVersion:   "15.0",
	}); err != nil {
		t.Fatal(err)
	}

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return nil, errors.New("tailscale unavailable")
	}
	t.Cleanup(func() { fanDiscoverPeers = origDiscoverPeers })

	rows, err := listHostRows(ctx, db, true)
	if err != nil {
		t.Fatalf("listHostRows = %v", err)
	}
	if len(rows) != 1 || rows[0].Hostname != "work-mac" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestListHostRowsDiscoveryErrorWithoutDb(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbPath := filepath.Join(home, ".spectra", "spectra.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return nil, errors.New("tailscale unavailable")
	}
	t.Cleanup(func() { fanDiscoverPeers = origDiscoverPeers })

	_, err = listHostRows(ctx, db, true)
	if err == nil {
		t.Fatal("listHostRows succeeded, want error")
	}
	if !strings.Contains(err.Error(), "discover remote hosts") {
		t.Fatalf("err = %v", err)
	}
}
