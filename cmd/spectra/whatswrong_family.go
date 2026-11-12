package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

const (
	// familyCPUThreshold is the summed CPU (percent of one core) at which a
	// multi-process simulator/emulator family becomes a finding even if no
	// member is heavy.
	familyCPUThreshold = 100.0
	// familyHighCPU escalates a simulator/emulator finding to high severity.
	familyHighCPU = 200.0
	// Noisier families (browser, build, Xcode) need a higher bar and never
	// exceed medium severity.
	noisyFamilyRawCPU     = 400.0
	noisyFamilyMachinePct = 50.0
)

// appendFamilyCauses adds one finding per process family whose combined CPU
// crosses its threshold. cores is the logical CPU count (0 if unknown, in
// which case the share of listed-process CPU is reported instead). Findings
// are appended before single-process ones so the stable severity sort ranks
// them above those.
func appendFamilyCauses(causes []slowCause, fams []process.Family, cores int) []slowCause {
	for _, f := range fams {
		severity, ok := familySeverity(f, cores)
		if !ok {
			continue
		}
		causes = append(causes, slowCause{
			Severity: severity,
			Title:    familyTitle(f, cores),
			Detail:   familyDetail(f),
		})
	}
	return causes
}

func familySeverity(f process.Family, cores int) (string, bool) {
	if f.Kind == process.FamilyOther || f.Members < 2 {
		return "", false
	}
	switch f.Kind {
	case process.FamilySimulator, process.FamilyEmulator:
		switch {
		case f.CPUPct >= familyHighCPU:
			return "high", true
		case f.CPUPct >= familyCPUThreshold:
			return "medium", true
		}
		return "", false
	}
	if f.CPUPct >= noisyFamilyRawCPU || (cores > 0 && machinePct(f, cores) >= noisyFamilyMachinePct) {
		return "medium", true
	}
	return "", false
}

func machinePct(f process.Family, cores int) float64 {
	return f.CPUPct / float64(cores)
}

func familyTitle(f process.Family, cores int) string {
	if cores > 0 {
		return fmt.Sprintf("%s uses %.0f%% of machine CPU", f.Name, machinePct(f, cores))
	}
	return fmt.Sprintf("%s uses %.0f%% of total process CPU", f.Name, f.SharePct)
}

func familyDetail(f process.Family) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%.0f%% CPU (%.1f cores busy) across %d processes", f.CPUPct, f.CPUPct/100, f.Members)
	if f.Age > 0 {
		fmt.Fprintf(&b, ", oldest member running for %s", formatDuration(f.Age))
	}
	if len(f.Heaviest) > 0 {
		b.WriteString("; heaviest: ")
		b.WriteString(formatMembers(f.Heaviest))
	}
	return b.String()
}

func formatMembers(ms []process.Member) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		parts = append(parts, fmt.Sprintf("%s (pid %d, %.0f%%)", m.Command, m.PID, m.CPUPct))
	}
	return strings.Join(parts, ", ")
}

// slowFamilies groups the process list; now may be zero.
func slowFamilies(procs []process.Info, now time.Time) []process.Family {
	return process.GroupFamilies(procs, now, 0)
}
