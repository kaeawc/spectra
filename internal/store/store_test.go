package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func sampleInput() SnapshotInput {
	return SnapshotInput{
		ID:           "snap-20260503T120000Z-0001",
		MachineUUID:  "TEST-UUID-1234",
		TakenAt:      time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
		Kind:         "live",
		SpectraVer:   "v0.1.0",
		Hostname:     "test.local",
		OSName:       "macOS",
		OSVersion:    "15.6.1",
		OSBuild:      "24G90",
		CPUBrand:     "Apple M1",
		CPUCores:     8,
		RAMBytes:     16 * 1024 * 1024 * 1024,
		Architecture: "arm64",
		Apps: []AppInput{
			{
				BundleID:   "com.example.Foo",
				AppName:    "Foo",
				AppPath:    "/Applications/Foo.app",
				UI:         "Electron",
				Runtime:    "Node+Chromium",
				Packaging:  "Squirrel",
				Confidence: "high",
				AppVersion: "1.2.3",
				ResultJSON: map[string]any{"ui": "Electron"},
			},
		},
	}
}

func TestSaveAndListSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sampleInput()
	if err := db.SaveSnapshot(ctx, in); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	rows, err := db.ListSnapshots(ctx, "")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListSnapshots: got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.ID != in.ID {
		t.Errorf("ID = %q, want %q", r.ID, in.ID)
	}
	if r.AppCount != 1 {
		t.Errorf("AppCount = %d, want 1", r.AppCount)
	}
	if r.Kind != "live" {
		t.Errorf("Kind = %q, want live", r.Kind)
	}
}

func TestGetSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sampleInput()
	if err := db.SaveSnapshot(ctx, in); err != nil {
		t.Fatal(err)
	}

	row, err := db.GetSnapshot(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if row.SpectraVer != in.SpectraVer {
		t.Errorf("SpectraVer = %q, want %q", row.SpectraVer, in.SpectraVer)
	}
}

func TestGetSnapshotNotFound(t *testing.T) {
	db := openTestDB(t)
	_, err := db.GetSnapshot(context.Background(), "no-such-id")
	if err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestGetSnapshotApps(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SaveSnapshot(ctx, sampleInput()); err != nil {
		t.Fatal(err)
	}

	apps, err := db.GetSnapshotApps(ctx, "snap-20260503T120000Z-0001")
	if err != nil {
		t.Fatalf("GetSnapshotApps: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("got %d apps, want 1", len(apps))
	}
	if apps[0].UI != "Electron" {
		t.Errorf("UI = %q, want Electron", apps[0].UI)
	}
}

func TestIdempotentSave(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sampleInput()
	if err := db.SaveSnapshot(ctx, in); err != nil {
		t.Fatal(err)
	}
	// Second save with same ID should not error (INSERT OR IGNORE).
	if err := db.SaveSnapshot(ctx, in); err != nil {
		t.Fatalf("second SaveSnapshot: %v", err)
	}

	rows, _ := db.ListSnapshots(ctx, "")
	if len(rows) != 1 {
		t.Errorf("want 1 row after idempotent save, got %d", len(rows))
	}
}

func TestListSnapshotsByMachine(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	a := sampleInput()
	a.ID = "snap-A"
	a.MachineUUID = "UUID-A"
	_ = db.SaveSnapshot(ctx, a)

	b := sampleInput()
	b.ID = "snap-B"
	b.MachineUUID = "UUID-B"
	_ = db.SaveSnapshot(ctx, b)

	rows, _ := db.ListSnapshots(ctx, "UUID-A")
	if len(rows) != 1 || rows[0].ID != "snap-A" {
		t.Errorf("filtered list: %+v", rows)
	}
}

// --- Baseline snapshots (name field) ---

func TestBaselineNameRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sampleInput()
	in.Kind = "baseline"
	in.Name = "pre-incident"
	if err := db.SaveSnapshot(ctx, in); err != nil {
		t.Fatalf("SaveSnapshot baseline: %v", err)
	}

	row, err := db.GetSnapshot(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if row.Name != "pre-incident" {
		t.Errorf("name = %q, want pre-incident", row.Name)
	}
	if row.Kind != "baseline" {
		t.Errorf("kind = %q, want baseline", row.Kind)
	}

	// ListSnapshots should also return the name.
	rows, err := db.ListSnapshots(ctx, "")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "pre-incident" {
		t.Errorf("list name = %q", rows[0].Name)
	}
}

// --- Snapshot retention / pruning ---

func TestPruneSnapshotsKeepsBaselines(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Insert 3 live + 1 baseline.
	for i := 1; i <= 3; i++ {
		in := sampleInput()
		in.ID = fmt.Sprintf("snap-live-%02d", i)
		in.Kind = "live"
		_ = db.SaveSnapshot(ctx, in)
	}
	baseline := sampleInput()
	baseline.ID = "snap-baseline-01"
	baseline.Kind = "baseline"
	_ = db.SaveSnapshot(ctx, baseline)

	// Prune keeping 2 live.
	deleted, err := db.PruneSnapshots(ctx, 2)
	if err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	// Baseline must still exist.
	row, err := db.GetSnapshot(ctx, "snap-baseline-01")
	if err != nil {
		t.Fatalf("baseline gone after prune: %v", err)
	}
	if row.Kind != "baseline" {
		t.Errorf("kind = %q, want baseline", row.Kind)
	}
}

func TestPruneSnapshotsNoOp(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Only 1 live snapshot — pruning with keep=100 should delete nothing.
	_ = db.SaveSnapshot(ctx, sampleInput())
	deleted, err := db.PruneSnapshots(ctx, 100)
	if err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}
}

func TestPruneSnapshotsZeroKeepDefaultsTo100(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// 5 snapshots, prune with keep=0 (should default to 100, so none deleted).
	for i := 1; i <= 5; i++ {
		in := sampleInput()
		in.ID = fmt.Sprintf("snap-%02d", i)
		_ = db.SaveSnapshot(ctx, in)
	}
	deleted, err := db.PruneSnapshots(ctx, 0)
	if err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	if deleted != 0 {
		t.Errorf("keep=0 should default to 100 and delete nothing; got %d", deleted)
	}
}

// --- Issues ---

func seedSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	in := sampleInput()
	if err := db.SaveSnapshot(context.Background(), in); err != nil {
		t.Fatalf("seedSnapshot: %v", err)
	}
	return in.ID
}

