package snapshot

import (
	"github.com/kaeawc/spectra/internal/gclog"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
)

// GCLogReport is the aggregate GC-pause summary of every GC log discovered
// among one running JVM's log files. Rotated logs are merged into one summary.
type GCLogReport struct {
	PID       int           `json:"pid"`
	MainClass string        `json:"main_class,omitempty"`
	LogPaths  []string      `json:"log_paths"`
	Summary   gclog.Summary `json:"summary"`
}

// GC-log discovery bounds, mirroring the OOM scan: a bounded tail of a bounded
// number of files per process.
const (
	gcLogMaxFilesPerProc       = 20
	gcLogMaxBytesPerFile int64 = 1 << 20 // 1 MiB tail
)

// collectGCLogReports finds GC logs among each running JVM's deep-mode
// LogFiles. A file is a GC log when it contains at least one unified
// `GC(N) Pause ...` line with a duration. Does no I/O when LogFiles is empty;
// I/O errors are absorbed per the partial-snapshot contract.
func collectGCLogReports(jvms []jvm.Info, procs []process.Info) []GCLogReport {
	if len(jvms) == 0 || len(procs) == 0 {
		return nil
	}
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	var reports []GCLogReport
	for _, j := range jvms {
		p, ok := byPID[j.PID]
		if !ok || len(p.LogFiles) == 0 {
			continue
		}
		if r, ok := gcLogReportFor(j, p.LogFiles); ok {
			reports = append(reports, r)
		}
	}
	return reports
}

func gcLogReportFor(j jvm.Info, files []string) (GCLogReport, bool) {
	if len(files) > gcLogMaxFilesPerProc {
		files = files[:gcLogMaxFilesPerProc]
	}
	r := GCLogReport{PID: j.PID, MainClass: j.MainClass}
	for _, path := range files {
		s, err := gclog.ParseFileTail(path, gcLogMaxBytesPerFile)
		if err != nil || s.Pauses == 0 {
			continue
		}
		r.LogPaths = append(r.LogPaths, path)
		r.Summary = gclog.Merge(r.Summary, s)
	}
	return r, len(r.LogPaths) > 0
}
