//! Findings: what's wrong with a JVM, ranked, each with its evidence and a fix. The shape follows
//! `spectra rules -json` (rule id, severity, message, fix, subject) so Spectra's own rule output
//! can replace these once it has a JVM-scoped, fast path.

use crate::data::{Jvm, Sample, flag_size_kib, fmt_kib};

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Severity {
    Critical,
    Warning,
    Info,
    Healthy,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Chart {
    HeapUsed,
    OldGen,
    None,
}

#[derive(Clone, Debug)]
pub struct Finding {
    pub rule: &'static str,
    pub pid: u32,
    pub subject: String,
    pub severity: Severity,
    pub title: String,
    pub detail: String,
    pub fix: String,
    pub chart: Chart,
}

impl Finding {
    pub fn id(&self) -> String {
        format!("{}/{}", self.pid, self.rule)
    }
}

/// Every finding for one JVM, given its history oldest first (the last entry is now).
pub fn diagnose(jvm: &Jvm, history: &[Sample]) -> Vec<Finding> {
    let mut out = Vec::new();
    let added = std::cell::Cell::new(0);
    let mut add = |rule, severity, title: String, detail: String, fix: &str, chart| {
        added.set(added.get() + 1);
        out.push(Finding { rule, pid: jvm.pid, subject: jvm.name(), severity, title, detail, fix: fix.into(), chart });
    };
    let Some(gc) = &jvm.gc else {
        add(
            "jvm-unreadable",
            Severity::Info,
            "No GC data".into(),
            "Spectra found this JVM but couldn't read its GC counters.".into(),
            "Run the app as the user that owns the JVM.",
            Chart::None,
        );
        return out;
    };

    let (used, max) = (jvm.heap_used_kib(), jvm.heap_max_kib());
    if max > 0.0 {
        let pct = used / max * 100.0;
        if pct >= 75.0 {
            let severity = if pct >= 90.0 { Severity::Critical } else { Severity::Warning };
            add(
                "jvm-heap-pressure",
                severity,
                format!("Heap {pct:.0}% full"),
                format!("{} used of {}.", fmt_kib(used), fmt_kib(max)),
                "Raise -Xmx if the workload needs it, or take a heap dump and look for what's retained.",
                Chart::HeapUsed,
            );
        }
    }

    if let Some(growth) = old_gen_growth(history) {
        add(
            "jvm-old-gen-growth",
            Severity::Warning,
            format!("Old generation grew {growth:.0}% and never dropped"),
            format!("It rose in every one of the last {} readings, which is how a leak looks.", history.len().min(WINDOW)),
            "Compare two heap histograms a few minutes apart: spectra jvm heap-histogram compare.",
            Chart::OldGen,
        );
    }

    if gc.fgc > 0 {
        let avg = gc.fgct / gc.fgc as f64;
        if avg >= 0.5 || gc.fgc >= 10 {
            add(
                "jvm-full-gc",
                Severity::Warning,
                format!("{} full GCs, {avg:.1} s each", gc.fgc),
                format!("Full collections have paused this JVM for {:.1} s in total.", gc.fgct),
                "Check the GC log for what triggers them: spectra jvm gc-log.",
                Chart::HeapUsed,
            );
        }
    }

    if let Some(cap) = flag_size_kib(&jvm.vm_args, "-XX:MaxMetaspaceSize") {
        let pct = gc.mu / cap * 100.0;
        if pct >= 90.0 {
            add(
                "jvm-metaspace-pressure",
                Severity::Warning,
                format!("Metaspace {pct:.0}% of its ceiling"),
                format!("{} used of {}.", fmt_kib(gc.mu), fmt_kib(cap)),
                "Raise -XX:MaxMetaspaceSize, or look for a classloader leak.",
                Chart::None,
            );
        }
    }

    if jvm.vm_args.contains("-Xmx") && !jvm.vm_args.contains("+HeapDumpOnOutOfMemoryError") {
        add(
            "jvm-oom-dump-disabled",
            Severity::Info,
            "No heap dump on out-of-memory".into(),
            "This JVM has a tuned heap but won't leave a dump if it runs out.".into(),
            "Add -XX:+HeapDumpOnOutOfMemoryError.",
            Chart::None,
        );
    }

    if added.get() == 0 {
        add(
            "jvm-healthy",
            Severity::Healthy,
            "Healthy".into(),
            format!("{} of {} heap, {} threads.", fmt_kib(used), fmt_kib(max), jvm.thread_count),
            "",
            Chart::HeapUsed,
        );
    }
    out
}

const WINDOW: usize = 6;

/// Percent growth when the old generation rose in each of the last `WINDOW` readings by 20% or more.
fn old_gen_growth(history: &[Sample]) -> Option<f64> {
    if history.len() < WINDOW {
        return None;
    }
    let tail = &history[history.len() - WINDOW..];
    let rising = tail.windows(2).all(|w| w[1].old_used_kib > w[0].old_used_kib);
    let first = tail[0].old_used_kib;
    let growth = (tail[WINDOW - 1].old_used_kib - first) / first.max(1.0) * 100.0;
    (rising && first > 0.0 && growth >= 20.0).then_some(growth)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data::Gc;

    fn jvm(args: &str, gc: Gc) -> Jvm {
        Jvm { pid: 1, main_class: "a.App".into(), vm_args: args.into(), gc: Some(gc), ..Jvm::default() }
    }

    fn rules(f: &[Finding]) -> Vec<&str> {
        f.iter().map(|f| f.rule).collect()
    }

    #[test]
    fn a_quiet_jvm_is_healthy() {
        let f = diagnose(&jvm("-Xmx1g -XX:+HeapDumpOnOutOfMemoryError", Gc { ou: 1024.0, ..Gc::default() }), &[]);
        assert_eq!(rules(&f), ["jvm-healthy"]);
        assert_eq!(f[0].severity, Severity::Healthy);
    }

    #[test]
    fn a_full_heap_is_critical_and_a_filling_one_warns() {
        let args = "-Xmx1g -XX:+HeapDumpOnOutOfMemoryError";
        let full = diagnose(&jvm(args, Gc { ou: 0.95 * 1024.0 * 1024.0, ..Gc::default() }), &[]);
        assert_eq!((full[0].rule, full[0].severity), ("jvm-heap-pressure", Severity::Critical));
        let filling = diagnose(&jvm(args, Gc { ou: 0.8 * 1024.0 * 1024.0, ..Gc::default() }), &[]);
        assert_eq!(filling[0].severity, Severity::Warning);
    }

    #[test]
    fn steady_old_gen_growth_reads_as_a_leak() {
        let grow: Vec<Sample> = (0..6).map(|i| Sample { old_used_kib: 1000.0 + 100.0 * i as f64, ..Sample::default() }).collect();
        assert_eq!(old_gen_growth(&grow), Some(50.0));
        let mut sawtooth = grow.clone();
        sawtooth[3].old_used_kib = 900.0;
        assert_eq!(old_gen_growth(&sawtooth), None);
        assert_eq!(old_gen_growth(&grow[..5]), None);
    }

    #[test]
    fn flags_slow_full_gcs_metaspace_and_missing_oom_dump() {
        let gc = Gc { fgc: 4, fgct: 3.0, mu: 250.0 * 1024.0, ..Gc::default() };
        let f = diagnose(&jvm("-Xmx4g -XX:MaxMetaspaceSize=256m", gc), &[]);
        assert_eq!(rules(&f), ["jvm-full-gc", "jvm-metaspace-pressure", "jvm-oom-dump-disabled"]);
    }
}
