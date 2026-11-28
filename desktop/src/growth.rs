//! "Memory and alerts": processes whose memory jumped past its usual level, from
//! `spectra anomalies --json`, and the alert history Spectra's daemon keeps, from `alerts.list`.
//! Acknowledging an alert (`alerts.ack`) is a write, and happens only when the person presses
//! its button.
//!
//! Spectra's anomaly check compares each process's latest memory with a rolling baseline, so it
//! finds sudden jumps, not slow creep; the page says so and never calls a row "growing".
//!
//! A look runs off the UI thread when the page opens, then at most every [`EVERY`] seconds while
//! the page is on screen and the window is visible, and never while another page is. Every
//! Spectra call goes through [`crate::inspect::Spectra`] and so through the daemon seam in
//! [`crate::daemon`], which answers the alert calls from the daemon.

use std::collections::HashSet;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::Deserialize;

use crate::app::{App, fmt_ago};
use crate::bundle::nullable;
use crate::findings::Severity;
use crate::groups::Nav;
use crate::inspect::{self, Failure, Spectra};
use crate::procs::parse_rfc3339;

/// How long one call may take.
pub const DEADLINE: Duration = Duration::from_secs(20);
/// Seconds to rest after a look lands before the next, while the page is on screen.
pub const EVERY: f64 = 30.0;
/// The most alerts asked for (Spectra allows up to 2000).
pub const ALERTS: usize = 100;
/// The most jumps listed.
pub const MAX_JUMPS: usize = 20;
/// A jump smaller than this is noise on a quiet process, whatever its z-score says.
const MIN_JUMP_KIB: f64 = 1024.0;
/// What `spectra anomalies` says on stderr before it has stored any process history.
const NO_HISTORY: &str = "no process metrics stored";

/// One finding of `spectra anomalies --json`. Memory is in KiB. Every field may be missing.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Anomaly {
    /// The pid, as text.
    #[serde(deserialize_with = "nullable")]
    pub key: String,
    /// The latest reading.
    #[serde(deserialize_with = "nullable")]
    pub latest: f64,
    /// The rolling baseline: what it usually is.
    #[serde(deserialize_with = "nullable")]
    pub baseline_mean: f64,
    /// How many standard deviations from the baseline; negative is below it.
    #[serde(deserialize_with = "nullable")]
    pub z: f64,
    /// Baseline points behind it.
    #[serde(deserialize_with = "nullable")]
    pub samples: u64,
}

impl Anomaly {
    pub fn pid(&self) -> Option<u32> {
        self.key.trim().parse().ok().filter(|p| *p > 0)
    }
}

/// One alert of Spectra's daemon. Every field may be missing or `null`.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Alert {
    #[serde(deserialize_with = "nullable")]
    pub id: String,
    #[serde(deserialize_with = "nullable")]
    pub key: String,
    /// `critical`, `warn` or `info`.
    #[serde(deserialize_with = "nullable")]
    pub severity: String,
    #[serde(deserialize_with = "nullable")]
    pub title: String,
    #[serde(deserialize_with = "nullable")]
    pub detail: String,
    /// `firing` or `resolved`.
    #[serde(deserialize_with = "nullable")]
    pub state: String,
    #[serde(deserialize_with = "nullable")]
    pub fired_at: String,
    pub resolved_at: Option<String>,
    pub acked_at: Option<String>,
}

impl Alert {
    /// Still going: it hasn't cleared.
    pub fn active(&self) -> bool {
        self.state != "resolved" && self.resolved_at.as_deref().is_none_or(str::is_empty)
    }

    pub fn acknowledged(&self) -> bool {
        self.acked_at.as_deref().is_some_and(|a| !a.is_empty())
    }

    pub fn severity(&self) -> Severity {
        match self.severity.as_str() {
            "critical" | "high" => Severity::Critical,
            "warn" | "warning" | "medium" => Severity::Warning,
            _ => Severity::Info,
        }
    }

    /// When it fired, in Unix seconds.
    pub fn fired(&self) -> Option<i64> {
        parse_rfc3339(&self.fired_at)
    }
}

/// `spectra anomalies --json`'s findings. Spectra writes `null` for none.
pub fn parse_anomalies(out: &str) -> Result<Vec<Anomaly>, String> {
    serde_json::from_str::<Option<Vec<Anomaly>>>(out)
        .map(Option::unwrap_or_default)
        .map_err(|e| format!("Spectra's answer didn't read as expected ({e})."))
}

