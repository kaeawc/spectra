package snapshot

import (
	"context"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/threadinspect"
)

// JVMDeadlockReport is the deadlock check result for one JVM whose thread
// dump was explicitly requested via Options.ThreadDumpPIDs. An empty Cycles
// means the dump was captured and no Java-level deadlock was present.
type JVMDeadlockReport struct {
	PID        int                           `json:"pid"`
	MainClass  string                        `json:"main_class,omitempty"`
	CapturedAt time.Time                     `json:"captured_at"`
	Cycles     []threadinspect.DeadlockCycle `json:"cycles,omitempty"`
}

// attachJVMDeadlocks captures thread dumps only for the PIDs the caller named.
// Thread dumps pause the target JVM at a safepoint and can be large, so they
// are never taken for every discovered JVM.
func attachJVMDeadlocks(ctx context.Context, s *Snapshot, opts Options, now func() time.Time) {
	if len(opts.ThreadDumpPIDs) == 0 {
		return
	}
	capturer := opts.ThreadCapturer
	if capturer == nil {
		capturer = jvm.ThreadDumpCapturer{Run: opts.JVMOpts.CmdRunner, Now: now}
	}
	reports, warnings := collectJVMDeadlocks(ctx, opts.ThreadDumpPIDs, s.JVMs, capturer)
	s.JVMDeadlocks = reports
	s.Warnings = append(s.Warnings, warnings...)
}

func collectJVMDeadlocks(ctx context.Context, pids []int, jvms []jvm.Info, capturer threadinspect.Capturer) ([]JVMDeadlockReport, []string) {
	mainClass := make(map[int]string, len(jvms))
	for _, j := range jvms {
		mainClass[j.PID] = j.MainClass
	}
	var reports []JVMDeadlockReport
	var warnings []string
	seen := make(map[int]bool, len(pids))
	for _, pid := range pids {
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		dump, err := capturer.CaptureThreads(ctx, pid)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("jvm: thread dump for PID %d failed: %v; deadlock check skipped", pid, err))
			continue
		}
		reports = append(reports, JVMDeadlockReport{
			PID:        pid,
			MainClass:  mainClass[pid],
			CapturedAt: dump.CapturedAt,
			Cycles:     dump.Deadlocks,
		})
	}
	return reports, warnings
}
