package store

import (
	"context"
	"testing"
)

func txRows() SnapshotRows {
	return SnapshotRows{
		Processes:    []ProcessSnapshotRow{{PID: 10, PPID: 1, Command: "java", RSSKiB: 100}},
		LoginItems:   []LoginItemRow{{BundleID: "com.example", PlistPath: "/L/a.plist", Label: "a"}},
		GrantedPerms: []GrantedPermRow{{BundleID: "com.example", Service: "kTCCServiceCamera"}},
	}
}

func cancelInSnapshotTx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	testHookInSnapshotTx = cancel
	t.Cleanup(func() {
		testHookInSnapshotTx = nil
		cancel()
	})
	return ctx
}

func snapshotCount(t *testing.T, db *DB) int {
	t.Helper()
	rows, err := db.ListSnapshots(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestSaveSnapshotWithRowsIsAtomic(t *testing.T) {
	db := openTestDB(t)
	snap := sampleInput()
	if err := db.SaveSnapshotWithRows(cancelInSnapshotTx(t), snap, txRows()); err == nil {
		t.Fatal("a save cancelled mid-transaction reported success")
	}
	testHookInSnapshotTx = nil
	ctx := context.Background()
	if n := snapshotCount(t, db); n != 0 {
		t.Fatalf("a cancelled save left %d snapshot(s)", n)
	}
	if procs, _ := db.GetSnapshotProcesses(ctx, snap.ID); len(procs) != 0 {
		t.Fatalf("a cancelled save left %d process row(s)", len(procs))
	}

	if err := db.SaveSnapshotWithRows(ctx, snap, txRows()); err != nil {
		t.Fatal(err)
	}
	procs, _ := db.GetSnapshotProcesses(ctx, snap.ID)
	items, _ := db.GetLoginItems(ctx, snap.ID)
	perms, _ := db.GetGrantedPerms(ctx, snap.ID)
	if snapshotCount(t, db) != 1 || len(procs) != 1 || len(items) != 1 || len(perms) != 1 {
		t.Fatalf("saved snapshot rows: %d processes, %d login items, %d perms", len(procs), len(items), len(perms))
	}
}

func TestSaveSnapshotAndUpsertIssuesIsAtomic(t *testing.T) {
	db := openTestDB(t)
	snap := sampleInput()
	findings := []FindingInput{{RuleID: "r1", Subject: "s", Severity: "high", Message: "m"}}
	if _, err := db.SaveSnapshotAndUpsertIssues(cancelInSnapshotTx(t), snap, snap.MachineUUID, findings); err == nil {
		t.Fatal("a persist cancelled mid-transaction reported success")
	}
	testHookInSnapshotTx = nil
	ctx := context.Background()
	if n := snapshotCount(t, db); n != 0 {
		t.Fatalf("a cancelled persist left %d snapshot(s)", n)
	}
	if issues, _ := db.ListIssues(ctx, snap.MachineUUID, ""); len(issues) != 0 {
		t.Fatalf("a cancelled persist left %d issue(s)", len(issues))
	}

	ids, err := db.SaveSnapshotAndUpsertIssues(ctx, snap, snap.MachineUUID, findings)
	if err != nil || len(ids) != 1 {
		t.Fatalf("persist = %v, %v; want one issue id", ids, err)
	}
	issues, _ := db.ListIssues(ctx, snap.MachineUUID, "")
	if snapshotCount(t, db) != 1 || len(issues) != 1 || issues[0].ID != ids[0] {
		t.Fatalf("persisted %d issue(s), want the returned id", len(issues))
	}
}
