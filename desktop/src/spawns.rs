//! Processes starting and exiting, live: `spectra spawns` polls the process table every 250 ms
//! (every second when only "Keep recording" runs it), so it sees the short-lived processes the 2 s process feed never lists. It runs in rounds of
//! about 10 s, off the UI thread, only while the Activity page is open or "Keep recording" is
//! on. What it saw lands in a ring buffer of the latest events, with a summary of the last
//! minute and a finding when one parent starts processes faster than anyone would by hand.

use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::time::Duration;

use rune::TextEdit;
use serde_json::Value;

use crate::app::App;
use crate::findings::{Chart, Finding, Link, Severity};
use crate::groups::Nav;
use crate::inspect::{Failure, Spectra};

/// Events kept: the oldest go first once there are more.
pub const MAX_EVENTS: usize = 5000;
/// How long one round traces, in seconds.
pub const ROUND_SECS: u64 = 10;
/// How often Spectra looks at the process table during a round while the Activity page is open.
pub const INTERVAL: &str = "250ms";
/// The same while only "Keep recording" runs rounds: about a quarter of the CPU.
pub const INTERVAL_BACKGROUND: &str = "1s";
/// How long to rest, in seconds, between two good rounds.
pub const REST: f64 = 1.0;
/// How long one round may take before it's stopped: its trace and a margin.
const ROUND_DEADLINE: Duration = Duration::from_secs(ROUND_SECS + 20);
/// How far a start's time may be from its exit's start estimate beyond the exit's own error, in
/// milliseconds: a start is seen up to one check after it happened.
const START_SLACK_MS: i64 = 1500;
/// The most a round may print.
const MAX_OUTPUT: usize = 16 << 20;
/// A process that exited sooner than this after it started is short-lived, in milliseconds.
pub const SHORT_LIVED_MS: i64 = 2000;
/// The summary's window, in seconds.
pub const WINDOW: f64 = 60.0;
/// More children than this from one parent within `WINDOW` is a spawn storm.
pub const STORM: usize = 100;
/// Parents the summary lists.
pub const TOP_PARENTS: usize = 5;
/// How long to wait, in seconds, after a failed round before the next.
const RETRY_FAILED: f64 = 10.0;
/// How long to wait, in seconds, before asking a Spectra without `spawns` again, in case it
/// was updated.
const RETRY_UNSUPPORTED: f64 = 60.0;

/// Whether a process started or exited.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Kind {
    Start,
    Exit,
}

impl Kind {
    pub fn label(self) -> &'static str {
        match self {
            Kind::Start => "Started",
            Kind::Exit => "Exited",
        }
    }
}

/// One process starting or exiting.
#[derive(Clone, Debug, PartialEq)]
pub struct Event {
    pub kind: Kind,
    /// Unix seconds.
    pub at: f64,
    /// The time of day as Spectra wrote it, `19:50:34`.
    pub clock: String,
    pub pid: u32,
    pub ppid: u32,
    /// The short name the process table gives.
    pub name: String,
    /// Its parent's name, then the parent's parent, up to launchd.
    pub chain: Vec<String>,
    /// How long it ran, for an exit; approximate, since start times have 1 s resolution.
    pub lifetime_ms: Option<i64>,
    /// How far off `lifetime_ms` may be.
    pub lifetime_err_ms: Option<i64>,
    /// Its command line, when the process feed listed the pid as the event landed.
    pub command_line: String,
    /// Started by Spectra Desktop itself (its own readings), directly or further up.
    pub own: bool,
}

impl Event {
    pub fn parent_name(&self) -> &str {
        self.chain.first().map_or("", String::as_str)
    }

    /// An exit after less than `SHORT_LIVED_MS`.
    pub fn short_lived(&self) -> bool {
        self.kind == Kind::Exit && self.lifetime_ms.is_some_and(|ms| ms < SHORT_LIVED_MS)
    }

    /// Tells this event from every other in the buffer, for an open row.
    pub fn id(&self) -> String {
        let kind = match self.kind {
            Kind::Start => "s",
            Kind::Exit => "x",
        };
        format!("{kind}-{}-{}", self.pid, (self.at * 1000.0).round() as i64)
    }
}

/// `1.7 s`, `42 s`, `3 min`.
pub fn fmt_lifetime(ms: i64) -> String {
    let s = ms.max(0) as f64 / 1000.0;
    if s < 10.0 {
        format!("{s:.1} s")
    } else {
        crate::app::fmt_ago(s)
    }
}

/// Days since 1970-01-01 of a proleptic Gregorian date.
fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// An RFC 3339 time (`2026-11-21T19:50:34.740791-05:00`) as Unix seconds.
pub fn unix_time(s: &str) -> Option<f64> {
    let n = |a: usize, b: usize| s.get(a..b)?.parse::<i64>().ok();
    let (y, mo, d) = (n(0, 4)?, n(5, 7)?, n(8, 10)?);
    let (h, mi, se) = (n(11, 13)?, n(14, 16)?, n(17, 19)?);
    let mut rest = s.get(19..)?;
    let mut frac = 0.0;
    if let Some(r) = rest.strip_prefix('.') {
        let digits = r.find(|c: char| !c.is_ascii_digit()).unwrap_or(r.len());
        frac = format!("0.{}", &r[..digits]).parse().ok()?;
        rest = &r[digits..];
    }
    let offset = match rest {
        "Z" | "z" => 0,
        _ => {
            let sign = match rest.get(..1)? {
                "+" => 1,
                "-" => -1,
                _ => return None,
            };
            let hh: i64 = rest.get(1..3)?.parse().ok()?;
            let mm: i64 = rest.get(4..6)?.parse().ok()?;
            sign * (hh * 3600 + mm * 60)
        }
    };
    let secs = days_from_civil(y, mo, d) * 86_400 + h * 3600 + mi * 60 + se - offset;
    Some(secs as f64 + frac)
}

