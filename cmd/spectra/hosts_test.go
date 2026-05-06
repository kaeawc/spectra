package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/store"
)

func TestRunHostsWithTable(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runHostsWith(nil, &stdout, &stderr, func(context.Context) ([]store.HostRow, error) {
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
	})
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
	code := runHostsWith([]string{"--json"}, &stdout, &stderr, func(context.Context) ([]store.HostRow, error) {
		return []store.HostRow{{MachineUUID: "UUID-A", Hostname: "a.local"}}, nil
	})
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
	code := runHostsWith(nil, &stdout, &stderr, func(context.Context) ([]store.HostRow, error) {
		return nil, nil
	})
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
	code := runHostsWith(nil, &stdout, &stderr, func(context.Context) ([]store.HostRow, error) {
		return nil, errors.New("db unavailable")
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "db unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
