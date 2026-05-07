// Package issues coordinates recommendations findings with persisted issue state.
package issues

import (
	"context"

	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

// Store is the persistence boundary used by Service.
type Store interface {
	ListIssues(ctx context.Context, machineUUID string, status store.IssueStatus) ([]store.IssueRow, error)
	UpdateIssueStatus(ctx context.Context, id string, status store.IssueStatus) error
	UpsertIssues(ctx context.Context, machineUUID, snapshotID string, findings []store.FindingInput) ([]string, error)
	RecordAppliedFix(ctx context.Context, fix store.AppliedFixInput) (string, error)
	ListAppliedFixes(ctx context.Context, issueID string) ([]store.AppliedFixRow, error)
}

// SnapshotStore persists snapshots captured during a live issue check.
type SnapshotStore interface {
	SaveSnapshot(ctx context.Context, snap store.SnapshotInput) error
	SaveSnapshotProcesses(ctx context.Context, snapshotID string, processes []store.ProcessSnapshotRow) error
	SaveLoginItems(ctx context.Context, snapshotID string, items []store.LoginItemRow) error
	SaveGrantedPerms(ctx context.Context, snapshotID string, perms []store.GrantedPermRow) error
}

// SnapshotSource returns either a live or previously persisted snapshot.
type SnapshotSource interface {
	Live(ctx context.Context) (snapshot.Snapshot, error)
	Stored(ctx context.Context, id string) (snapshot.Snapshot, error)
}

// Engine evaluates recommendation rules against a snapshot.
type Engine interface {
	Evaluate(snapshot.Snapshot) []rules.Finding
}

// EngineFunc adapts a function into an Engine.
type EngineFunc func(snapshot.Snapshot) []rules.Finding

// Evaluate implements Engine.
func (f EngineFunc) Evaluate(snap snapshot.Snapshot) []rules.Finding {
	return f(snap)
}

// Service owns issue lifecycle operations.
type Service struct {
	Store         Store
	SnapshotStore SnapshotStore
	Snapshots     SnapshotSource
	Engine        Engine
}

// CheckOptions configures a rule evaluation and persistence pass.
type CheckOptions struct {
	SnapshotID string
}

// CheckResult is the persisted result of a rule evaluation.
type CheckResult struct {
	Snapshot    snapshot.Snapshot `json:"snapshot"`
	MachineUUID string            `json:"machine_uuid"`
	Findings    []rules.Finding   `json:"findings"`
	IssueIDs    []string          `json:"issue_ids"`
}

// Check evaluates rules against a stored or live snapshot and upserts issues.
func (s Service) Check(ctx context.Context, opts CheckOptions) (CheckResult, error) {
	var snap snapshot.Snapshot
	var err error
	if opts.SnapshotID != "" {
		snap, err = s.Snapshots.Stored(ctx, opts.SnapshotID)
	} else {
		snap, err = s.Snapshots.Live(ctx)
		if err == nil && s.SnapshotStore != nil {
			if err = persistSnapshot(ctx, s.SnapshotStore, snap); err != nil {
				return CheckResult{}, err
			}
		}
	}
	if err != nil {
		return CheckResult{}, err
	}

	findings := s.Engine.Evaluate(snap)
	ids, err := s.Store.UpsertIssues(ctx, MachineUUID(snap), snap.ID, FindingInputs(findings))
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{
		Snapshot:    snap,
		MachineUUID: MachineUUID(snap),
		Findings:    findings,
		IssueIDs:    ids,
	}, nil
}

// List returns persisted issues for one machine.
func (s Service) List(ctx context.Context, machineUUID string, status store.IssueStatus) ([]store.IssueRow, error) {
	return s.Store.ListIssues(ctx, machineUUID, status)
}

// Update changes issue status.
func (s Service) Update(ctx context.Context, id string, status store.IssueStatus) error {
	return s.Store.UpdateIssueStatus(ctx, id, status)
}

// Acknowledge marks an issue acknowledged.
func (s Service) Acknowledge(ctx context.Context, id string) error {
	return s.Update(ctx, id, store.IssueAcknowledged)
}

// Dismiss marks an issue dismissed.
func (s Service) Dismiss(ctx context.Context, id string) error {
	return s.Update(ctx, id, store.IssueDismissed)
}

// RecordFix appends one fix attempt.
func (s Service) RecordFix(ctx context.Context, fix store.AppliedFixInput) (string, error) {
	return s.Store.RecordAppliedFix(ctx, fix)
}

// FixHistory returns recorded fix attempts for one issue.
func (s Service) FixHistory(ctx context.Context, issueID string) ([]store.AppliedFixRow, error) {
	return s.Store.ListAppliedFixes(ctx, issueID)
}

// FindingInputs converts rule findings to store inputs.
func FindingInputs(findings []rules.Finding) []store.FindingInput {
	inputs := make([]store.FindingInput, 0, len(findings))
	for _, f := range findings {
		inputs = append(inputs, store.FindingInput{
			RuleID:   f.RuleID,
			Subject:  f.Subject,
			Severity: string(f.Severity),
			Message:  f.Message,
			Fix:      f.Fix,
		})
	}
	return inputs
}

// MachineUUID returns the stable machine identity used for issue matching.
func MachineUUID(snap snapshot.Snapshot) string {
	if snap.Host.MachineUUID != "" {
		return snap.Host.MachineUUID
	}
	if snap.Host.Hostname != "" {
		return snap.Host.Hostname
	}
	return "local"
}

func persistSnapshot(ctx context.Context, dst SnapshotStore, snap snapshot.Snapshot) error {
	input := store.FromSnapshot(snap)
	if err := dst.SaveSnapshot(ctx, input); err != nil {
		return err
	}
	_ = dst.SaveSnapshotProcesses(ctx, snap.ID, store.ProcessesFromSnapshot(snap))
	_ = dst.SaveLoginItems(ctx, snap.ID, store.LoginItemsFromSnapshot(snap))
	_ = dst.SaveGrantedPerms(ctx, snap.ID, store.GrantedPermsFromSnapshot(snap))
	return nil
}