fn int(v: &Value, key: &str) -> Option<i64> {
    v.get(key).and_then(Value::as_i64)
}

/// One event line, or `None` for the summary or anything else Spectra prints.
fn event(v: &Value) -> Option<Event> {
    let kind = match v.get("event")?.as_str()? {
        "start" => Kind::Start,
        "exit" => Kind::Exit,
        _ => return None,
    };
    let time = v.get("time").and_then(Value::as_str).unwrap_or_default();
    let pid = u32::try_from(int(v, "pid")?).ok()?;
    let chain = v
        .get("parent_chain")
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default();
    Some(Event {
        kind,
        at: unix_time(time).unwrap_or(0.0),
        clock: time.get(11..19).unwrap_or("").to_owned(),
        pid,
        ppid: int(v, "ppid")
            .and_then(|p| u32::try_from(p).ok())
            .unwrap_or(0),
        name: v
            .get("command")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        chain,
        lifetime_ms: int(v, "lifetime_ms"),
        lifetime_err_ms: int(v, "lifetime_max_error_ms"),
        command_line: String::new(),
        own: false,
    })
}

/// What one round printed: one JSON object per event, then a summary. Lines that aren't
/// events are skipped; a round with neither an event nor the summary failed.
pub fn parse(out: &str) -> Result<Vec<Event>, String> {
    let mut events = Vec::new();
    let mut summary = false;
    for line in out.lines().map(str::trim).filter(|l| !l.is_empty()) {
        let Ok(v) = serde_json::from_str::<Value>(line) else {
            continue;
        };
        if v.get("type").and_then(Value::as_str) == Some("summary") {
            summary = true;
        } else if let Some(e) = event(&v) {
            events.push(e);
        }
    }
    if events.is_empty() && !summary {
        return Err("Spectra's spawns output had no events and no summary".into());
    }
    Ok(events)
}

/// `spectra spawns` for one round of `secs` seconds, looking every `interval`.
pub fn args(secs: u64, interval: &str) -> Vec<String> {
    ["spawns", "--json", "--interval", interval, "--duration"]
        .iter()
        .map(|s| (*s).to_owned())
        .chain([format!("{secs}s")])
        .collect()
}

/// Why a round has no events.
#[derive(Clone, Debug, PartialEq)]
pub enum Fail {
    /// An older Spectra without `spawns`.
    Unsupported(String),
    Failed(String),
    /// Cancelled: the page was left, or the app quit.
    Stopped,
}

/// What a failed run means. An older Spectra takes `spawns` and its flags for paths to inspect
/// and says it can't find them; a newer one without the command says it doesn't know it.
pub fn classify(why: &str) -> Fail {
    let lower = why.to_ascii_lowercase();
    let old = (lower.contains("stat ") && lower.contains("no such file"))
        || lower.contains("unknown command")
        || lower.contains("flag provided but not defined");
    if old {
        Fail::Unsupported(
            "This Spectra has no `spectra spawns`. Update Spectra to see processes start and exit."
                .into(),
        )
    } else {
        Fail::Failed(why.trim().to_owned())
    }
}

/// One round's result, for round number `job`.
pub struct Round {
    pub job: u64,
    pub result: Result<Vec<Event>, Fail>,
}

/// A parent and how many children it started.
#[derive(Clone, Debug, PartialEq)]
pub struct ParentCount {
    pub ppid: u32,
    pub name: String,
    pub count: usize,
}

/// The last minute in numbers.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Summary {
    pub starts: usize,
    pub exits: usize,
    /// Parents by children started, most first, at most `TOP_PARENTS`.
    pub parents: Vec<ParentCount>,
}

/// The events at or after `now - window`, counted.
pub fn summary<'a>(events: impl IntoIterator<Item = &'a Event>, now: f64, window: f64) -> Summary {
    let mut s = Summary::default();
    let mut by_parent: HashMap<u32, ParentCount> = HashMap::new();
    for e in events
        .into_iter()
        .filter(|e| e.at >= now - window && e.at <= now + 1.0)
    {
        match e.kind {
            Kind::Start => {
                s.starts += 1;
                let p = by_parent.entry(e.ppid).or_insert_with(|| ParentCount {
                    ppid: e.ppid,
                    name: e.parent_name().to_owned(),
                    count: 0,
                });
                p.count += 1;
            }
            Kind::Exit => s.exits += 1,
        }
    }
    let mut parents: Vec<ParentCount> = by_parent.into_values().collect();
    parents.sort_by(|a, b| b.count.cmp(&a.count).then(a.ppid.cmp(&b.ppid)));
    parents.truncate(TOP_PARENTS);
    s.parents = parents;
    s
}

