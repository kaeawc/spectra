//! "Right now" on the Overview: why this Mac may be slow at this moment, from
//! `spectra whatswrong --json`, Spectra's ranked look at the whole machine. Each cause becomes a
//! line in plain words with its evidence and the page that shows more; a cause that says what a
//! finding already says takes that finding's place instead of repeating it.
//!
//! It runs off the UI thread when the Overview opens, then at most every [`EVERY`] seconds while
//! the Overview stays on screen, and never while another page is.

use std::collections::HashSet;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::Deserialize;

use crate::app::{App, fmt_ago};
use crate::bundle::nullable;
use crate::findings::{Finding, Severity};
use crate::groups::Nav;
use crate::inspect::{self, Failure, Spectra};
use crate::procs::{Process, fmt_mem};

/// How long one look may take. It usually takes a few seconds; Spectra gives its own daemon one.
pub const DEADLINE: Duration = Duration::from_secs(20);
/// Seconds to rest after a look lands before the next, while the Overview is on screen.
pub const EVERY: f64 = 30.0;
/// The most causes shown.
pub const TOP: usize = 3;

/// What `spectra whatswrong --json` prints. Every field may be missing or `null`.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Report {
    /// Worst first.
    #[serde(deserialize_with = "nullable")]
    pub causes: Vec<Cause>,
    /// Groups of processes that work as one: a browser, a build, a simulator.
    #[serde(deserialize_with = "nullable")]
    pub families: Vec<Family>,
    #[serde(deserialize_with = "nullable")]
    pub trend: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Cause {
    /// `high`, `medium` or `low`.
    #[serde(deserialize_with = "nullable")]
    pub severity: String,
    #[serde(deserialize_with = "nullable")]
    pub title: String,
    #[serde(deserialize_with = "nullable")]
    pub detail: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Family {
    #[serde(deserialize_with = "nullable")]
    pub key: String,
    #[serde(deserialize_with = "nullable")]
    pub kind: String,
    #[serde(deserialize_with = "nullable")]
    pub name: String,
    #[serde(deserialize_with = "nullable")]
    pub members: u64,
    #[serde(deserialize_with = "nullable")]
    pub cpu_pct: f64,
    #[serde(deserialize_with = "nullable")]
    pub share_pct: f64,
    #[serde(deserialize_with = "nullable")]
    pub rss_kib: f64,
    #[serde(deserialize_with = "nullable")]
    pub age_seconds: i64,
    /// Its busiest members, busiest first.
    #[serde(deserialize_with = "nullable")]
    pub heaviest: Vec<Member>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Member {
    #[serde(deserialize_with = "nullable")]
    pub pid: u32,
    #[serde(deserialize_with = "nullable")]
    pub command: String,
    #[serde(deserialize_with = "nullable")]
    pub cpu_pct: f64,
}

pub fn parse(out: &str) -> Result<Report, String> {
    serde_json::from_str(out)
        .map_err(|e| format!("Spectra's answer didn't read as expected ({e})."))
}

pub fn args() -> Vec<String> {
    vec!["whatswrong".into(), "--json".into()]
}

/// Where a line's link goes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Action {
    Process(u32),
    /// An app's page, by its bundle's path.
    App(String),
    /// The Machine page.
    Machine,
}

/// One line of "Right now".
#[derive(Clone, Debug, PartialEq)]
pub struct Item {
    pub severity: Severity,
    pub title: String,
    pub evidence: String,
    /// What to do, from the finding it took the place of.
    pub fix: String,
    pub action: Action,
    /// Ids of the findings it says the same as, which the Findings list then leaves out.
    pub covers: Vec<String>,
}

fn severity(s: &str) -> Severity {
    match s {
        "high" | "critical" => Severity::Critical,
        "medium" => Severity::Warning,
        _ => Severity::Info,
    }
}

/// The first `(pid N)` in `detail`.
fn pid_in(detail: &str) -> Option<u32> {
    let rest = &detail[detail.find("pid ")? + 4..];
    let digits: String = rest.chars().take_while(char::is_ascii_digit).collect();
    digits.parse().ok().filter(|p| *p > 0)
}

fn family_of<'a>(c: &Cause, families: &'a [Family]) -> Option<&'a Family> {
    families
        .iter()
        .filter(|f| !f.name.is_empty())
        .find(|f| c.title.starts_with(&format!("{} uses ", f.name)))
}

/// A family's evidence in plain words: how many cores, how many processes, how much memory, and
/// who is busiest.
fn family_evidence(f: &Family) -> String {
    let mut out = format!(
        "{:.1} cores busy across {} processes, holding {}",
        f.cpu_pct / 100.0,
        f.members,
        fmt_mem(f.rss_kib)
    );
    let busy: Vec<String> = f
        .heaviest
        .iter()
        .filter(|m| m.cpu_pct >= 1.0 && !m.command.is_empty())
        .take(2)
        .map(|m| format!("{} at {:.0}%", m.command, m.cpu_pct))
        .collect();
    if !busy.is_empty() {
        out.push_str(&format!("; busiest: {}", busy.join(", ")));
    }
    out.push('.');
    out
}