/// The daemon's alert list, newest first whatever order it came in.
pub fn parse_alerts(out: &str) -> Result<Vec<Alert>, String> {
    let mut alerts = serde_json::from_str::<Option<Vec<Alert>>>(out)
        .map(Option::unwrap_or_default)
        .map_err(|e| format!("Spectra's answer didn't read as expected ({e})."))?;
    // Stable, so alerts with no readable time keep the daemon's order, after the dated ones.
    alerts.sort_by_key(|a| std::cmp::Reverse(a.fired()));
    Ok(alerts)
}

/// An acknowledged alert as the daemon answers it.
pub fn parse_alert(out: &str) -> Result<Alert, String> {
    serde_json::from_str(out)
        .map_err(|e| format!("Spectra's answer didn't read as expected ({e})."))
}

pub fn anomalies_args() -> Vec<String> {
    vec!["anomalies".into(), "--json".into()]
}

pub fn list_args() -> Vec<String> {
    ["alerts", "--json", "--state", "all", "--limit"]
        .into_iter()
        .map(str::to_owned)
        .chain([ALERTS.to_string()])
        .collect()
}

pub fn ack_args(id: &str) -> Vec<String> {
    vec!["alerts".into(), "ack".into(), id.into(), "--json".into()]
}

/// A plain phrase for how far past its usual a process is.
pub fn how_far(z: f64) -> &'static str {
    if z < 6.0 {
        "above its usual"
    } else if z < 30.0 {
        "well above its usual"
    } else {
        "far above its usual"
    }
}

/// A row of "Memory jumps".
#[derive(Clone, Debug, PartialEq)]
pub struct Jump {
    pub pid: u32,
    pub name: String,
    pub now_kib: f64,
    pub usual_kib: f64,
    pub z: f64,
}

/// Why the memory check has no rows.
#[derive(Clone, Debug, PartialEq)]
pub enum Note {
    /// Spectra hasn't stored enough process history to have a baseline.
    NotEnough,
    Failed(String),
}

/// What a call came back with, for a look.
pub enum Part {
    Jumps(Result<Vec<Anomaly>, Note>),
    Alerts(Result<Vec<Alert>, String>),
}

pub enum Landed {
    Look {
        job: u64,
        part: Part,
    },
    Ack {
        id: String,
        result: Result<Alert, String>,
    },
}

pub type Work = Box<dyn FnOnce() -> Landed + Send>;

/// How an acknowledgement went.
#[derive(Clone, Debug, PartialEq)]
pub enum AckStatus {
    Sending,
    Done,
    Failed(String),
}

#[derive(Clone, Debug, PartialEq)]
pub struct AckOutcome {
    pub title: String,
    pub status: AckStatus,
}

impl AckOutcome {
    pub fn line(&self) -> String {
        match &self.status {
            AckStatus::Sending => format!("Acknowledging “{}”…", self.title),
            AckStatus::Done => format!(
                "Acknowledged “{}”. Spectra's daemon has it recorded.",
                self.title
            ),
            AckStatus::Failed(why) => format!("Couldn't acknowledge “{}”: {why}", self.title),
        }
    }
}

/// A look in flight.
struct Flight {
    job: u64,
    cancel: Arc<AtomicBool>,
    /// Calls still to come back.
    left: u8,
}

pub struct Growth {
    /// The last memory check that worked, and the window clock when it landed.
    pub jumps: Option<(Vec<Anomaly>, f64)>,
    /// Why the memory check has nothing to show, or didn't work the last time.
    pub jumps_note: Option<Note>,
    /// The last alert history that read, and the window clock when it landed.
    pub alerts: Option<(Vec<Alert>, f64)>,
    /// Why the last read of the alerts failed.
    pub alerts_error: Option<String>,
    flight: Option<Flight>,
    /// The window clock when the next look may start.
    next: f64,
    job: u64,
    pub spectra: Arc<dyn Spectra>,
    /// Tests hold the work here and run it themselves; `None` runs it on threads.
    pub held: Option<Arc<Mutex<Vec<Work>>>>,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
    /// Whether looks start on their own. Off until the window opens, so tests and sample scenes
    /// never run Spectra.
    pub auto: bool,
    /// The age line as last drawn, to redraw when it changes.
    shown: Option<String>,
    /// The alert being acknowledged, and how the last acknowledgement went.
    pub acking: Option<String>,
    pub outcome: Option<AckOutcome>,
    /// Alerts acknowledged this run: a list read before the press but landing after it must not
    /// bring the button back.
    acked: HashSet<String>,
}

