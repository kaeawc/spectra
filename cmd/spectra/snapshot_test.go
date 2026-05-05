package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

func tempDB(t *testing.T) *store.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestResolveSnapshotNotFoundReturnsError(t *testing.T) {
	db := tempDB(t)
	_, err := resolveSnapshot(context.Background(), db, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent snapshot ID, got nil")
	}
}

func TestSnapshotNameFromArgs(t *testing.T) {
	tests := []struct {
		name         string
		baseline     bool
		explicitName string
		args         []string
		wantName     string
		wantOK       bool
	}{
		{name: "no name", wantOK: true},
		{name: "explicit name", explicitName: "release", wantName: "release", wantOK: true},
		{name: "baseline positional", baseline: true, args: []string{"pre-incident"}, wantName: "pre-incident", wantOK: true},
		{name: "live positional rejected", args: []string{"pre-incident"}, wantOK: false},
		{name: "ambiguous name rejected", baseline: true, explicitName: "a", args: []string{"b"}, wantOK: false},
		{name: "too many args rejected", baseline: true, args: []string{"a", "b"}, wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := snapshotNameFromArgs(tc.baseline, tc.explicitName, tc.args)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.wantName {
				t.Fatalf("name = %q, want %q", got, tc.wantName)
			}
		})
	}
}

func TestResolveBaselineSnapshotByNameAndLatest(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	old := testSnapshot("snap-old", snapshot.KindBaseline, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	newer := testSnapshot("snap-new", snapshot.KindBaseline, time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	live := testSnapshot("snap-live", snapshot.KindLive, time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC))
	saveTestSnapshot(t, db, old, "pre-incident")
	saveTestSnapshot(t, db, newer, "release")
	saveTestSnapshot(t, db, live, "")

	got, err := resolveBaselineSnapshot(ctx, db, "pre-incident")
	if err != nil {
		t.Fatalf("resolve by name: %v", err)
	}
	if got.ID != old.ID {
		t.Fatalf("resolve by name ID = %q, want %q", got.ID, old.ID)
	}

	got, err = resolveBaselineSnapshot(ctx, db, "")
	if err != nil {
		t.Fatalf("resolve latest: %v", err)
	}
	if got.ID != newer.ID {
		t.Fatalf("latest ID = %q, want %q", got.ID, newer.ID)
	}
}

func TestResolveBaselineSnapshotRejectsLiveID(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	live := testSnapshot("snap-live", snapshot.KindLive, time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC))
	saveTestSnapshot(t, db, live, "")

	if _, err := resolveBaselineSnapshot(ctx, db, live.ID); err == nil {
		t.Fatal("expected live snapshot ID to be rejected as a baseline")
	}
}

func testSnapshot(id string, kind snapshot.Kind, takenAt time.Time) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:      id,
		TakenAt: takenAt,
		Kind:    kind,
		Host: snapshot.HostInfo{
			Hostname:    "test.local",
			MachineUUID: "TEST-MACHINE",
			OSName:      "macOS",
		},
	}
}

func saveTestSnapshot(t *testing.T, db *store.DB, snap snapshot.Snapshot, name string) {
	t.Helper()
	input := store.FromSnapshot(snap)
	input.Name = name
	if err := db.SaveSnapshot(context.Background(), input); err != nil {
		t.Fatalf("SaveSnapshot(%s): %v", snap.ID, err)
	}
}

func TestSaveSnapshotNamedPersistsChildTables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	snap := snapshot.Snapshot{
		ID:      "snap-20260505T120000Z-0001",
		TakenAt: time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		Kind:    snapshot.KindLive,
		Host: snapshot.HostInfo{
			Hostname:    "test.local",
			MachineUUID: "TEST-MACHINE",
			OSName:      "macOS",
		},
		Apps: []detect.Result{
			{
				Path:     "/Applications/Foo.app",
				BundleID: "com.example.foo",
				LoginItems: []detect.LoginItem{
					{
						Path:      "/Library/LaunchAgents/com.example.foo.plist",
						Label:     "com.example.foo",
						Scope:     "system",
						RunAtLoad: true,
					},
				},
				GrantedPermissions: []string{"Microphone"},
			},
		},
		Processes: []process.Info{
			{
				PID:     412,
				PPID:    1,
				Command: "Foo",
				RSSKiB:  184320,
				CPUPct:  1.2,
				AppPath: "/Applications/Foo.app",
			},
		},
	}

	if err := saveSnapshotNamed(snap, "with-children"); err != nil {
		t.Fatalf("saveSnapshotNamed: %v", err)
	}

	dbPath, err := store.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	procs, err := db.GetSnapshotProcesses(ctx, snap.ID)
	if err != nil {
		t.Fatalf("GetSnapshotProcesses: %v", err)
	}
	if len(procs) != 1 || procs[0].AppPath != "/Applications/Foo.app" {
		t.Fatalf("process rows = %+v", procs)
	}

	items, err := db.GetLoginItems(ctx, snap.ID)
	if err != nil {
		t.Fatalf("GetLoginItems: %v", err)
	}
	if len(items) != 1 || !items[0].RunAtLoad {
		t.Fatalf("login item rows = %+v", items)
	}

	perms, err := db.GetGrantedPerms(ctx, snap.ID)
	if err != nil {
		t.Fatalf("GetGrantedPerms: %v", err)
	}
	if len(perms) != 1 || perms[0].Service != "Microphone" {
		t.Fatalf("permission rows = %+v", perms)
	}
}
