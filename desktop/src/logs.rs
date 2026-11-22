//! A page's Logs: recent unified-log entries for its process, from `spectra logs`. The person
//! picks how far back and the lowest level, presses Show logs, and can Follow: a short query
//! every few seconds while the page is open, merged in at the top. Nothing runs on its own, and
//! leaving the page or quitting stops whatever is in flight.
//!
//! Spectra matches by process name, not pid. `--top` keeps the earliest entries of the window,
//! so the query asks for more than the page keeps and keeps the newest.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use rune::TextEdit;
use serde::Deserialize;

use crate::app::App;
use crate::inspect::{Failure, Landed, RunState, Subject};
use crate::procs::Process;
use crate::tools::Report;

/// The most entries a page keeps.
pub const MAX_ROWS: usize = 500;
/// The most entries Spectra is asked for; it keeps the earliest, the page the newest.
pub const QUERY_TOP: usize = 2000;
/// The most a query may print: 2,000 entries are about 1.5 MB.
pub const MAX_OUTPUT: usize = 8 << 20;
/// How long a query may take; `log show` scans the whole window.
pub const DEADLINE: Duration = Duration::from_secs(30);
/// How far back a Follow round looks; rounds overlap and only newer entries are added.
pub const FOLLOW_LAST: &str = "1m";
/// How long Follow rests after a round lands, in seconds.
pub const FOLLOW_REST: f64 = 5.0;

/// How far back a query looks.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Window {
    #[default]
    Min5,
    Min15,
    Hour,
}

impl Window {
    pub const ALL: [Window; 3] = [Window::Min5, Window::Min15, Window::Hour];

    pub fn label(self) -> &'static str {
        match self {
            Window::Min5 => "5 min",
            Window::Min15 => "15 min",
            Window::Hour => "1 h",
        }
    }

    /// Spectra's `--last`.
    pub fn arg(self) -> &'static str {
        match self {
            Window::Min5 => "5m",
            Window::Min15 => "15m",
            Window::Hour => "1h",
        }
    }

    pub fn words(self) -> &'static str {
        match self {
            Window::Min5 => "the last 5 minutes",
            Window::Min15 => "the last 15 minutes",
            Window::Hour => "the last hour",
        }
    }
}

/// The lowest level a query keeps.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Level {
    #[default]
    All,
    Error,
    Fault,
}

impl Level {
    pub const ALL: [Level; 3] = [Level::All, Level::Error, Level::Fault];

    pub fn label(self) -> &'static str {
        match self {
            Level::All => "All",
            Level::Error => "Error",
            Level::Fault => "Fault",
        }
    }

    /// Spectra's `--level`, when there is one.
    pub fn arg(self) -> Option<&'static str> {
        match self {
            Level::All => None,
            Level::Error => Some("Error"),
            Level::Fault => Some("Fault"),
        }
    }

    /// Whether an entry of `kind` is at this level or above.
    pub fn admits(self, kind: Kind) -> bool {
        match self {
            Level::All => true,
            Level::Error => matches!(kind, Kind::Error | Kind::Fault),
            Level::Fault => kind == Kind::Fault,
        }
    }

    pub fn words(self) -> &'static str {
        match self {
            Level::All => "",
            Level::Error => " at Error or Fault",
            Level::Fault => " at Fault",
        }
    }
}

/// An entry's level, or what kind of event it is when it has none.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Debug,
    Info,
    Default,
    Error,
    Fault,
    /// An activity starting: it has a name but no level.
    Activity,
    Other,
}

impl Kind {
    fn of(message_type: &str, event_type: &str) -> Kind {
        match message_type {
            "Debug" => Kind::Debug,
            "Info" => Kind::Info,
            "Default" => Kind::Default,
            "Error" => Kind::Error,
            "Fault" => Kind::Fault,
            _ if event_type.starts_with("activity") => Kind::Activity,
            _ => Kind::Other,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Kind::Debug => "Debug",
            Kind::Info => "Info",
            Kind::Default => "Default",
            Kind::Error => "Error",
            Kind::Fault => "Fault",
            Kind::Activity => "Activity",
            Kind::Other => "Other",
        }
    }

