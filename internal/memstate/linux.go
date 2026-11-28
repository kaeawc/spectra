package memstate

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/pressure"
)

// Linux pressure thresholds. Available memory matches hostwatch's Linux
// sample; the PSI cut-offs match internal/pressure's assessment.
const (
	linuxAvailWarnPct = 15.0
	linuxAvailCritPct = 5.0
	psiSomeWarnPct    = 10.0
	psiFullWarnPct    = 1.0
	psiFullCritPct    = 10.0
)

// CollectLinux runs the Linux backend against opts.ProcRoot on any host, so
// a fixture /proc tree can be read on macOS too.
func CollectLinux(opts Options) (MemoryState, error) {
	return collectLinux(opts)
}

// collectLinux reads <procRoot>/meminfo, <procRoot>/vmstat and, when the
// kernel exposes it, <procRoot>/pressure/memory.
func collectLinux(opts Options) (MemoryState, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	root := opts.ProcRoot
	if root == "" {
		root = "/proc"
	}
	data, err := os.ReadFile(filepath.Join(root, "meminfo"))
	if err != nil {
		return MemoryState{}, fmt.Errorf("read meminfo: %w", err)
	}
	ms, err := ParseMeminfo(data)
	if err != nil {
		return MemoryState{}, err
	}
	ms.PageSizeBytes = opts.PageSize
	if p := os.Getpagesize(); ms.PageSizeBytes == 0 && p > 0 {
		ms.PageSizeBytes = uint64(p)
	}
	ms.CollectedAt = now().UTC()
	if vm, err := os.ReadFile(filepath.Join(root, "vmstat")); err == nil {
		applyProcVMStat(&ms, vm)
	}
	if psi, err := pressure.ReadPSI(root); err == nil {
		mem := psi.Memory
		ms.PSI = &mem
	}
	ms.PressureLevel, ms.PressureReason = linuxPressure(ms)
	return ms, nil
}

// ParseMeminfo maps /proc/meminfo onto MemoryState. Values there are KiB.
// Unevictable (mlocked and otherwise unreclaimable pages) stands in for
// wired memory.
// Zswap is Linux's compressed swap cache: Zswap is the compressed size held
// in RAM and Zswapped the uncompressed data it stores, which are the
// compressor's occupied and stored sizes.
func ParseMeminfo(data []byte) (MemoryState, error) {
	kib := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			kib[key] = v
		}
	}
	if kib["MemTotal"] == 0 {
		return MemoryState{}, fmt.Errorf("meminfo: no MemTotal")
	}
	b := func(k string) uint64 { return kib[k] * 1024 }
	ms := MemoryState{
		OS:                 "linux",
		PhysicalBytes:      b("MemTotal"),
		Free:               b("MemFree"),
		AvailableBytes:     b("MemAvailable"),
		Active:             b("Active"),
		Inactive:           b("Inactive"),
		Wired:              b("Unevictable"),
		FileBacked:         b("Active(file)") + b("Inactive(file)"),
		Anonymous:          b("AnonPages"),
		CompressorOccupied: b("Zswap"),
		CompressorStored:   b("Zswapped"),
		Swap: SwapUsage{
			TotalBytes: b("SwapTotal"),
			FreeBytes:  b("SwapFree"),
		},
	}
	if _, ok := kib["MemAvailable"]; !ok {
		// Kernels before 3.14: free plus reclaimable page cache.
		ms.AvailableBytes = ms.Free + b("Cached") + b("Buffers")
	}
	if ms.Swap.TotalBytes > ms.Swap.FreeBytes {
		ms.Swap.UsedBytes = ms.Swap.TotalBytes - ms.Swap.FreeBytes
	}
	if ms.CompressorOccupied > 0 {
		ms.CompressorRatio = float64(ms.CompressorStored) / float64(ms.CompressorOccupied)
	}
	ms.PressureFreePercent = float64(ms.AvailableBytes) / float64(ms.PhysicalBytes) * 100
	return ms, nil
}

// applyProcVMStat adds the cumulative swap-in/out page counts.
func applyProcVMStat(ms *MemoryState, data []byte) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "pswpin":
			ms.SwapIns = v
		case "pswpout":
			ms.SwapOuts = v
		}
	}
}

// linuxPressure classifies the sample. Linux has no kernel pressure level
// like macOS's memorystatus, so it combines how much memory is still
// available with how long tasks stalled waiting for memory (PSI).
func linuxPressure(ms MemoryState) (PressureLevel, string) {
	avail := ms.PressureFreePercent
	if ms.PSI != nil && ms.PSI.Full != nil && ms.PSI.Full.Avg10 >= psiFullCritPct {
		return PressureCritical, fmt.Sprintf("all tasks stalled on memory %.1f%% of the last 10s", ms.PSI.Full.Avg10)
	}
	if avail < linuxAvailCritPct {
		return PressureCritical, fmt.Sprintf("%.1f%% of memory available", avail)
	}
	if ms.PSI != nil && ms.PSI.Full != nil && ms.PSI.Full.Avg10 >= psiFullWarnPct {
		return PressureWarning, fmt.Sprintf("all tasks stalled on memory %.1f%% of the last 10s", ms.PSI.Full.Avg10)
	}
	if ms.PSI != nil && ms.PSI.Some.Avg10 >= psiSomeWarnPct {
		return PressureWarning, fmt.Sprintf("some tasks stalled on memory %.1f%% of the last 10s", ms.PSI.Some.Avg10)
	}
	if avail < linuxAvailWarnPct {
		return PressureWarning, fmt.Sprintf("%.1f%% of memory available", avail)
	}
	return PressureNormal, fmt.Sprintf("%.1f%% of memory available", avail)
}
