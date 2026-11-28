//! Findings about the whole machine, from the System page's readings: memory pressure, swap that
//! keeps growing, thermal throttling, and a volume close to full. Each opens the System page.

use crate::findings::{Chart, Finding, Link, Severity};
use crate::procs::fmt_mem;
use crate::system::{Pressure, System, ThermalState, swap_growth};

/// How full a volume may get before it's worth a look, and before it's urgent, in percent.
pub use crate::system::VOLUME_WARN;
pub const VOLUME_CRITICAL: f64 = 97.0;

fn finding(
    rule: &'static str,
    subject: &str,
    severity: Severity,
    title: String,
    detail: String,
    fix: &str,
) -> Finding {
    Finding {
        rule,
        pid: 0,
        subject: subject.to_owned(),
        severity,
        title,
        detail,
        fix: fix.into(),
        chart: Chart::None,
        link: Link::System,
    }
}

fn mib(bytes: f64) -> String {
    fmt_mem(bytes / 1024.0)
}

/// Every machine-wide finding, from the latest good readings at window time `now`. A part whose
/// last reading failed, or is older than three rounds, says nothing: it can't be trusted.
pub fn diagnose(s: &System, now: f64) -> Vec<Finding> {
    let mut out = Vec::new();
    if let Some(m) = s.memory.last.as_ref().filter(|_| !s.memory.stale(now)) {
        let free = m.free_pct.map_or_else(String::new, |p| {
            format!(" {p:.0}% of memory is free for new work.")
        });
        let held = s.held_pressure;
        let severity = match held {
            Some(Pressure::Critical) => Some(Severity::Critical),
            Some(_) => Some(Severity::Warning),
            None => None,
        };
        if let Some(severity) = severity {
            out.push(finding(
                "sys-memory-pressure",
                "Memory",
                severity,
                format!(
                    "Memory pressure is {}",
                    held.unwrap_or(m.pressure).label().to_lowercase()
                ),
                format!("The system is compressing and swapping to make room.{free}"),
                "Quit what you aren't using; Top consumers on the Machine page lists who holds the most.",
            ));
        }
        if let Some(grown) = swap_growth(&s.mem_history) {
            out.push(finding(
                "sys-swap-growing",
                "Memory",
                Severity::Warning,
                format!("Swap grew {} in the last minute", mib(grown)),
                format!("{} of swap is in use and still rising, so memory is short.", mib(m.swap_used)),
                "Find the process that keeps growing (By memory on the Machine page) and restart or quit it.",
            ));
        }
    }
    let thermal_fresh = if s.thermal.last.is_some() {
        !s.thermal.stale(now)
    } else {
        !s.power.stale(now)
    };
    if thermal_fresh && s.thermal_state() == Some(ThermalState::Throttled) {
        let limit = s.speed_limit().map_or_else(String::new, |l| {
            format!(" The CPU may run at {l:.0}% of its speed.")
        });
        let evidence = s
            .thermal
            .last
            .as_ref()
            .map(|t| t.evidence.clone())
            .unwrap_or_default();
        let detail = if evidence.is_empty() {
            format!("The system is slowing the CPU to cool down.{limit}")
        } else {
            format!("{}.{limit}", evidence.trim_end_matches('.'))
        };
        out.push(finding(
            "sys-thermal-throttled",
            "Thermal",
            Severity::Warning,
            "The CPU is thermally throttled".into(),
            detail,
            "Everything runs slower until it cools. Check what's busy, and give the machine air.",
        ));
    }
    let full: Vec<_> = if s.storage.stale(now) {
        Vec::new()
    } else {
        s.volumes()
            .into_iter()
            .filter(|v| s.full_mounts.contains(&v.mount))
            .collect()
    };
    if let Some(worst) = full
        .iter()
        .max_by(|a, b| a.used_pct().total_cmp(&b.used_pct()))
    {
        let severity = if worst.used_pct() >= VOLUME_CRITICAL {
            Severity::Critical
        } else {
            Severity::Warning
        };
        let title = match full.len() {
            1 => format!("{} is {:.0}% full", worst.label(), worst.used_pct()),
            n => format!("{n} volumes are over {VOLUME_WARN:.0}% full"),
        };
        let detail = full
            .iter()
            .map(|v| {
                format!(
                    "{}: {} free of {}",
                    v.label(),
                    mib(v.avail),
                    mib(v.used + v.avail)
                )
            })
            .collect::<Vec<_>>()
            .join(" · ");
        out.push(finding(
            "sys-volume-full",
            "Disks",
            severity,
            title,
            detail,
            "Free space before it runs out: apps and the system fail to save when a disk is full.",
        ));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::system::{Fail, Memory, Reading, SWAP_WINDOW, Thermal, Volume};

    fn mem(pressure: Pressure, swap_used: f64) -> Memory {
        Memory {
            physical: 8e9,
            pressure,
            free_pct: Some(12.0),
            swap_used,
            ..Memory::default()
        }
    }

    fn vol(mount: &str, used: f64, avail: f64) -> Volume {
        Volume {
            mount: mount.into(),
            device: String::new(),
            fs_type: "apfs".into(),
            read_only: false,
            total: 2e12,
            used,
            avail,
        }
    }

    /// The window time of the newest good reading, so nothing is old yet.
    fn at(s: &System) -> f64 {
        [
            s.memory.updated,
            s.thermal.updated,
            s.storage.updated,
            s.power.updated,
        ]
        .into_iter()
        .flatten()
        .fold(0.0, f64::max)
    }

    fn rules(s: &System) -> Vec<(&'static str, Severity)> {
        diagnose(s, at(s))
            .iter()
            .map(|f| (f.rule, f.severity))
            .collect()
    }

    #[test]
    fn memory_pressure_warns_then_turns_critical() {
        let mut s = System::new();
        s.land(Reading::Memory(Ok(mem(Pressure::Normal, 0.0))), 0.0, 0.0);
        assert!(
            rules(&s).is_empty(),
            "normal pressure is nothing to look at"
        );
        s.land(Reading::Memory(Ok(mem(Pressure::Warning, 0.0))), 5.0, 0.0);
        assert_eq!(rules(&s), [("sys-memory-pressure", Severity::Warning)]);
        s.land(Reading::Memory(Ok(mem(Pressure::Critical, 0.0))), 10.0, 0.0);
        let f = diagnose(&s, at(&s));
        assert_eq!(
            (f[0].severity, f[0].title.as_str()),
            (Severity::Critical, "Memory pressure is critical")
        );
        assert_eq!(
            (f[0].link, f[0].pid),
            (Link::System, 0),
            "it opens the System page"
        );
        assert!(f[0].detail.contains("12% of memory is free"));
        // A failed reading keeps the last good one for the page, but its finding is dropped.
        s.land(Reading::Memory(Err(Fail::Failed("boom".into()))), 15.0, 0.0);
        assert!(diagnose(&s, 15.0).is_empty());
    }

    #[test]
    fn memory_pressure_clears_only_after_three_calm_readings() {
        let mut s = System::new();
        s.land(Reading::Memory(Ok(mem(Pressure::Warning, 0.0))), 0.0, 0.0);
        for (i, calm) in [(1, true), (2, true)] {
            s.land(
                Reading::Memory(Ok(mem(Pressure::Normal, 0.0))),
                i as f64 * 5.0,
                0.0,
            );
            assert!(
                calm && !rules(&s).is_empty(),
                "still showing after {i} calm readings"
            );
        }
        // A relapse starts the count over.
        s.land(Reading::Memory(Ok(mem(Pressure::Warning, 0.0))), 15.0, 0.0);
        for i in 4..6 {
            s.land(
                Reading::Memory(Ok(mem(Pressure::Normal, 0.0))),
                i as f64 * 5.0,
                0.0,
            );
            assert!(!rules(&s).is_empty());
        }
        s.land(Reading::Memory(Ok(mem(Pressure::Normal, 0.0))), 30.0, 0.0);
        assert!(
            rules(&s).is_empty(),
            "the third calm reading in a row clears it"
        );
    }

    #[test]
    fn a_volume_finding_clears_only_below_88_percent() {
        let mut s = System::new();
        let tb = 1e12;
        let put = |s: &mut System, pct: f64, now: f64| {
            s.land(
                Reading::Storage(Ok(vec![vol(
                    "/System/Volumes/Data",
                    pct / 100.0 * tb,
                    (1.0 - pct / 100.0) * tb,
                )])),
                now,
                0.0,
            )
        };
        put(&mut s, 91.0, 0.0);
        assert_eq!(rules(&s).len(), 1);
        put(&mut s, 89.0, 30.0);
        assert_eq!(rules(&s).len(), 1, "89% is inside the margin: still shown");
        put(&mut s, 87.0, 60.0);
        assert!(rules(&s).is_empty(), "below 88% clears it");
        put(&mut s, 89.0, 90.0);
        assert!(rules(&s).is_empty(), "89% alone doesn't bring it back");
    }

    #[test]
    fn findings_from_old_or_failed_readings_are_dropped() {
        let mut s = System::new();
        let tb = 1e12;
        s.land(Reading::Memory(Ok(mem(Pressure::Critical, 0.0))), 0.0, 0.0);
        s.land(
            Reading::Thermal(Ok(Thermal {
                state: ThermalState::Throttled,
                evidence: String::new(),
                speed_limit: None,
            })),
            0.0,
            0.0,
        );
        s.land(
            Reading::Storage(Ok(vec![vol("/System/Volumes/Data", 0.95 * tb, 0.05 * tb)])),
            0.0,
            0.0,
        );
        assert_eq!(diagnose(&s, 10.0).len(), 3);
        // Fast readings are old after about 21 s, slow ones after 96 s.
        let late = diagnose(&s, 30.0);
        assert_eq!(late.len(), 1);
        assert_eq!(late[0].rule, "sys-volume-full");
        assert!(diagnose(&s, 200.0).is_empty());
        s.land(
            Reading::Storage(Err(Fail::Failed("df hung".into()))),
            20.0,
            0.0,
        );
        assert!(
            diagnose(&s, 20.0)
                .iter()
                .all(|f| f.rule != "sys-volume-full")
        );
    }

    #[test]
    fn swap_counts_once_in_use_and_growing() {
        let mut s = System::new();
        let mb = 1024.0 * 1024.0;
        for i in 0..SWAP_WINDOW {
            s.land(
                Reading::Memory(Ok(mem(Pressure::Normal, 512.0 * mb))),
                i as f64 * 5.0,
                0.0,
            );
        }
        assert!(rules(&s).is_empty(), "swap in use but steady");
        for i in 0..SWAP_WINDOW {
            s.land(
                Reading::Memory(Ok(mem(Pressure::Normal, (512.0 + 10.0 * i as f64) * mb))),
                100.0 + i as f64 * 5.0,
                0.0,
            );
        }
        let f = diagnose(&s, at(&s));
        assert_eq!(f.len(), 1);
        assert_eq!(f[0].rule, "sys-swap-growing");
        assert_eq!(f[0].title, "Swap grew 120 MB in the last minute");
    }

    #[test]
    fn throttling_counts_and_warm_does_not() {
        let mut s = System::new();
        let t = |state, speed_limit| Thermal {
            state,
            evidence: "CPU speed limit 62%".into(),
            speed_limit,
        };
        s.land(Reading::Thermal(Ok(t(ThermalState::Warm, None))), 0.0, 0.0);
        assert!(
            rules(&s).is_empty(),
            "warm is shown on the page, not a finding"
        );
        s.land(
            Reading::Thermal(Ok(t(ThermalState::Throttled, Some(62.0)))),
            5.0,
            0.0,
        );
        let f = diagnose(&s, at(&s));
        assert_eq!(f[0].rule, "sys-thermal-throttled");
        assert_eq!(
            f[0].detail,
            "CPU speed limit 62%. The CPU may run at 62% of its speed."
        );
    }

    #[test]
    fn a_volume_over_90_percent_full_counts_and_one_almost_full_is_critical() {
        let mut s = System::new();
        let tb = 1e12;
        s.land(
            Reading::Storage(Ok(vec![vol("/System/Volumes/Data", 0.89 * tb, 0.11 * tb)])),
            0.0,
            0.0,
        );
        assert!(rules(&s).is_empty(), "89% is fine");
        s.land(
            Reading::Storage(Ok(vec![vol("/System/Volumes/Data", 0.92 * tb, 0.08 * tb)])),
            30.0,
            0.0,
        );
        let f = diagnose(&s, at(&s));
        assert_eq!(
            (f[0].rule, f[0].severity),
            ("sys-volume-full", Severity::Warning)
        );
        assert_eq!(f[0].title, "Startup disk is 92% full");
        s.land(
            Reading::Storage(Ok(vec![
                vol("/System/Volumes/Data", 0.92 * tb, 0.08 * tb),
                vol("/Volumes/Backup", 0.98 * tb, 0.02 * tb),
                // A simulator image is full by design and read-only: never counted.
                Volume {
                    read_only: true,
                    ..vol("/Library/Developer/CoreSimulator/Volumes/iOS", tb, 0.0)
                },
            ])),
            60.0,
            0.0,
        );
        let f = diagnose(&s, at(&s));
        assert_eq!(
            f.len(),
            1,
            "one finding for every full volume, so ids stay unique"
        );
        assert_eq!(
            (f[0].severity, f[0].title.as_str()),
            (Severity::Critical, "2 volumes are over 90% full")
        );
        assert!(f[0].detail.contains("Startup disk") && f[0].detail.contains("Backup"));
    }
}
