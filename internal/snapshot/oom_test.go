package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/oom"
	"github.com/kaeawc/spectra/internal/process"
)

func TestCollectOOMReportsFindsEventInLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	if err := os.WriteFile(logPath, []byte("ok\njava.lang.OutOfMemoryError: Java heap space\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jvms := []jvm.Info{{PID: 10, MainClass: "svc.App"}}
	procs := []process.Info{{PID: 10, LogFiles: []string{logPath}}}

	reports := collectOOMReports(jvms, procs)
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d: %+v", len(reports), reports)
	}
	if reports[0].PID != 10 || reports[0].LogPath != logPath {
		t.Fatalf("report = %+v", reports[0])
	}
	if len(reports[0].Events) != 1 || reports[0].Events[0].Variant != oom.VariantHeapSpace {
		t.Fatalf("events = %+v", reports[0].Events)
	}
}

func TestCollectOOMReportsEmptyWhenNoLogFiles(t *testing.T) {
	// No LogFiles (non-deep mode) -> no I/O, no reports.
	jvms := []jvm.Info{{PID: 10}}
	procs := []process.Info{{PID: 10}}
	if r := collectOOMReports(jvms, procs); r != nil {
		t.Fatalf("expected nil reports without LogFiles, got %+v", r)
	}
}

func TestCollectOOMReportsNoProcessMatch(t *testing.T) {
	jvms := []jvm.Info{{PID: 10}}
	procs := []process.Info{{PID: 99, LogFiles: []string{"/whatever"}}}
	if r := collectOOMReports(jvms, procs); r != nil {
		t.Fatalf("expected nil reports without a PID match, got %+v", r)
	}
}

func TestCollectOOMReportsSkipsUnreadableAndCleanLogs(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.log")
	if err := os.WriteFile(clean, []byte("nothing to see\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "gone.log")
	jvms := []jvm.Info{{PID: 10}}
	procs := []process.Info{{PID: 10, LogFiles: []string{missing, clean}}}
	if r := collectOOMReports(jvms, procs); r != nil {
		t.Fatalf("expected nil reports for unreadable + clean logs, got %+v", r)
	}
}
