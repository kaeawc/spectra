package thermal

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/proc"
)

var (
	limitRe = regexp.MustCompile(`^(CPU_Scheduler_Limit|CPU_Available_CPUs|CPU_Speed_Limit)\s*=\s*(\d+)`)
	levelRe = regexp.MustCompile(`(?i)^(thermal|performance) warning level\D*(\d+)`)
)

// The sysinfo power parser only exposes a bool/level and a speed-limit sample
// list, so the full set of limit and warning fields is parsed here.
func parsePmsetTherm(out string) Limits {
	var l Limits
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "Note:"))
		if strings.HasPrefix(strings.ToLower(line), "no ") {
			continue // "No thermal warning level has been recorded"
		}
		if m := limitRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			switch m[1] {
			case "CPU_Scheduler_Limit":
				l.SchedulerLimit = &n
			case "CPU_Available_CPUs":
				l.AvailableCPUs = &n
			default:
				l.SpeedLimit = &n
			}
			continue
		}
		if m := levelRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			if strings.EqualFold(m[1], "thermal") {
				l.ThermalWarning = &n
			} else {
				l.PerformanceWarning = &n
			}
		}
	}
	return l
}

func (c *Collector) collectDarwin(ctx context.Context) (Report, error) {
	out, err := proc.Output(ctx, c.Runner, "pmset", "-g", "therm")
	if err != nil {
		return Report{}, fmt.Errorf("pmset -g therm: %w", err)
	}
	l := parsePmsetTherm(string(out))
	r := Report{OS: "darwin", Limits: &l, Findings: []Finding{}}
	if l.SpeedLimit != nil && *l.SpeedLimit < FullSpeedPct {
		r.add(Throttled, "CPU_Speed_Limit %d", *l.SpeedLimit)
	}
	if l.SchedulerLimit != nil && *l.SchedulerLimit < FullSpeedPct {
		r.add(Throttled, "CPU_Scheduler_Limit %d", *l.SchedulerLimit)
	}
	if l.PerformanceWarning != nil && *l.PerformanceWarning > 0 {
		r.add(Throttled, "performance warning level %d", *l.PerformanceWarning)
	}
	if l.ThermalWarning != nil && *l.ThermalWarning > 0 {
		r.add(Warm, "thermal warning level %d", *l.ThermalWarning)
	}
	if l.SpeedLimit == nil && l.SchedulerLimit == nil {
		r.Notes = append(r.Notes, "pmset printed no CPU limit fields (common on Apple silicon); only warning levels were available")
	}
	r.decide("no CPU speed limit or thermal warning recorded")
	return r, nil
}

func (r *Report) add(v Verdict, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{Verdict: v, Evidence: fmt.Sprintf(format, args...)})
}