/// The spawn-storm findings: each parent that started more than `STORM` processes within the
/// last `WINDOW` seconds before `now`. Each opens the Activity page filtered to that parent.
pub fn storms<'a>(events: impl IntoIterator<Item = &'a Event>, now: f64) -> Vec<Finding> {
    let mut by_parent: HashMap<u32, (String, usize)> = HashMap::new();
    for e in events
        .into_iter()
        .filter(|e| e.kind == Kind::Start && e.at >= now - WINDOW && e.at <= now + 1.0)
    {
        by_parent
            .entry(e.ppid)
            .or_insert_with(|| (e.parent_name().to_owned(), 0))
            .1 += 1;
    }
    let mut out: Vec<Finding> = by_parent
        .into_iter()
        .filter(|(_, (_, n))| *n > STORM)
        .map(|(ppid, (name, n))| {
            let name = if name.is_empty() { format!("pid {ppid}") } else { name };
            Finding {
                rule: "spawn-storm",
                pid: ppid,
                subject: name.clone(),
                severity: Severity::Warning,
                title: format!("{name} started {n} processes in a minute"),
                detail: "Something keeps starting processes that soon exit. That costs CPU and battery even when each one is quick.".into(),
                fix: format!("Open Activity to see what {name} starts, then fix or stop the script, watcher or loop behind it."),
                chart: Chart::None,
                link: Link::Activity(ppid),
            }
        })
        .collect();
    out.sort_by_key(|f| f.pid);
    out
}

/// A round in flight.
struct Flight {
    job: u64,
    interval: &'static str,
    cancel: Arc<AtomicBool>,
}

/// The Activity page's state: the events, the filters, and the round in flight.
pub struct Activity {
    pub events: VecDeque<Event>,
    /// Record on every page, not only while the Activity page is open. Off at start.
    pub keep_recording: bool,
    pub query: TextEdit,
    /// Only the children of this parent, set by a summary row or the spawn-storm finding.
    pub parent: Option<u32>,
    pub short_only: bool,
    /// Also list, count and watch for storms in what Spectra Desktop's own readings started.
    pub show_own: bool,
    /// This app's pid: what its own readings descend from.
    pub me: u32,
    /// The open row, by `Event::id`.
    pub expanded: Option<String>,
    /// What went wrong with the last round; cleared by a good one.
    pub error: Option<Fail>,
    /// The window clock when the last good round landed.
    pub updated: Option<f64>,
    /// Good rounds landed.
    pub rounds: u64,
    /// The window clock before which no round starts (after a failure).
    pub next: f64,
    /// False in sample scenes: their pids are made up, so nothing records on its own.
    pub auto: bool,
    pub spectra: Arc<dyn Spectra>,
    flight: Option<Flight>,
    job: u64,
    tx: Sender<Round>,
    rx: Receiver<Round>,
}

impl Default for Activity {
    fn default() -> Activity {
        Activity::new()
    }
}

/// The real Spectra, its rounds' output capped at `MAX_OUTPUT`.
struct Capped;

impl Spectra for Capped {
    fn run(
        &self,
        args: &[String],
        deadline: Duration,
        cancel: &AtomicBool,
    ) -> Result<String, Failure> {
        let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
        let mut cmd = std::process::Command::new(&bin);
        cmd.args(args);
        let out =
            crate::run::run_until(cmd, deadline, MAX_OUTPUT, cancel).map_err(|e| match e {
                crate::run::Ended::Cancelled => Failure::Cancelled,
                crate::run::Ended::Failed(e) => Failure::Failed(crate::procs::run_failure(&bin, e)),
            })?;
        if !out.status.success() {
            return Err(Failure::Failed(crate::inspect::failure_line(
                &bin,
                args,
                &out.stderr,
                out.status.code(),
            )));
        }
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    }
}

impl Activity {
    pub fn new() -> Activity {
        let (tx, rx) = channel();
        Activity {
            events: VecDeque::new(),
            keep_recording: false,
            query: TextEdit::new(""),
            parent: None,
            short_only: false,
            show_own: false,
            me: std::process::id(),
            expanded: None,
            error: None,
            updated: None,
            rounds: 0,
            next: 0.0,
            auto: true,
            spectra: Arc::new(Capped),
            flight: None,
            job: 0,
            tx,
            rx,
        }
    }

    /// Adds a round's events, oldest first, dropping the oldest past `MAX_EVENTS`.
    pub fn push(&mut self, events: impl IntoIterator<Item = Event>) {
        for e in events {
            self.events.push_back(e);
        }
        while self.events.len() > MAX_EVENTS {
            self.events.pop_front();
        }
    }

    /// Whether a round is running.
    pub fn recording(&self) -> bool {
        self.flight.is_some()
    }

    /// A round that never lands and runs nothing, so a sample scene shows the page as it looks
    /// while recording.
    pub fn sample_flight(&mut self) {
        self.job += 1;
        self.flight = Some(Flight {
            job: self.job,
            interval: INTERVAL,
            cancel: Arc::new(AtomicBool::new(false)),
        });
    }

