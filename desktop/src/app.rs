//! App state: the feed's latest JVMs, each JVM's recent readings, the pinned baseline and what's
//! on screen.

use std::collections::{HashMap, VecDeque};
use std::sync::mpsc::{Receiver, Sender, channel};

use crate::data::{self, Jvm, Sample};
use crate::findings::{Finding, Severity, diagnose};
use rune_kit::shell::PaneState;

pub const HISTORY: usize = 120;

pub type Feed = Result<Vec<Jvm>, String>;

/// A reading pinned by "Pin baseline", and which reading it was, so the timeline can mark it.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Baseline {
    pub sample: Sample,
    /// How many readings of this JVM had landed when it was pinned (the pinned one included).
    pub reading: u64,
}

pub struct App {
    pub sidebar: PaneState,
    pub jvms: Vec<Jvm>,
    pub history: HashMap<u32, VecDeque<Sample>>,
    /// Readings landed per JVM since it appeared, including those rolled out of `history`.
    pub readings: HashMap<u32, u64>,
    /// The readings the evidence compares against.
    pub baseline: HashMap<u32, Baseline>,
    /// `None` shows every JVM's findings; `Some` shows that JVM's detail page.
    pub selected: Option<u32>,
    pub expanded: Option<String>,
    pub args_open: bool,
    pub loaded: bool,
    pub error: Option<String>,
    pub feed: Option<Receiver<Feed>>,
    pub feed_tx: Option<Sender<Feed>>,
}

pub fn init() -> App {
    let (tx, rx) = channel();
    App {
        sidebar: PaneState::new(240.0).range(160.0, 420.0),
        jvms: Vec::new(),
        history: HashMap::new(),
        readings: HashMap::new(),
        baseline: HashMap::new(),
        selected: None,
        expanded: None,
        args_open: false,
        loaded: false,
        error: None,
        feed: Some(rx),
        feed_tx: Some(tx),
    }
}

impl App {
    pub fn land(&mut self, feed: Feed) {
        self.loaded = true;
        match feed {
            Ok(jvms) => {
                self.error = None;
                let live = |pid: &u32| jvms.iter().any(|j| j.pid == *pid);
                self.history.retain(|pid, _| live(pid));
                self.readings.retain(|pid, _| live(pid));
                self.baseline.retain(|pid, _| live(pid));
                for j in &jvms {
                    let h = self.history.entry(j.pid).or_default();
                    h.push_back(j.sample());
                    if h.len() > HISTORY {
                        h.pop_front();
                    }
                    *self.readings.entry(j.pid).or_default() += 1;
                }
                if self
                    .selected
                    .is_some_and(|pid| jvms.iter().all(|j| j.pid != pid))
                {
                    self.selected = None;
                }
                self.jvms = jvms;
            }
            Err(e) => self.error = Some(e),
        }
    }

    pub fn history(&self, pid: u32) -> Vec<Sample> {
        self.history
            .get(&pid)
            .map(|h| h.iter().copied().collect())
            .unwrap_or_default()
    }

    pub fn jvm(&self, pid: u32) -> Option<&Jvm> {
        self.jvms.iter().find(|j| j.pid == pid)
    }

    /// The selected JVM, when it is still running.
    pub fn selected_jvm(&self) -> Option<&Jvm> {
        self.selected.and_then(|pid| self.jvm(pid))
    }

    /// The findings on screen, worst first.
    pub fn findings(&self) -> Vec<Finding> {
        let mut all: Vec<Finding> = self
            .jvms
            .iter()
            .filter(|j| self.selected.is_none_or(|pid| pid == j.pid))
            .flat_map(|j| diagnose(j, &self.history(j.pid)))
            .collect();
        all.sort_by_key(|f| (f.severity, f.pid));
        all
    }

    /// One JVM's findings, worst first.
    pub fn findings_for(&self, pid: u32) -> Vec<Finding> {
        let mut all = self
            .jvm(pid)
            .map(|j| diagnose(j, &self.history(pid)))
            .unwrap_or_default();
        all.sort_by_key(|f| f.severity);
        all
    }

    pub fn worst(&self, pid: u32) -> Severity {
        self.findings_for(pid)
            .first()
            .map_or(Severity::Healthy, |f| f.severity)
    }

    /// Where the baseline sits in `history(pid)`, if it hasn't rolled out of it.
    pub fn baseline_index(&self, pid: u32) -> Option<usize> {
        let base = self.baseline.get(&pid)?;
        let len = self.history.get(&pid).map_or(0, VecDeque::len);
        baseline_index(
            self.readings.get(&pid).copied().unwrap_or(0),
            len,
            base.reading,
        )
    }

    /// Pins every JVM's latest reading, or clears the baseline when one is pinned.
    pub fn toggle_baseline(&mut self) {
        if !self.baseline.is_empty() {
            self.baseline.clear();
            return;
        }
        self.baseline = self
            .jvms
            .iter()
            .map(|j| {
                (
                    j.pid,
                    Baseline {
                        sample: j.sample(),
                        reading: self.readings.get(&j.pid).copied().unwrap_or(0),
                    },
                )
            })
            .collect();
    }

