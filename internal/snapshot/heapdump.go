package snapshot

import (
	"github.com/kaeawc/spectra/internal/heapdump"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
)

// HeapDump is an .hprof file on disk attributed to a running JVM — typically
// left behind by -XX:+HeapDumpOnOutOfMemoryError.
type HeapDump struct {
	PID       int    `json:"pid"`
	MainClass string `json:"main_class,omitempty"`
	heapdump.Dump
}

// collectHeapDumps locates .hprof files for each running JVM in its
// -XX:HeapDumpPath or, failing that, its working directory (deep-mode lsof cwd,
// else the java user.dir property). A path shared by several JVMs is reported
// once, preferring the JVM whose PID the java_pid<PID>.hprof name encodes.
func collectHeapDumps(fsys heapdump.FS, jvms []jvm.Info, procs []process.Info) []HeapDump {
	if len(jvms) == 0 {
		return nil
	}
	cwdByPID := make(map[int]string, len(procs))
	for _, p := range procs {
		cwdByPID[p.PID] = p.Cwd
	}
	var out []HeapDump
	index := make(map[string]int)
	for _, j := range jvms {
		req := heapdump.Request{
			PID:          j.PID,
			HeapDumpPath: heapdump.HeapDumpPathFromArgs(j.VMArgs),
			WorkingDir:   cwdByPID[j.PID],
		}
		if req.WorkingDir == "" {
			req.WorkingDir = j.SysProps["user.dir"]
		}
		for _, d := range heapdump.Find(fsys, req) {
			hd := HeapDump{PID: j.PID, MainClass: j.MainClass, Dump: d}
			if i, seen := index[d.Path]; seen {
				if d.DumpPID == j.PID {
					out[i] = hd
				}
				continue
			}
			index[d.Path] = len(out)
			out = append(out, hd)
		}
	}
	return out
}