impl Growth {
    pub fn new() -> Growth {
        let (tx, rx) = channel();
        Growth {
            jumps: None,
            jumps_note: None,
            alerts: None,
            alerts_error: None,
            flight: None,
            next: 0.0,
            job: 0,
            spectra: Arc::new(inspect::System),
            held: None,
            tx,
            rx,
            auto: false,
            shown: None,
            acking: None,
            outcome: None,
            acked: HashSet::new(),
        }
    }

    pub fn running(&self) -> bool {
        self.flight.is_some()
    }

    /// Stops the look in flight; what it would have said is dropped.
    pub fn cancel(&mut self) {
        if let Some(f) = self.flight.take() {
            f.cancel.store(true, Ordering::Relaxed);
        }
    }

    /// Whether any reading has landed or failed yet.
    #[cfg(test)]
    pub fn started(&self) -> bool {
        self.jumps.is_some()
            || self.jumps_note.is_some()
            || self.alerts.is_some()
            || self.alerts_error.is_some()
            || self.running()
    }
}

impl Default for Growth {
    fn default() -> Self {
        Growth::new()
    }
}

/// What a call says when it panicked: a failure the page shows, so it never waits forever.
const INTERNAL: &str = "Spectra Desktop hit an internal error reading it.";

/// `work`, with `failed` standing in for its answer if it panics.
fn guarded(work: impl FnOnce() -> Landed + Send + 'static, failed: Landed) -> Work {
    Box::new(move || std::panic::catch_unwind(std::panic::AssertUnwindSafe(work)).unwrap_or(failed))
}

fn check(spectra: &dyn Spectra, cancel: &AtomicBool) -> Result<Vec<Anomaly>, Note> {
    match spectra.run(&anomalies_args(), DEADLINE, cancel) {
        Ok(out) => parse_anomalies(&out).map_err(Note::Failed),
        Err(Failure::Cancelled) => Err(Note::Failed("Stopped.".into())),
        Err(Failure::Failed(why)) if why.contains(NO_HISTORY) => Err(Note::NotEnough),
        Err(Failure::Failed(why)) => Err(Note::Failed(why)),
    }
}

fn history(spectra: &dyn Spectra, cancel: &AtomicBool) -> Result<Vec<Alert>, String> {
    match spectra.run(&list_args(), DEADLINE, cancel) {
        Ok(out) => parse_alerts(&out),
        Err(Failure::Cancelled) => Err("Stopped.".into()),
        Err(Failure::Failed(why)) => Err(why),
    }
}

impl App {
    /// Whether the page is what's on screen.
    pub fn growth_visible(&self) -> bool {
        self.nav == Nav::Growth && !self.on_page()
    }

    /// The jumps to list: above the usual, still running, and not trivial, biggest first.
    pub fn jump_rows(&self) -> Vec<Jump> {
        let Some((found, _)) = &self.growth.jumps else {
            return Vec::new();
        };
        let mut rows: Vec<Jump> = found
            .iter()
            .filter(|a| a.z.is_finite() && a.z > 0.0 && a.latest - a.baseline_mean >= MIN_JUMP_KIB)
            .filter_map(|a| {
                let pid = a.pid()?;
                let p = self.process(pid)?;
                Some(Jump {
                    pid,
                    name: p.name().into_owned(),
                    now_kib: a.latest,
                    usual_kib: a.baseline_mean,
                    z: a.z,
                })
            })
            .collect();
        rows.sort_by(|a, b| b.z.total_cmp(&a.z));
        rows
    }

    /// Alert detail in plain words.
    pub fn alert_detail(&self, a: &Alert) -> String {
        crate::whatswrong::plain(&a.detail, (self.wall)())
    }

    /// How old the newest reading is, in a line, with what's happening since.
    pub fn growth_age(&self) -> Option<String> {
        let g = &self.growth;
        let at = [
            g.jumps.as_ref().map(|j| j.1),
            g.alerts.as_ref().map(|a| a.1),
        ]
        .into_iter()
        .flatten()
        .reduce(f64::max)?;
        let age = self.now - at;
        let mut line = if age < 2.0 {
            "Checked just now".to_owned()
        } else {
            format!("Checked {} ago", fmt_ago(age))
        };
        if g.running() {
            line.push_str(" · checking again");
        }
        Some(line)
    }

    /// "Check now": the next tick looks at once.
    pub fn growth_refresh(&mut self) {
        self.growth.next = 0.0;
    }

