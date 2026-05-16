//go:build darwin && cgo

package sysinfo

/*
#cgo darwin LDFLAGS: -lproc
#include <errno.h>
#include <libproc.h>
#include <sys/resource.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// ReadRusage reads a per-pid rusage snapshot via proc_pid_rusage(2).
//
// V6 is tried first (macOS 14+); on older kernels the V6 flavor is rejected
// and we fall back to V4 — which still exposes ri_billed_energy. EPERM (the
// caller doesn't own the pid and lacks the entitlement to read it) is
// mapped to ErrRusagePermission so callers can skip and continue.
func ReadRusage(pid int) (ProcRusage, error) {
	var v6 C.struct_rusage_info_v6
	rc, err := C.proc_pid_rusage(C.int(pid), C.RUSAGE_INFO_V6, (*C.rusage_info_t)(unsafe.Pointer(&v6)))
	if rc == 0 {
		return ProcRusage{
			PID:              pid,
			BilledEnergyNJ:   uint64(v6.ri_billed_energy),
			ServicedEnergyNJ: uint64(v6.ri_serviced_energy),
			EnergyNJ:         uint64(v6.ri_energy_nj),
			PEnergyNJ:        uint64(v6.ri_penergy_nj),
			InterruptWakeups: uint64(v6.ri_interrupt_wkups),
			PkgIdleWakeups:   uint64(v6.ri_pkg_idle_wkups),
			UserNs:           uint64(v6.ri_user_time),
			SystemNs:         uint64(v6.ri_system_time),
			DiskBytesRead:    uint64(v6.ri_diskio_bytesread),
			DiskBytesWritten: uint64(v6.ri_diskio_byteswritten),
		}, nil
	}
	if isPermissionErrno(err) {
		return ProcRusage{}, fmt.Errorf("pid %d: %w", pid, ErrRusagePermission)
	}
	// EINVAL on older kernels that don't know V6 — fall back to V4.
	var v4 C.struct_rusage_info_v4
	rc, err = C.proc_pid_rusage(C.int(pid), C.RUSAGE_INFO_V4, (*C.rusage_info_t)(unsafe.Pointer(&v4)))
	if rc != 0 {
		if isPermissionErrno(err) {
			return ProcRusage{}, fmt.Errorf("pid %d: %w", pid, ErrRusagePermission)
		}
		return ProcRusage{}, fmt.Errorf("proc_pid_rusage(%d): rc=%d: %w", pid, int(rc), err)
	}
	return ProcRusage{
		PID:              pid,
		BilledEnergyNJ:   uint64(v4.ri_billed_energy),
		ServicedEnergyNJ: uint64(v4.ri_serviced_energy),
		InterruptWakeups: uint64(v4.ri_interrupt_wkups),
		PkgIdleWakeups:   uint64(v4.ri_pkg_idle_wkups),
		UserNs:           uint64(v4.ri_user_time),
		SystemNs:         uint64(v4.ri_system_time),
		DiskBytesRead:    uint64(v4.ri_diskio_bytesread),
		DiskBytesWritten: uint64(v4.ri_diskio_byteswritten),
	}, nil
}

func isPermissionErrno(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.EPERM || errno == syscall.EACCES
}