    /// How often the round in flight looks, if one is running.
    pub fn interval(&self) -> Option<&'static str> {
        self.flight.as_ref().map(|f| f.interval)
    }

    /// Whether the table, summary and storms count `e`: not an own reading, unless asked for.
    pub fn counts(&self, e: &Event) -> bool {
        self.show_own || !e.own
    }

    /// Stops the round in flight, if any. Its result, when it comes, is dropped.
    pub fn cancel(&mut self) {
        if let Some(f) = self.flight.take() {
            f.cancel.store(true, Ordering::SeqCst);
        }
    }

    /// The events the table shows, newest first: the search (a name, a parent's name, or a pid
    /// or parent pid), the parent filter and "Short-lived only" applied.
    pub fn shown(&self) -> Vec<&Event> {
        let q = self.query.text.trim().to_lowercase();
        // A short exit's process began at its time minus its lifetime; a start belongs to it
        // only if it began about then, so a reused pid doesn't borrow another's lifetime.
        let mut short: HashMap<u32, Vec<(i64, i64)>> = HashMap::new();
        if self.short_only {
            for e in self.events.iter().filter(|e| e.short_lived()) {
                let began = (e.at * 1000.0).round() as i64 - e.lifetime_ms.unwrap_or(0);
                let slack = e.lifetime_err_ms.unwrap_or(0) + START_SLACK_MS;
                short.entry(e.pid).or_default().push((began, slack));
            }
        }
        let started_short = |e: &Event| {
            let at = (e.at * 1000.0).round() as i64;
            short
                .get(&e.pid)
                .is_some_and(|v| v.iter().any(|(began, slack)| (at - began).abs() <= *slack))
        };
        self.events
            .iter()
            .rev()
            .filter(|e| self.counts(e))
            .filter(|e| self.parent.is_none_or(|p| e.ppid == p))
            .filter(|e| {
                !self.short_only || e.short_lived() || e.kind == Kind::Start && started_short(e)
            })
            .filter(|e| {
                q.is_empty()
                    || e.pid.to_string() == q
                    || e.ppid.to_string() == q
                    || e.name.to_lowercase().contains(&q)
                    || e.parent_name().to_lowercase().contains(&q)
            })
            .collect()
    }

    /// The last minute before Unix time `now`.
    pub fn summary(&self, now: f64) -> Summary {
        summary(self.events.iter().filter(|e| self.counts(e)), now, WINDOW)
    }

    /// Spawn storms in the minute before Unix time `now`.
    pub fn findings(&self, now: f64) -> Vec<Finding> {
        storms(self.events.iter().filter(|e| self.counts(e)), now)
    }
}

/// Whether `pid`'s parents in the process feed lead to `ancestor`.
fn descends_from(s: &App, pid: u32, ancestor: u32) -> bool {
    let mut at = pid;
    for _ in 0..64 {
        match s.process(at) {
            Some(p) if p.ppid == ancestor => return true,
            Some(p) if p.ppid > 1 => at = p.ppid,
            _ => return false,
        }
    }
    false
}

impl App {
    /// Whether the Activity page is on screen, with no process page over it.
    pub fn activity_open(&self) -> bool {
        self.window_shown() && self.nav == Nav::Activity && !self.on_page()
    }

    /// Whether rounds should run now: the page is open or "Keep recording" is on.
    pub fn activity_wanted(&self) -> bool {
        self.activity.auto && (self.activity_open() || self.activity.keep_recording)
    }

    /// Lands finished rounds, stops the round in flight once nobody wants it, and starts the
    /// next once one is wanted and due. True when anything landed.
    pub fn tick_activity(&mut self) -> bool {
        let mut landed = false;
        while let Ok(r) = self.activity.rx.try_recv() {
            landed |= self.land_round(r);
        }
        if !self.activity_wanted() {
            self.activity.cancel();
        } else if self.activity.flight.is_none() && self.now >= self.activity.next {
            self.start_round();
        }
        landed
    }

    fn start_round(&mut self) {
        // Looking every 250 ms costs about four times the CPU of every second: only while the page
        // is on screen.
        let interval = if self.activity_open() {
            INTERVAL
        } else {
            INTERVAL_BACKGROUND
        };
        let a = &mut self.activity;
        a.job += 1;
        let (job, cancel) = (a.job, Arc::new(AtomicBool::new(false)));
        a.flight = Some(Flight {
            job,
            interval,
            cancel: Arc::clone(&cancel),
        });
        let (spectra, tx, wake) = (Arc::clone(&a.spectra), a.tx.clone(), self.wake.clone());
        // Quit waits for this round from now until it's done, even before Spectra starts.
        let hold = crate::run::pending();
        let thread_tx = tx.clone();
        let started = std::thread::Builder::new()
            .name("spectra-spawns".into())
            .spawn(move || {
                let result = match spectra.run(&args(ROUND_SECS, interval), ROUND_DEADLINE, &cancel)
                {
                    Ok(out) => parse(&out).map_err(Fail::Failed),
                    Err(Failure::Cancelled) => Err(Fail::Stopped),
                    Err(Failure::Failed(why)) => Err(classify(&why)),
                };
                let _ = thread_tx.send(Round { job, result });
                drop(hold);
                if let Some(wake) = wake {
                    wake();
                }
            });
        if let Err(e) = started {
            let _ = tx.send(Round {
                job,
                result: Err(Fail::Failed(format!(
                    "Couldn't start a thread to record activity: {e}."
                ))),
            });
        }
    }

    /// Lands one round, if it's the one in flight. True when it changed anything.
    pub fn land_round(&mut self, r: Round) -> bool {
        let a = &mut self.activity;
        if a.flight.as_ref().is_none_or(|f| f.job != r.job) {
            return false;
        }
        a.flight = None;
        match r.result {
            Ok(mut events) => {
                // Own readings: what descends from this app, by the pids this round and the
                // buffer saw it start, and by the process feed's parents for older ones.
                let me = self.activity.me;
                let mut own: HashSet<u32> = self
                    .activity
                    .events
                    .iter()
                    .filter(|e| e.own)
                    .map(|e| e.pid)
                    .collect();
                for e in &mut events {
                    e.own =
                        e.ppid == me || own.contains(&e.ppid) || descends_from(self, e.ppid, me);
                    if e.own {
                        own.insert(e.pid);
                    }
                }
                for e in &mut events {
                    if let Some(p) = self.process(e.pid).filter(|p| p.name() == e.name) {
                        e.command_line = p.full_command_line.clone();
                    }
                }
                let a = &mut self.activity;
                a.push(events);
                a.error = None;
                a.updated = Some(self.now);
                a.rounds += 1;
                a.next = self.now + REST;
            }
            Err(Fail::Stopped) => return false,
            Err(fail) => {
                a.next = self.now
                    + match fail {
                        Fail::Unsupported(_) => RETRY_UNSUPPORTED,
                        _ => RETRY_FAILED,
                    };
                a.error = Some(fail);
            }
        }
        self.refresh_findings();
        true
    }

