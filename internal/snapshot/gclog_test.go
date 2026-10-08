package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
)

func writeLog(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollectGCLogReportsDiscoversAndMergesGCLogs(t *testing.T) {
	dir := t.TempDir()
	app := writeLog(t, dir, "app.log", "INFO started\nGC(1) Pause mentioned without a duration\n")
	cur := writeLog(t, dir, "gc.log", "[1.0s][info][gc] GC(5) Pause Full (Allocation Failure) 200M->190M(256M) 800.0ms\n")
	old := writeLog(t, dir, "gc.log.0", "[0.1s][info][gc] GC(0) Pause Young (Normal) (G1 Evacuation Pause) 25M->5M(256M) 4.0ms\n")
	missing := filepath.Join(dir, "gone.log")

	jvms := []jvm.Info{{PID: 10, MainClass: "svc.App"}}
	procs := []process.Info{{PID: 10, LogFiles: []string{app, missing, cur, old}}}

	reports := collectGCLogReports(jvms, procs)
	if len(reports) != 1 {
		t.Fatalf("expected 1 per-PID report, got %d: %+v", len(reports), reports)
	}
	r := reports[0]
	if r.PID != 10 || r.MainClass != "svc.App" {
		t.Fatalf("report identity = %+v", r)
	}
	if len(r.LogPaths) != 2 || r.LogPaths[0] != cur || r.LogPaths[1] != old {
		t.Fatalf("log paths = %v, want [gc.log gc.log.0]", r.LogPaths)
	}
	if r.Summary.Pauses != 2 || r.Summary.FullGCCount != 1 || r.Summary.MaxPauseMs != 800 {
		t.Fatalf("summary = %+v", r.Summary)
	}
}

func TestCollectGCLogReportsNoGCLogs(t *testing.T) {
	dir := t.TempDir()
	app := writeLog(t, dir, "app.log", "nothing to see\n")
	jvms := []jvm.Info{{PID: 10}}
	procs := []process.Info{{PID: 10, LogFiles: []string{app}}}
	if r := collectGCLogReports(jvms, procs); r != nil {
		t.Fatalf("expected nil reports without GC logs, got %+v", r)
	}
}

func TestCollectGCLogReportsEmptyWithoutLogFilesOrMatch(t *testing.T) {
	if r := collectGCLogReports([]jvm.Info{{PID: 10}}, []process.Info{{PID: 10}}); r != nil {
		t.Fatalf("expected nil without LogFiles, got %+v", r)
	}
	if r := collectGCLogReports([]jvm.Info{{PID: 10}}, []process.Info{{PID: 99, LogFiles: []string{"/x"}}}); r != nil {
		t.Fatalf("expected nil without a PID match, got %+v", r)
	}
}

func TestCollectGCLogReportsBoundsFileCount(t *testing.T) {
	dir := t.TempDir()
	line := "[0.1s][info][gc] GC(0) Pause Young (Normal) (G1 Evacuation Pause) 25M->5M(256M) 4.0ms\n"
	var files []string
	for i := 0; i < gcLogMaxFilesPerProc+5; i++ {
		files = append(files, writeLog(t, dir, "gc.log."+string(rune('a'+i)), line))
	}
	reports := collectGCLogReports([]jvm.Info{{PID: 10}}, []process.Info{{PID: 10, LogFiles: files}})
	if len(reports) != 1 || len(reports[0].LogPaths) != gcLogMaxFilesPerProc {
		t.Fatalf("expected %d scanned files, got %+v", gcLogMaxFilesPerProc, reports)
	}
}
