package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

const (
	// familyCPUThreshold is the summed CPU (percent of one core) at which a
	// multi-process family becomes a finding even if no member is heavy.
	familyCPUThreshold = 100.0
	// familyHighCPU escalates the finding to high severity.
	familyHighCPU = 200.0
)

// appendFamilyCauses adds one finding per process family whose combined CPU
// crosses the threshold. They are appended before single-process findings so
// the stable severity sort ranks them above those.
func appendFamilyCauses(causes []slowCause, fams []process.Family) []slowCause {
	for _, f := range fams {
		if f.Kind == process.FamilyOther || f.Members < 2 || f.CPUPct < familyCPUThreshold {
			continue
		}
		severity := "medium"
		if f.CPUPct >= familyHighCPU {
			severity = "high"
		}
		causes = append(causes, slowCause{
			Severity: severity,
			Title:    fmt.Sprintf("%s uses %.0f%% of total CPU", f.Name, f.SharePct),
			Detail:   familyDetail(f),
		})
	}
	return causes
}

func familyDetail(f process.Family) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%.0f%% CPU across %d processes", f.CPUPct, f.Members)
	if f.Age > 0 {
		fmt.Fprintf(&b, ", running for %s", formatDuration(f.Age))
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
