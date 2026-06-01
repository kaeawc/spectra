package rules

import (
	"fmt"

	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// MemoryFacts is the rule-facing projection of host memory state.
type MemoryFacts struct {
	CompressorOccupiedFraction float64
	SwapUsedFraction           float64
	PressureLevel              string
	UptimeHours                float64
}

func MemoryFactsFor(s snapshot.Snapshot) MemoryFacts {
	m := s.Host.Memory
	physical := float64(m.PhysicalBytes)
	var f MemoryFacts
	if physical > 0 {
		f.CompressorOccupiedFraction = float64(m.CompressorOccupied) / physical
		f.SwapUsedFraction = float64(m.Swap.UsedBytes) / physical
	}
	f.PressureLevel = string(m.PressureLevel)
	f.UptimeHours = float64(s.Host.UptimeSeconds) / 3600
	return f
}

func ruleMemoryCompressorExcess() Rule {
	return Rule{
		ID:       "memory.compressor_excess",
		Severity: SeverityMedium,
		Message:  "VM compressor holds >25% of physical RAM after 24h+ uptime.",
		Fix:      "Run `spectra memory` and check long-lived daemons.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			f := MemoryFactsFor(s)
			if f.CompressorOccupiedFraction <= 0.25 || f.UptimeHours <= 24 {
				return nil
			}
			return []Finding{{
				RuleID:   "memory.compressor_excess",
				Severity: SeverityMedium,
				Subject:  "host memory",
				Message:  fmt.Sprintf("VM compressor holds %.0f%% of physical RAM after %.0fh uptime; likely a leaking subscriber.", f.CompressorOccupiedFraction*100, f.UptimeHours),
				Fix:      "Run `spectra memory` and check long-lived daemons.",
			}}
		},
	}
}

func ruleMemorySwapExcess() Rule {
	return Rule{
		ID:       "memory.swap_excess",
		Severity: SeverityMedium,
		Message:  "Swap used exceeds 10% of physical RAM.",
		Fix:      "Run `spectra memory`; expect latency spikes until the leaking or memory-heavy process is stopped.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			f := MemoryFactsFor(s)
			if f.SwapUsedFraction <= 0.10 {
				return nil
			}
			return []Finding{{
				RuleID:   "memory.swap_excess",
				Severity: SeverityMedium,
				Subject:  "host memory",
				Message:  fmt.Sprintf("Swap used is %.0f%% of physical RAM; expect latency spikes.", f.SwapUsedFraction*100),
				Fix:      "Run `spectra memory` and inspect top resident processes with `spectra process`.",
			}}
		},
	}
}

func ruleMemorySustainedPressure() Rule {
	return Rule{
		ID:       "memory.sustained_pressure",
		Severity: SeverityMedium,
		Message:  "Pressure is Warning for >1h uptime, or Critical at any uptime.",
		Fix:      "Run `spectra memory` to inspect compressor, swap, and pressure details.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			f := MemoryFactsFor(s)
			switch f.PressureLevel {
			case string(memstate.PressureCritical):
				return []Finding{memoryPressureFinding(SeverityMedium, f, "Critical memory pressure; user-visible stalls are likely.")}
			case string(memstate.PressureWarning):
				if f.UptimeHours > 1 {
					return []Finding{memoryPressureFinding(SeverityInfo, f, "Memory pressure has stayed at Warning past the first hour of uptime.")}
				}
			}
			return nil
		},
	}
}

func memoryPressureFinding(severity Severity, f MemoryFacts, message string) Finding {
	return Finding{
		RuleID:   "memory.sustained_pressure",
		Severity: severity,
		Subject:  "host memory",
		Message:  fmt.Sprintf("%s pressure=%s uptime=%.1fh.", message, f.PressureLevel, f.UptimeHours),
		Fix:      "Run `spectra memory` to inspect compressor, swap, and pressure details.",
	}
}
