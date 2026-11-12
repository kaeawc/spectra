package pressure

import "fmt"

// Severity ranks a Finding.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Finding is one conclusion drawn from pressure data.
type Finding struct {
	Kind     string   `json:"kind"`
	Severity Severity `json:"severity"`
	Summary  string   `json:"summary"`
}

// Thresholds are percentages of wall time over the trailing 10s window. They
// follow the usual PSI guidance: a "some" stall above ~10% is noticeable
// latency, and any sustained "full" stall means the resource is the
// bottleneck rather than merely contended.
const (
	someWarnPct  = 10.0
	someCritPct  = 30.0
	fullWarnPct  = 1.0
	fullCritPct  = 10.0
	nearLimitPct = 90.0
)

// AssessPSI turns host PSI into findings. A healthy host returns none.
func AssessPSI(p PSI) []Finding {
	var out []Finding
	for _, r := range []struct {
		name string
		res  Resource
	}{{"cpu", p.CPU}, {"memory", p.Memory}, {"io", p.IO}} {
		out = append(out, assessResource("host", r.name, r.res)...)
	}
	return out
}

func assessResource(scope, name string, r Resource) []Finding {
	var out []Finding
	if sev := bySeverity(r.Some.Avg10, someWarnPct, someCritPct); sev != "" {
		out = append(out, Finding{
			Kind: name + "-pressure", Severity: sev,
			Summary: fmt.Sprintf("%s %s: tasks stalled on %s %.1f%% of the last 10s (60s avg %.1f%%)", scope, name, name, r.Some.Avg10, r.Some.Avg60),
		})
	}
	// CPU "full" is not meaningful for the host; only memory and I/O.
	if r.Full != nil && name != "cpu" {
		if sev := bySeverity(r.Full.Avg10, fullWarnPct, fullCritPct); sev != "" {
			out = append(out, Finding{
				Kind: name + "-stall", Severity: sev,
				Summary: fmt.Sprintf("%s %s: ALL non-idle tasks stalled on %s %.1f%% of the last 10s — it is the bottleneck", scope, name, name, r.Full.Avg10),
			})
		}
	}
	return out
}

func bySeverity(v, warn, crit float64) Severity {
	switch {
	case v >= crit:
		return SeverityCritical
	case v >= warn:
		return SeverityWarning
	default:
		return ""
	}
}

// AssessCgroup turns cgroup accounting into findings.
func AssessCgroup(cg Cgroup) []Finding {
	var out []Finding
	if m := cg.Memory; m != nil {
		out = append(out, assessMemory(cg.Path, m)...)
		if m.Pressure != nil {
			out = append(out, assessResource("cgroup "+cg.Path, "memory", *m.Pressure)...)
		}
	}
	if c := cg.CPU; c != nil {
		if c.NrThrottled > 0 && c.NrPeriods > 0 {
			pct := 100 * float64(c.NrThrottled) / float64(c.NrPeriods)
			sev := SeverityInfo
			if pct >= 25 {
				sev = SeverityWarning
			}
			out = append(out, Finding{
				Kind: "cpu-throttled", Severity: sev,
				Summary: fmt.Sprintf("cgroup %s was CPU-throttled in %d of %d periods (%.0f%%, %.1fs total) by its cpu.max quota", cg.Path, c.NrThrottled, c.NrPeriods, pct, float64(c.ThrottledUsec)/1e6),
			})
		}
		if c.Pressure != nil {
			out = append(out, assessResource("cgroup "+cg.Path, "cpu", *c.Pressure)...)
		}
	}
	if p := cg.Pids; p != nil && p.Max > 0 && 100*float64(p.Current)/float64(p.Max) >= nearLimitPct {
		out = append(out, Finding{
			Kind: "pids-near-limit", Severity: SeverityCritical,
			Summary: fmt.Sprintf("cgroup %s has %d of %d allowed tasks; fork/clone will start failing with EAGAIN", cg.Path, p.Current, p.Max),
		})
	}
	if cg.IOPressure != nil {
		out = append(out, assessResource("cgroup "+cg.Path, "io", *cg.IOPressure)...)
	}
	return out
}

func assessMemory(cgPath string, m *Memory) []Finding {
	var out []Finding
	if m.Events.OOMKill > 0 {
		out = append(out, Finding{
			Kind: "cgroup-oom-kill", Severity: SeverityCritical,
			Summary: fmt.Sprintf("cgroup %s: kernel OOM-killed %d process(es) for exceeding a memory limit (memory.events oom_kill)", cgPath, m.Events.OOMKill),
		})
	}
	if m.Events.Max > 0 && m.Events.OOMKill == 0 {
		out = append(out, Finding{
			Kind: "cgroup-memory-max-hit", Severity: SeverityWarning,
			Summary: fmt.Sprintf("cgroup %s hit memory.max %d time(s); allocations were reclaimed or failed but nothing was killed yet", cgPath, m.Events.Max),
		})
	}
	if m.Events.High > 0 {
		out = append(out, Finding{
			Kind: "cgroup-memory-throttled", Severity: SeverityWarning,
			Summary: fmt.Sprintf("cgroup %s exceeded memory.high %d time(s); its tasks were throttled into direct reclaim", cgPath, m.Events.High),
		})
	}
	if m.EffectiveMax > 0 {
		// memory.current includes reclaimable page cache, which sits near
		// the limit on any I/O-heavy workload without being a problem;
		// anonymous memory is what actually leads to an OOM kill.
		used := m.Current
		if m.Anon > 0 {
			used = m.Anon
		}
		pct := 100 * float64(used) / float64(m.EffectiveMax)
		if pct >= nearLimitPct {
			where := ""
			if m.LimitedBy != "" && m.LimitedBy != cgPath {
				where = " (limit set on " + m.LimitedBy + ")"
			}
			out = append(out, Finding{
				Kind: "cgroup-memory-near-limit", Severity: SeverityCritical,
				Summary: fmt.Sprintf("cgroup %s has anonymous memory at %.0f%% of its %d MiB memory limit%s", cgPath, pct, m.EffectiveMax>>20, where),
			})
		}
	}
	return out
}