    pub fn select(&mut self, pid: Option<u32>) {
        self.selected = pid;
    }
}

/// The index in a history of `len` readings of the reading that was number `pinned` (1-based)
/// when `landed` readings have landed in all. `None` once it has rolled out, or before any landed.
pub fn baseline_index(landed: u64, len: usize, pinned: u64) -> Option<usize> {
    let first = landed.checked_sub(len as u64)?; // readings that rolled out before the window
    let idx = pinned.checked_sub(1)?.checked_sub(first)?;
    (idx < len as u64).then_some(idx as usize)
}

/// Sample JVMs for headless renders: one leaking, one tight on heap, one healthy.
pub fn sample_scene(s: &mut App) {
    // `young` is the leaking JVM's young collections so far: (count, seconds), so its GC and
    // thread lanes have something to show without changing what the findings say.
    let jvm = |pid, main: &str, args: &str, ou: f64, fgc, fgct, young: (u64, f64), threads| Jvm {
        pid,
        main_class: main.into(),
        jdk_vendor: "Temurin".into(),
        jdk_version: "21.0.5".into(),
        vm_args: args.into(),
        thread_count: threads,
        gc: Some(data::Gc {
            ou,
            eu: 80_000.0,
            mu: 60_000.0,
            ygc: 40 + young.0,
            fgc,
            fgct,
            gct: fgct + 0.4 + young.1,
            ..data::Gc::default()
        }),
        classes: Some(data::Classes { loaded: 15_000 }),
    };
    const YOUNG_SECONDS: [f64; 8] = [0.0, 0.05, 0.17, 0.2, 0.4, 0.48, 0.63, 0.73];
    const THREADS: [u64; 8] = [46, 48, 48, 51, 49, 52, 55, 54];
    s.baseline.clear();
    for i in 0..8 {
        let step = i as f64;
        s.land(Ok(vec![
            jvm(
                4101,
                "com.acme.checkout.Server",
                "-Xmx2g -Xms512m -XX:+UseG1GC -Dspring.profiles.active=prod",
                900_000.0 + 110_000.0 * step,
                6,
                4.2,
                (3 * i as u64, YOUNG_SECONDS[i]),
                THREADS[i],
            ),
            jvm(
                4102,
                "org.gradle.launcher.daemon.bootstrap.GradleDaemon",
                "-Xmx4g -XX:+HeapDumpOnOutOfMemoryError",
                3_150_000.0,
                0,
                0.0,
                (0, 0.0),
                48,
            ),
            jvm(
                4103,
                "com.acme.search.Indexer",
                "-Xmx1g -XX:+HeapDumpOnOutOfMemoryError",
                210_000.0 + 15_000.0 * (step % 3.0),
                0,
                0.0,
                (0, 0.0),
                48,
            ),
        ]));
        if i == 2 {
            s.toggle_baseline();
        }
    }
    s.expanded = s.findings().first().map(Finding::id);
}

/// The sample scene with the leaking JVM's detail page open.
pub fn detail_scene(s: &mut App) {
    sample_scene(s);
    s.selected = Some(4101);
    s.expanded = None;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn baseline_index_follows_history_as_it_rolls() {
        // Pinned at reading 3 of 8, nothing rolled out: index 2.
        assert_eq!(baseline_index(8, 8, 3), Some(2));
        // 130 readings, history holds the last 120 (11..=130): reading 15 is at index 4.
        assert_eq!(baseline_index(130, HISTORY, 15), Some(4));
        // Reading 10 has rolled out.
        assert_eq!(baseline_index(130, HISTORY, 10), None);
        assert_eq!(baseline_index(0, 0, 0), None);
    }

    #[test]
    fn baseline_marker_survives_history_rolling_past_its_limit() {
        let mut s = init();
        let jvm = |ou: f64| Jvm {
            pid: 9,
            main_class: "a.B".into(),
            gc: Some(data::Gc {
                ou,
                ..data::Gc::default()
            }),
            ..Jvm::default()
        };
        for i in 0..5 {
            s.land(Ok(vec![jvm(i as f64)]));
        }
        s.toggle_baseline();
        assert_eq!(s.baseline_index(9), Some(4));
        for i in 5..(HISTORY + 2) {
            s.land(Ok(vec![jvm(i as f64)]));
        }
        // 122 readings landed, history holds 3..=122; the pinned reading 5 is at index 2.
        assert_eq!(s.baseline_index(9), Some(2));
        assert_eq!(
            s.history(9)[2].old_used_kib,
            s.baseline[&9].sample.old_used_kib
        );
        for i in 0..3 {
            s.land(Ok(vec![jvm(i as f64)]));
        }
        assert_eq!(s.baseline_index(9), None);
    }
}
