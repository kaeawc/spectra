// Package memstate captures host-wide memory pressure, compressor, and swap
// state on macOS.
package memstate

import (
	"errors"
	"time"
)

// ErrNotSupported is returned on platforms without the macOS VM APIs.
var ErrNotSupported = errors.New("memory state collection not supported")

// PressureLevel is the kernel memorystatus pressure classification.
type PressureLevel string

const (
	PressureUnknown  PressureLevel = "Unknown"
	PressureNormal   PressureLevel = "Normal"
	PressureWarning  PressureLevel = "Warning"
	PressureCritical PressureLevel = "Critical"
)

// SwapUsage is the host swap device state.
type SwapUsage struct {
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	Encrypted  bool   `json:"encrypted"`
}

// MemoryState is one point-in-time host memory sample.
type MemoryState struct {
	PhysicalBytes       uint64        `json:"physical_bytes"`
	PageSizeBytes       uint64        `json:"page_size_bytes"`
	Wired               uint64        `json:"wired"`
	Active              uint64        `json:"active"`
	Inactive            uint64        `json:"inactive"`
	Speculative         uint64        `json:"speculative"`
	Free                uint64        `json:"free"`
	Purgeable           uint64        `json:"purgeable"`
	FileBacked          uint64        `json:"file_backed"`
	Anonymous           uint64        `json:"anonymous"`
	CompressorOccupied  uint64        `json:"compressor_occupied"`
	CompressorStored    uint64        `json:"compressor_stored"`
	CompressorRatio     float64       `json:"compressor_ratio"`
	Swap                SwapUsage     `json:"swap"`
	PressureLevel       PressureLevel `json:"pressure_level"`
	PressureFreePercent float64       `json:"pressure_free_percent"`
	Compressions        uint64        `json:"compressions"`
	Decompressions      uint64        `json:"decompressions"`
	SwapIns             uint64        `json:"swap_ins"`
	SwapOuts            uint64        `json:"swap_outs"`
	CollectedAt         time.Time     `json:"collected_at"`
}

// Options configures collection for tests.
type Options struct {
	Now func() time.Time
}

func finish(ms MemoryState) MemoryState {
	if ms.CompressorOccupied > 0 {
		ms.CompressorRatio = float64(ms.CompressorStored) / float64(ms.CompressorOccupied)
	}
	if ms.PhysicalBytes > 0 {
		free := ms.Free + ms.Speculative
		ms.PressureFreePercent = float64(free) / float64(ms.PhysicalBytes) * 100
	}
	return ms
}

func pressureLevel(raw uint64) PressureLevel {
	switch raw {
	case 1:
		return PressureNormal
	case 2:
		return PressureWarning
	case 4:
		return PressureCritical
	default:
		return PressureUnknown
	}
}
