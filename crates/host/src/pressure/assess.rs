//! Findings drawn from pressure data (Go's `internal/pressure/assess.go`).

use serde::Serialize;

use super::{Cgroup, Memory, Psi, Resource};

/// How bad a [`Finding`] is.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Severity {
    Info,
    Warning,
    Critical,
}

impl Severity {
    pub fn as_str(self) -> &'static str {
        match self {
            Severity::Info => "info",
            Severity::Warning => "warning",
            Severity::Critical => "critical",
        }
    }
}

/// One conclusion drawn from pressure data.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Finding {
    pub kind: String,
    pub severity: Severity,
    pub summary: String,
}

// Percentages of wall time over the trailing 10s window, after the usual PSI guidance: a "some"
// stall above ~10% is noticeable latency, and any sustained "full" stall means the resource is
// the bottleneck rather than merely contended.
const SOME_WARN_PCT: f64 = 10.0;
const SOME_CRIT_PCT: f64 = 30.0;
const FULL_WARN_PCT: f64 = 1.0;
const FULL_CRIT_PCT: f64 = 10.0;
const NEAR_LIMIT_PCT: f64 = 90.0;

fn finding(kind: impl Into<String>, severity: Severity, summary: String) -> Finding {
    Finding { kind: kind.into(), severity, summary }
}

/// Go's `AssessPSI`: findings from host PSI. A healthy host has none.
pub fn assess_psi(p: &Psi) -> Vec<Finding> {
    let mut out = Vec::new();
    for (name, res) in [("cpu", &p.cpu), ("memory", &p.memory), ("io", &p.io)] {
        out.extend(assess_resource("host", name, res));
    }
    out
}

fn assess_resource(scope: &str, name: &str, r: &Resource) -> Vec<Finding> {
    let mut out = Vec::new();
    if let Some(sev) = by_severity(r.some.avg10, SOME_WARN_PCT, SOME_CRIT_PCT) {
        out.push(finding(
            format!("{name}-pressure"),
            sev,
            format!("{scope} {name}: tasks stalled on {name} {:.1}% of the last 10s (60s avg {:.1}%)", r.some.avg10, r.some.avg60),
        ));
    }
    // CPU "full" is not meaningful for the host; only memory and I/O.
    if let Some(full) = r.full.filter(|_| name != "cpu")
        && let Some(sev) = by_severity(full.avg10, FULL_WARN_PCT, FULL_CRIT_PCT)
    {
        out.push(finding(
            format!("{name}-stall"),
            sev,
            format!("{scope} {name}: ALL non-idle tasks stalled on {name} {:.1}% of the last 10s — it is the bottleneck", full.avg10),
        ));
    }
    out
}

fn by_severity(v: f64, warn: f64, crit: f64) -> Option<Severity> {
    if v >= crit {
        Some(Severity::Critical)
    } else if v >= warn {
        Some(Severity::Warning)
    } else {
        None
    }
}

/// Go's `AssessCgroup`: findings from cgroup accounting.
pub fn assess_cgroup(cg: &Cgroup) -> Vec<Finding> {
    let mut out = Vec::new();
    let scope = format!("cgroup {}", cg.path);
    if let Some(m) = &cg.memory {
        out.extend(assess_memory(&cg.path, m));
        if let Some(p) = &m.pressure {
            out.extend(assess_resource(&scope, "memory", p));
        }
    }
    if let Some(c) = &cg.cpu {
        if c.nr_throttled > 0 && c.nr_periods > 0 {
            let pct = 100.0 * c.nr_throttled as f64 / c.nr_periods as f64;
            let sev = if pct >= 25.0 { Severity::Warning } else { Severity::Info };
            out.push(finding(
                "cpu-throttled",
                sev,
                format!(
                    "cgroup {} was CPU-throttled in {} of {} periods ({pct:.0}%, {:.1}s total) by its cpu.max quota",
                    cg.path,
                    c.nr_throttled,
                    c.nr_periods,
                    c.throttled_usec as f64 / 1e6
                ),
            ));
        }
        if let Some(p) = &c.pressure {
            out.extend(assess_resource(&scope, "cpu", p));
        }
    }
    if let Some(p) = &cg.pids
        && p.max > 0
        && 100.0 * p.current as f64 / p.max as f64 >= NEAR_LIMIT_PCT
    {
        out.push(finding(
            "pids-near-limit",
            Severity::Critical,
            format!("cgroup {} has {} of {} allowed tasks; fork/clone will start failing with EAGAIN", cg.path, p.current, p.max),
        ));
    }
    if let Some(p) = &cg.io_pressure {
        out.extend(assess_resource(&scope, "io", p));
    }
    out
}

fn assess_memory(cg_path: &str, m: &Memory) -> Vec<Finding> {
    let mut out = Vec::new();
    if m.events.oom_kill > 0 {
        out.push(finding(
            "cgroup-oom-kill",
            Severity::Critical,
            format!(
                "cgroup {cg_path}: kernel OOM-killed {} process(es) for exceeding a memory limit (memory.events oom_kill)",
                m.events.oom_kill
            ),
        ));
    }
    if m.events.max > 0 && m.events.oom_kill == 0 {
        out.push(finding(
            "cgroup-memory-max-hit",
            Severity::Warning,
            format!(
                "cgroup {cg_path} hit memory.max {} time(s); allocations were reclaimed or failed but nothing was killed yet",
                m.events.max
            ),
        ));
    }
    if m.events.high > 0 {
        out.push(finding(
            "cgroup-memory-throttled",
            Severity::Warning,
            format!("cgroup {cg_path} exceeded memory.high {} time(s); its tasks were throttled into direct reclaim", m.events.high),
        ));
    }
    if m.effective_max > 0 {
        // memory.current includes reclaimable page cache, which sits near the limit on any
        // I/O-heavy workload without being a problem; anonymous memory is what actually leads
        // to an OOM kill.
        let used = if m.anon > 0 { m.anon } else { m.current };
        let pct = 100.0 * used as f64 / m.effective_max as f64;
        if pct >= NEAR_LIMIT_PCT {
            let mut limit_from = String::new();
            if !m.limited_by.is_empty() && m.limited_by != cg_path {
                limit_from = format!(" (limit set on {})", m.limited_by);
            }
            out.push(finding(
                "cgroup-memory-near-limit",
                Severity::Critical,
                format!("cgroup {cg_path} has anonymous memory at {pct:.0}% of its {} MiB memory limit{limit_from}", m.effective_max >> 20),
            ));
        }
    }
    out
}
