package main

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <sys/resource.h>
#include <errno.h>

static int task_info(int pid, unsigned long long *rss, unsigned long long *cpu, int *err) {
	struct proc_taskinfo ti;
	int n = proc_pidinfo(pid, PROC_PIDTASKINFO, 0, &ti, sizeof(ti));
	*err = errno;
	if (n != (int)sizeof(ti)) return -1;
	*rss = ti.pti_resident_size;
	*cpu = ti.pti_total_user + ti.pti_total_system;
	return 0;
}

static int rusage_v4(int pid, int *err) {
	struct rusage_info_v4 ri;
	int rc = proc_pid_rusage(pid, RUSAGE_INFO_V4, (rusage_info_t *)&ri);
	*err = errno;
	return rc;
}
*/
import "C"

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// measurePidinfo checks which pids proc_pidinfo(PROC_PIDTASKINFO) and
// proc_pid_rusage can read without root; together they would replace the
// rss and cpu columns the daemon reads from ps today.
func measurePidinfo() {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		fmt.Println("kern.proc.all error:", err)
		return
	}
	me := uint32(os.Getuid())
	taskRes := map[string]map[string]int{}
	rusageRes := map[string]map[string]int{}
	note := func(m map[string]map[string]int, owner string, rc C.int, e C.int) {
		outcome := "ok"
		if rc != 0 {
			outcome = syscall.Errno(e).Error()
		}
		if m[owner] == nil {
			m[owner] = map[string]int{}
		}
		m[owner][outcome]++
	}
	var taskTime, rusageTime time.Duration
	for _, r := range rows {
		pid := C.int(r.Proc.P_pid)
		if pid == 0 {
			continue
		}
		owner := "other"
		switch uid := r.Eproc.Pcred.P_ruid; {
		case uid == me:
			owner = "self"
		case uid == 0:
			owner = "root"
		}
		var rss, cpuNs C.ulonglong
		var e C.int
		t := time.Now()
		rc := C.task_info(pid, &rss, &cpuNs, &e)
		taskTime += time.Since(t)
		note(taskRes, owner, rc, e)
		t = time.Now()
		rc = C.rusage_v4(pid, &e)
		rusageTime += time.Since(t)
		note(rusageRes, owner, rc, e)
	}
	n := time.Duration(len(rows))
	fmt.Printf("proc_pidinfo PROC_PIDTASKINFO: %d pids total=%v (%v/pid) results=%v\n", len(rows), taskTime, taskTime/n, taskRes)
	fmt.Printf("proc_pid_rusage V4: %d pids total=%v (%v/pid) results=%v\n", len(rows), rusageTime, rusageTime/n, rusageRes)
}
