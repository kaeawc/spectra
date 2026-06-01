package playbook

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

const FSEventsdLeakRuleID = "backup.destinationless_scheduler_leak"

var FSEventsdLeakRemediation = []string{
	"sudo tmutil disable",
	"sudo killall backupd",
	"sudo killall -HUP fseventsd",
}

type FSEventsdLeakReport struct {
	RuleID      string               `json:"rule_id"`
	Matched     bool                 `json:"matched"`
	ExitReason  string               `json:"exit_reason,omitempty"`
	Signals     FSEventsdLeakSignals `json:"signals"`
	Steps       []FSEventsdLeakStep  `json:"steps"`
	Remediation []string             `json:"remediation,omitempty"`
	Finding     string               `json:"finding,omitempty"`
}

type FSEventsdLeakSignals struct {
	MemoryPressure       bool    `json:"memory_pressure"`
	CompressorRatio      float64 `json:"compressor_ratio"`
	SwapRatio            float64 `json:"swap_ratio"`
	FSEventsdTop3        bool    `json:"fseventsd_top3"`
	FSEventsdRSSBytes    int64   `json:"fseventsd_rss_bytes"`
	TMDestinationCount   int     `json:"tm_destination_count"`
	BackupdAutoLoaded    bool    `json:"backupd_auto_loaded"`
	MSUPrepareSnapshot   bool    `json:"msu_prepare_snapshot"`
	BackupdErrorCount    int     `json:"backupd_error_count"`
	FSSnapshotListFailed int     `json:"fs_snapshot_list_failed"`
}

type FSEventsdLeakStep struct {
	ID      string   `json:"id"`
	Command []string `json:"command"`
	Passed  bool     `json:"passed"`
	Summary string   `json:"summary"`
}

func AnalyzeFSEventsdLeak(s snapshot.Snapshot, logs logquery.Result) FSEventsdLeakReport {
	signals := fseventsdSignals(s, logs)
	report := FSEventsdLeakReport{
		RuleID:  FSEventsdLeakRuleID,
		Signals: signals,
		Steps:   fseventsdSteps(signals),
	}
	if !signals.MemoryPressure {
		report.ExitReason = "no memory pressure detected."
		return report
	}
	report.Matched = signals.FSEventsdTop3 &&
		signals.TMDestinationCount == 0 &&
		signals.BackupdAutoLoaded &&
		signals.MSUPrepareSnapshot
	if report.Matched {
		report.Remediation = append([]string(nil), FSEventsdLeakRemediation...)
		report.Finding = fseventsdFinding(signals)
	}
	return report
}

func fseventsdSignals(s snapshot.Snapshot, logs logquery.Result) FSEventsdLeakSignals {
	physical := s.Host.Memory.PhysicalBytes
	compressorRatio := ratio(s.Host.Memory.CompressorOccupied, physical)
	swapRatio := ratio(s.Host.Memory.Swap.UsedBytes, physical)
	fseventsd := largestProcessByName(s.Processes, "fseventsd")
	errorCount, snapshotFailures := backupdLogCounts(logs)
	return FSEventsdLeakSignals{
		MemoryPressure:       compressorRatio > 0.25 || swapRatio > 0.10,
		CompressorRatio:      compressorRatio,
		SwapRatio:            swapRatio,
		FSEventsdTop3:        processInTopRSS(s.Processes, "fseventsd", 3),
		FSEventsdRSSBytes:    processRSSBytes(fseventsd),
		TMDestinationCount:   len(s.Host.TimeMachine.Destinations),
		BackupdAutoLoaded:    backupdAutoLoaded(s.Services.Jobs),
		MSUPrepareSnapshot:   hasMSUPrepareSnapshot(s.Storage.Volumes),
		BackupdErrorCount:    errorCount,
		FSSnapshotListFailed: snapshotFailures,
	}
}

func fseventsdSteps(signals FSEventsdLeakSignals) []FSEventsdLeakStep {
	return []FSEventsdLeakStep{
		{ID: "memory", Command: []string{"memory", "--json"}, Passed: signals.MemoryPressure, Summary: fmt.Sprintf("compressor=%.0f%% swap=%.0f%%", signals.CompressorRatio*100, signals.SwapRatio*100)},
		{ID: "process", Command: []string{"process", "--min-rss=1GB", "--sort=rss", "--json"}, Passed: signals.FSEventsdTop3, Summary: fmt.Sprintf("fseventsd RSS: %s", FormatBytes(signals.FSEventsdRSSBytes))},
		{ID: "timemachine", Command: []string{"timemachine", "--json"}, Passed: signals.TMDestinationCount == 0, Summary: fmt.Sprintf("destinations=%d", signals.TMDestinationCount)},
		{ID: "storage", Command: []string{"storage", "--snapshots", "--json"}, Passed: signals.MSUPrepareSnapshot, Summary: fmt.Sprintf("MSUPrepare snapshot=%t", signals.MSUPrepareSnapshot)},
		{ID: "services", Command: []string{"services", "--label", "com.apple.backupd-auto", "--json"}, Passed: signals.BackupdAutoLoaded, Summary: fmt.Sprintf("backupd-auto loaded=%t", signals.BackupdAutoLoaded)},
		{ID: "logs", Command: []string{"logs", "--process", "backupd", "--level", "Error", "--last", "24h", "--top", "50", "--json"}, Passed: signals.BackupdErrorCount > 0, Summary: fmt.Sprintf("backupd errors=%d fs_snapshot_list failed=%d", signals.BackupdErrorCount, signals.FSSnapshotListFailed)},
	}
}

func fseventsdFinding(signals FSEventsdLeakSignals) string {
	return fmt.Sprintf("%s matched: fseventsd RSS %s, swap %.0f%%, TM destinations %d, backupd-auto loaded=%t, MSUPrepare snapshot=%t, backupd errors=%d.",
		FSEventsdLeakRuleID,
		FormatBytes(signals.FSEventsdRSSBytes),
		signals.SwapRatio*100,
		signals.TMDestinationCount,
		signals.BackupdAutoLoaded,
		signals.MSUPrepareSnapshot,
		signals.BackupdErrorCount,
	)
}

func backupdLogCounts(logs logquery.Result) (errors int, snapshotFailures int) {
	for _, entry := range logs.Entries {
		if strings.EqualFold(entry.MessageType, "Error") || strings.EqualFold(entry.MessageType, "Fault") {
			errors++
		}
		if strings.Contains(entry.EventMessage, "fs_snapshot_list failed") {
			snapshotFailures++
		}
	}
	return errors, snapshotFailures
}

func processInTopRSS(procs []process.Info, name string, n int) bool {
	rows := append([]process.Info(nil), procs...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].RSSKiB > rows[j].RSSKiB })
	if n > len(rows) {
		n = len(rows)
	}
	for _, proc := range rows[:n] {
		if processNameMatches(proc, name) {
			return true
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

func ratio(n, d uint64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func FormatBytes(bytes int64) string {
	const gib = 1024 * 1024 * 1024
	if bytes >= gib {
		return fmt.Sprintf("%.1f GB", float64(bytes)/gib)
	}
	const mib = 1024 * 1024
	if bytes >= mib {
		return fmt.Sprintf("%.0f MB", float64(bytes)/mib)
	}
	return fmt.Sprintf("%d B", bytes)
}

func FormatBackupdErrorRate(count int, window time.Duration) string {
	if window <= 0 {
		return fmt.Sprintf("%d errors", count)
	}
	return fmt.Sprintf("%d errors / %s (= %.1f/h)", count, window.Round(time.Hour), float64(count)/window.Hours())
}