    /// Opens the Activity page showing only `ppid`'s children: where a spawn-storm finding goes.
    pub fn show_activity_of(&mut self, ppid: u32) {
        self.show(Nav::Activity);
        self.activity.parent = Some(ppid);
        self.activity.query = TextEdit::new("");
        self.activity.expanded = None;
    }

    pub fn toggle_show_own(&mut self) {
        self.activity.show_own = !self.activity.show_own;
        self.refresh_findings();
    }

    pub fn toggle_keep_recording(&mut self) {
        self.activity.keep_recording = !self.activity.keep_recording;
    }

    /// Opens or closes an event's row.
    pub fn toggle_event(&mut self, id: &str) {
        let a = &mut self.activity;
        a.expanded = if a.expanded.as_deref() == Some(id) {
            None
        } else {
            Some(id.to_owned())
        };
    }
}

#[cfg(test)]
pub mod tests {
    use super::*;
    use std::sync::Mutex;
    use std::time::Instant;

    /// Lines `spectra spawns --json` printed on a Mac, trimmed to a few events and its summary.
    pub const FIXTURE: &str = r#"{"event":"exit","time":"2026-11-21T19:50:34.740791-05:00","pid":88516,"ppid":88512,"command":"bash","parent_chain":["bash","zsh","login","launchd"],"lifetime_ms":1740,"lifetime_max_error_ms":1227}
{"event":"exit","time":"2026-11-21T19:50:34.740791-05:00","pid":88526,"ppid":84716,"command":"rustc","parent_chain":["cargo","cargo-clippy","bash","zsh","login","launchd"],"lifetime_ms":1740,"lifetime_max_error_ms":1227}
{"event":"exit","time":"2026-11-21T19:50:34.740791-05:00","pid":88736,"ppid":2763,"command":"git","parent_chain":["Terminal","launchd"],"lifetime_ms":740,"lifetime_max_error_ms":740}
{"event":"start","time":"2026-11-21T19:50:34.740791-05:00","pid":88758,"ppid":88512,"command":"bash","parent_chain":["bash","zsh","login","launchd"]}
{"event":"start","time":"2026-11-21T19:50:34.990803-05:00","pid":88767,"ppid":88766,"command":"gh","parent_chain":["bash","bash","bash","bash","zsh","login","launchd"]}
{"starts":2,"exits":3,"polls":12,"skipped_ticks":0,"note":"polling misses processes that live for less than the interval","by_parent":[{"name":"bash[88512]","count":1}],"by_command":[{"name":"bash","count":1}],"by_parent_total":1,"by_command_total":1,"respawn_loops":[],"type":"summary","duration_seconds":3.152,"interval_seconds":0.25,"effective_interval_seconds":0.250076868,"shortest_lived":[{"pid":88736,"command":"git","lifetime_seconds":0.74}]}
"#;

    pub fn ev(
        kind: Kind,
        at: f64,
        pid: u32,
        ppid: u32,
        name: &str,
        parent: &str,
        lifetime_ms: Option<i64>,
    ) -> Event {
        Event {
            kind,
            at,
            clock: String::new(),
            pid,
            ppid,
            name: name.into(),
            chain: vec![parent.into(), "launchd".into()],
            lifetime_ms,
            lifetime_err_ms: None,
            command_line: String::new(),
            own: false,
        }
    }

    #[test]
    fn parses_a_real_round() {
        let events = parse(FIXTURE).unwrap();
        assert_eq!(events.len(), 5, "the summary line isn't an event");
        let e = &events[0];
        assert_eq!(
            (e.kind, e.pid, e.ppid, e.name.as_str()),
            (Kind::Exit, 88516, 88512, "bash")
        );
        assert_eq!(e.parent_name(), "bash");
        assert_eq!(e.clock, "19:50:34");
        assert_eq!((e.lifetime_ms, e.lifetime_err_ms), (Some(1740), Some(1227)));
        assert!(e.short_lived());
        // 2026-11-22T00:50:34.740791Z.
        assert!((e.at - 1_795_308_634.740_791).abs() < 1e-3, "{}", e.at);
        let start = &events[3];
        assert_eq!((start.kind, start.lifetime_ms), (Kind::Start, None));
        assert!(!start.short_lived());
        assert_eq!(events[4].chain.len(), 7);
    }

    #[test]
    fn parses_tolerantly() {
        let events =
            parse("{\"event\":\"start\",\"pid\":5}\nnot json\n{\"event\":\"wat\",\"pid\":6}\n")
                .unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(
            (
                events[0].ppid,
                events[0].name.as_str(),
                events[0].parent_name()
            ),
            (0, "", "")
        );
        assert_eq!(
            parse("{\"type\":\"summary\",\"starts\":0}").unwrap(),
            Vec::new(),
            "a quiet round"
        );
        assert!(parse("").is_err());
        assert!(parse("Usage of spectra").is_err());
    }

    #[test]
    fn reads_rfc3339_times() {
        assert_eq!(unix_time("1970-01-01T00:00:00Z"), Some(0.0));
        assert_eq!(
            unix_time("2026-11-20T12:00:00Z"),
            Some(crate::sample::SAMPLE_NOW as f64)
        );
        assert_eq!(
            unix_time("2026-11-20T07:00:00-05:00"),
            Some(crate::sample::SAMPLE_NOW as f64)
        );
        assert_eq!(
            unix_time("2026-11-20T13:30:00.5+01:30"),
            Some(crate::sample::SAMPLE_NOW as f64 + 0.5)
        );
        assert_eq!(unix_time("yesterday"), None);
    }

