//go:build darwin && cgo

package process

/*
#cgo darwin LDFLAGS: -lproc
#include <libproc.h>
*/
import "C"
import "unsafe"

func collectThreadCounts(procs []Info) map[int]int {
	counts := make(map[int]int, len(procs))
	for _, p := range procs {
		if p.PID <= 0 {
			continue
		}
		var taskInfo C.struct_proc_taskinfo
		size := C.int(unsafe.Sizeof(taskInfo))
		n := C.proc_pidinfo(C.int(p.PID), C.PROC_PIDTASKINFO, 0, unsafe.Pointer(&taskInfo), size)
		if n == size && taskInfo.pti_threadnum > 0 {
			counts[p.PID] = int(taskInfo.pti_threadnum)
		}
	}
	return counts
}