    /// Errors and faults stand out.
    pub fn loud(self) -> bool {
        matches!(self, Kind::Error | Kind::Fault)
    }
}

/// One entry, as the page shows it.
#[derive(Clone, Debug, PartialEq)]
pub struct Entry {
    /// RFC 3339, as Spectra prints it.
    pub timestamp: String,
    pub pid: u32,
    pub kind: Kind,
    pub subsystem: String,
    pub category: String,
    /// The library or executable that logged it.
    pub sender: String,
    pub message: String,
}

impl Entry {
    /// The time of day, to the millisecond: `23:19:24.215`.
    pub fn clock(&self) -> &str {
        // `2026-11-21 23:19:24.215-0500` or `2026-11-21T10:00:00-05:00`: the time runs from
        // character 11 up to the zone offset, and the fraction is optional.
        let rest = self.timestamp.get(11..).unwrap_or(&self.timestamp);
        let end = rest
            .find(|c: char| !(c.is_ascii_digit() || c == ':' || c == '.'))
            .unwrap_or(rest.len());
        let time = &rest[..end];
        // Keep milliseconds at most.
        time.get(..12).unwrap_or(time)
    }

    /// Where it came from: subsystem and category, else the sender.
    pub fn source(&self) -> String {
        match (self.subsystem.as_str(), self.category.as_str()) {
            ("", "") => self.sender.clone(),
            (s, "") => s.to_owned(),
            ("", c) => c.to_owned(),
            (s, c) => format!("{s} · {c}"),
        }
    }

    /// Whether `query` (any case) is in its message, source or level.
    pub fn matches(&self, query: &str) -> bool {
        let q = query.trim().to_lowercase();
        q.is_empty()
            || [
                self.message.as_str(),
                self.subsystem.as_str(),
                self.category.as_str(),
                self.sender.as_str(),
                self.kind.label(),
            ]
            .iter()
            .any(|f| f.to_lowercase().contains(&q))
    }
}

/// A query's answer.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct LogsReport {
    /// Newest first, at most [`MAX_ROWS`].
    pub entries: Vec<Entry>,
    /// Spectra stopped at [`QUERY_TOP`]: the window holds more, and these are its earliest.
    pub truncated: bool,
    /// Entries left out because another process of the same name logged them.
    pub others: usize,
    /// A Follow round came back cut off at [`QUERY_TOP`], so entries between rounds are missing.
    pub follow_gaps: bool,
}