    /// Opens the page: the next tick looks at once.
    pub fn open_growth(&mut self) {
        self.growth.next = 0.0;
        // An acknowledgement still out keeps its line, so its answer has somewhere to land.
        if self.growth.acking.is_none() {
            self.growth.outcome = None;
        }
    }

    /// Lands finished calls, stops a look in flight once the page is off screen, and starts the
    /// next when the page is on screen, the window is visible and it's due. True when anything
    /// shown changed.
    pub fn tick_growth(&mut self) -> bool {
        let mut changed = false;
        while let Ok(l) = self.growth.rx.try_recv() {
            changed |= self.land_growth(l);
        }
        let visible = self.growth_visible();
        let shown = self.window_shown();
        // Off the page, or the window hidden: nothing keeps running.
        if !(visible && shown) && self.growth.running() {
            self.growth.cancel();
            changed = true;
        }
        let g = &self.growth;
        if shown && visible && g.auto && !g.running() && self.now >= g.next {
            self.start_growth();
            changed = true;
        }
        if visible && shown {
            let age = self.growth_age();
            if age != self.growth.shown {
                self.growth.shown = age;
                changed = true;
            }
        } else {
            self.growth.shown = None;
        }
        changed
    }

    /// Starts a look now: the memory check and the alert history, side by side.
    pub fn start_growth(&mut self) {
        let g = &mut self.growth;
        g.job += 1;
        let job = g.job;
        let cancel = Arc::new(AtomicBool::new(false));
        g.flight = Some(Flight {
            job,
            cancel: Arc::clone(&cancel),
            left: 2,
        });
        let jumps: Work = {
            let (spectra, cancel) = (Arc::clone(&g.spectra), Arc::clone(&cancel));
            guarded(
                move || Landed::Look {
                    job,
                    part: Part::Jumps(check(&*spectra, &cancel)),
                },
                Landed::Look {
                    job,
                    part: Part::Jumps(Err(Note::Failed(INTERNAL.into()))),
                },
            )
        };
        let alerts: Work = {
            let (spectra, cancel) = (Arc::clone(&g.spectra), cancel);
            guarded(
                move || Landed::Look {
                    job,
                    part: Part::Alerts(history(&*spectra, &cancel)),
                },
                Landed::Look {
                    job,
                    part: Part::Alerts(Err(INTERNAL.into())),
                },
            )
        };
        self.run_growth(vec![jumps, alerts], "spectra-growth");
    }

    /// Acknowledges an alert. Only ever called by its button.
    pub fn ack_alert(&mut self, id: &str) {
        if self.growth.acking.is_some() {
            return;
        }
        let Some(alert) = self
            .growth
            .alerts
            .as_ref()
            .and_then(|(l, _)| l.iter().find(|a| a.id == id))
        else {
            return;
        };
        let title = alert.title.clone();
        let g = &mut self.growth;
        g.acking = Some(id.to_owned());
        g.outcome = Some(AckOutcome {
            title,
            status: AckStatus::Sending,
        });
        let (spectra, id) = (Arc::clone(&g.spectra), id.to_owned());
        let failed = Landed::Ack {
            id: id.clone(),
            result: Err(INTERNAL.into()),
        };
        let work = guarded(
            move || {
                let result = match spectra.run(&ack_args(&id), DEADLINE, &AtomicBool::new(false)) {
                    Ok(out) => parse_alert(&out),
                    Err(Failure::Cancelled) => Err("Stopped.".into()),
                    Err(Failure::Failed(why)) => Err(why),
                };
                Landed::Ack { id, result }
            },
            failed,
        );
        self.run_growth(vec![work], "spectra-ack");
    }

    /// Runs `work` on a thread each, or holds it for a test.
    fn run_growth(&mut self, work: Vec<Work>, name: &str) {
        if let Some(q) = &self.growth.held {
            q.lock().expect("the held work").extend(work);
            return;
        }
        for w in work {
            let (tx, wake) = (self.growth.tx.clone(), self.wake.clone());
            let spawned = std::thread::Builder::new().name(name.into()).spawn({
                let tx = tx.clone();
                move || {
                    let _ = tx.send(w());
                    if let Some(wake) = wake {
                        wake();
                    }
                }
            });
            if spawned.is_err() {
                self.growth.flight = None;
                self.growth.alerts_error = Some("Couldn't start a thread to ask Spectra.".into());
            }
        }
    }

