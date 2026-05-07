package issues

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

type fakeStore struct {
	listMachine string
	listStatus  store.IssueStatus
	listRows    []store.IssueRow

	upsertMachine  string
	upsertSnapshot string
	upsertFindings []store.FindingInput
	upsertIDs      []string
	upsertErr      error

	updatedID     string
	updatedStatus store.IssueStatus

	recordedFix store.AppliedFixInput
	fixID       string

	historyIssueID string
	historyRows    []store.AppliedFixRow
}

func (f *fakeStore) ListIssues(_ context.Context, machineUUID string, status store.IssueStatus) ([]store.IssueRow, error) {
	f.listMachine = machineUUID
	f.listStatus = status
	return f.listRows, nil
}

func (f *fakeStore) UpdateIssueStatus(_ context.Context, id string, status store.IssueStatus) error {
	f.updatedID = id
	f.updatedStatus = status
	return nil
}

func (f *fakeStore) UpsertIssues(_ context.Context, machineUUID, snapshotID string, findings []store.FindingInput) ([]string, error) {
	f.upsertMachine = machineUUID
	f.upsertSnapshot = snapshotID
	f.upsertFindings = append([]store.FindingInput(nil), findings...)
	return f.upsertIDs, f.upsertErr
}

func (f *fakeStore) RecordAppliedFix(_ context.Context, fix store.AppliedFixInput) (string, error) {
	f.recordedFix = fix
	return f.fixID, nil
}

func (f *fakeStore) ListAppliedFixes(_ context.Context, issueID string) ([]store.AppliedFixRow, error) {
	f.historyIssueID = issueID
	return f.historyRows, nil
}

type fakeSnapshots struct {
	liveCalled   bool
	storedCalled string
	live         snapshot.Snapshot
	stored       snapshot.Snapshot
	err          error
}

func (f *fakeSnapshots) Live(context.Context) (snapshot.Snapshot, error) {
	f.liveCalled = true
	return f.live, f.err
}

func (f *fakeSnapshots) Stored(_ context.Context, id string) (snapshot.Snapshot, error) {
	f.storedCalled = id
	return f.stored, f.err
}

type fakeSnapshotStore struct {
	savedSnapshotID string
	savedProcesses  bool
	savedLoginItems bool
	savedPerms      bool
	err             error
}

func (f *fakeSnapshotStore) SaveSnapshot(_ context.Context, snap store.SnapshotInput) error {
	f.savedSnapshotID = snap.ID
	return f.err
}

func (f *fakeSnapshotStore) SaveSnapshotProcesses(context.Context, string, []store.ProcessSnapshotRow) error {
	f.savedProcesses = true
	return nil
}

func (f *fakeSnapshotStore) SaveLoginItems(context.Context, string, []store.LoginItemRow) error {
	f.savedLoginItems = true
	return nil
}

func (f *fakeSnapshotStore) SaveGrantedPerms(context.Context, string, []store.GrantedPermRow) error {
	f.savedPerms = true
	return nil
}

func testSnap() snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:      "snap-1",
		TakenAt: time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		Kind:    snapshot.KindLive,
		Host: snapshot.HostInfo{
			MachineUUID: "machine-1",
			Hostname:    "host-1",
			OSName:      "macOS",
			OSVersion:   "15.6",
		},
	}
}