/// Whether `p` is still the process Spectra named `command`: the same file name, allowing for
/// the 15 or 16 characters the kernel keeps of a long one. An empty name says nothing either way.
fn same_name(p: &Process, command: &str) -> bool {
    let base = |s: &str| {
        s.rsplit('/')
            .find(|n| !n.is_empty())
            .unwrap_or(s)
            .to_owned()
    };
    if command.is_empty() {
        return true;
    }
    let (a, b) = (base(&p.name()), base(command));
    a == b || (a.len().min(b.len()) >= 15 && (a.starts_with(&b) || b.starts_with(&a)))
}

/// The listed process `pid` when it still has the name Spectra gave it.
fn listed<'a>(procs: &'a [Process], pid: u32, command: &str) -> Option<&'a Process> {
    procs
        .iter()
        .find(|p| p.pid == pid)
        .filter(|p| same_name(p, command))
}

/// The page a family opens, going by its members still listed under the names Spectra gave,
/// busiest first: an app family opens the app of its busiest member that belongs to one; any
/// other family opens its busiest member, on its app's page when it belongs to one. Else the
/// Machine page.
fn family_action(f: &Family, procs: &[Process]) -> Action {
    let mut members: Vec<(&Member, &Process)> = f
        .heaviest
        .iter()
        .filter_map(|m| listed(procs, m.pid, &m.command).map(|p| (m, p)))
        .collect();
    members.sort_by(|a, b| b.0.cpu_pct.total_cmp(&a.0.cpu_pct));
    // Families that are an app and its helpers (Spectra's kinds; "app" is the samples'): open
    // the app, even when its busiest member is a helper with no bundle of its own.
    let app_like = matches!(
        f.kind.as_str(),
        "browser" | "xcode" | "simulator" | "emulator" | "app"
    );
    if app_like && let Some(path) = members.iter().find_map(|(_, p)| p.bundle_path()) {
        return Action::App(path.to_owned());
    }
    match members.first() {
        Some((_, p)) => match p.bundle_path() {
            Some(path) => Action::App(path.to_owned()),
            None => Action::Process(p.pid),
        },
        None => Action::Machine,
    }
}

/// The name a process cause gives before its `(pid N)`.
fn name_in(detail: &str) -> &str {
    let Some(at) = detail.find(" (pid ") else {
        return "";
    };
    let before = &detail[..at];
    before
        .rsplit("; ")
        .next()
        .unwrap_or(before)
        .rsplit(": ")
        .next()
        .unwrap_or(before)
        .trim()
}

/// The rule of the per-process finding a cause titled `title` says the same as. Holding a lot
/// of memory is about size, not growth, so it repeats none.
fn process_rule(title: &str) -> Option<&'static str> {
    let t = title.to_lowercase();
    if t.contains("cpu") {
        Some("proc-busy-cpu")
    } else if t.contains("memory") && (t.contains("grow") || t.contains("leak")) {
        Some("proc-memory-growth")
    } else {
        None
    }
}

/// A Go duration (`3h0m0s`, `12m0s`, `1.5s`) in seconds.
fn go_duration(s: &str) -> Option<f64> {
    let (mut total, mut num, mut any) = (0.0, String::new(), false);
    for ch in s.chars() {
        if ch.is_ascii_digit() || ch == '.' {
            num.push(ch);
            continue;
        }
        let n: f64 = num.parse().ok()?;
        num.clear();
        total += n * match ch {
            'h' => 3600.0,
            'm' => 60.0,
            's' => 1.0,
            _ => return None,
        };
        any = true;
    }
    (any && num.is_empty()).then_some(total)
}

/// How long ago an RFC 3339 time was, from `wall` (Unix seconds).
fn ago(stamp: &str, wall: i64) -> Option<String> {
    crate::spawns::unix_time(stamp).map(|t| fmt_ago(wall as f64 - t))
}

