package store

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// FromSnapshot converts a snapshot.Snapshot into a SnapshotInput ready
// for SaveSnapshot. machine_uuid falls back to hostname when absent.
func FromSnapshot(s snapshot.Snapshot) SnapshotInput {
	uuid := s.Host.MachineUUID
	if uuid == "" {
		uuid = s.Host.Hostname
	}

	apps := make([]AppInput, len(s.Apps))
	for i, r := range s.Apps {
		apps[i] = fromResult(r)
	}

	snapJSON, _ := json.Marshal(s)

	return SnapshotInput{
		ID:           s.ID,
		MachineUUID:  uuid,
		TakenAt:      s.TakenAt,
		Kind:         string(s.Kind),
		SpectraVer:   s.Host.SpectraVersion,
		Hostname:     s.Host.Hostname,
		OSName:       s.Host.OSName,
		OSVersion:    s.Host.OSVersion,
		OSBuild:      s.Host.OSBuild,
		CPUBrand:     s.Host.CPUBrand,
		CPUCores:     s.Host.CPUCores,
		RAMBytes:     s.Host.RAMBytes,
		Architecture: s.Host.Architecture,
		Apps:         apps,
		SnapshotJSON: snapJSON,
	}
}

// ProcessesFromSnapshot converts a snapshot's Processes into ProcessSnapshotRows
// ready for SaveSnapshotProcesses.
func ProcessesFromSnapshot(s snapshot.Snapshot) []ProcessSnapshotRow {
	rows := make([]ProcessSnapshotRow, len(s.Processes))
	for i, p := range s.Processes {
		rows[i] = ProcessSnapshotRow{
			PID:     p.PID,
			PPID:    p.PPID,
			Command: p.Command,
			RSSKiB:  p.RSSKiB,
			CPUPct:  p.CPUPct,
			AppPath: p.AppPath,
		}
	}
	return rows
}

func fromResult(r detect.Result) AppInput {
	name := appName(r.Path)
	return AppInput{
		BundleID:      r.BundleID,
		AppName:       name,
		AppPath:       r.Path,
		UI:            r.UI,
		Runtime:       r.Runtime,
		Packaging:     r.Packaging,
		Confidence:    r.Confidence,
		AppVersion:    r.AppVersion,
		Architectures: r.Architectures,
		ResultJSON:    r,
	}
}

// appName derives a human-readable name from the bundle path.
// "/Applications/Google Chrome.app" → "Google Chrome"
func appName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".app")
}
