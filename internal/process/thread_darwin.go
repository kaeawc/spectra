//go:build darwin && cgo

package process

/*
#cgo darwin LDFLAGS: -lproc
#include <libproc.h>
*/
import "C"
import "unsafe"

func collectThreadCounts(procs []Info) map[int]int {
	details := collectProcessDetails(procs)
	counts := make(map[int]int, len(details))
	for pid, d := range details {
		if d.ThreadCount > 0 {
			counts[pid] = d.ThreadCount
		}
	}
	return counts
}

func collectProcessDetails(procs []Info) map[int]Details {
	details := make(map[int]Details, len(procs))
	for _, p := range procs {
		if p.PID <= 0 {
			continue
		}

		d := Details{}
		var taskAllInfo C.struct_proc_taskallinfo
		taskAllInfoSize := C.int(unsafe.Sizeof(taskAllInfo))
		n := C.proc_pidinfo(C.int(p.PID), C.PROC_PIDTASKALLINFO, 0, unsafe.Pointer(&taskAllInfo), taskAllInfoSize)
		if n == taskAllInfoSize {
			if taskAllInfo.ptinfo.pti_threadnum > 0 {
				d.ThreadCount = int(taskAllInfo.ptinfo.pti_threadnum)
			}
			d.BSDName = C.GoString(&taskAllInfo.pbsd.pbi_name[0])
		}

		var pathBuf [C.PROC_PIDPATHINFO_MAXSIZE]C.char
		if n := C.proc_pidpath(C.int(p.PID), unsafe.Pointer(&pathBuf[0]), C.uint32_t(len(pathBuf))); n > 0 {
			d.ExecutablePath = C.GoString(&pathBuf[0])
		}

		if d.ThreadCount > 0 || d.BSDName != "" || d.ExecutablePath != "" {
			details[p.PID] = d
		}
	}
	return details
}