/// One part of a detail in plain words, or `None` to leave it out.
fn plain_part(part: &str, wall: i64) -> Option<String> {
    let part = part.trim();
    if part.is_empty()
        || part.starts_with("next: ")
        || part.starts_with("sample ")
        || part.starts_with("thread summary")
    {
        return None;
    }
    if let Some(chain) = part.strip_prefix("owner chain: ") {
        // Outermost first, ending with the process itself.
        let mut names: Vec<&str> = chain
            .split(" > ")
            .map(|l| l.rfind('(').map_or(l, |i| &l[..i]).trim())
            .filter(|n| !n.is_empty())
            .collect();
        names.pop();
        let parent = names.pop()?;
        names.reverse();
        return Some(if names.is_empty() {
            format!("started by {parent}")
        } else {
            format!("started by {parent} (under {})", names.join(", "))
        });
    }
    if let Some(hint) = part.strip_prefix("hint: ") {
        return Some(hint.to_owned());
    }
    if let Some(up) = part.strip_prefix("up ") {
        let started = up
            .split_once("(started ")
            .and_then(|(_, r)| ago(r.trim_end_matches(')'), wall));
        let dur = up
            .split_whitespace()
            .next()
            .and_then(go_duration)
            .map(fmt_ago);
        return started.or(dur).map(|d| format!("running for {d}"));
    }
    let words: Vec<&str> = part.split(' ').collect();
    let mut out: Vec<String> = Vec::new();
    let mut i = 0;
    while i < words.len() {
        let w = words[i];
        if w == "since"
            && let Some(a) = words.get(i + 1).and_then(|t| ago(t, wall))
        {
            // "for 3h0m0s since <time>" says one thing twice.
            if out.len() >= 2 && out[out.len() - 2] == "for" {
                out.truncate(out.len() - 2);
            }
            out.push(format!("for the last {a}"));
            i += 2;
            continue;
        }
        out.push(match (ago(w, wall), go_duration(w)) {
            (Some(a), _) => format!("{a} ago"),
            (None, Some(d)) if w.chars().any(|c| c.is_ascii_alphabetic()) => fmt_ago(d),
            _ => w.to_owned(),
        });
        i += 1;
    }
    Some(out.join(" "))
}

/// A cause's detail in plain words: times as how long ago, the owner chain as who started it,
/// and no command lines to run, which Spectra's daemon adds for people at a terminal.
pub fn plain(detail: &str, wall: i64) -> String {
    let detail = match detail.find(" [probe unavailable") {
        Some(i) => &detail[..i],
        None => detail,
    };
    detail
        .split("; ")
        .filter_map(|p| plain_part(p, wall))
        .map(|p| short_name(&p))
        .collect::<Vec<_>>()
        .join("; ")
}

/// `/System/.../mds_stores (pid 412) averaged …` as `mds_stores (pid 412) averaged …`: Spectra
/// names a process by `ps -o comm=`, which on macOS is its whole path.
fn short_name(part: &str) -> String {
    let Some(at) = part.find(" (pid ") else {
        return part.to_owned();
    };
    let (name, rest) = part.split_at(at);
    match name.rfind('/') {
        Some(slash) if name.starts_with('/') => format!("{}{rest}", &name[slash + 1..]),
        _ => part.to_owned(),
    }
}

/// The rules of machine-wide findings a cause titled `title` repeats.
fn machine_rules(title: &str) -> &'static [&'static str] {
    if title.starts_with("Memory pressure") || title.starts_with("Heavy swap") {
        &["sys-memory-pressure", "sys-swap-growing"]
    } else if title.starts_with("CPU is thermally") || title.starts_with("Thermal pressure") {
        &["sys-thermal-throttled"]
    } else {
        &[]
    }
}

/// The lines to show, worst first and at most [`TOP`]: each cause in plain words with the page
/// that shows more, and the ids of the `findings` it repeats. A process cause about the busiest
/// member of a family already shown is the same news and is left out. `wall` is Unix seconds now,
/// for saying how long ago the times in a detail were.
pub fn items(r: &Report, findings: &[Finding], procs: &[Process], wall: i64) -> Vec<Item> {
    let ids: HashSet<String> = findings.iter().map(Finding::id).collect();
    let mut shown_pids: HashSet<u32> = HashSet::new();
    let mut out: Vec<Item> = Vec::new();
    for c in &r.causes {
        if c.title.is_empty() && c.detail.is_empty() {
            continue;
        }
        let (evidence, action, mut covers) = if let Some(f) = family_of(c, &r.families) {
            shown_pids.extend(f.heaviest.iter().map(|m| m.pid));
            let covers = f
                .heaviest
                .iter()
                .map(|m| format!("{}/proc-busy-cpu", m.pid))
                .collect::<Vec<_>>();
            (family_evidence(f), family_action(f, procs), covers)
        } else if let Some(pid) = pid_in(&c.detail) {
            if shown_pids.contains(&pid) {
                continue;
            }
            shown_pids.insert(pid);
            let name = name_in(&c.detail);
            // A pid the list now gives a different name is another process: no link, and not
            // the same news as that process's findings.
            let reused = procs.iter().any(|p| p.pid == pid && !same_name(p, name));
            let action = match listed(procs, pid, name) {
                Some(_) => Action::Process(pid),
                None => Action::Machine,
            };
            let covers = process_rule(&c.title)
                .filter(|_| !reused)
                .map(|rule| format!("{pid}/{rule}"))
                .into_iter()
                .collect();
            (plain(&c.detail, wall), action, covers)
        } else {
            let covers = machine_rules(&c.title)
                .iter()
                .map(|r| format!("0/{r}"))
                .collect();
            (plain(&c.detail, wall), Action::Machine, covers)
        };
        covers.retain(|id| ids.contains(id));
        let fix = covers
            .iter()
            .find_map(|id| findings.iter().find(|f| f.id() == *id))
            .map(|f| f.fix.clone())
            .unwrap_or_default();
        out.push(Item {
            severity: severity(&c.severity),
            title: if c.title.is_empty() {
                plain(&c.detail, wall)
            } else {
                c.title.clone()
            },
            evidence: if c.title.is_empty() {
                String::new()
            } else {
                evidence
            },
            fix,
            action,
            covers,
        });
        if out.len() == TOP {
            break;
        }
    }
    out
}

