package sysinfo

import (
	"errors"
	"time"
)

// ErrUnsupportedHardware is returned by SampleSoCPower on platforms that
// don't expose IOReport energy counters (non-darwin, Intel macs, old macOS).
var ErrUnsupportedHardware = errors.New("SoC power sampling not supported on this hardware")

// SoCPower is a single sample of system-wide energy consumed by each SoC
// subsystem over Interval. All joule fields are non-negative.
//
// PackageJoules is the sum of the per-subsystem joules reported by the
// IOReport "Energy Model" group. On Apple Silicon this approximates what
// `powermetrics --samplers cpu_power,gpu_power` calls "Combined Power".
type SoCPower struct {
	Interval      time.Duration `json:"interval_ns"`
	CPUPJoules    float64       `json:"cpu_p_joules"`
	CPUEJoules    float64       `json:"cpu_e_joules"`
	GPUJoules     float64       `json:"gpu_joules"`
	ANEJoules     float64       `json:"ane_joules"`
	DRAMJoules    float64       `json:"dram_joules"`
	PackageJoules float64       `json:"package_joules"`
}

// Watts returns the average package power draw across the sample window.
func (p SoCPower) Watts() float64 {
	if p.Interval <= 0 {
		return 0
	}
	return p.PackageJoules / p.Interval.Seconds()
}
