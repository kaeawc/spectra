package playbook

import (
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/timemachine"
)

func TestAnalyzeFSEventsdLeakIncident(t *testing.T) {
	s := incidentSnapshot()
	logs := logquery.Result{Entries: []logquery.LogEntry{
		{MessageType: "Error", EventMessage: "fs_snapshot_list failed"},
		{MessageType: "Error", EventMessage: "backup failed"},
	}}
	report := AnalyzeFSEventsdLeak(s, logs)
	if !report.Matched {
		t.Fatalf("Matched = false, report = %#v", report)
	}
	if report.RuleID != FSEventsdLeakRuleID {
		t.Fatalf("RuleID = %q", report.RuleID)
	}
	for _, want := range FSEventsdLeakRemediation {
		if !contains(report.Remediation, want) {
			t.Fatalf("missing remediation %q in %#v", want, report.Remediation)
		}
	}
	if !strings.Contains(report.Finding, FSEventsdLeakRuleID) || !strings.Contains(report.Finding, "fseventsd RSS") {
		t.Fatalf("Finding = %q", report.Finding)
	}
	if report.Signals.FSSnapshotListFailed != 1 {
		t.Fatalf("FSSnapshotListFailed = %d, want 1", report.Signals.FSSnapshotListFailed)
	}
}

func TestAnalyzeFSEventsdLeakHealthyExitsAtMemory(t *testing.T) {
	s := incidentSnapshot()
	s.Host.Memory.CompressorOccupied = 0
	s.Host.Memory.Swap.UsedBytes = 0
	report := AnalyzeFSEventsdLeak(s, logquery.Result{})
	if report.Matched {
		t.Fatalf("healthy report matched: %#v", report)
	}
	if report.ExitReason != "no memory pressure detected." {
		t.Fatalf("ExitReason = %q", report.ExitReason)
	}
}

func incidentSnapshot() snapshot.Snapshot {
	const gib = 1024 * 1024 * 1024
	return snapshot.Snapshot{
		ID:   "incident-2026-05-fseventsd",
		Kind: snapshot.KindLive,
		Host: snapshot.HostInfo{
			Hostname:      "incident-mac",
			UptimeSeconds: int64((7 * 24 * time.Hour).Seconds()),
			Memory: memstate.MemoryState{
				PhysicalBytes:      128 * gib,
				CompressorOccupied: 70 * gib,
				CompressorStored:   80 * gib,
				PressureLevel:      memstate.PressureCritical,
				Swap:               memstate.SwapUsage{UsedBytes: 60 * gib},
			},
			TimeMachine: timemachine.TimeMachineState{
				Destinations:    nil,
				SchedulerLoaded: true,
			},
		},
		Processes: []process.Info{
			{PID: 321, Command: "fseventsd", FullCommandLine: "/System/Library/PrivateFrameworks/FSEvents.framework/Versions/A/Support/fseventsd", RSSKiB: 14 * 1024 * 1024},
			{PID: 10, Command: "backupd", RSSKiB: 1024},
		},
		Storage: storagestate.State{Volumes: []storagestate.Volume{{
			MountPoint: "/",
			Snapshots:  []storagestate.APFSSnapshot{{Name: "com.apple.os.update-MSUPrepareUpdate", Kind: storagestate.SnapshotMSUPrepare}},
		}}},
		Services: services.LaunchInventory{Jobs: []services.LaunchJob{{
			Label:     "com.apple.backupd-auto",
			Domain:    "system",
			PlistPath: "/System/Library/LaunchDaemons/com.apple.backupd-auto.plist",
		}}},
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