    #[test]
    fn an_old_spectra_is_unsupported_and_other_failures_say_why() {
        for why in [
            "--interval: stat --interval: stat --interval: no such file or directory",
            "spawns: stat spawns: no such file or directory",
            "flag provided but not defined: -interval",
        ] {
            assert!(matches!(classify(why), Fail::Unsupported(_)), "{why}");
        }
        assert_eq!(
            classify("spawns: collect: operation not permitted\n"),
            Fail::Failed("spawns: collect: operation not permitted".into())
        );
    }

    #[test]
    fn the_ring_buffer_keeps_the_newest_events() {
        let mut a = Activity::new();
        a.push(
            (0..MAX_EVENTS as u32 + 250)
                .map(|i| ev(Kind::Start, f64::from(i), i, 1, "sh", "zsh", None)),
        );
        assert_eq!(a.events.len(), MAX_EVENTS);
        assert_eq!(a.events.front().unwrap().pid, 250, "the oldest went first");
        a.push([ev(Kind::Exit, 1e9, 1, 1, "sh", "zsh", Some(5))]);
        assert_eq!(a.events.len(), MAX_EVENTS);
        assert_eq!(a.events.back().unwrap().pid, 1);
        assert_eq!(a.shown()[0].pid, 1, "newest first");
    }

    #[test]
    fn the_summary_counts_the_last_minute_and_ranks_parents() {
        let now = 1000.0;
        let mut events = Vec::new();
        for i in 0..30 {
            events.push(ev(Kind::Start, now - 10.0, 100 + i, 7, "git", "zsh", None));
        }
        for i in 0..5 {
            events.push(ev(
                Kind::Start,
                now - 50.0,
                200 + i,
                9,
                "rustc",
                "cargo",
                None,
            ));
            events.push(ev(
                Kind::Exit,
                now - 40.0,
                200 + i,
                9,
                "rustc",
                "cargo",
                Some(900),
            ));
        }
        // Older than a minute: not counted.
        for i in 0..50 {
            events.push(ev(
                Kind::Start,
                now - 61.0,
                300 + i,
                11,
                "sleep",
                "bash",
                None,
            ));
        }
        let s = summary(&events, now, WINDOW);
        assert_eq!((s.starts, s.exits), (35, 5));
        assert_eq!(
            s.parents,
            vec![
                ParentCount {
                    ppid: 7,
                    name: "zsh".into(),
                    count: 30
                },
                ParentCount {
                    ppid: 9,
                    name: "cargo".into(),
                    count: 5
                },
            ]
        );
    }

    #[test]
    fn short_lived_only_keeps_quick_exits_and_their_starts() {
        let mut a = Activity::new();
        a.push([
            ev(Kind::Start, 1.0, 10, 1, "git", "zsh", None),
            ev(Kind::Start, 1.0, 11, 1, "vim", "zsh", None),
            ev(Kind::Exit, 2.0, 10, 1, "git", "zsh", Some(400)),
            ev(Kind::Exit, 9.0, 11, 1, "vim", "zsh", Some(8000)),
            ev(Kind::Start, 9.5, 12, 1, "ls", "zsh", None),
        ]);
        assert_eq!(a.shown().len(), 5);
        a.short_only = true;
        let kept: Vec<(Kind, u32)> = a.shown().iter().map(|e| (e.kind, e.pid)).collect();
        assert_eq!(kept, vec![(Kind::Exit, 10), (Kind::Start, 10)]);
    }

    #[test]
    fn the_filter_matches_name_pid_and_parent() {
        let mut a = Activity::new();
        a.push([
            ev(Kind::Start, 1.0, 10, 7, "git", "zsh", None),
            ev(Kind::Start, 1.0, 11, 8, "rustc", "cargo", None),
        ]);
        let pids = |a: &Activity| a.shown().iter().map(|e| e.pid).collect::<Vec<_>>();
        a.query = TextEdit::new("GIT");
        assert_eq!(pids(&a), vec![10]);
        a.query = TextEdit::new("cargo");
        assert_eq!(pids(&a), vec![11]);
        a.query = TextEdit::new("7");
        assert_eq!(pids(&a), vec![10], "a parent pid");
        a.query = TextEdit::new("11");
        assert_eq!(pids(&a), vec![11], "a pid");
        a.query = TextEdit::new("");
        a.parent = Some(8);
        assert_eq!(pids(&a), vec![11]);
    }

    #[test]
    fn a_storm_is_more_than_a_hundred_children_in_a_minute() {
        let now = 1000.0;
        let starts = |n: u32, ppid: u32, at: f64| {
            (0..n).map(move |i| ev(Kind::Start, at, ppid * 1000 + i, ppid, "sleep", "zsh", None))
        };
        let events: Vec<Event> = starts(100, 7, now - 5.0).collect();
        assert!(
            storms(&events, now).is_empty(),
            "exactly the threshold isn't a storm"
        );
        let events: Vec<Event> = starts(101, 7, now - 5.0)
            .chain(starts(150, 8, now - 90.0))
            .collect();
        let f = storms(&events, now);
        assert_eq!(f.len(), 1, "an old burst doesn't count");
        assert_eq!(
            (f[0].rule, f[0].pid, f[0].link, f[0].severity),
            ("spawn-storm", 7, Link::Activity(7), Severity::Warning)
        );
        assert_eq!(f[0].title, "zsh started 101 processes in a minute");
    }