    /// Lands a call, if it's for the look in flight or the acknowledgement. True when it was.
    pub fn land_growth(&mut self, l: Landed) -> bool {
        match l {
            Landed::Look { job, part } => self.land_look(job, part),
            Landed::Ack { id, result } => self.land_ack(&id, result),
        }
    }

    fn land_look(&mut self, job: u64, part: Part) -> bool {
        let now = self.now;
        let g = &mut self.growth;
        let Some(f) = g.flight.as_mut().filter(|f| f.job == job) else {
            return false;
        };
        f.left = f.left.saturating_sub(1);
        if f.left == 0 {
            g.flight = None;
            g.next = now + EVERY;
        }
        match part {
            Part::Jumps(Ok(found)) => {
                g.jumps = Some((found, now));
                g.jumps_note = None;
            }
            // No baseline yet: any older rows are no use.
            Part::Jumps(Err(Note::NotEnough)) => {
                g.jumps = None;
                g.jumps_note = Some(Note::NotEnough);
            }
            Part::Jumps(Err(note)) => g.jumps_note = Some(note),
            Part::Alerts(Ok(mut list)) => {
                for a in list.iter_mut().filter(|a| g.acked.contains(&a.id)) {
                    a.acked_at.get_or_insert_with(|| format!("{now}"));
                }
                g.alerts = Some((list, now));
                g.alerts_error = None;
            }
            Part::Alerts(Err(why)) => g.alerts_error = Some(why),
        }
        true
    }

    fn land_ack(&mut self, id: &str, result: Result<Alert, String>) -> bool {
        let now = self.now;
        let g = &mut self.growth;
        if g.acking.as_deref() != Some(id) {
            return false;
        }
        g.acking = None;
        match result {
            Ok(done) => {
                g.acked.insert(id.to_owned());
                if let Some((list, _)) = g.alerts.as_mut()
                    && let Some(a) = list.iter_mut().find(|a| a.id == id)
                {
                    // The daemon says when; a reply that doesn't still means it was recorded.
                    a.acked_at = done
                        .acked_at
                        .filter(|t| !t.is_empty())
                        .or(Some(format!("{now}")));
                }
                if let Some(o) = g.outcome.as_mut() {
                    o.status = AckStatus::Done;
                }
            }
            Err(why) => {
                if let Some(o) = g.outcome.as_mut() {
                    o.status = AckStatus::Failed(why);
                }
            }
        }
        true
    }