/// What a look hands back, numbered by the job it was.
pub struct Landed {
    pub job: u64,
    pub result: Result<Report, String>,
}

pub type Work = Box<dyn FnOnce() -> Landed + Send>;

/// A look in flight.
struct Flight {
    job: u64,
    cancel: Arc<AtomicBool>,
}

pub struct WhatsWrong {
    /// The last look that worked, and the window clock when it landed.
    pub report: Option<(Report, f64)>,
    /// Why the last look failed, while no newer one worked.
    pub error: Option<String>,
    flight: Option<Flight>,
    /// The window clock when the next look may start.
    next: f64,
    job: u64,
    pub spectra: Arc<dyn Spectra>,
    /// Tests hold the work here and run it themselves; `None` runs it on a thread.
    pub held: Option<Arc<Mutex<Vec<Work>>>>,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
    /// Whether looks start on their own. Off until the window opens, so tests and sample scenes
    /// never run Spectra.
    pub auto: bool,
    /// The age line as last drawn, to redraw when it changes.
    shown: Option<String>,
}

impl WhatsWrong {
    pub fn new() -> WhatsWrong {
        let (tx, rx) = channel();
        WhatsWrong {
            report: None,
            error: None,
            flight: None,
            next: 0.0,
            job: 0,
            spectra: Arc::new(inspect::System),
            held: None,
            tx,
            rx,
            auto: false,
            shown: None,
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
}

impl App {
    /// Whether the Overview is what's on screen.
    pub fn overview_visible(&self) -> bool {
        self.nav == Nav::Overview && !self.on_page()
    }

    /// "Right now"'s lines, as of the last look and the findings now.
    pub fn right_now(&self) -> Vec<Item> {
        self.whatswrong
            .report
            .as_ref()
            .map(|(r, _)| items(r, &self.findings, &self.procs, (self.wall)()))
            .unwrap_or_default()
    }

    /// How old the last look is, in a line, with what's happening since.
    pub fn right_now_age(&self) -> Option<String> {
        let w = &self.whatswrong;
        let (_, at) = w.report.as_ref()?;
        let age = self.now - at;
        let mut line = if age < 2.0 {
            "Checked just now".to_owned()
        } else {
            format!("Checked {} ago", fmt_ago(age))
        };
        if w.running() {
            line.push_str(" · checking again");
        } else if let Some(e) = &w.error {
            line.push_str(&format!(
                " · the last check failed: {}",
                e.trim_end_matches('.')
            ));
        }
        Some(line)
    }

    /// Lands finished looks, stops one in flight once the Overview is off screen, and starts the
    /// next when the Overview is on screen and it's due. While the window is hidden a look in
    /// flight may finish, but none starts and the age line isn't redrawn. True when anything
    /// shown changed.
    pub fn tick_whatswrong(&mut self) -> bool {
        let mut changed = false;
        while let Ok(l) = self.whatswrong.rx.try_recv() {
            changed |= self.land_whatswrong(l);
        }
        let visible = self.overview_visible();
        if !visible && self.whatswrong.running() {
            self.whatswrong.cancel();
            changed = true;
        }
        let shown = self.window_shown();
        let w = &self.whatswrong;
        if shown && visible && w.auto && !w.running() && self.now >= w.next {
            self.start_whatswrong();
            changed = true;
        }
        // The age line is only redrawn while it's on screen, and only when its words change.
        if visible && shown {
            let age = self.right_now_age();
            if age != self.whatswrong.shown {
                self.whatswrong.shown = age;
                changed = true;
            }
        } else {
            self.whatswrong.shown = None;
        }
        changed
    }

    /// Starts a look now.
    pub fn start_whatswrong(&mut self) {
        let w = &mut self.whatswrong;
        w.job += 1;
        let job = w.job;
        let cancel = Arc::new(AtomicBool::new(false));
        w.flight = Some(Flight {
            job,
            cancel: Arc::clone(&cancel),
        });
        let spectra = Arc::clone(&w.spectra);
        let work: Work = Box::new(move || {
            let result = match spectra.run(&args(), DEADLINE, &cancel) {
                Ok(out) => parse(&out),
                Err(Failure::Cancelled) => Err("Stopped.".into()),
                Err(Failure::Failed(why)) => Err(why),
            };
            Landed { job, result }
        });
        if let Some(q) = &w.held {
            q.lock().expect("the held work").push(work);
            return;
        }
        let (tx, wake) = (w.tx.clone(), self.wake.clone());
        let spawned = std::thread::Builder::new()
            .name("spectra-whatswrong".into())
            .spawn({
                let tx = tx.clone();
                let wake = wake.clone();
                move || {
                    let landed = std::panic::catch_unwind(std::panic::AssertUnwindSafe(work))
                        .unwrap_or_else(|_| Landed {
                            job,
                            result: Err("Spectra Desktop hit an internal error reading it.".into()),
                        });
                    let _ = tx.send(landed);
                    if let Some(wake) = wake {
                        wake();
                    }
                }
            });
        if spawned.is_err() {
            let _ = tx.send(Landed {
                job,
                result: Err("Couldn't start a thread to ask Spectra.".into()),
            });
        }
    }

    /// Lands a look, if it's the one in flight. True when it was.
    pub fn land_whatswrong(&mut self, l: Landed) -> bool {
        let w = &mut self.whatswrong;
        if w.flight.as_ref().is_none_or(|f| f.job != l.job) {
            return false;
        }
        w.flight = None;
        w.next = self.now + EVERY;
        match l.result {
            Ok(r) => {
                w.report = Some((r, self.now));
                w.error = None;
            }
            Err(why) => w.error = Some(why),
        }
        true
    }

    /// Runs the looks a test is holding, on this thread, and lands them.
    #[cfg(test)]
    pub fn run_held_whatswrong(&mut self) {
        let work = match &self.whatswrong.held {
            Some(q) => std::mem::take(&mut *q.lock().unwrap()),
            None => Vec::new(),
        };
        for w in work {
            self.land_whatswrong(w());
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use crate::findings::{Chart, Link};
    use crate::groups::tests::{app, proc};
    use crate::inspect::tests::Fake;
    use crate::procs::Role;

    pub const BUSY: &str = include_str!("../tests/fixtures/whatswrong/busy.json");
    pub const CALM: &str = include_str!("../tests/fixtures/whatswrong/calm.json");
    /// A report with an alert from Spectra's daemon: a process on high CPU for hours.
    pub const SUSTAINED: &str = include_str!("../tests/fixtures/whatswrong/sustained.json");

    fn finding(pid: u32, rule: &'static str, fix: &str) -> Finding {
        Finding {
            rule,
            pid,
            subject: "x".into(),
            severity: Severity::Warning,
            title: rule.into(),
            detail: String::new(),
            fix: fix.into(),
            chart: Chart::None,
            link: if pid == 0 {
                Link::System
            } else {
                Link::Process(pid)
            },
        }
    }

    fn cause(severity: &str, title: &str, detail: &str) -> Cause {
        Cause {
            severity: severity.into(),
            title: title.into(),
            detail: detail.into(),
        }
    }

    #[test]
    fn the_captured_report_reads() {
        let r = parse(BUSY).unwrap();
        assert_eq!(r.causes.len(), 2);
        assert_eq!(r.causes[0].severity, "medium");
        assert_eq!(r.causes[0].title, "Build tools uses 52% of machine CPU");
        assert_eq!(r.families.len(), 4);
        let build = &r.families[0];
        assert_eq!(
            (build.kind.as_str(), build.members, build.heaviest[0].pid),
            ("build", 2, 18523)
        );
        assert_eq!(r.families[3].name, "Google Chrome");
        assert!(r.trend.starts_with("load 5.80"));
    }

    #[test]
    fn missing_fields_and_nulls_read_as_empty() {
        let r = parse(CALM).unwrap();
        assert!(r.causes.is_empty(), "Go writes an empty list as null");
        assert!(r.families[0].heaviest.is_empty());
        assert_eq!(parse("{}").unwrap(), Report::default());
        let r = parse(r#"{"causes":[{"severity":null,"title":"Memory pressure is elevated","detail":null}],"families":[{"name":"X","cpu_pct":null,"heaviest":[{"pid":null}]}],"trend":null}"#).unwrap();
        assert_eq!(r.causes[0], cause("", "Memory pressure is elevated", ""));
        assert_eq!(r.families[0].heaviest[0].pid, 0);
        assert!(parse("not json").unwrap_err().contains("didn't read"));
    }

    #[test]
    fn a_family_and_its_busiest_process_are_one_line() {
        let r = parse(BUSY).unwrap();
        let procs = vec![
            proc(18523, "java", 786.0, 1400.0),
            proc(12201, "java", 47.0, 2000.0),
        ];
        let got = items(&r, &[], &procs, 0);
        assert_eq!(
            got.len(),
            1,
            "the heavy-CPU java is the build's busiest member: {got:?}"
        );
        assert_eq!(got[0].title, "Build tools uses 52% of machine CPU");
        assert_eq!(got[0].severity, Severity::Warning);
        assert_eq!(got[0].action, Action::Process(18523));
        assert_eq!(
            got[0].evidence,
            "8.3 cores busy across 2 processes, holding 3.5 GB; busiest: java at 786%, java at 48%."
        );
    }

    #[test]
    fn a_family_inside_an_app_opens_the_app() {
        let f = Family {
            name: "Google Chrome".into(),
            members: 9,
            cpu_pct: 420.0,
            heaviest: vec![Member {
                pid: 60,
                command: "Google Chrome Helper (Renderer)".into(),
                cpu_pct: 300.0,
            }],
            ..Family::default()
        };
        let r = Report {
            causes: vec![cause("medium", "Google Chrome uses 42% of machine CPU", "")],
            families: vec![f],
            ..Report::default()
        };
        let helper = app(
            proc(60, "Google Chrome Helper (Renderer)", 300.0, 500.0),
            "Google Chrome",
            "/Applications/Google Chrome.app",
            Role::AppHelper,
        );
        assert_eq!(
            items(&r, &[], &[helper], 0)[0].action,
            Action::App("/Applications/Google Chrome.app".into())
        );
        assert_eq!(
            items(&r, &[], &[], 0)[0].action,
            Action::Machine,
            "nobody listed to open"
        );
    }

    #[test]
    fn a_family_opens_its_busiest_member_not_the_first_with_an_app() {
        let f = Family {
            name: "Build tools".into(),
            heaviest: vec![
                Member {
                    pid: 70,
                    command: "Code Helper".into(),
                    cpu_pct: 20.0,
                },
                Member {
                    pid: 71,
                    command: "java".into(),
                    cpu_pct: 400.0,
                },
            ],
            ..Family::default()
        };
        let r = Report {
            causes: vec![cause("medium", "Build tools uses 42% of machine CPU", "")],
            families: vec![f],
            ..Report::default()
        };
        let helper = app(
            proc(70, "Code Helper", 20.0, 500.0),
            "Visual Studio Code",
            "/Applications/Visual Studio Code.app",
            Role::AppHelper,
        );
        let java = proc(71, "java", 400.0, 900.0);
        assert_eq!(
            items(&r, &[], &[helper.clone(), java], 0)[0].action,
            Action::Process(71),
            "java is the busiest"
        );
        // An app's family opens the app, even when a child outside it is busier.
        let mut app_family = r.clone();
        app_family.families[0].kind = "browser".into();
        assert_eq!(
            items(
                &app_family,
                &[],
                &[helper.clone(), proc(71, "java", 400.0, 900.0)],
                0
            )[0]
            .action,
            Action::App("/Applications/Visual Studio Code.app".into())
        );
        // pid 71 now belongs to something else: the busiest that still matches is the helper.
        let other = proc(71, "zsh", 0.0, 10.0);
        assert_eq!(
            items(&r, &[], &[helper, other], 0)[0].action,
            Action::App("/Applications/Visual Studio Code.app".into())
        );
    }

    #[test]
    fn a_pid_that_now_names_another_process_is_not_linked() {
        let r = Report {
            causes: vec![cause(
                "medium",
                "A process is using heavy CPU",
                "rust-analyzer (pid 1300) at 138% CPU",
            )],
            ..Report::default()
        };
        let findings = vec![finding(1300, "proc-busy-cpu", "")];
        let got = items(&r, &findings, &[proc(1300, "zsh", 0.0, 10.0)], 0);
        assert_eq!(got[0].action, Action::Machine);
        assert!(got[0].covers.is_empty(), "zsh's findings aren't this news");
        let got = items(
            &r,
            &findings,
            &[proc(1300, "rust-analyzer", 138.0, 900.0)],
            0,
        );
        assert_eq!(got[0].action, Action::Process(1300));
    }

    #[test]
    fn a_process_named_by_its_path_reads_by_its_name() {
        assert_eq!(
            plain(
                "/System/Library/Frameworks/CoreServices.framework/Support/mds_stores (pid 412) averaged 96% CPU",
                0
            ),
            "mds_stores (pid 412) averaged 96% CPU"
        );
        assert_eq!(
            plain("java (pid 7) at 138% CPU", 0),
            "java (pid 7) at 138% CPU"
        );
    }

    #[test]
    fn holding_memory_does_not_hide_memory_growth() {
        let r = Report {
            causes: vec![cause(
                "low",
                "A process is holding a lot of memory",
                "java (pid 4102) at 4.1 GB RSS",
            )],
            ..Report::default()
        };
        let findings = vec![finding(4102, "proc-memory-growth", "Restart it.")];
        let got = items(&r, &findings, &[proc(4102, "java", 4.0, 4200.0)], 0);
        assert!(got[0].covers.is_empty(), "size isn't growth: {got:?}");
        assert!(got[0].fix.is_empty());
    }

    #[test]
    fn a_daemon_alert_reads_in_plain_words() {
        let r = parse(SUSTAINED).unwrap();
        let wall = crate::spawns::unix_time("2026-11-22T10:00:00Z").unwrap() as i64;
        let procs = vec![proc(412, "mds_stores", 87.0, 300.0)];
        let findings = vec![finding(412, "proc-busy-cpu", "Open it.")];
        let got = items(&r, &findings, &procs, wall);
        assert_eq!(got[0].title, "mds_stores sustained high CPU");
        assert_eq!(
            got[0].evidence,
            "mds_stores (pid 412) averaged 87% CPU for the last 3 h; running for 5 h; \
             started by mds (under launchd); Spotlight is re-indexing or a client keeps \
             triggering indexing; check `mdutil -s /` and recently changed large directories"
        );
        assert_eq!(got[0].action, Action::Process(412));
        assert_eq!(got[0].covers, ["412/proc-busy-cpu"]);
        for raw in [
            "next:",
            "sample 412",
            "2026-",
            "owner chain",
            "3h0m0s",
            "probe",
        ] {
            assert!(!got[0].evidence.contains(raw), "{raw} left in");
        }
        // Without a start time the age Spectra gave is used.
        assert_eq!(
            plain("up 1h30m0s; owner chain: launchd(1) > x(9)", wall),
            "running for 1 h; started by launchd"
        );
    }

    #[test]
    fn a_cause_that_repeats_a_finding_takes_its_place_and_its_fix() {
        let r = Report {
            causes: vec![
                cause(
                    "high",
                    "Memory pressure is critical",
                    "swap in use: 9000 MB of 10240 MB",
                ),
                cause(
                    "medium",
                    "A process is using heavy CPU",
                    "rust-analyzer (pid 1300) at 138% CPU",
                ),
                cause(
                    "low",
                    "A process is holding a lot of memory",
                    "java (pid 4102) at 4.1 GB RSS",
                ),
                cause("low", "Something else", "no pid here"),
            ],
            ..Report::default()
        };
        let findings = vec![
            finding(0, "sys-memory-pressure", "Quit what you aren't using."),
            finding(1300, "proc-busy-cpu", "Open it."),
            finding(4102, "proc-busy-cpu", ""),
            finding(0, "sys-volume-full", ""),
        ];
        let procs = vec![
            proc(1300, "rust-analyzer", 138.0, 1100.0),
            proc(4102, "java", 4.0, 4200.0),
        ];
        let got = items(&r, &findings, &procs, 0);
        assert_eq!(got.len(), TOP, "at most three");
        assert_eq!(got[0].severity, Severity::Critical);
        assert_eq!(got[0].covers, ["0/sys-memory-pressure"]);
        assert_eq!(got[0].fix, "Quit what you aren't using.");
        assert_eq!(got[0].action, Action::Machine);
        assert_eq!(got[1].covers, ["1300/proc-busy-cpu"]);
        assert_eq!(got[1].action, Action::Process(1300));
        assert!(
            got[2].covers.is_empty(),
            "holding memory isn't the same as busy CPU"
        );
        assert_eq!(got[2].action, Action::Process(4102));
    }

    #[test]
    fn a_family_covers_its_members_busy_findings() {
        let r = parse(BUSY).unwrap();
        let findings = vec![
            finding(18523, "proc-busy-cpu", "Look at it."),
            finding(7, "proc-busy-cpu", ""),
        ];
        let got = items(&r, &findings, &[], 0);
        assert_eq!(got[0].covers, ["18523/proc-busy-cpu"]);
        assert_eq!(got[0].fix, "Look at it.");
    }

    #[test]
    fn nothing_wrong_is_no_lines() {
        assert!(
            items(
                &parse(CALM).unwrap(),
                &[finding(0, "sys-volume-full", "")],
                &[],
                0
            )
            .is_empty()
        );
    }

    fn faked(reply: Result<&'static str, &'static str>) -> (App, Arc<Fake>) {
        let mut s = crate::app::init();
        let fake = Arc::new(Fake::with(&[("whatswrong", reply)]));
        s.whatswrong.spectra = fake.clone();
        s.whatswrong.held = Some(Arc::new(Mutex::new(Vec::new())));
        s.whatswrong.auto = true;
        (s, fake)
    }

    #[test]
    fn it_looks_when_the_overview_opens_then_every_30_s_while_it_shows() {
        let (mut s, fake) = faked(Ok(BUSY));
        s.now = 100.0;
        assert!(s.tick_whatswrong());
        assert!(s.whatswrong.running());
        s.tick_whatswrong();
        s.run_held_whatswrong();
        assert_eq!(fake.calls(), [args()], "one look, not one per tick");
        assert_eq!(s.right_now_age().as_deref(), Some("Checked just now"));
        s.now = 112.0;
        s.tick_whatswrong();
        assert_eq!(s.right_now_age().as_deref(), Some("Checked 12 s ago"));
        assert!(!s.whatswrong.running(), "not due yet");
        s.now = 130.0;
        s.tick_whatswrong();
        assert!(s.whatswrong.running());
        assert_eq!(
            s.right_now_age().as_deref(),
            Some("Checked 30 s ago · checking again")
        );
    }

    #[test]
    fn it_never_looks_while_another_page_shows_and_stops_a_look_when_one_opens() {
        let (mut s, fake) = faked(Ok(BUSY));
        s.show(Nav::All);
        s.tick_whatswrong();
        assert!(!s.whatswrong.running());
        s.show(Nav::Overview);
        s.tick_whatswrong();
        assert!(s.whatswrong.running());
        s.show(Nav::System);
        s.tick_whatswrong();
        assert!(!s.whatswrong.running(), "leaving the Overview stops it");
        s.run_held_whatswrong();
        assert!(
            s.whatswrong.report.is_none(),
            "what the stopped look said is dropped"
        );
        assert_eq!(fake.calls().len(), 1);
        // Back on the Overview it looks at once: nothing landed, so nothing is fresh.
        s.show(Nav::Overview);
        s.tick_whatswrong();
        assert!(s.whatswrong.running());
    }

    #[test]
    fn the_age_line_redraws_only_on_the_overview_and_only_when_its_words_change() {
        let (mut s, _) = faked(Ok(BUSY));
        s.whatswrong.auto = false;
        s.whatswrong.report = Some((parse(BUSY).unwrap(), 0.0));
        s.now = 10.0;
        assert!(s.tick_whatswrong(), "the first age drawn");
        s.now = 10.3;
        assert!(!s.tick_whatswrong(), "still 10 s ago");
        s.show(Nav::All);
        s.now = 20.0;
        assert!(!s.tick_whatswrong(), "the Overview isn't showing");
        s.now = 25.0;
        assert!(!s.tick_whatswrong());
        s.show(Nav::Overview);
        assert!(
            s.tick_whatswrong(),
            "back on the Overview it is drawn again"
        );
    }

    #[test]
    fn a_failed_look_says_why_and_keeps_the_last_good_one() {
        let (mut s, _) = faked(Err("spectra took longer than 20 s and was stopped"));
        s.tick_whatswrong();
        s.run_held_whatswrong();
        assert!(s.whatswrong.report.is_none());
        assert_eq!(
            s.whatswrong.error.as_deref(),
            Some("spectra took longer than 20 s and was stopped")
        );
        s.whatswrong.report = Some((parse(BUSY).unwrap(), 0.0));
        s.now = 40.0;
        assert_eq!(
            s.right_now_age().as_deref(),
            Some(
                "Checked 40 s ago · the last check failed: spectra took longer than 20 s and was stopped"
            )
        );
    }

    #[test]
    fn no_look_starts_while_the_window_is_hidden_and_one_starts_once_shown() {
        let (mut s, fake) = faked(Ok(BUSY));
        s.now = 100.0;
        s.tick_whatswrong();
        // A look in flight when the window hides may finish.
        s.visible = false;
        s.tick_whatswrong();
        s.run_held_whatswrong();
        s.tick_whatswrong();
        assert!(s.whatswrong.report.is_some(), "the look in flight landed");
        for t in 101..200 {
            s.now = f64::from(t);
            assert!(!s.tick_whatswrong(), "nothing drawn while hidden");
            assert!(!s.whatswrong.running(), "no look starts while hidden");
        }
        assert_eq!(fake.calls().len(), 1);
        s.visible = true;
        assert!(s.tick_whatswrong());
        assert!(s.whatswrong.running(), "overdue, so it looks at once");
    }

    #[test]
    fn nothing_runs_until_the_window_turns_it_on() {
        let mut s = crate::app::init();
        let fake = Arc::new(Fake::with(&[("whatswrong", Ok(BUSY))]));
        s.whatswrong.spectra = fake.clone();
        s.whatswrong.held = Some(Arc::new(Mutex::new(Vec::new())));
        s.tick_whatswrong();
        s.run_held_whatswrong();
        assert!(fake.calls().is_empty());
    }

    /// A live run against the real Spectra: `SPECTRA_BIN=... cargo test -- --ignored`.
    #[test]
    #[ignore]
    fn live_whatswrong_reads() {
        let out = inspect::System
            .run(&args(), DEADLINE, &AtomicBool::new(false))
            .unwrap();
        parse(&out).unwrap();
    }
}