    /// A Spectra that records each round and holds it until cancelled, or prints `out` at once.
    struct Fake {
        calls: Mutex<Vec<Vec<String>>>,
        cancelled: AtomicBool,
        out: Option<Result<String, String>>,
    }

    impl Spectra for Fake {
        fn run(
            &self,
            args: &[String],
            _: Duration,
            cancel: &AtomicBool,
        ) -> Result<String, Failure> {
            self.calls.lock().unwrap().push(args.to_vec());
            if let Some(out) = &self.out {
                return out.clone().map_err(Failure::Failed);
            }
            let start = Instant::now();
            while !cancel.load(Ordering::SeqCst) {
                assert!(start.elapsed() < Duration::from_secs(10), "never cancelled");
                std::thread::sleep(Duration::from_millis(5));
            }
            self.cancelled.store(true, Ordering::SeqCst);
            Err(Failure::Cancelled)
        }
    }

    fn faked(out: Option<Result<String, String>>) -> (App, Arc<Fake>) {
        let fake = Arc::new(Fake {
            calls: Mutex::new(Vec::new()),
            cancelled: AtomicBool::new(false),
            out,
        });
        let mut s = crate::app::init();
        s.activity.spectra = fake.clone();
        (s, fake)
    }

    fn calls(f: &Fake) -> usize {
        f.calls.lock().unwrap().len()
    }

