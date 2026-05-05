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