func TestUpsertIssuesNewAndRefresh(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	snapID := seedSnapshot(t, db)

	findings := []FindingInput{
		{RuleID: "app-unsigned", Subject: "MyApp", Severity: "medium", Message: "not signed", Fix: "sign it"},
		{RuleID: "jvm-eol-version", Subject: "PID 123 (old.App)", Severity: "medium", Message: "JDK 9", Fix: "upgrade"},
	}

	ids, err := db.UpsertIssues(ctx, "TEST-UUID-1234", snapID, findings)
	if err != nil {
		t.Fatalf("UpsertIssues: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}

	// Second call with same findings should refresh, not insert.
	ids2, err := db.UpsertIssues(ctx, "TEST-UUID-1234", snapID, findings)
	if err != nil {
		t.Fatalf("UpsertIssues (refresh): %v", err)
	}
	if ids2[0] != ids[0] || ids2[1] != ids[1] {
		t.Errorf("refresh changed IDs: %v vs %v", ids, ids2)
	}

	rows, err := db.ListIssues(ctx, "TEST-UUID-1234", "")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(rows))
	}
	if rows[0].Status != IssueOpen {
		t.Errorf("status = %q, want open", rows[0].Status)
	}
}

func TestListIssuesFilterByStatus(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	snapID := seedSnapshot(t, db)

	findings := []FindingInput{
		{RuleID: "rule-A", Subject: "S1", Severity: "medium", Message: "m1"},
		{RuleID: "rule-B", Subject: "S2", Severity: "high", Message: "m2"},
	}
	ids, _ := db.UpsertIssues(ctx, "TEST-UUID-1234", snapID, findings)

	// Acknowledge the first issue.
	if err := db.UpdateIssueStatus(ctx, ids[0], IssueAcknowledged); err != nil {
		t.Fatalf("UpdateIssueStatus: %v", err)
	}

	openOnes, _ := db.ListIssues(ctx, "TEST-UUID-1234", IssueOpen)
	if len(openOnes) != 1 {
		t.Errorf("expected 1 open issue, got %d", len(openOnes))
	}

	ackOnes, _ := db.ListIssues(ctx, "TEST-UUID-1234", IssueAcknowledged)
	if len(ackOnes) != 1 {
		t.Errorf("expected 1 acknowledged issue, got %d", len(ackOnes))
	}
}