    /// Ticks until a round lands, or panics.
    fn land(s: &mut App) {
        let start = Instant::now();
        while !s.tick_activity() {
            assert!(start.elapsed() < Duration::from_secs(5), "no round landed");
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    fn wait_for(what: &str, f: impl Fn() -> bool) {
        let start = Instant::now();
        while !f() {
            assert!(start.elapsed() < Duration::from_secs(5), "{what}");
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    #[test]
    fn rounds_run_only_while_the_page_is_open_and_stop_on_leave() {
        let (mut s, fake) = faked(None);
        s.tick_activity();
        assert!(!s.activity.recording(), "nothing records on the overview");
        s.show(Nav::Activity);
        s.tick_activity();
        assert!(s.activity.recording());
        wait_for("the round started", || calls(&fake) == 1);
        assert_eq!(fake.calls.lock().unwrap()[0], args(ROUND_SECS, INTERVAL));
        assert_eq!(
            args(10, "250ms").join(" "),
            "spawns --json --interval 250ms --duration 10s"
        );
        s.tick_activity();
        assert_eq!(calls(&fake), 1, "one round at a time");
        s.show(Nav::System);
        s.tick_activity();
        assert!(!s.activity.recording());
        wait_for("leaving the page cancels the round", || {
            fake.cancelled.load(Ordering::SeqCst)
        });
        // The cancelled round's answer lands as nothing.
        std::thread::sleep(Duration::from_millis(20));
        assert!(!s.tick_activity());
        assert!(s.activity.error.is_none());
        assert_eq!(calls(&fake), 1, "and no round starts while away");
    }

    #[test]
    fn a_process_page_over_activity_counts_as_leaving_it() {
        let (mut s, fake) = faked(None);
        s.show(Nav::Activity);
        s.tick_activity();
        s.open(4242);
        s.tick_activity();
        assert!(!s.activity.recording());
        wait_for("cancelled", || fake.cancelled.load(Ordering::SeqCst));
    }

    #[test]
    fn keep_recording_runs_rounds_on_other_pages_and_quit_cancels() {
        let (mut s, fake) = faked(None);
        assert!(!s.activity.keep_recording, "off by default");
        s.toggle_keep_recording();
        s.tick_activity();
        assert!(s.activity.recording(), "recording from the overview");
        wait_for("started", || calls(&fake) == 1);
        assert_eq!(
            fake.calls.lock().unwrap()[0],
            args(ROUND_SECS, INTERVAL_BACKGROUND),
            "off the page it looks once a second"
        );
        assert_eq!(s.activity.interval(), Some("1s"));
        // What `on_quit` does.
        s.activity.cancel();
        wait_for("quit cancels", || fake.cancelled.load(Ordering::SeqCst));
        assert!(!s.activity.recording());
    }

    #[test]
    fn a_hidden_window_stops_the_page_but_not_keep_recording() {
        let (mut s, _fake) = faked(None);
        s.show(Nav::Activity);
        assert!(s.activity_wanted());
        s.visible = false;
        assert!(!s.activity_wanted(), "nobody can see the page");
        s.toggle_keep_recording();
        assert!(s.activity_wanted(), "keep recording is the one deliberate exception");
        assert!(!s.activity_open(), "and it looks at the background interval");
    }

    #[test]
    fn the_page_being_open_decides_the_interval_not_keep_recording() {
        let (mut s, fake) = faked(None);
        s.toggle_keep_recording();
        s.show(Nav::Activity);
        s.tick_activity();
        wait_for("started", || calls(&fake) == 1);
        assert_eq!(
            fake.calls.lock().unwrap()[0],
            args(ROUND_SECS, "250ms"),
            "open page, even with Keep recording on"
        );
        assert_eq!(s.activity.interval(), Some("250ms"));
        s.activity.cancel();
    }

    #[test]
    fn a_landed_round_fills_the_buffer_and_the_next_waits_a_second() {
        let (mut s, fake) = faked(Some(Ok(FIXTURE.to_owned())));
        s.show(Nav::Activity);
        s.tick_activity();
        land(&mut s);
        assert_eq!(s.activity.events.len(), 5);
        assert_eq!(s.activity.rounds, 1);
        assert!(!s.activity.recording(), "it rests between rounds");
        assert_eq!(s.activity.next, s.now + REST);
        s.now += REST - 0.1;
        s.tick_activity();
        assert!(!s.activity.recording(), "still resting");
        assert_eq!(calls(&fake), 1);
        s.now += 0.1;
        s.tick_activity();
        assert!(s.activity.recording(), "then the next round starts");
        land(&mut s);
        assert_eq!(s.activity.events.len(), 10);
        assert_eq!(calls(&fake), 2);
    }

    #[test]
    fn short_lived_only_does_not_match_a_reused_pid_by_number_alone() {
        let mut a = Activity::new();
        // pid 10 ran 0.4 s at t=2; the pid was reused at t=500 by a long-running process.
        a.push([
            ev(Kind::Start, 1.6, 10, 1, "git", "zsh", None),
            ev(Kind::Exit, 2.0, 10, 1, "git", "zsh", Some(400)),
            ev(Kind::Start, 500.0, 10, 1, "vim", "zsh", None),
        ]);
        a.short_only = true;
        let kept: Vec<(Kind, f64)> = a.shown().iter().map(|e| (e.kind, e.at)).collect();
        assert_eq!(kept, vec![(Kind::Exit, 2.0), (Kind::Start, 1.6)]);
    }

    #[test]
    fn own_readings_are_hidden_from_table_summary_and_storms_until_asked_for() {
        let mut a = Activity::new();
        let mut mine: Vec<Event> = (0..=STORM as u32)
            .map(|i| {
                ev(
                    Kind::Start,
                    100.0,
                    5000 + i,
                    77,
                    "spectra",
                    "Spectra Desktop",
                    None,
                )
            })
            .collect();
        mine.iter_mut().for_each(|e| e.own = true);
        a.push(mine);
        a.push([ev(Kind::Start, 100.0, 9, 1, "git", "zsh", None)]);
        assert_eq!(a.shown().len(), 1, "only the other process");
        assert_eq!(a.summary(101.0).starts, 1);
        assert!(a.findings(101.0).is_empty(), "no storm from own readings");
        a.show_own = true;
        assert_eq!(a.shown().len(), STORM + 2);
        assert_eq!(a.summary(101.0).starts, STORM + 2);
        assert_eq!(a.findings(101.0).len(), 1);
    }

    #[test]
    fn landing_marks_what_descends_from_this_app() {
        let mut s = crate::app::init();
        s.activity.me = 4000;
        s.activity.job = 1;
        s.activity.flight = Some(Flight {
            job: 1,
            interval: INTERVAL,
            cancel: Arc::new(AtomicBool::new(false)),
        });
        let evs = vec![
            ev(
                Kind::Start,
                1.0,
                4100,
                4000,
                "spectra",
                "Spectra Desktop",
                None,
            ),
            ev(Kind::Start, 1.1, 4101, 4100, "ps", "spectra", None),
            ev(Kind::Exit, 1.2, 4101, 4100, "ps", "spectra", Some(100)),
            ev(Kind::Start, 1.3, 9, 1, "git", "zsh", None),
        ];
        assert!(s.land_round(Round {
            job: 1,
            result: Ok(evs)
        }));
        let own: Vec<bool> = s.activity.events.iter().map(|e| e.own).collect();
        assert_eq!(own, [true, true, true, false]);
    }

    #[test]
    fn a_failed_round_says_why_and_retries_later() {
        let (mut s, fake) = faked(Some(Err("spectra spawns failed: collect: busy".into())));
        s.show(Nav::Activity);
        s.tick_activity();
        land(&mut s);
        assert_eq!(
            s.activity.error,
            Some(Fail::Failed("spectra spawns failed: collect: busy".into()))
        );
        assert!(!s.activity.recording(), "it rests before retrying");
        s.now += RETRY_FAILED;
        s.tick_activity();
        assert!(s.activity.recording(), "and retries the next round");
        wait_for("retried", || calls(&fake) == 2);
    }

    #[test]
    fn an_old_spectra_gets_a_notice_and_is_asked_again_much_later() {
        let (mut s, _) = faked(Some(Err(
            "spawns: stat spawns: no such file or directory".into()
        )));
        s.show(Nav::Activity);
        s.tick_activity();
        land(&mut s);
        assert!(matches!(s.activity.error, Some(Fail::Unsupported(_))));
        s.now += RETRY_FAILED;
        s.tick_activity();
        assert!(!s.activity.recording());
        s.now += RETRY_UNSUPPORTED;
        s.tick_activity();
        assert!(s.activity.recording());
        s.activity.cancel();
    }

    #[test]
    fn a_landed_event_picks_up_the_command_line_the_feed_lists() {
        let mut s = crate::app::init();
        crate::sample::land_sample_procs(&mut s);
        let p = s.process(1311).unwrap().clone();
        s.activity.job = 1;
        s.activity.flight = Some(Flight {
            job: 1,
            interval: INTERVAL,
            cancel: Arc::new(AtomicBool::new(false)),
        });
        let mut e = ev(Kind::Start, 1.0, p.pid, p.ppid, &p.name(), "zsh", None);
        e.chain.clear();
        let other = ev(Kind::Start, 1.0, p.pid + 99_000, 1, "ghost", "zsh", None);
        assert!(s.land_round(Round {
            job: 1,
            result: Ok(vec![e, other])
        }));
        assert_eq!(s.activity.events[0].command_line, p.full_command_line);
        assert_eq!(s.activity.events[1].command_line, "");
    }
}
