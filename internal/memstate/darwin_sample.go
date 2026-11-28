package memstate

import "time"

// DarwinVMCounts are the host_statistics64(HOST_VM_INFO64) counters the
// macOS collector reads: page counts, except the four cumulative event
// counters at the end.
type DarwinVMCounts struct {
	Wire                   uint64 `json:"wire_count"`
	Active                 uint64 `json:"active_count"`
	Inactive               uint64 `json:"inactive_count"`
	Speculative            uint64 `json:"speculative_count"`
	Free                   uint64 `json:"free_count"`
	Purgeable              uint64 `json:"purgeable_count"`
	External               uint64 `json:"external_page_count"`
	Internal               uint64 `json:"internal_page_count"`
	Compressor             uint64 `json:"compressor_page_count"`
	UncompressedCompressor uint64 `json:"total_uncompressed_pages_in_compressor"`
	Compressions           uint64 `json:"compressions"`
	Decompressions         uint64 `json:"decompressions"`
	Swapins                uint64 `json:"swapins"`
	Swapouts               uint64 `json:"swapouts"`
}

// DarwinSample is one raw macOS reading, before FromDarwinSample assembles
// it: the sysctls and host_statistics64 the cgo collector reads. A nil level
// means that sysctl could not be read. Recorded contract cases hold one, so
// the assembly runs on any host.
type DarwinSample struct {
	PageSizeBytes uint64 `json:"page_size_bytes"`
	// PhysicalBytes is hw.memsize, 0 when it could not be read.
	PhysicalBytes uint64         `json:"physical_bytes"`
	Swap          SwapUsage      `json:"swap"`
	VM            DarwinVMCounts `json:"vm"`
	// PressureLevel is kern.memorystatus_vm_pressure_level.
	PressureLevel *uint32 `json:"pressure_level"`
	// MemorystatusLevel is kern.memorystatus_level.
	MemorystatusLevel *uint32 `json:"memorystatus_level"`
}

// FromDarwinSample assembles a macOS sample into a MemoryState, as Collect
// does on macOS.
func FromDarwinSample(s DarwinSample, collectedAt time.Time) MemoryState {
	ms := s.base(collectedAt)
	page := s.PageSizeBytes
	ms.Wired = s.VM.Wire * page
	ms.Active = s.VM.Active * page
	ms.Inactive = s.VM.Inactive * page
	ms.Speculative = s.VM.Speculative * page
	ms.Free = s.VM.Free * page
	ms.Purgeable = s.VM.Purgeable * page
	ms.FileBacked = s.VM.External * page
	ms.Anonymous = s.VM.Internal * page
	ms.CompressorOccupied = s.VM.Compressor * page
	ms.CompressorStored = s.VM.UncompressedCompressor * page
	ms.Compressions = s.VM.Compressions
	ms.Decompressions = s.VM.Decompressions
	ms.SwapIns = s.VM.Swapins
	ms.SwapOuts = s.VM.Swapouts
	if s.PressureLevel != nil {
		ms.PressureLevel = pressureLevel(uint64(*s.PressureLevel))
	}
	ms = finish(ms)
	if s.MemorystatusLevel != nil {
		ms = applyMemorystatusLevel(ms, *s.MemorystatusLevel)
	}
	return ms
}

// base is what the collector returns alongside a read error: the fields read
// before the failing call.
func (s DarwinSample) base(collectedAt time.Time) MemoryState {
	return MemoryState{
		PageSizeBytes: s.PageSizeBytes,
		CollectedAt:   collectedAt,
		PressureLevel: PressureUnknown,
		PhysicalBytes: s.PhysicalBytes,
		Swap:          s.Swap,
	}
}
