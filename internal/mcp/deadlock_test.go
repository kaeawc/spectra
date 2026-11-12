package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/threadinspect"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// recordingSnapshotCollector captures the Options it was built with and
// returns a snapshot carrying a deadlock for whichever PIDs were targeted.
type recordingSnapshotCollector struct {
	opts *snapshot.Options
}

func (r recordingSnapshotCollector) BuildSnapshot(_ context.Context, opts snapshot.Options) snapshot.Snapshot {
	*r.opts = opts
	var s snapshot.Snapshot
	for _, pid := range opts.ThreadDumpPIDs {
		s.JVMDeadlocks = append(s.JVMDeadlocks, snapshot.JVMDeadlockReport{
			PID:       pid,
			MainClass: "svc.App",
			Cycles: []threadinspect.DeadlockCycle{{
				Threads: []string{"worker-1", "worker-2"},
				Waits: []threadinspect.DeadlockWait{
					{Thread: "worker-1", Lock: "<0x1> (a java.lang.Object)", HeldBy: "worker-2"},
					{Thread: "worker-2", Lock: "<0x2> (a java.lang.Object)", HeldBy: "worker-1"},
				},
			}},
		})
	}
	return s
}

// jvmOnlyCollector reports a JVM for the given PIDs. Unused methods fall
// through to the nil embedded interface.
type jvmOnlyCollector struct {
	JVMCollector
	pids map[int]bool
}

func (j jvmOnlyCollector) InspectJVM(_ context.Context, pid int, _ jvm.CollectOptions) *jvm.Info {
	if !j.pids[pid] {
		return nil
	}
	return &jvm.Info{PID: pid, MainClass: "svc.App"}
}

type emptyToolchainCollector struct{}

func (emptyToolchainCollector) CollectToolchains(context.Context, toolchain.CollectOptions) toolchain.Toolchains {
	return toolchain.Toolchains{}
}

func newDeadlockTestServer(opts *snapshot.Options, jvmPIDs ...int) *Server {
	pids := map[int]bool{}
	var procs []process.Info
	for _, p := range jvmPIDs {
		pids[p] = true
		procs = append(procs, process.Info{PID: p, Command: "java"})
	}
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetCollectors(Collectors{
		Processes: &fakeProcessCollector{procs: procs},
		Snapshots: recordingSnapshotCollector{opts: opts},
		JVMs:      jvmOnlyCollector{pids: pids},
		Toolchain: emptyToolchainCollector{},
		Clock:     fixedClock{t: time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)},
	})
	return s
}

func TestTriageJVMPIDThreadDumpsForDeadlock(t *testing.T) {
	var opts snapshot.Options
	s := newDeadlockTestServer(&opts, 4242)

	result := s.toolTriage(json.RawMessage(`{"pid":4242}`))
	if result.IsError {
		t.Fatalf("triage error: %+v", result.Content)
	}
	if !reflect.DeepEqual(opts.ThreadDumpPIDs, []int{4242}) {
		t.Fatalf("ThreadDumpPIDs = %v, want [4242]", opts.ThreadDumpPIDs)
	}
	if text := result.Content[0].Text; !strings.Contains(text, "jvm-deadlock") {
		t.Fatalf("triage should surface the jvm-deadlock finding:\n%s", text)
	}
}

func TestTriageNonJVMPIDSkipsThreadDump(t *testing.T) {
	var opts snapshot.Options
	s := newDeadlockTestServer(&opts)
	if result := s.toolTriage(json.RawMessage(`{"pid":4242}`)); result.IsError {
		t.Fatalf("triage error: %+v", result.Content)
	}
	if len(opts.ThreadDumpPIDs) != 0 {
		t.Fatalf("non-JVM PID must not be thread-dumped, got %v", opts.ThreadDumpPIDs)
	}
}

func TestDiagnoseThreadDumpPIDs(t *testing.T) {
	var opts snapshot.Options
	s := newDeadlockTestServer(&opts)
	result := s.toolDiagnose(json.RawMessage(`{"thread_dump_pids":[7,8],"include_raw":true}`))
	if result.IsError {
		t.Fatalf("diagnose error: %+v", result.Content)
	}
	if !reflect.DeepEqual(opts.ThreadDumpPIDs, []int{7, 8}) {
		t.Fatalf("ThreadDumpPIDs = %v, want [7 8]", opts.ThreadDumpPIDs)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "jvm-deadlock") || !strings.Contains(text, "worker-1") {
		t.Fatalf("diagnose should report the deadlock naming its threads:\n%s", text)
	}

	if r := s.toolDiagnose(json.RawMessage(`{"snapshot_id":"snap-x","thread_dump_pids":[7]}`)); !r.IsError {
		t.Fatal("thread_dump_pids with snapshot_id should be rejected")
	}
}
