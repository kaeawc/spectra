// Package powerreport holds the `spectra power --json` envelope and the pieces
// of its baseline assembly that the CLI and the daemon share, so both emit the
// same JSON.
package powerreport

import (
	"time"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

// Report is the stable JSON envelope emitted by `spectra power --json`.
// Field names match across modes; backends fill what they can and missing
// data is omitted rather than zeroed.
type Report struct {
	IntervalMS int64                    `json:"interval_ms"`
	Mode       string                   `json:"mode"`
	SampledAt  time.Time                `json:"sampled_at"`
	Battery    *BatteryInfo             `json:"battery,omitempty"`
	Thermal    *ThermalInfo             `json:"thermal,omitempty"`
	Assertions []sysinfo.PowerAssertion `json:"assertions,omitempty"`
	Package    *PackageEnergy           `json:"package,omitempty"`
	Processes  []ProcessEnergy          `json:"processes,omitempty"`
	Skipped    int                      `json:"skipped,omitempty"`
	Notes      []string                 `json:"notes,omitempty"`
}

type BatteryInfo struct {
	OnBattery bool `json:"on_battery"`
	Pct       int  `json:"pct,omitempty"`
	// Present is false on a machine with no battery (desktop, server, VM);
	// absent when that could not be read.
	Present *bool `json:"present,omitempty"`
}

type ThermalInfo struct {
	Pressure                string `json:"pressure,omitempty"`
	Throttled               bool   `json:"throttled,omitempty"`
	CPUSpeedLimitPct        int    `json:"cpu_speed_limit_pct,omitempty"`
	LowestCPUSpeedLimitPct  int    `json:"lowest_cpu_speed_limit_pct,omitempty"`
	AverageCPUSpeedLimitPct int    `json:"average_cpu_speed_limit_pct,omitempty"`
	PercentThermalThrottled int    `json:"percent_thermal_throttled,omitempty"`
}

type PackageEnergy struct {
	Joules     float64 `json:"joules,omitempty"`
	CPUJoules  float64 `json:"cpu_joules,omitempty"`
	GPUJoules  float64 `json:"gpu_joules,omitempty"`
	ANEJoules  float64 `json:"ane_joules,omitempty"`
	DRAMJoules float64 `json:"dram_joules,omitempty"`
	IntervalNS int64   `json:"interval_ns,omitempty"`
}

type ProcessWakeups struct {
	Interrupt uint64 `json:"interrupt,omitempty"`
	PkgIdle   uint64 `json:"pkg_idle,omitempty"`
}

type ProcessEnergy struct {
	PID            int                      `json:"pid"`
	Command        string                   `json:"command,omitempty"`
	BilledEnergyNJ uint64                   `json:"billed_energy_nj,omitempty"`
	GPUTimeNs      uint64                   `json:"gpu_time_ns,omitempty"`
	Wakeups        *ProcessWakeups          `json:"wakeups,omitempty"`
	Impact         *sysinfo.ImpactBreakdown `json:"impact,omitempty"`
	Source         string                   `json:"source"`
}

func FillState(r *Report, s sysinfo.PowerState) {
	if s.OnBattery || s.BatteryPct > 0 || s.HasBattery != nil {
		r.Battery = &BatteryInfo{OnBattery: s.OnBattery, Pct: s.BatteryPct, Present: s.HasBattery}
	}
	if s.ThermalPressure != "" || s.ThermalThrottled || s.CPUSpeedLimitPct > 0 {
		r.Thermal = &ThermalInfo{
			Pressure:                s.ThermalPressure,
			Throttled:               s.ThermalThrottled,
			CPUSpeedLimitPct:        s.CPUSpeedLimitPct,
			LowestCPUSpeedLimitPct:  s.LowestCPUSpeedLimitPct,
			AverageCPUSpeedLimitPct: s.AverageCPUSpeedLimitPct,
			PercentThermalThrottled: s.PercentThermalThrottled,
		}
	}
	if len(s.Assertions) > 0 {
		r.Assertions = s.Assertions
	}
}

func ProcessesFromEnergyTopUsers(users []sysinfo.EnergyUser) []ProcessEnergy {
	if len(users) == 0 {
		return nil
	}
	rows := make([]ProcessEnergy, 0, len(users))
	for _, u := range users {
		rows = append(rows, ProcessEnergy{
			PID:     u.PID,
			Command: u.Command,
			Impact:  &sysinfo.ImpactBreakdown{Total: u.EnergyImpact, FromEnergy: u.EnergyImpact},
			Source:  "top",
		})
	}
	return rows
}

// DefaultInterval is the interval_ms the CLI reports when --interval is unset.
const DefaultInterval = time.Second

// Baseline assembles the report for the no-flag (pmset + top) mode.
func Baseline(state sysinfo.PowerState, now time.Time, interval time.Duration) Report {
	r := Report{IntervalMS: interval.Milliseconds(), Mode: "baseline", SampledAt: now}
	FillState(&r, state)
	r.Processes = ProcessesFromEnergyTopUsers(state.EnergyTopUsers)
	return r
}
