package rules

import (
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

const stagedMajorUpdateAge = 7 * 24 * time.Hour

func ruleStorageStagedMajorUpdate() Rule {
	return Rule{
		ID:       "storage.staged_major_update",
		Severity: SeverityInfo,
		Message:  "A staged macOS upgrade snapshot has been sitting on disk for more than 7 days.",
		Fix:      "Run `spectra storage --snapshots` and configure or run Time Machine before continuing the macOS upgrade.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			return stagedMajorUpdateFindings(s, time.Now())
		},
	}
}

func stagedMajorUpdateFindings(s snapshot.Snapshot, now time.Time) []Finding {
	if hasLatestTimeMachineBackup(s) {
		return nil
	}
	for _, volume := range s.Storage.Volumes {
		for _, snap := range volume.Snapshots {
			if snap.Kind != storagestate.SnapshotMSUPrepare || snap.CreatedAt.IsZero() {
				continue
			}
			age := now.Sub(snap.CreatedAt)
			if age <= stagedMajorUpdateAge {
				continue
			}
			return []Finding{{
				RuleID:   "storage.staged_major_update",
				Severity: SeverityInfo,
				Subject:  volume.MountPoint,
				Message:  "Staged macOS upgrade has been sitting on disk for >7 days. On Macs without a Time Machine destination this can drive an fseventsd leak.",
				Fix:      "Run `spectra storage --snapshots`; configure a Time Machine destination or complete/cancel the staged macOS upgrade.",
			}}
		}
	}
	return nil
}

func hasLatestTimeMachineBackup(s snapshot.Snapshot) bool {
	for _, destination := range s.Host.TimeMachine.Destinations {
		if !destination.LastBackup.IsZero() {
			return true
		}
	}
	return false
}