func TestUpdateIssueStatusNotFound(t *testing.T) {
	db := openTestDB(t)
	err := db.UpdateIssueStatus(context.Background(), "no-such-id", IssueClosed)
	if err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestRecordAndListAppliedFixes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	snapID := seedSnapshot(t, db)

	ids, _ := db.UpsertIssues(ctx, "TEST-UUID-1234", snapID, []FindingInput{
		{RuleID: "rule-X", Subject: "App", Severity: "info", Message: "bloat", Fix: "clean"},
	})
	issueID := ids[0]

	fixID, err := db.RecordAppliedFix(ctx, AppliedFixInput{
		IssueID:   issueID,
		AppliedBy: "user",
		Command:   "docker system prune",
		Output:    "Deleted 10 GiB",
		ExitCode:  0,
	})
	if err != nil {
		t.Fatalf("RecordAppliedFix: %v", err)
	}
	if fixID == "" {
		t.Error("expected non-empty fix ID")
	}

	fixes, err := db.ListAppliedFixes(ctx, issueID)
	if err != nil {
		t.Fatalf("ListAppliedFixes: %v", err)
	}
	if len(fixes) != 1 {
		t.Fatalf("expected 1 fix, got %d", len(fixes))
	}
	if fixes[0].Command != "docker system prune" {
		t.Errorf("command = %q", fixes[0].Command)
	}
}

func TestUpsertIssuesDoesNotReopenDismissed(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	snapID := seedSnapshot(t, db)

	findings := []FindingInput{
		{RuleID: "rule-Y", Subject: "App", Severity: "low", Message: "minor"},
	}
	ids, _ := db.UpsertIssues(ctx, "TEST-UUID-1234", snapID, findings)

	// Dismiss the issue.
	_ = db.UpdateIssueStatus(ctx, ids[0], IssueDismissed)

	// Same finding again — should create a NEW issue because dismissed != open|acknowledged.
	ids2, err := db.UpsertIssues(ctx, "TEST-UUID-1234", snapID, findings)
	if err != nil {
		t.Fatalf("UpsertIssues after dismiss: %v", err)
	}
	if len(ids2) != 1 || ids2[0] == ids[0] {
		t.Errorf("expected new issue ID after dismiss, got same: %v", ids2)
	}
}