func TestCheckLivePersistsSnapshotAndFindings(t *testing.T) {
	st := &fakeStore{upsertIDs: []string{"issue-1"}}
	snapStore := &fakeSnapshotStore{}
	source := &fakeSnapshots{live: testSnap()}
	finding := rules.Finding{
		RuleID:   "rule-1",
		Subject:  "subject-1",
		Severity: rules.SeverityHigh,
		Message:  "message",
		Fix:      "fix",
	}
	svc := Service{
		Store:         st,
		SnapshotStore: snapStore,
		Snapshots:     source,
		Engine:        EngineFunc(func(snapshot.Snapshot) []rules.Finding { return []rules.Finding{finding} }),
	}

	got, err := svc.Check(context.Background(), CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !source.liveCalled {
		t.Fatal("expected live snapshot source")
	}
	if snapStore.savedSnapshotID != "snap-1" || !snapStore.savedProcesses || !snapStore.savedLoginItems || !snapStore.savedPerms {
		t.Fatalf("snapshot persistence incomplete: %+v", snapStore)
	}
	if got.MachineUUID != "machine-1" || got.Snapshot.ID != "snap-1" || !reflect.DeepEqual(got.IssueIDs, []string{"issue-1"}) {
		t.Fatalf("unexpected result: %+v", got)
	}
	wantInputs := []store.FindingInput{{RuleID: "rule-1", Subject: "subject-1", Severity: "high", Message: "message", Fix: "fix"}}
	if st.upsertMachine != "machine-1" || st.upsertSnapshot != "snap-1" || !reflect.DeepEqual(st.upsertFindings, wantInputs) {
		t.Fatalf("unexpected upsert: machine=%q snapshot=%q findings=%+v", st.upsertMachine, st.upsertSnapshot, st.upsertFindings)
	}
}

func TestCheckStoredDoesNotPersistSnapshot(t *testing.T) {
	st := &fakeStore{}
	snapStore := &fakeSnapshotStore{}
	source := &fakeSnapshots{stored: testSnap()}
	svc := Service{
		Store:         st,
		SnapshotStore: snapStore,
		Snapshots:     source,
		Engine:        EngineFunc(func(snapshot.Snapshot) []rules.Finding { return nil }),
	}

	if _, err := svc.Check(context.Background(), CheckOptions{SnapshotID: "snap-1"}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if source.storedCalled != "snap-1" {
		t.Fatalf("stored snapshot id = %q", source.storedCalled)
	}
	if snapStore.savedSnapshotID != "" {
		t.Fatalf("stored check should not save snapshot: %+v", snapStore)
	}
}

func TestCheckReturnsUpsertError(t *testing.T) {
	wantErr := errors.New("upsert failed")
	svc := Service{
		Store:     &fakeStore{upsertErr: wantErr},
		Snapshots: &fakeSnapshots{live: testSnap()},
		Engine:    EngineFunc(func(snapshot.Snapshot) []rules.Finding { return nil }),
	}
	if _, err := svc.Check(context.Background(), CheckOptions{}); !errors.Is(err, wantErr) {
		t.Fatalf("Check err = %v, want %v", err, wantErr)
	}
}

func TestLifecycleMethodsUseStore(t *testing.T) {
	st := &fakeStore{
		listRows:    []store.IssueRow{{ID: "issue-1"}},
		fixID:       "fix-1",
		historyRows: []store.AppliedFixRow{{ID: "fix-1"}},
	}
	svc := Service{Store: st}

	rows, err := svc.List(context.Background(), "machine-1", store.IssueOpen)
	if err != nil || len(rows) != 1 || st.listMachine != "machine-1" || st.listStatus != store.IssueOpen {
		t.Fatalf("List rows=%+v err=%v store=%+v", rows, err, st)
	}
	if err := svc.Acknowledge(context.Background(), "issue-1"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if st.updatedID != "issue-1" || st.updatedStatus != store.IssueAcknowledged {
		t.Fatalf("ack update = %q/%q", st.updatedID, st.updatedStatus)
	}
	if err := svc.Dismiss(context.Background(), "issue-2"); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if st.updatedID != "issue-2" || st.updatedStatus != store.IssueDismissed {
		t.Fatalf("dismiss update = %q/%q", st.updatedID, st.updatedStatus)
	}
	id, err := svc.RecordFix(context.Background(), store.AppliedFixInput{IssueID: "issue-2", Command: "fix"})
	if err != nil || id != "fix-1" || st.recordedFix.IssueID != "issue-2" {
		t.Fatalf("RecordFix id=%q err=%v fix=%+v", id, err, st.recordedFix)
	}
	history, err := svc.FixHistory(context.Background(), "issue-2")
	if err != nil || len(history) != 1 || st.historyIssueID != "issue-2" {
		t.Fatalf("FixHistory rows=%+v err=%v issue=%q", history, err, st.historyIssueID)
	}
}

func TestMachineUUIDFallbacks(t *testing.T) {
	if got := MachineUUID(snapshot.Snapshot{Host: snapshot.HostInfo{MachineUUID: "uuid", Hostname: "host"}}); got != "uuid" {
		t.Fatalf("MachineUUID = %q", got)
	}
	if got := MachineUUID(snapshot.Snapshot{Host: snapshot.HostInfo{Hostname: "host"}}); got != "host" {
		t.Fatalf("MachineUUID hostname fallback = %q", got)
	}
	if got := MachineUUID(snapshot.Snapshot{}); got != "local" {
		t.Fatalf("MachineUUID local fallback = %q", got)
	}
}