    /// Runs the work a test is holding, on this thread, and lands it.
    #[cfg(test)]
    pub fn run_held_growth(&mut self) {
        let work = match &self.growth.held {
            Some(q) => std::mem::take(&mut *q.lock().unwrap()),
            None => Vec::new(),
        };
        for w in work {
            self.land_growth(w());
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use crate::app::init;
    use crate::groups::tests::proc;
    use crate::inspect::tests::Fake;
    use crate::procs::Process;

    pub const ANOMALIES: &str = include_str!("../tests/fixtures/growth/anomalies.json");
    pub const ALERTS: &str = include_str!("../tests/fixtures/growth/alerts.json");
    pub const ALERTS_ODD: &str = include_str!("../tests/fixtures/growth/alerts-odd.json");
    const ANOMALIES_NULL: &str = include_str!("../tests/fixtures/growth/anomalies-null.json");
    const ANOMALIES_ODD: &str = include_str!("../tests/fixtures/growth/anomalies-odd.json");

    #[test]
    fn the_captured_anomalies_read() {
        let a = parse_anomalies(ANOMALIES).unwrap();
        assert_eq!(a.len(), 5);
        assert_eq!(a[0].pid(), Some(2001));
        assert_eq!(a[0].latest, 2516582.0);
        assert_eq!(a[0].z, 36.7);
        assert_eq!(a[0].samples, 42);
    }

    #[test]
    fn nothing_flagged_and_odd_rows_read_without_failing() {
        assert!(parse_anomalies(ANOMALIES_NULL).unwrap().is_empty());
        assert!(parse_anomalies("[]").unwrap().is_empty());
        let a = parse_anomalies(ANOMALIES_ODD).unwrap();
        assert_eq!(a.len(), 3);
        assert_eq!((a[0].pid(), a[0].latest), (Some(4101), 0.0));
        assert_eq!(a[1].pid(), None, "a key that isn't a pid");
        assert_eq!(a[2].pid(), None, "no key at all");
        assert!(
            parse_anomalies("not json")
                .unwrap_err()
                .contains("didn't read")
        );
    }

    #[test]
    fn the_captured_alerts_read_newest_first() {
        let a = parse_alerts(ALERTS).unwrap();
        assert_eq!(a.len(), 6);
        assert!(a.windows(2).all(|w| w[0].fired() >= w[1].fired()));
        assert!(a.iter().any(|a| a.active()));
        let load = a
            .iter()
            .find(|a| a.key == "load.high" && a.active())
            .unwrap();
        assert_eq!(load.severity(), Severity::Critical);
        assert!(
            a.iter()
                .filter(|a| !a.active())
                .all(|a| a.resolved_at.is_some())
        );
    }

    #[test]
    fn missing_fields_nulls_and_new_fields_read() {
        let a = parse_alerts(ALERTS_ODD).unwrap();
        assert_eq!(a.len(), 2);
        // The dated one first, the one with no readable time after it.
        assert_eq!(a[0].id, "a1");
        assert!(a[0].active() && a[0].acknowledged());
        assert_eq!(a[0].detail, "");
        assert_eq!(a[1].severity(), Severity::Info);
        assert!(!a[1].active(), "resolved");
        assert_eq!(a[1].fired(), None);
        assert!(parse_alerts("null").unwrap().is_empty());
        assert!(parse_alerts("{").is_err());
    }

    #[test]
    fn z_scores_get_plain_phrases() {
        assert_eq!(how_far(3.2), "above its usual");
        assert_eq!(how_far(12.0), "well above its usual");
        assert_eq!(how_far(1400.0), "far above its usual");
    }

    fn app(procs: Vec<Process>) -> App {
        let mut s = init();
        s.land_procs(Ok(procs));
        s
    }

    fn anomaly(pid: u32, latest: f64, mean: f64, z: f64) -> Anomaly {
        Anomaly {
            key: pid.to_string(),
            latest,
            baseline_mean: mean,
            z,
            samples: 10,
        }
    }

    #[test]
    fn rows_are_jumps_up_of_processes_that_are_still_running() {
        let mut s = app(vec![
            proc(1, "alpha", 1.0, 100.0),
            proc(2, "beta", 1.0, 100.0),
        ]);
        s.growth.jumps = Some((
            vec![
                anomaly(1, 300_000.0, 200_000.0, 8.0),
                anomaly(2, 100_000.0, 300_000.0, -9.0),
                anomaly(99, 900_000.0, 100_000.0, 50.0),
                anomaly(2, 150_000.5, 150_000.0, 40.0),
                anomaly(2, 500_000.0, 100_000.0, 20.0),
            ],
            0.0,
        ));
        let rows = s.jump_rows();
        // Below usual, gone, and a trivial rise are all left out; the biggest z leads.
        assert_eq!(rows.len(), 2);
        assert_eq!(
            (rows[0].pid, rows[0].name.as_str(), rows[0].z),
            (2, "beta", 20.0)
        );
        assert_eq!(
            (rows[1].pid, rows[1].now_kib, rows[1].usual_kib),
            (1, 300_000.0, 200_000.0)
        );
    }

    fn faked(replies: &[(&'static str, Result<&str, &str>)]) -> (App, Arc<Fake>) {
        let mut s = app(vec![
            proc(2001, "Editor", 1.0, 100.0),
            proc(2002, "Browser", 1.0, 100.0),
        ]);
        let fake = Arc::new(Fake::with(replies));
        s.growth.spectra = fake.clone();
        s.growth.held = Some(Arc::new(Mutex::new(Vec::new())));
        s.growth.auto = true;
        s.visible = true;
        s.nav = Nav::Growth;
        (s, fake)
    }

    fn all_good() -> [(&'static str, Result<&'static str, &'static str>); 2] {
        [("anomalies", Ok(ANOMALIES)), ("alerts --json", Ok(ALERTS))]
    }

    #[test]
    fn a_look_asks_both_through_the_seam_then_lands_and_rests() {
        let (mut s, fake) = faked(&all_good());
        s.now = 10.0;
        assert!(s.tick_growth());
        assert!(s.growth.running());
        s.run_held_growth();
        assert!(!s.growth.running());
        assert!(s.growth.jumps.is_some() && s.growth.alerts.is_some());
        let mut calls = fake.calls();
        calls.sort();
        assert_eq!(
            calls,
            [
                vec!["alerts", "--json", "--state", "all", "--limit", "100"],
                vec!["anomalies", "--json"]
            ]
        );
        // Not again for 30 s, then again.
        s.now = 39.0;
        s.tick_growth();
        assert!(!s.growth.running());
        s.now = 40.5;
        s.tick_growth();
        assert!(s.growth.running());
    }

    #[test]
    fn nothing_runs_while_hidden_or_off_the_page() {
        let (mut s, fake) = faked(&all_good());
        s.visible = false;
        s.tick_growth();
        assert!(!s.growth.running());
        s.visible = true;
        s.nav = Nav::All;
        s.tick_growth();
        assert!(!s.growth.running());
        s.nav = Nav::Growth;
        s.tick_growth();
        assert!(s.growth.running());
        // Leaving the page stops the look in flight.
        s.nav = Nav::All;
        s.tick_growth();
        assert!(!s.growth.running());
        assert!(fake.calls().is_empty(), "held work never ran");
        // Its answers, held from before, land nowhere.
        s.run_held_growth();
        assert!(!s.growth.started());
    }

    #[test]
    fn no_stored_history_is_its_own_state_not_a_failure() {
        let (mut s, _) = faked(&[
            (
                "anomalies",
                Err(
                    "no process metrics stored — process anomaly analysis is unavailable in the local-only core",
                ),
            ),
            ("alerts --json", Ok(ALERTS)),
        ]);
        s.tick_growth();
        s.run_held_growth();
        assert_eq!(s.growth.jumps_note, Some(Note::NotEnough));
        assert!(s.growth.jumps.is_none());
        assert!(s.growth.alerts_error.is_none());
    }

    #[test]
    fn a_failed_check_keeps_the_last_good_rows() {
        let (mut s, _) = faked(&all_good());
        s.tick_growth();
        s.run_held_growth();
        s.now = 100.0;
        s.growth.spectra = Arc::new(Fake::with(&[
            ("anomalies", Err("spectra anomalies: boom")),
            ("alerts --json", Err("spectra daemon is not running")),
        ]));
        s.tick_growth();
        s.run_held_growth();
        assert!(s.growth.jumps.is_some() && s.growth.alerts.is_some());
        assert_eq!(
            s.growth.jumps_note,
            Some(Note::Failed("spectra anomalies: boom".into()))
        );
        assert_eq!(
            s.growth.alerts_error.as_deref(),
            Some("spectra daemon is not running")
        );
    }

    #[test]
    fn acknowledging_happens_only_on_the_call_and_marks_the_alert() {
        let (mut s, fake) = faked(&[
            ("anomalies", Ok(ANOMALIES)),
            ("alerts --json", Ok(ALERTS)),
            (
                "alerts ack",
                Ok(
                    r#"{"id":"x","title":"Host load is critical","state":"firing","acked_at":"2026-11-23T04:05:00Z"}"#,
                ),
            ),
        ]);
        s.tick_growth();
        s.run_held_growth();
        // A look never writes.
        assert!(
            fake.calls()
                .iter()
                .all(|c| c.get(1).map(String::as_str) != Some("ack"))
        );
        let active = s
            .growth
            .alerts
            .as_ref()
            .unwrap()
            .0
            .iter()
            .find(|a| a.active())
            .unwrap()
            .clone();
        assert!(!active.acknowledged());
        s.ack_alert(&active.id);
        assert_eq!(
            s.growth.outcome.as_ref().unwrap().status,
            AckStatus::Sending
        );
        // A second press while one is out does nothing.
        s.ack_alert(&active.id);
        s.run_held_growth();
        assert_eq!(
            fake.calls()
                .iter()
                .filter(|c| c[1] == "ack")
                .collect::<Vec<_>>(),
            [&vec![
                "alerts".to_owned(),
                "ack".into(),
                active.id.clone(),
                "--json".into()
            ]]
        );
        let o = s.growth.outcome.as_ref().unwrap();
        assert_eq!(o.status, AckStatus::Done);
        assert_eq!(
            o.line(),
            "Acknowledged “Host load is critical”. Spectra's daemon has it recorded."
        );
        assert!(
            s.growth
                .alerts
                .as_ref()
                .unwrap()
                .0
                .iter()
                .find(|a| a.id == active.id)
                .unwrap()
                .acknowledged()
        );
        assert!(s.growth.acking.is_none());
    }

    #[test]
    fn a_refused_acknowledgement_says_why_and_changes_nothing() {
        let (mut s, _) = faked(&[
            ("anomalies", Ok(ANOMALIES)),
            ("alerts --json", Ok(ALERTS)),
            (
                "alerts ack",
                Err("Spectra's daemon: unknown alert id (-32602)"),
            ),
        ]);
        s.tick_growth();
        s.run_held_growth();
        let id = s
            .growth
            .alerts
            .as_ref()
            .unwrap()
            .0
            .iter()
            .find(|a| a.active())
            .unwrap()
            .id
            .clone();
        s.ack_alert(&id);
        s.run_held_growth();
        let o = s.growth.outcome.as_ref().unwrap();
        assert_eq!(
            o.line(),
            "Couldn't acknowledge “Host load is critical”: Spectra's daemon: unknown alert id (-32602)"
        );
        assert!(
            !s.growth
                .alerts
                .as_ref()
                .unwrap()
                .0
                .iter()
                .any(Alert::acknowledged)
        );
        assert!(s.growth.acking.is_none(), "the person can try again");
    }

    #[test]
    fn an_alert_that_isnt_listed_is_never_acknowledged() {
        let (mut s, fake) = faked(&all_good());
        s.ack_alert("nope");
        assert!(s.growth.outcome.is_none());
        s.run_held_growth();
        assert!(fake.calls().is_empty());
    }

    #[test]
    fn opening_the_page_looks_at_once_and_clears_the_last_outcome() {
        let (mut s, _) = faked(&all_good());
        s.growth.next = 500.0;
        s.growth.outcome = Some(AckOutcome {
            title: "x".into(),
            status: AckStatus::Done,
        });
        s.open_growth();
        s.tick_growth();
        assert!(s.growth.running());
        assert!(s.growth.outcome.is_none());
    }

    #[test]
    fn leaving_and_returning_before_the_answer_lands_keeps_the_outcome_and_the_mark() {
        let (mut s, _) = faked(&[
            ("anomalies", Ok(ANOMALIES)),
            ("alerts --json", Ok(ALERTS)),
            (
                "alerts ack",
                Ok(r#"{"id":"0a1","acked_at":"2026-11-20T11:59:00Z"}"#),
            ),
        ]);
        s.tick_growth();
        s.run_held_growth();
        let id = s
            .growth
            .alerts
            .as_ref()
            .unwrap()
            .0
            .iter()
            .find(|a| a.active())
            .unwrap()
            .id
            .clone();
        s.ack_alert(&id);
        // Away and back while it is out.
        s.nav = Nav::All;
        s.open_growth();
        s.nav = Nav::Growth;
        assert!(s.growth.outcome.is_some(), "kept while acking");
        s.run_held_growth();
        assert_eq!(s.growth.outcome.as_ref().unwrap().status, AckStatus::Done);
        assert!(
            s.growth
                .alerts
                .as_ref()
                .unwrap()
                .0
                .iter()
                .find(|a| a.id == id)
                .unwrap()
                .acknowledged()
        );
        // And once it landed, opening again clears the line.
        s.open_growth();
        assert!(s.growth.outcome.is_none());
    }

    #[test]
    fn a_list_read_before_the_press_does_not_bring_the_button_back() {
        let (mut s, _) = faked(&[
            ("anomalies", Ok(ANOMALIES)),
            ("alerts --json", Ok(ALERTS)),
            ("alerts ack", Ok(r#"{"id":"0a1"}"#)),
        ]);
        s.tick_growth();
        s.run_held_growth();
        // A second look starts (its answers held), then the press lands first.
        s.now = 100.0;
        s.tick_growth();
        let looks = std::mem::take(&mut *s.growth.held.as_ref().unwrap().lock().unwrap());
        s.ack_alert("0a1");
        s.run_held_growth();
        for w in looks {
            s.land_growth(w());
        }
        let list = &s.growth.alerts.as_ref().unwrap().0;
        assert!(list.iter().find(|a| a.id == "0a1").unwrap().acknowledged());
    }

    struct Panics;

    impl Spectra for Panics {
        fn run(&self, _: &[String], _: Duration, _: &AtomicBool) -> Result<String, Failure> {
            panic!("boom")
        }
    }

    #[test]
    fn a_call_that_panics_lands_a_failure_instead_of_checking_forever() {
        let (mut s, _) = faked(&all_good());
        s.growth.spectra = Arc::new(Panics);
        s.tick_growth();
        let quiet = std::panic::take_hook();
        std::panic::set_hook(Box::new(|_| {}));
        s.run_held_growth();
        std::panic::set_hook(quiet);
        assert!(!s.growth.running(), "both calls landed");
        assert_eq!(s.growth.jumps_note, Some(Note::Failed(INTERNAL.into())));
        assert_eq!(s.growth.alerts_error.as_deref(), Some(INTERNAL));
    }
}
