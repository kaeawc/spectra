package snapshot

import (
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/oom"
	"github.com/kaeawc/spectra/internal/process"
)

// OOMReport is the set of OutOfMemoryError occurrences found in one log file
// belonging to one running JVM.
type OOMReport struct {
	PID       int         `json:"pid"`
	MainClass string      `json:"main_class,omitempty"`
	LogPath   string      `json:"log_path"`
	Events    []oom.Event `json:"events"`
}

// OOM scan bounds. Deep-mode log discovery can surface many files; scan a
// bounded tail of a bounded number of them so a snapshot never blocks on a
// pathological log directory.
const (
	oomMaxFilesPerProc       = 20
	oomMaxBytesPerFile int64 = 1 << 20 // 1 MiB tail
)

// collectOOMReports scans the discovered log files of each running JVM for
// OutOfMemoryError occurrences. It matches a JVM to its process by PID and only
// scans that process's LogFiles, so it does no I/O when deep-mode log discovery
// did not run (LogFiles empty). All I/O errors are absorbed per the
// partial-snapshot contract.
func collectOOMReports(jvms []jvm.Info, procs []process.Info) []OOMReport {
	if len(jvms) == 0 || len(procs) == 0 {
		return nil
	}
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	var reports []OOMReport
	for _, j := range jvms {
		p, ok := byPID[j.PID]
		if !ok || len(p.LogFiles) == 0 {
			continue
		}
		files := p.LogFiles
		if len(files) > oomMaxFilesPerProc {
			files = files[:oomMaxFilesPerProc]
		}
		for _, path := range files {
			events, err := oom.ScanFile(path, oomMaxBytesPerFile)
			if err != nil || len(events) == 0 {
				continue
			}
			reports = append(reports, OOMReport{
				PID:       j.PID,
				MainClass: j.MainClass,
				LogPath:   path,
				Events:    events,
			})
		}
	}
	return reports
}
