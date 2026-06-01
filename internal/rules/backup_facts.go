package rules

import (
	"fmt"
	"strings"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

type BackupFacts struct {
	TMDestinationCount    int
	BackupdAutoLoaded     bool
	HasMSUPrepareSnapshot bool
	FSEventsdRSSBytes     int64
	UptimeHours           float64
}

const fseventsdCriticalRSSBytes int64 = 2 * 1024 * 1024 * 1024

func BackupFactsFor(s snapshot.Snapshot) BackupFacts {
	return BackupFacts{
		TMDestinationCount:    len(s.Host.TimeMachine.Destinations),
		BackupdAutoLoaded:     backupdAutoLoaded(s.Services.Jobs),
		HasMSUPrepareSnapshot: hasMSUPrepareSnapshot(s.Storage.Volumes),
		FSEventsdRSSBytes:     processRSSBytes(largestProcessByName(s.Processes, "fseventsd")),
		UptimeHours:           float64(s.Host.UptimeSeconds) / 3600,
	}
}

func ruleBackupDestinationlessSchedulerLeak() Rule {
	return Rule{
		ID:       "backup.destinationless_scheduler_leak",
		Severity: SeverityMedium,
		Message:  "backupd-auto is loaded with no Time Machine destination while an MSUPrepare snapshot is present.",
		Fix:      "Configure a Time Machine destination, or run `sudo tmutil disable` and `sudo killall -HUP fseventsd`.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			facts := BackupFactsFor(s)
			if facts.TMDestinationCount != 0 || !facts.BackupdAutoLoaded || !facts.HasMSUPrepareSnapshot {
				return nil
			}
			severity := SeverityMedium
			if facts.FSEventsdRSSBytes > fseventsdCriticalRSSBytes {
				severity = SeverityHigh
			}
			return []Finding{{
				RuleID:   "backup.destinationless_scheduler_leak",
				Severity: severity,
				Subject:  "Time Machine scheduler",
				Message:  fmt.Sprintf("backupd is scheduled with no destination on a Mac with a staged macOS upgrade. This is a known fseventsd memory-leak pattern; fseventsd is currently %s RSS.", formatBytes(facts.FSEventsdRSSBytes)),
				Fix:      "Remediation: `sudo tmutil disable` (or configure a destination) and `sudo killall -HUP fseventsd`.",
			}}
		},
	}
}

func backupdAutoLoaded(jobs []services.LaunchJob) bool {
	for _, job := range jobs {
		if job.Label == "com.apple.backupd-auto" && (job.PID > 0 || job.PlistPath != "") {
			return true
		}
	}
	return false
}

func hasMSUPrepareSnapshot(volumes []storagestate.Volume) bool {
	for _, volume := range volumes {
		for _, snap := range volume.Snapshots {
			if snap.Kind == storagestate.SnapshotMSUPrepare || snap.Name == "com.apple.os.update-MSUPrepareUpdate" {
				return true
			}
		}
	}
	return false
}

func largestProcessByName(processes []process.Info, name string) process.Info {
	var largest process.Info
	for _, proc := range processes {
		if !processNameMatches(proc, name) || proc.RSSKiB <= largest.RSSKiB {
			continue
		}
		largest = proc
	}
	return largest
}

func processNameMatches(proc process.Info, name string) bool {
	return proc.Command == name ||
		proc.BSDName == name ||
		strings.Contains(proc.FullCommandLine, name)
}

func processRSSBytes(proc process.Info) int64 {
	if proc.RSSKiB <= 0 {
		return 0
	}
	return proc.RSSKiB * 1024
}

func formatBytes(bytes int64) string {
	const gib = 1024 * 1024 * 1024
	if bytes >= gib {
		return fmt.Sprintf("%.1f GiB", float64(bytes)/gib)
	}
	const mib = 1024 * 1024
	if bytes >= mib {
		return fmt.Sprintf("%.0f MiB", float64(bytes)/mib)
	}
	return fmt.Sprintf("%d B", bytes)
}
