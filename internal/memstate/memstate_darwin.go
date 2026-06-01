//go:build darwin && cgo

package memstate

/*
#include <errno.h>
#include <mach/mach.h>
#include <mach/mach_host.h>
#include <mach/vm_statistics.h>
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
#include <sys/sysctl.h>

typedef struct {
	uint64_t total;
	uint64_t avail;
	uint64_t used;
	uint32_t pagesize;
	bool encrypted;
} swap_usage_out;

static int read_swap_usage(swap_usage_out *out) {
	struct xsw_usage xsu;
	size_t size = sizeof(xsu);
	if (sysctlbyname("vm.swapusage", &xsu, &size, NULL, 0) != 0) {
		return errno;
	}
	out->total = xsu.xsu_total;
	out->avail = xsu.xsu_avail;
	out->used = xsu.xsu_used;
	out->pagesize = xsu.xsu_pagesize;
	out->encrypted = xsu.xsu_encrypted;
	return 0;
}

static kern_return_t read_vm_statistics(vm_statistics64_data_t *out) {
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	memset(out, 0, sizeof(*out));
	return host_statistics64(mach_host_self(), HOST_VM_INFO64, (host_info64_t)out, &count);
}
*/
import "C"

import (
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Collect gathers current host memory state.
func Collect() (MemoryState, error) {
	return CollectWithOptions(Options{})
}

// CollectWithOptions gathers current host memory state with testable options.
func CollectWithOptions(opts Options) (MemoryState, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	ms := MemoryState{
		PageSizeBytes: uint64(syscall.Getpagesize()),
		CollectedAt:   now().UTC(),
		PressureLevel: PressureUnknown,
	}
	if v, err := unix.SysctlUint64("hw.memsize"); err == nil {
		ms.PhysicalBytes = v
	}
	if err := readSwapUsage(&ms.Swap); err != nil {
		return ms, err
	}
	if err := readVMStat(&ms); err != nil {
		return ms, err
	}
	if v, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level"); err == nil {
		ms.PressureLevel = pressureLevel(uint64(v))
	}
	return finish(ms), nil
}

func readSwapUsage(out *SwapUsage) error {
	var raw C.swap_usage_out
	if errno := C.read_swap_usage(&raw); errno != 0 {
		return fmt.Errorf("sysctl vm.swapusage: %w", syscall.Errno(errno))
	}
	out.TotalBytes = uint64(raw.total)
	out.UsedBytes = uint64(raw.used)
	out.FreeBytes = uint64(raw.avail)
	out.Encrypted = bool(raw.encrypted)
	return nil
}

func readVMStat(ms *MemoryState) error {
	var raw C.vm_statistics64_data_t
	if kr := C.read_vm_statistics(&raw); kr != C.KERN_SUCCESS {
		return fmt.Errorf("host_statistics64: kern_return=%d", int(kr))
	}
	page := ms.PageSizeBytes
	ms.Wired = uint64(raw.wire_count) * page
	ms.Active = uint64(raw.active_count) * page
	ms.Inactive = uint64(raw.inactive_count) * page
	ms.Speculative = uint64(raw.speculative_count) * page
	ms.Free = uint64(raw.free_count) * page
	ms.Purgeable = uint64(raw.purgeable_count) * page
	ms.FileBacked = uint64(raw.external_page_count) * page
	ms.Anonymous = uint64(raw.internal_page_count) * page
	ms.CompressorOccupied = uint64(raw.compressor_page_count) * page
	ms.CompressorStored = uint64(raw.total_uncompressed_pages_in_compressor) * page
	ms.Compressions = uint64(raw.compressions)
	ms.Decompressions = uint64(raw.decompressions)
	ms.SwapIns = uint64(raw.swapins)
	ms.SwapOuts = uint64(raw.swapouts)
	return nil
}