impl LogsReport {
    /// The entries `query` matches, newest first.
    pub fn shown(&self, query: &str) -> Vec<&Entry> {
        self.entries.iter().filter(|e| e.matches(query)).collect()
    }
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Raw {
    entries: Option<Vec<RawEntry>>,
    truncated: Option<bool>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawEntry {
    timestamp: Option<String>,
    pid: Option<u32>,
    sender: Option<String>,
    subsystem: Option<String>,
    category: Option<String>,
    event_type: Option<String>,
    message_type: Option<String>,
    event_message: Option<String>,
}

/// Reads `spectra logs --json`: entries oldest first, newest kept. `log show`'s closing count
/// line comes through as an entry with no time and no message, and is dropped.
pub fn parse(json: &str) -> Result<LogsReport, String> {
    let raw: Raw = crate::jvm_tools::read(json)?;
    let mut entries: Vec<Entry> = raw
        .entries
        .unwrap_or_default()
        .into_iter()
        .filter_map(|r| {
            let timestamp = r.timestamp.unwrap_or_default();
            let message = r.event_message.unwrap_or_default();
            if (timestamp.is_empty() || timestamp.starts_with("0001-")) && message.is_empty() {
                return None;
            }
            Some(Entry {
                timestamp,
                pid: r.pid.unwrap_or_default(),
                kind: Kind::of(
                    r.message_type.as_deref().unwrap_or_default(),
                    r.event_type.as_deref().unwrap_or_default(),
                ),
                subsystem: r.subsystem.unwrap_or_default(),
                category: r.category.unwrap_or_default(),
                sender: r.sender.unwrap_or_default().trim_matches('.').to_owned(),
                message,
            })
        })
        .collect();
    entries.reverse();
    Ok(LogsReport {
        entries,
        truncated: raw.truncated.unwrap_or_default(),
        ..LogsReport::default()
    })
}

/// Keeps `level` and up and, when `only` names a pid, that process's entries; then the newest
/// [`MAX_ROWS`].
pub fn narrow(mut r: LogsReport, level: Level, only: Option<u32>) -> LogsReport {
    r.entries.retain(|e| level.admits(e.kind));
    if let Some(pid) = only {
        let before = r.entries.len();
        r.entries.retain(|e| e.pid == pid || e.pid == 0);
        r.others += before - r.entries.len();
    }
    r.entries.truncate(MAX_ROWS);
    r
}

/// The arguments for `spectra logs`. The process name goes in as its own argument after
/// `--process`, which Go's flag package takes as the value whatever it starts with.
pub fn args(name: &str, last: &str, level: Level) -> Vec<String> {
    let mut v: Vec<String> = ["logs", "--json", "--process", name, "--last", last]
        .map(str::to_owned)
        .to_vec();
    if let Some(l) = level.arg() {
        v.extend(["--level".to_owned(), l.to_owned()]);
    }
    v.extend(["--top".to_owned(), QUERY_TOP.to_string()]);
    v
}

/// The name the unified log knows a process by: its executable's file name.
pub fn process_name(p: &Process) -> String {
    match p.executable_path.rsplit('/').find(|n| !n.is_empty()) {
        Some(n) => n.to_owned(),
        None => p.name().into_owned(),
    }
}

/// What a failed query says, in the page's words for the two it can do something about.
pub fn explain(why: &str) -> String {
    if why.contains("took longer than") {
        format!(
            "The log query took longer than {} s and was stopped. Pick a shorter window or Error.",
            DEADLINE.as_secs()
        )
    } else if why.contains("printed more than") {
        "The log query printed too much and was stopped. Pick a shorter window or Error.".into()
    } else {
        why.to_owned()
    }
}

/// Whose logs a page reads.
#[derive(Clone, Debug, PartialEq)]
pub struct Target {
    pub name: String,
    pub pid: u32,
    /// Other running processes with the same name: Spectra can't tell them apart.
    pub namesakes: usize,
}

struct Flight {
    job: u64,
    cancel: Arc<AtomicBool>,
    follow: bool,
}

/// What a page's Logs keeps. A new page starts over.
#[derive(Default)]
pub struct LogsPage {
    pub window: Window,
    pub level: Level,
    pub filter: TextEdit,
    pub follow: bool,
    /// The last full query: running, its answer (merged with Follow's), or why it failed.
    pub state: Option<RunState>,
    /// Why Follow stopped, when a round failed.
    pub follow_error: Option<String>,
    flight: Option<Flight>,
    next_follow: f64,
}

impl LogsPage {
    /// Asks the query in flight to stop.
    pub fn cancel(&mut self) {
        if let Some(f) = self.flight.take() {
            f.cancel.store(true, Ordering::Relaxed);
        }
    }

    pub fn report(&self) -> Option<&LogsReport> {
        match self.state.as_ref()? {
            RunState::Done(r) => match &**r {
                Report::Logs(l) => Some(l),
                _ => None,
            },
            _ => None,
        }
    }

    /// Whether a Follow round is in flight.
    pub fn following_now(&self) -> bool {
        self.flight.as_ref().is_some_and(|f| f.follow)
    }
}

impl App {
    /// Whose logs the open page reads, or why it can't: a process page reads its process, an
    /// app page its main process.
    pub fn logs_target(&self) -> Result<Target, String> {
        let pid = match self.inspect.subject.as_ref() {
            Some(Subject::Process { pid, .. }) => {
                if self.ended_page().is_some() {
                    return Err("That process has ended".into());
                }
                *pid
            }
            Some(Subject::App(path)) => self.app_lead(path).ok_or("Not running now")?,
            None => return Err("Open a process to read its logs".into()),
        };
        let p = self
            .process(pid)
            .ok_or("That process is no longer listed")?;
        let name = process_name(p);
        let namesakes = self
            .procs
            .iter()
            .filter(|o| o.pid != pid && process_name(o) == name)
            .count();
        Ok(Target {
            name,
            pid,
            namesakes,
        })
    }

    /// Reads the open page's logs for the picked window and level, stopping a query in flight.
    pub fn run_logs(&mut self) {
        let Ok(t) = self.logs_target() else { return };
        let page = &mut self.inspect.logs;
        page.cancel();
        page.follow_error = None;
        let (window, level) = (page.window, page.level);
        page.state = Some(RunState::Running { since: self.now });
        self.submit_logs(args(&t.name, window.arg(), level), level, &t, false);
    }

    /// A Follow round: the last minute, merged in when it lands.
    fn follow_logs(&mut self) {
        let Ok(t) = self.logs_target() else { return };
        let level = self.inspect.logs.level;
        self.submit_logs(args(&t.name, FOLLOW_LAST, level), level, &t, true);
    }

    fn submit_logs(&mut self, argv: Vec<String>, level: Level, t: &Target, follow: bool) {
        let (job, cancel) = self.inspect.next_job();
        let spectra = Arc::clone(&self.inspect.spectra);
        let flag = Arc::clone(&cancel);
        self.inspect.logs.flight = Some(Flight {
            job,
            cancel,
            follow,
        });
        let only = (t.namesakes > 0).then_some(t.pid);
        self.submit_inspect(
            Box::new(move || {
                let result = spectra
                    .run_capped(&argv, DEADLINE, MAX_OUTPUT, &flag)
                    .and_then(|out| parse(&out).map_err(Failure::Failed))
                    .map(|r| narrow(r, level, only));
                Landed::Logs { job, result }
            }),
            Landed::Logs {
                job,
                result: Err(Failure::Failed(
                    "Spectra Desktop hit an internal error reading the log.".into(),
                )),
            },
        );
    }

    /// Stops the query running; the page says it was cancelled.
    pub fn cancel_logs(&mut self) {
        let page = &mut self.inspect.logs;
        if matches!(page.state, Some(RunState::Running { .. })) {
            page.cancel();
            page.follow = false;
            page.state = Some(RunState::Cancelled);
        }
    }

    /// Picks how far back to look, and reads again when the page already has.
    pub fn set_logs_window(&mut self, w: Window) {
        let page = &mut self.inspect.logs;
        if page.window != w {
            page.window = w;
            if page.state.is_some() {
                self.run_logs();
            }
        }
    }

    /// Picks the lowest level, and reads again when the page already has.
    pub fn set_logs_level(&mut self, l: Level) {
        let page = &mut self.inspect.logs;
        if page.level != l {
            page.level = l;
            if page.state.is_some() {
                self.run_logs();
            }
        }
    }

    /// Turns Follow on (reading first when nothing has been read) or off.
    pub fn toggle_logs_follow(&mut self) {
        let page = &mut self.inspect.logs;
        page.follow = !page.follow;
        page.follow_error = None;
        if !page.follow {
            if page.following_now() {
                page.cancel();
            }
            return;
        }
        page.next_follow = self.now + FOLLOW_REST;
        if page.report().is_none() && !matches!(page.state, Some(RunState::Running { .. })) {
            self.run_logs();
        }
    }

    /// Starts the next Follow round when it's due: Follow is on, the page has an answer and
    /// nothing is in flight. Runs only while the page is open.
    pub fn tick_logs(&mut self) {
        let page = &self.inspect.logs;
        if self.inspect.auto
            && page.follow
            && page.flight.is_none()
            && page.report().is_some()
            && self.now >= page.next_follow
        {
            self.follow_logs();
        }
    }

    /// Lands a query: a full one replaces the page's entries, a Follow round adds what's newer.
    pub fn land_logs(&mut self, job: u64, result: Result<LogsReport, Failure>) {
        let now = self.now;
        let page = &mut self.inspect.logs;
        let Some(flight) = page.flight.take_if(|f| f.job == job) else {
            return;
        };
        page.next_follow = now + FOLLOW_REST;
        if flight.follow {
            match result {
                Ok(new) => {
                    if let Some(RunState::Done(r)) = &mut page.state
                        && let Report::Logs(old) = &mut **r
                    {
                        merge(old, new);
                    }
                }
                Err(Failure::Cancelled) => {}
                Err(Failure::Failed(why)) => {
                    page.follow = false;
                    page.follow_error = Some(explain(&why));
                }
            }
            return;
        }
        page.state = Some(match result {
            Ok(r) => RunState::Done(Box::new(Report::Logs(r))),
            Err(Failure::Cancelled) => RunState::Cancelled,
            Err(Failure::Failed(why)) => {
                page.follow = false;
                RunState::Failed(explain(&why))
            }
        });
    }
}

/// Adds `new`'s entries newer than `old`'s newest at the top, keeping the newest [`MAX_ROWS`].
pub fn merge(old: &mut LogsReport, new: LogsReport) {
    let newest = old
        .entries
        .first()
        .map(|e| e.timestamp.clone())
        .unwrap_or_default();
    let mut fresh: Vec<Entry> = new
        .entries
        .into_iter()
        .filter(|e| e.timestamp > newest)
        .collect();
    old.others += new.others;
    old.follow_gaps |= new.truncated;
    fresh.append(&mut old.entries);
    fresh.truncate(MAX_ROWS);
    old.entries = fresh;
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    /// Captured from `spectra logs --json --process Finder --last 5m`, one entry of each kind,
    /// with `log show`'s closing count line as Spectra passes it on.
    pub const FINDER: &str = include_str!("../tests/fixtures/logs/finder.json");

    #[test]
    fn a_cut_off_follow_round_is_remembered_as_gaps() {
        let mut old = LogsReport::default();
        merge(
            &mut old,
            LogsReport {
                truncated: false,
                ..Default::default()
            },
        );
        assert!(!old.follow_gaps);
        merge(
            &mut old,
            LogsReport {
                truncated: true,
                ..Default::default()
            },
        );
        assert!(
            old.follow_gaps && !old.truncated,
            "the first query's own note stays as it was"
        );
    }

    #[test]
    fn parses_the_fixture_newest_first_and_drops_the_count_line() {
        let r = parse(FINDER).unwrap();
        assert_eq!(r.entries.len(), 4, "the closing count line is dropped");
        assert!(!r.truncated);
        let kinds: Vec<Kind> = r.entries.iter().map(|e| e.kind).collect();
        assert_eq!(
            kinds,
            [Kind::Default, Kind::Error, Kind::Info, Kind::Activity],
            "newest first"
        );
        let e = &r.entries[1];
        assert_eq!(e.clock(), "23:23:18.532");
        assert_eq!(e.source(), "com.apple.CarbonCore · FileIDMap");
        assert_eq!(e.pid, 685);
        assert!(e.kind.loud() && !r.entries[0].kind.loud());
        assert_eq!(
            r.entries[3].source(),
            "CoreServicesInternal",
            "no subsystem: the sender"
        );
    }

    #[test]
    fn missing_fields_and_null_arrays_never_fail() {
        assert_eq!(parse(r#"{"entries":null,"clock":{}}"#).unwrap().entries, []);
        assert_eq!(parse("{}").unwrap(), LogsReport::default());
        let r = parse(r#"{"entries":[{"event_message":"hi"},{"timestamp":"2026-11-21T10:00:00-05:00","subsystem":null,"pid":null}],"truncated":true}"#).unwrap();
        assert!(r.truncated);
        assert_eq!(r.entries.len(), 2);
        assert_eq!(r.entries[0].clock(), "10:00:00", "no fraction");
        assert_eq!(
            (r.entries[0].kind, r.entries[0].source()),
            (Kind::Other, String::new())
        );
        assert_eq!(r.entries[1].message, "hi");
        assert!(
            parse("usage: spectra logs")
                .unwrap_err()
                .starts_with("Couldn't read Spectra's output")
        );
    }

    #[test]
    fn the_process_name_is_its_own_argument_and_cannot_be_read_as_a_flag() {
        assert_eq!(
            args("Finder", "5m", Level::All),
            [
                "logs",
                "--json",
                "--process",
                "Finder",
                "--last",
                "5m",
                "--top",
                "2000"
            ]
        );
        let a = args("--level", "1h", Level::Fault);
        assert_eq!(
            a,
            [
                "logs",
                "--json",
                "--process",
                "--level",
                "--last",
                "1h",
                "--level",
                "Fault",
                "--top",
                "2000"
            ]
        );
        assert_eq!(
            a.iter()
                .position(|x| x == "--process")
                .map(|i| a[i + 1].as_str()),
            Some("--level")
        );
        assert_eq!(
            args("Chat Helper (GPU)", FOLLOW_LAST, Level::Error)[3],
            "Chat Helper (GPU)",
            "spaces stay in one argument"
        );
        assert_eq!(Window::ALL.map(Window::arg), ["5m", "15m", "1h"]);
    }

    #[test]
    fn the_level_filter_keeps_that_level_and_up() {
        let r = parse(FINDER).unwrap();
        assert_eq!(narrow(r.clone(), Level::All, None).entries.len(), 4);
        let errors = narrow(r.clone(), Level::Error, None);
        assert_eq!(
            errors.entries.iter().map(|e| e.kind).collect::<Vec<_>>(),
            [Kind::Error]
        );
        assert!(narrow(r, Level::Fault, None).entries.is_empty());
        assert!(Level::Error.admits(Kind::Fault) && !Level::Fault.admits(Kind::Error));
        assert_eq!(
            Level::ALL.map(Level::arg),
            [None, Some("Error"), Some("Fault")]
        );
    }

    #[test]
    fn a_shared_name_keeps_only_the_pages_process_and_counts_the_rest() {
        let mut r = parse(FINDER).unwrap();
        r.entries[0].pid = 900;
        let mine = narrow(r.clone(), Level::All, Some(685));
        assert_eq!((mine.entries.len(), mine.others), (3, 1));
        assert_eq!(
            narrow(r, Level::All, None).others,
            0,
            "a unique name keeps every pid: a restart is still it"
        );
    }

    #[test]
    fn rows_are_capped_newest_kept_and_follow_merges_only_newer() {
        let entry = |i: usize| Entry {
            timestamp: format!("2026-11-21T10:{:02}:{:02}.000000-05:00", i / 60, i % 60),
            pid: 1,
            kind: Kind::Info,
            subsystem: String::new(),
            category: String::new(),
            sender: "x".into(),
            message: format!("m{i}"),
        };
        let many = LogsReport {
            entries: (0..700).rev().map(entry).collect(),
            ..LogsReport::default()
        };
        let kept = narrow(many, Level::All, None);
        assert_eq!(kept.entries.len(), MAX_ROWS);
        assert_eq!(kept.entries[0].message, "m699", "the newest kept");
        let mut old = LogsReport {
            entries: vec![entry(5), entry(4)],
            ..LogsReport::default()
        };
        merge(
            &mut old,
            LogsReport {
                entries: vec![entry(7), entry(6), entry(5), entry(4)],
                ..LogsReport::default()
            },
        );
        let msgs: Vec<&str> = old.entries.iter().map(|e| e.message.as_str()).collect();
        assert_eq!(msgs, ["m7", "m6", "m5", "m4"]);
    }

    #[test]
    fn the_filter_matches_message_source_and_level_in_any_case() {
        let r = parse(FINDER).unwrap();
        assert_eq!(r.shown("").len(), 4);
        assert_eq!(r.shown("fileidmap").len(), 1);
        assert_eq!(r.shown("ERROR").len(), 1);
        assert_eq!(
            r.shown("trash")[0].message,
            "Checking empty state isEmpty=0"
        );
        assert!(r.shown("nothing like it").is_empty());
    }

    #[test]
    fn a_timeout_and_too_much_output_say_what_to_do() {
        use crate::run::RunError;
        let timeout = crate::procs::run_failure("spectra", RunError::Timeout(DEADLINE));
        assert_eq!(
            explain(&timeout),
            "The log query took longer than 30 s and was stopped. Pick a shorter window or Error."
        );
        let big = crate::procs::run_failure("spectra", RunError::TooLarge(MAX_OUTPUT));
        assert!(explain(&big).starts_with("The log query printed too much"));
        assert_eq!(explain("log: bad predicate"), "log: bad predicate");
    }
}