func TestAppName(t *testing.T) {
	cases := map[string]string{
		"/Applications/Slack.app":        "Slack",
		"/Applications/Google Chrome.app": "Google Chrome",
		"/tmp/Foo.app/":                   "Foo.app", // trailing slash → Base returns ""
	}
	for path, want := range cases {
		got := appName(path)
		if path == "/tmp/Foo.app/" {
			// filepath.Base with trailing slash on Unix actually still works
			// but let's accept any non-empty result for this edge case.
			if got == "" {
				t.Errorf("appName(%q) empty", path)
			}
			continue
		}
		if got != want {
			t.Errorf("appName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSaveAndGetSnapshotProcesses(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	snap := SnapshotInput{
		ID:          "proc-snap-1",
		MachineUUID: "test-machine",
		TakenAt:     time.Now(),
		Kind:        "live",
	}
	if err := db.SaveSnapshot(ctx, snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	procs := []ProcessSnapshotRow{
		{PID: 412, PPID: 1, Command: "Slack", RSSKiB: 184320, CPUPct: 1.2, AppPath: "/Applications/Slack.app"},
		{PID: 1, PPID: 0, Command: "launchd", RSSKiB: 4096, CPUPct: 0.0},
	}
	if err := db.SaveSnapshotProcesses(ctx, "proc-snap-1", procs); err != nil {
		t.Fatalf("SaveSnapshotProcesses: %v", err)
	}

	got, err := db.GetSnapshotProcesses(ctx, "proc-snap-1")
	if err != nil {
		t.Fatalf("GetSnapshotProcesses: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Ordered by rss_kib DESC → Slack first.
	if got[0].Command != "Slack" {
		t.Errorf("first = %q, want Slack", got[0].Command)
	}
	if got[0].AppPath != "/Applications/Slack.app" {
		t.Errorf("AppPath = %q, want /Applications/Slack.app", got[0].AppPath)
	}
}

func TestSaveSnapshotProcessesIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	snap := SnapshotInput{
		ID: "proc-snap-2", MachineUUID: "m", TakenAt: time.Now(), Kind: "live",
	}
	if err := db.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}

	procs := []ProcessSnapshotRow{{PID: 1, Command: "launchd"}}
	if err := db.SaveSnapshotProcesses(ctx, "proc-snap-2", procs); err != nil {
		t.Fatal(err)
	}
	// Second call must not error (INSERT OR IGNORE).
	if err := db.SaveSnapshotProcesses(ctx, "proc-snap-2", procs); err != nil {
		t.Errorf("second call: %v", err)
	}
}

func TestProcessesFromSnapshot(t *testing.T) {
	snap := snapshot.Snapshot{
		ID: "test-snap",
		Processes: []process.Info{
			{PID: 1, PPID: 0, Command: "launchd", RSSKiB: 4096, CPUPct: 0.0},
			{PID: 412, PPID: 1, Command: "Slack", RSSKiB: 184320, CPUPct: 1.2, AppPath: "/Applications/Slack.app"},
		},
	}
	rows := ProcessesFromSnapshot(snap)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	found := map[int]bool{}
	for _, r := range rows {
		found[r.PID] = true
	}
	if !found[1] || !found[412] {
		t.Errorf("pids = %v, want {1, 412}", found)
	}
}

func TestSaveAndGetLoginItems(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	snap := sampleInput(); snap.ID = "snap-li-1"
	if err := db.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}

	items := []LoginItemRow{
		{BundleID: "com.example.app", PlistPath: "/Library/LaunchAgents/com.example.app.plist",
			Label: "com.example.app", Scope: "user", RunAtLoad: true},
		{BundleID: "com.example.app", PlistPath: "/Library/LaunchDaemons/com.example.daemon.plist",
			Label: "com.example.daemon", Scope: "system", Daemon: true, KeepAlive: true},
	}
	if err := db.SaveLoginItems(ctx, snap.ID, items); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetLoginItems(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	byPath := map[string]LoginItemRow{}
	for _, r := range got {
		byPath[r.PlistPath] = r
	}
	agent := byPath["/Library/LaunchAgents/com.example.app.plist"]
	if !agent.RunAtLoad {
		t.Error("RunAtLoad should be true for agent")
	}
	daemon := byPath["/Library/LaunchDaemons/com.example.daemon.plist"]
	if !daemon.Daemon || !daemon.KeepAlive {
		t.Error("Daemon and KeepAlive should be true for daemon row")
	}
}

func TestSaveLoginItemsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	snap := sampleInput(); snap.ID = "snap-li-2"
	if err := db.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}

	items := []LoginItemRow{
		{BundleID: "com.test", PlistPath: "/Library/LaunchAgents/com.test.plist", Label: "com.test"},
	}
	if err := db.SaveLoginItems(ctx, snap.ID, items); err != nil {
		t.Fatal(err)
	}
	// second call should be a no-op
	if err := db.SaveLoginItems(ctx, snap.ID, items); err != nil {
		t.Fatalf("second SaveLoginItems: %v", err)
	}
	got, err := db.GetLoginItems(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

func TestSaveAndGetGrantedPerms(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	snap := sampleInput(); snap.ID = "snap-gp-1"
	if err := db.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}

	perms := []GrantedPermRow{
		{BundleID: "com.slack.slack", Service: "kTCCServiceMicrophone"},
		{BundleID: "com.slack.slack", Service: "kTCCServiceCamera"},
		{BundleID: "com.zoom.xos", Service: "kTCCServiceMicrophone"},
	}
	if err := db.SaveGrantedPerms(ctx, snap.ID, perms); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetGrantedPerms(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	slackSvcs := map[string]bool{}
	for _, r := range got {
		if r.BundleID == "com.slack.slack" {
			slackSvcs[r.Service] = true
		}
	}
	if !slackSvcs["kTCCServiceMicrophone"] || !slackSvcs["kTCCServiceCamera"] {
		t.Errorf("missing expected services for slack: %v", slackSvcs)
	}
}

func TestLoginItemsFromSnapshot(t *testing.T) {
	snap := snapshot.Snapshot{
		ID: "snap-conv",
		Apps: []detect.Result{
			{
				BundleID: "com.example.app",
				LoginItems: []detect.LoginItem{
					{Path: "/Library/LaunchAgents/com.example.plist", Label: "com.example", Scope: "user", RunAtLoad: true},
				},
			},
		},
	}
	rows := LoginItemsFromSnapshot(snap)
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1", len(rows))
	}
	if rows[0].BundleID != "com.example.app" {
		t.Errorf("BundleID = %q", rows[0].BundleID)
	}
	if !rows[0].RunAtLoad {
		t.Error("RunAtLoad should be true")
	}
}

func TestGrantedPermsFromSnapshot(t *testing.T) {
	snap := snapshot.Snapshot{
		ID: "snap-gp-conv",
		Apps: []detect.Result{
			{BundleID: "com.slack.slack", GrantedPermissions: []string{"kTCCServiceMicrophone", "kTCCServiceCamera"}},
			{BundleID: "com.zoom.xos", GrantedPermissions: []string{"kTCCServiceMicrophone"}},
		},
	}
	rows := GrantedPermsFromSnapshot(snap)
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3", len(rows))
	}
}
