package rules

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

const stagedMajorUpdateAge = 7 * 24 * time.Hour
const largeSpotlightRSSKiB = 1024 * 1024

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

func ruleStorageDataVolumeNearFull() Rule {
	return Rule{
		ID:       "storage.data_volume_near_full",
		Severity: SeverityMedium,
		Message:  "APFS data volume is more than 90% full.",
		Fix:      "Run `spectra storage` and free space on the Data volume.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, mount := range s.Storage.Mounts {
				if mount.APFSRole != "data" || mount.Capacity.UsedPercent <= 90 {
					continue
				}
				findings = append(findings, storageCapacityFinding(
					"storage.data_volume_near_full",
					SeverityMedium,
					mount,
					"APFS Data volume is more than 90% full.",
				))
			}
			return findings
		},
	}
}

func ruleStorageSystemVolumeNearFull() Rule {
	return Rule{
		ID:       "storage.system_volume_near_full",
		Severity: SeverityHigh,
		Message:  "APFS system volume is more than 95% full.",
		Fix:      "Run `spectra storage`; verify staged macOS updates and system volume snapshots.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, mount := range s.Storage.Mounts {
				if mount.APFSRole != "system" || mount.Capacity.UsedPercent <= 95 {
					continue
				}
				findings = append(findings, storageCapacityFinding(
					"storage.system_volume_near_full",
					SeverityHigh,
					mount,
					"APFS system volume is more than 95% full.",
				))
			}
			return findings
		},
	}
}

func ruleSpotlightLargeResidentIndexer() Rule {
	return Rule{
		ID:       "spotlight.large_resident_indexer",
		Severity: SeverityMedium,
		Message:  "Spotlight indexer RSS exceeds 1 GiB after 6h uptime while indexing is enabled.",
		Fix:      "Run `spectra storage --spotlight`; if indexing remains stuck, consider `sudo mdutil -E <volume>`.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			if float64(s.Host.UptimeSeconds)/3600 <= 6 || !spotlightEnabled(s.Storage.Spotlight) {
				return nil
			}
			proc, ok := largestMDSStores(s.Processes)
			if !ok || proc.RSSKiB <= largeSpotlightRSSKiB {
				return nil
			}
			return []Finding{{
				RuleID:   "spotlight.large_resident_indexer",
				Severity: SeverityMedium,
				Subject:  fmt.Sprintf("PID %d (mds_stores)", proc.PID),
				Message:  fmt.Sprintf("Spotlight indexer has accumulated %.1f GiB RSS after %.1fh uptime; index may be corrupt.", float64(proc.RSSKiB)/(1024*1024), float64(s.Host.UptimeSeconds)/3600),
				Fix:      "Run `spectra storage --spotlight`; if indexing remains stuck, consider `sudo mdutil -E <volume>`.",
			}}
		},
	}
}

func storageCapacityFinding(ruleID string, severity Severity, mount storagestate.Mount, message string) Finding {
	return Finding{
		RuleID:   ruleID,
		Severity: severity,
		Subject:  mount.MountPoint,
		Message:  message,
		Fix:      "Run `spectra storage` to inspect mount capacity, flags, and APFS role.",
	}
}

func spotlightEnabled(volumes []storagestate.SpotlightVolume) bool {
	for _, volume := range volumes {
		if volume.Status == storagestate.SpotlightEnabled {
			return true
		}
	}
	return false
}

func largestMDSStores(processes []process.Info) (process.Info, bool) {
	var largest process.Info
	var ok bool
	for _, proc := range processes {
		if !isMDSStores(proc) || proc.RSSKiB <= largest.RSSKiB {
			continue
		}
		largest = proc
		ok = true
	}
	return largest, ok
}

func isMDSStores(proc process.Info) bool {
	return proc.Command == "mds_stores" ||
		proc.BSDName == "mds_stores" ||
		strings.Contains(proc.FullCommandLine, "mds_stores")
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
