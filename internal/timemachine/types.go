// Package timemachine collects read-only Time Machine state.
package timemachine

import (
	"errors"
	"time"

	"github.com/kaeawc/spectra/internal/plistread"
)

// ErrNeedsFullDiskAccess marks Time Machine reads denied by macOS privacy
// protections.
var ErrNeedsFullDiskAccess = plistread.ErrNeedsFullDiskAccess

// FullDiskAccessRemediation is the one-line CLI remediation for privacy denial.
const FullDiskAccessRemediation = plistread.FullDiskAccessRemediation

// TimeMachineState is the read-only host Time Machine state.
type TimeMachineState struct {
	Status            TMStatus          `json:"status"`
	Destinations      []TMDestination   `json:"destinations"`
	LocalSnapshots    []TMLocalSnapshot `json:"local_snapshots"`
	AutoBackupEnabled bool              `json:"auto_backup_enabled"`
	SchedulerLoaded   bool              `json:"scheduler_loaded"`
	CollectedAt       time.Time         `json:"collected_at"`
}

// TMStatus is the current tmutil status payload.
type TMStatus struct {
	Running               bool    `json:"running"`
	Percent               float64 `json:"percent"`
	ClientID              string  `json:"client_id,omitempty"`
	BackupPhase           string  `json:"backup_phase,omitempty"`
	DestinationID         string  `json:"destination_id,omitempty"`
	DestinationMountPoint string  `json:"destination_mount_point,omitempty"`
	FirstBackup           bool    `json:"first_backup"`
}

// TMDestination is one configured backup destination.
type TMDestination struct {
	ID             string    `json:"id,omitempty"`
	Name           string    `json:"name,omitempty"`
	Kind           string    `json:"kind,omitempty"`
	MountPoint     string    `json:"mount_point,omitempty"`
	URL            string    `json:"url,omitempty"`
	BytesAvailable uint64    `json:"bytes_available"`
	BytesUsed      uint64    `json:"bytes_used"`
	LastBackup     time.Time `json:"last_backup,omitempty"`
	NextBackup     time.Time `json:"next_backup,omitempty"`
	QuotaGB        uint64    `json:"quota_gb"`
	Encrypted      bool      `json:"encrypted"`
}

// TMLocalSnapshot is one local APFS Time Machine snapshot.
type TMLocalSnapshot struct {
	Name   string    `json:"name"`
	Date   time.Time `json:"date,omitempty"`
	Volume string    `json:"volume,omitempty"`
}

// IsZero lets containing JSON structs omit a missing Time Machine sample.
func (s TimeMachineState) IsZero() bool {
	return s.CollectedAt.IsZero() &&
		len(s.Destinations) == 0 &&
		len(s.LocalSnapshots) == 0 &&
		!s.AutoBackupEnabled &&
		!s.SchedulerLoaded &&
		!s.Status.Running
}

func isFullDiskAccessErr(err error) bool {
	return errors.Is(err, ErrNeedsFullDiskAccess)
}
