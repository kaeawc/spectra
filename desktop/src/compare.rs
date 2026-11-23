//! "What's different from when it was fine?": the snapshots and baselines Spectra has stored
//! (`spectra snapshot list`), a diff of one against another or against now (`spectra snapshot
//! diff`), and a snapshot taken on a press (`spectra snapshot create`).
//!
//! The list is read when the page opens. A diff and a snapshot run only on a press, each on a
//! thread of its own with a deadline and the run seam's output cap (4 MB: a diff of two real
//! snapshots was 100 KB, and `snapshot create` prints a short summary, not the 10 MB snapshot
//! it stores), through the same run seam as every other
//! Spectra call (the daemon has no method for these, so the seam runs the CLI). Spectra's diff
//! covers the host, apps, Java installs, Homebrew packages, active language versions, listening
//! ports, VPN, PATH, resource limits, kernel settings and install history; login items and
//! services are not in it, and the page says so.

use std::collections::HashSet;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::Value;

use crate::app::App;
use crate::storage::{Ran, Runner, SpectraBin};
use crate::system::fmt_size;

/// Reading the stored list: a database read, well under a second.
pub const LIST_DEADLINE: Duration = Duration::from_secs(30);
/// A diff against "now" first inspects every app on the Mac, which can take a minute or more.
pub const DIFF_DEADLINE: Duration = Duration::from_secs(5 * 60);
/// Taking a snapshot inspects every app and writes the result.
pub const TAKE_DEADLINE: Duration = Duration::from_secs(5 * 60);
/// Stored snapshots listed on the page, newest first.
pub const LISTED: usize = 30;
/// Lines shown per group before "Show all".
pub const SHOWN: usize = 8;
/// Where Spectra keeps its snapshots.
pub const STORE: &str = "~/.spectra/spectra.db";
/// The label a baseline taken here gets.
const BASELINE_NAME: &str = "Taken in Spectra Desktop";

pub fn list_args() -> Vec<String> {
    ["snapshot", "list", "--json"].map(String::from).to_vec()
}

/// The flags come first: Spectra stops reading flags at the first snapshot id. `to` is `None`
/// for now.
pub fn diff_args(from: &str, to: Option<&str>) -> Vec<String> {
    ["snapshot", "diff", "--json", from, to.unwrap_or("live")]
        .map(String::from)
        .to_vec()
}

pub fn take_args(baseline: bool) -> Vec<String> {
    let mut a = vec!["snapshot".to_owned(), "create".to_owned()];
    if baseline {
        a.extend(["--baseline".to_owned(), "--name".to_owned()]);
        a.push(BASELINE_NAME.to_owned());
    }
    a
}

/// One stored snapshot or baseline.
#[derive(Clone, Debug, PartialEq)]
pub struct Stored {
    pub id: String,
    /// `2026-11-22 12:15 UTC`, or what Spectra gave when it isn't a date.
    pub when: String,
    taken_at: String,
    pub baseline: bool,
    /// The label a baseline was given, empty when none.
    pub name: String,
    pub apps: u64,
}

fn field<'a>(v: &'a Value, keys: &[&str]) -> Option<&'a Value> {
    keys.iter().find_map(|k| v.get(*k)).filter(|x| !x.is_null())
}

fn st(v: &Value, keys: &[&str]) -> String {
    field(v, keys)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

/// `2026-11-22T12:15:40.4Z` as `2026-11-22 12:15 UTC`; anything else as it came.
fn when(date: &str) -> String {
    let d = date.trim();
    match (d.get(..10), d.get(11..16)) {
        (Some(day), Some(time)) if d.as_bytes().get(10) == Some(&b'T') => {
            let utc = if d.ends_with('Z') { " UTC" } else { "" };
            format!("{day} {time}{utc}")
        }
        _ => d.to_owned(),
    }
}

/// Reads `spectra snapshot list --json`: a list, newest first. Spectra prints nothing at all
/// when none is stored, and `null` for an empty list of baselines; both read as no snapshots.
pub fn parse_list(json: &str) -> Result<Vec<Stored>, String> {
    if json.trim().is_empty() {
        return Ok(Vec::new());
    }
    let v: Value = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's snapshot list didn't read as JSON: {e}"))?;
    let rows = match &v {
        Value::Null => return Ok(Vec::new()),
        Value::Array(a) => a,
        _ => return Err("Spectra's snapshot list wasn't a JSON list".into()),
    };
    let mut out: Vec<Stored> = rows
        .iter()
        .map(|r| {
            let taken_at = st(r, &["TakenAt", "taken_at"]);
            Stored {
                id: st(r, &["ID", "id"]),
                when: when(&taken_at),
                taken_at,
                baseline: st(r, &["Kind", "kind"]) == "baseline",
                name: st(r, &["Name", "name"]),
                apps: field(r, &["AppCount", "app_count"])
                    .and_then(Value::as_u64)
                    .unwrap_or(0),
            }
        })
        .filter(|s| !s.id.is_empty())
        .collect();
    out.sort_by(|a, b| b.taken_at.cmp(&a.taken_at));
    Ok(out)
}

/// What a snapshot is called on the page.
pub fn label(s: &Stored) -> String {
    match (s.baseline, s.name.is_empty()) {
        (true, false) => format!("{} · {}", s.when, s.name),
        (true, true) => format!("{} · baseline", s.when),
        _ => s.when.clone(),
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Kind {
    Added,
    Removed,
    Changed,
}

impl Kind {
    fn of(s: &str) -> Kind {
        match s {
            "added" => Kind::Added,
            "removed" => Kind::Removed,
            _ => Kind::Changed,
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Change {
    pub kind: Kind,
    pub key: String,
    pub before: String,
    pub after: String,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Section {
    pub name: String,
    pub changes: Vec<Change>,
}

/// Spectra's diff: the two snapshots' ids and what differs, section by section.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Diff {
    pub a: String,
    pub b: String,
    pub sections: Vec<Section>,
}

/// Reads `spectra snapshot diff --json`. A section with no changes has `null` for them; a
/// section or field this version doesn't know is kept or ignored, never a failure.
pub fn parse_diff(json: &str) -> Result<Diff, String> {
    let v: Value = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's diff didn't read as JSON: {e}"))?;
    if !v.is_object() {
        return Err("Spectra's diff wasn't a JSON object".into());
    }
    let sections = field(&v, &["Sections", "sections"])
        .and_then(Value::as_array)
        .map_or(&[][..], Vec::as_slice)
        .iter()
        .map(|s| Section {
            name: st(s, &["Name", "name"]),
            changes: field(s, &["Changes", "changes"])
                .and_then(Value::as_array)
                .map_or(&[][..], Vec::as_slice)
                .iter()
                .map(|c| Change {
                    kind: Kind::of(&st(c, &["Kind", "kind"])),
                    key: st(c, &["Key", "key"]),
                    before: st(c, &["Before", "before"]),
                    after: st(c, &["After", "after"]),
                })
                .filter(|c| !c.key.is_empty())
                .collect(),
        })
        .filter(|s| !s.name.is_empty())
        .collect();
    Ok(Diff {
        a: st(&v, &["AID", "aid"]),
        b: st(&v, &["BID", "bid"]),
        sections,
    })
}

impl Diff {
    pub fn total(&self) -> usize {
        self.sections.iter().map(|s| s.changes.len()).sum()
    }
}

/// One difference in words.
#[derive(Clone, Debug, PartialEq)]
pub struct Line {
    pub kind: Kind,
    pub text: String,
}

/// One area of the Mac and what changed in it.
#[derive(Clone, Debug, PartialEq)]
pub struct Group {
    pub id: String,
    pub title: String,
    /// `3 added · 2 removed · 5 changed`.
    pub summary: String,
    pub lines: Vec<Line>,
}

/// The sizes Spectra gives as plain byte counts.
fn size(v: &str) -> String {
    match v.trim().parse::<f64>() {
        Ok(b) if b.is_finite() && b >= 0.0 => fmt_size(b),
        _ => v.to_owned(),
    }
}

fn or_none(v: &str) -> &str {
    if v.is_empty() { "none" } else { v }
}

fn host_line(c: &Change) -> String {
    let (b, a) = (c.before.as_str(), c.after.as_str());
    let plain = |label: &str| format!("{label}: {} → {}", or_none(b), or_none(a));
    let sized = |label: &str| format!("{label}: {} → {}", size(b), size(a));
    match c.key.as_str() {
        "hostname" => plain("Computer name"),
        "os_version" => plain("macOS version"),
        "os_build" => plain("macOS build"),
        "cpu_brand" => plain("Processor"),
        "cpu_cores" => plain("CPU cores"),
        "arch" => plain("Architecture"),
        "ram_bytes" => sized("Memory installed"),
        "memory.compressor_occupied" => sized("Memory the compressor takes"),
        "memory.compressor_stored" => sized("Data held compressed"),
        "memory.swap_used" => sized("Swap in use"),
        "memory.pressure_level" => plain("Memory pressure"),
        "time_machine.destinations" => plain("Time Machine disks"),
        "time_machine.local_snapshots" => plain("Time Machine local snapshots"),
        "spectra_version" => plain("Spectra version"),
        other => plain(other),
    }
}

/// The name an app goes by: its bundle id, or the file name when it has none.
fn app_name(key: &str) -> &str {
    if key.starts_with('/') {
        key.rsplit('/')
            .next()
            .map(|n| n.strip_suffix(".app").unwrap_or(n))
            .filter(|n| !n.is_empty())
            .unwrap_or(key)
    } else {
        key
    }
}

fn app_line(c: &Change) -> String {
    if let Some((name, what)) = c.key.split_once(':') {
        let name = app_name(name);
        return match what {
            "hardened-runtime" if c.after == "true" => {
                format!("{name} now uses the hardened runtime")
            }
            "hardened-runtime" => format!("{name} no longer uses the hardened runtime"),
            "gatekeeper" => format!("{name}: Gatekeeper {} → {}", c.before, c.after),
            "team-id" => format!(
                "{name} is signed by a different developer ({} → {})",
                c.before, c.after
            ),
            other => format!("{name}: {other} {} → {}", c.before, c.after),
        };
    }
    let name = app_name(&c.key);
    match c.kind {
        Kind::Added if c.after.is_empty() => format!("{name} added"),
        Kind::Added => format!("{name} added (version {})", c.after),
        Kind::Removed if c.before.is_empty() => format!("{name} removed"),
        Kind::Removed => format!("{name} removed (was version {})", c.before),
        Kind::Changed => format!("{name} updated: {} → {}", c.before, c.after),
    }
}

/// `tcp:3000` as `Port 3000 (tcp)`.
fn port_line(c: &Change) -> String {
    let port = match c.key.split_once(':') {
        Some((proto, n)) => format!("Port {n} ({proto})"),
        None => format!("Port {}", c.key),
    };
    match c.kind {
        Kind::Added => format!("{port} is now listening"),
        Kind::Removed => format!("{port} is no longer listening"),
        Kind::Changed => format!("{port} changed"),
    }
}

fn limit_line(c: &Change) -> String {
    let name = match c.key.strip_suffix(":pct").unwrap_or(&c.key) {
        "pty" => "Terminal sessions",
        "files" => "Open files",
        "procs" => "Processes",
        "procs_per_uid" => "Processes for your user",
        other => other,
    };
    format!("{name} in use: {}% → {}%", c.before, c.after)
}

fn kernel_line(c: &Change) -> String {
    if c.key == "kern.boottime" {
        // `{ sec = 1795161463, usec = 148696 } Fri Nov 20 02:57:43 2026`: say the date part.
        let date = |s: &str| s.rsplit_once('}').map_or(s, |(_, d)| d).trim().to_owned();
        return format!("Last restart: {} → {}", date(&c.before), date(&c.after));
    }
    format!("{}: {} → {}", c.key, or_none(&c.before), or_none(&c.after))
}

fn pair(c: &Change, noun: &str) -> String {
    match c.kind {
        Kind::Added => format!("{} {noun} (now {})", c.key, or_none(&c.after)),
        Kind::Removed => format!("{} no longer {noun} (was {})", c.key, or_none(&c.before)),
        Kind::Changed => format!("{}: {} → {}", c.key, or_none(&c.before), or_none(&c.after)),
    }
}

fn line_of(section: &str, c: &Change) -> String {
    match section {
        "host" => host_line(c),
        "apps" => app_line(c),
        "jdks" => match c.kind {
            Kind::Added => format!("Java {} installed at {}", c.key, c.after),
            Kind::Removed => format!("Java {} is gone (was at {})", c.key, c.before),
            Kind::Changed => format!("Java {} changed", c.key),
        },
        "brew_formulae" => match c.kind {
            Kind::Added => format!("{} installed (version {})", c.key, c.after),
            Kind::Removed => format!("{} removed (was version {})", c.key, c.before),
            Kind::Changed => format!("{} updated: {} → {}", c.key, c.before, c.after),
        },
        "active_runtimes" => match c.kind {
            Kind::Added => format!("{} is now active (version {})", c.key, c.after),
            Kind::Removed => format!("{} is no longer active (was {})", c.key, c.before),
            Kind::Changed => format!("{} switched: {} → {}", c.key, c.before, c.after),
        },
        "listening_ports" => port_line(c),
        "vpn" => format!("VPN: {} → {}", vpn_word(&c.before), vpn_word(&c.after)),
        "path_dirs" => match c.kind {
            Kind::Added => format!("{} added to PATH ({})", c.key, c.after),
            Kind::Removed => format!("{} removed from PATH (was {})", c.key, c.before),
            Kind::Changed => format!("{} moved on PATH: {} → {}", c.key, c.before, c.after),
        },
        "system_limits" => limit_line(c),
        "sysctls" => kernel_line(c),
        "install_history" => match c.kind {
            Kind::Removed => format!("{} no longer in the install history", c.key),
            _ if c.after.is_empty() => format!("{} installed", c.key),
            _ => format!("{} installed: {}", c.key, c.after),
        },
        _ => pair(c, "present"),
    }
}

fn vpn_word(v: &str) -> &str {
    match v {
        "true" => "on",
        "false" => "off",
        "" => "unknown",
        other => other,
    }
}

/// Group title and the order groups appear in: what a person most likely changed first.
fn title_of(section: &str) -> (usize, String) {
    let (rank, t) = match section {
        "apps" => (0, "Apps"),
        "jdks" => (1, "Java installs"),
        "brew_formulae" => (2, "Homebrew packages"),
        "active_runtimes" => (3, "Active language versions"),
        "listening_ports" => (4, "Listening network ports"),
        "vpn" => (5, "VPN"),
        "system_limits" => (6, "Resources in use"),
        "host" => (7, "This Mac"),
        "path_dirs" => (8, "Folders on PATH"),
        "sysctls" => (9, "Kernel settings"),
        "install_history" => (10, "Software installs and updates"),
        other => return (11, other.replace('_', " ")),
    };
    (rank, t.to_owned())
}

fn summary(lines: &[Line]) -> String {
    let n = |k: Kind| lines.iter().filter(|l| l.kind == k).count();
    let mut parts = Vec::new();
    for (k, word) in [
        (Kind::Added, "added"),
        (Kind::Removed, "removed"),
        (Kind::Changed, "changed"),
    ] {
        if n(k) > 0 {
            parts.push(format!("{} {word}", n(k)));
        }
    }
    parts.join(" · ")
}

/// The diff in words, grouped by area, groups with nothing in them left out. Spectra lists its
/// changes in no fixed order, so each group is sorted: added, removed, then changed, by name.
pub fn groups(d: &Diff) -> Vec<Group> {
    let mut out: Vec<(usize, Group)> = d
        .sections
        .iter()
        .filter(|s| !s.changes.is_empty())
        .map(|s| {
            let mut changes: Vec<&Change> = s.changes.iter().collect();
            changes.sort_by(|a, b| a.kind.cmp(&b.kind).then_with(|| a.key.cmp(&b.key)));
            let lines: Vec<Line> = changes
                .into_iter()
                .map(|c| Line {
                    kind: c.kind,
                    text: line_of(&s.name, c),
                })
                .collect();
            let (rank, title) = title_of(&s.name);
            (
                rank,
                Group {
                    id: s.name.clone(),
                    title,
                    summary: summary(&lines),
                    lines,
                },
            )
        })
        .collect();
    out.sort_by_key(|(rank, _)| *rank);
    out.into_iter().map(|(_, g)| g).collect()
}

/// Why a command printed nothing to read.
pub(crate) fn failure(what: &str, ran: &Ran) -> String {
    let said = ran
        .stderr
        .lines()
        .map(str::trim)
        .find(|l| !l.is_empty())
        .unwrap_or("");
    let lower = said.to_ascii_lowercase();
    if lower.contains("unknown command") || lower.contains("unknown subcommand") {
        return format!("This Spectra has no `spectra {what}`. Update Spectra to use it.");
    }
    if lower.contains("permission denied")
        || lower.contains("not permitted")
        || lower.contains("sudo")
    {
        return format!(
            "{said}. Spectra needs more access for this. This app never runs sudo; run `spectra {what}` in a terminal instead."
        );
    }
    match (said.is_empty(), ran.code) {
        (false, _) => said.to_owned(),
        (true, Some(c)) => format!("spectra {what} stopped with exit status {c}"),
        (true, None) => format!("spectra {what} was stopped"),
    }
}

fn read_list(runner: &dyn Runner, cancel: &AtomicBool) -> Result<Vec<Stored>, String> {
    let ran = runner.run(&list_args(), LIST_DEADLINE, cancel)?;
    if ran.code != Some(0) {
        return Err(failure("snapshot list", &ran));
    }
    parse_list(&ran.stdout)
}

fn run_diff(
    runner: &dyn Runner,
    from: &str,
    to: Option<&str>,
    cancel: &AtomicBool,
) -> Result<Diff, String> {
    // An id is never a flag; one that looks like it isn't ours to pass on.
    if from.starts_with('-') || to.is_some_and(|t| t.starts_with('-')) {
        return Err("That doesn't look like a snapshot id.".into());
    }
    let ran = runner.run(&diff_args(from, to), DIFF_DEADLINE, cancel)?;
    if ran.code != Some(0) || ran.stdout.trim().is_empty() {
        return Err(failure("snapshot diff", &ran));
    }
    parse_diff(&ran.stdout)
}

/// A snapshot Spectra stored.
#[derive(Clone, Debug, PartialEq)]
pub struct Taken {
    pub id: String,
    pub baseline: bool,
}

/// The id from `spectra snapshot create`'s summary (`id:  snap-…`).
pub fn parse_taken(out: &str) -> Option<String> {
    out.lines()
        .find_map(|l| l.trim().strip_prefix("id:"))
        .map(|id| id.trim().to_owned())
        .filter(|id| !id.is_empty())
}

fn take(runner: &dyn Runner, baseline: bool, cancel: &AtomicBool) -> Result<Taken, String> {
    let ran = runner.run(&take_args(baseline), TAKE_DEADLINE, cancel)?;
    if ran.code != Some(0) {
        return Err(failure("snapshot create", &ran));
    }
    // Spectra exits 0 when it couldn't save the snapshot and says so on stderr.
    if let Some(l) = ran.stderr.lines().find(|l| l.contains("could not persist")) {
        return Err(format!(
            "Spectra took the snapshot but couldn't save it ({}).",
            l.trim().trim_start_matches("warning: ")
        ));
    }
    match parse_taken(&ran.stdout) {
        Some(id) => Ok(Taken { id, baseline }),
        None => Err("Spectra took a snapshot but didn't say which one it saved.".into()),
    }
}

pub enum Landed {
    List {
        job: u64,
        result: Result<Vec<Stored>, String>,
    },
    Diffed {
        job: u64,
        /// What the diff ran with, which the page's choice may no longer be.
        from: String,
        to: Option<String>,
        result: Result<Diff, String>,
    },
    Taken {
        job: u64,
        result: Result<Taken, String>,
    },
}

/// A finished comparison: what was compared (`to` is `None` for now) and when it landed.
#[derive(Clone, Debug)]
pub struct Shown {
    pub from: String,
    pub to: Option<String>,
    pub diff: Diff,
    pub at: f64,
}

/// A job on its way: when it started on the window clock, and how to stop it.
pub struct Going {
    pub started: f64,
    pub baseline: bool,
    job: u64,
    cancel: Arc<AtomicBool>,
}

impl Going {
    /// A job shown as on its way, for the sample scenes: nothing runs.
    pub fn shown(started: f64) -> Going {
        Going {
            started,
            baseline: false,
            job: 0,
            cancel: Arc::new(AtomicBool::new(false)),
        }
    }
}

/// The Compare page's state.
pub struct Compare {
    pub runner: Arc<dyn Runner>,
    /// Off in tests: work runs on the calling thread and lands on the next `land_compare`.
    pub threaded: bool,
    /// Stored snapshots, newest first; `None` until read.
    pub list: Option<Vec<Stored>>,
    pub list_error: Option<String>,
    pub reading: bool,
    /// The earlier snapshot, and the later one (`None` is now).
    pub from: Option<String>,
    pub to: Option<String>,
    pub shown: Option<Shown>,
    pub diff_error: Option<String>,
    pub diffing: Option<Going>,
    pub taking: Option<Going>,
    /// The last snapshot taken here, or why it couldn't be.
    pub taken: Option<Result<Taken, String>>,
    /// Groups opened to show every line.
    pub expanded: HashSet<String>,
    /// The page opened while the window was hidden: the list is read once it's shown.
    wanted: bool,
    list_job: u64,
    list_cancel: Arc<AtomicBool>,
    job: u64,
    /// The "started … ago" last shown, so a tick redraws only when it changes.
    age: Option<String>,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
}

impl Compare {
    pub fn new() -> Compare {
        let (tx, rx) = channel();
        Compare {
            runner: Arc::new(SpectraBin),
            threaded: true,
            list: None,
            list_error: None,
            reading: false,
            from: None,
            to: None,
            shown: None,
            diff_error: None,
            diffing: None,
            taking: None,
            taken: None,
            expanded: HashSet::new(),
            wanted: false,
            list_job: 0,
            list_cancel: Arc::new(AtomicBool::new(false)),
            job: 0,
            age: None,
            tx,
            rx,
        }
    }

    pub fn busy(&self) -> bool {
        self.diffing.is_some() || self.taking.is_some()
    }

    pub fn stored(&self, id: &str) -> Option<&Stored> {
        self.list.as_ref()?.iter().find(|s| s.id == id)
    }

    /// What a snapshot is called, by id: its label, or the id when it isn't listed.
    pub fn name_of(&self, id: &str) -> String {
        self.stored(id).map_or_else(|| id.to_owned(), label)
    }

    /// Whether Compare can run: an earlier snapshot is chosen and isn't also the later one.
    pub fn can_compare(&self) -> bool {
        self.from.is_some() && self.from != self.to && self.diffing.is_none()
    }
}

impl Default for Compare {
    fn default() -> Compare {
        Compare::new()
    }
}

/// Runs `work` on a thread of its own (or here, in tests), turning a panic into `failed`.
fn submit(
    c: &Compare,
    wake: Option<rune::Waker>,
    work: impl FnOnce() -> Landed + Send + 'static,
    failed: Landed,
) {
    let tx = c.tx.clone();
    if !c.threaded {
        let _ = tx.send(work());
        return;
    }
    // Quit waits for this from now until it's done, even before the thread starts.
    let hold = crate::run::pending();
    let failed = Arc::new(Mutex::new(Some(failed)));
    let take = |f: &Mutex<Option<Landed>>| f.lock().ok().and_then(|mut f| f.take());
    let (on_panic, failed_tx, wake2) = (Arc::clone(&failed), tx.clone(), wake.clone());
    let job = move || {
        let landed = std::panic::catch_unwind(std::panic::AssertUnwindSafe(work));
        if let Some(l) = landed.ok().or_else(|| take(&on_panic)) {
            let _ = tx.send(l);
        }
        drop(hold);
        if let Some(wake) = wake {
            wake();
        }
    };
    if std::thread::Builder::new()
        .name("spectra-compare".into())
        .spawn(job)
        .is_err()
    {
        if let Some(l) = take(&failed) {
            let _ = failed_tx.send(l);
        }
        if let Some(wake) = wake2 {
            wake();
        }
    }
}

const THREAD_FAILED: &str = "the working thread failed";

impl App {
    /// The page opened: reads the stored snapshots (quick), or once the window is shown when it's
    /// hidden. Nothing is compared or taken until a press asks for it.
    pub fn open_compare(&mut self) {
        let shown = self.window_shown();
        let c = &mut self.compare;
        if c.reading {
            return;
        }
        if !shown {
            c.wanted = true;
            return;
        }
        c.wanted = false;
        c.reading = true;
        c.list_job += 1;
        let job = c.list_job;
        c.list_cancel = Arc::new(AtomicBool::new(false));
        let cancel = Arc::clone(&c.list_cancel);
        let runner = Arc::clone(&c.runner);
        let failed = Landed::List {
            job,
            result: Err(format!(
                "Couldn't ask Spectra for its snapshots: {THREAD_FAILED}."
            )),
        };
        submit(
            c,
            self.wake.clone(),
            move || Landed::List {
                job,
                result: read_list(runner.as_ref(), &cancel),
            },
            failed,
        );
    }

    pub fn pick_from(&mut self, id: &str) {
        let c = &mut self.compare;
        c.from = if c.from.as_deref() == Some(id) {
            None
        } else {
            Some(id.to_owned())
        };
    }

    /// The later snapshot: a stored one, or `None` for now.
    pub fn pick_to(&mut self, id: Option<&str>) {
        let c = &mut self.compare;
        c.to = match id {
            Some(id) if c.to.as_deref() != Some(id) => Some(id.to_owned()),
            _ => None,
        };
    }

    /// Diffs the chosen snapshots with `spectra snapshot diff`. Against now, Spectra inspects
    /// the Mac first, which can take a minute.
    pub fn run_compare(&mut self) {
        let now = self.now;
        let c = &mut self.compare;
        if !c.can_compare() {
            return;
        }
        let Some(from) = c.from.clone() else { return };
        let to = c.to.clone();
        c.job += 1;
        let job = c.job;
        let cancel = Arc::new(AtomicBool::new(false));
        c.diffing = Some(Going {
            started: now,
            baseline: false,
            job,
            cancel: Arc::clone(&cancel),
        });
        c.diff_error = None;
        let runner = Arc::clone(&c.runner);
        let failed = Landed::Diffed {
            job,
            from: from.clone(),
            to: to.clone(),
            result: Err(format!(
                "Couldn't ask Spectra to compare them: {THREAD_FAILED}."
            )),
        };
        submit(
            c,
            self.wake.clone(),
            move || Landed::Diffed {
                job,
                from: from.clone(),
                to: to.clone(),
                result: run_diff(runner.as_ref(), &from, to.as_deref(), &cancel),
            },
            failed,
        );
    }

    /// Takes a snapshot now with `spectra snapshot create`. Only on a press; it writes to
    /// Spectra's snapshot store. `baseline` keeps it from being pruned later.
    pub fn take_snapshot(&mut self, baseline: bool) {
        let now = self.now;
        let c = &mut self.compare;
        if c.taking.is_some() {
            return;
        }
        c.job += 1;
        let job = c.job;
        let cancel = Arc::new(AtomicBool::new(false));
        c.taking = Some(Going {
            started: now,
            baseline,
            job,
            cancel: Arc::clone(&cancel),
        });
        c.taken = None;
        let runner = Arc::clone(&c.runner);
        let failed = Landed::Taken {
            job,
            result: Err(format!(
                "Couldn't ask Spectra to take a snapshot: {THREAD_FAILED}."
            )),
        };
        submit(
            c,
            self.wake.clone(),
            move || Landed::Taken {
                job,
                result: take(runner.as_ref(), baseline, &cancel),
            },
            failed,
        );
    }

    /// Shows or hides the lines past the first few in a group.
    pub fn toggle_compare_group(&mut self, id: &str) {
        let e = &mut self.compare.expanded;
        if !e.remove(id) {
            e.insert(id.to_owned());
        }
    }

    pub fn stop_compare(&mut self) {
        if let Some(d) = self.compare.diffing.take() {
            d.cancel.store(true, Ordering::SeqCst);
        }
    }

    /// Stops the reads and the comparison on their way: on leaving the page and on quit. A
    /// snapshot being taken is left to finish, since stopping it half-way could leave a partly
    /// written one in Spectra's store; its answer lands whenever it comes.
    pub fn cancel_compare(&mut self) {
        self.stop_compare();
        let c = &mut self.compare;
        c.list_cancel.store(true, Ordering::SeqCst);
        c.reading = false;
        c.wanted = false;
    }

    /// Lands what finished and says whether anything shown changed: a landing, or the
    /// "started … ago" of a job on its way now reading differently. While the window is hidden
    /// jobs may land but nothing new starts and the age isn't redrawn.
    pub fn tick_compare(&mut self) -> bool {
        let mut changed = self.land_compare();
        if !self.window_shown() {
            self.compare.age = None;
            return changed;
        }
        if self.compare.wanted {
            self.open_compare();
            changed = true;
        }
        let ago = |g: &Going| crate::app::fmt_ago(self.now - g.started);
        let age = self
            .compare
            .diffing
            .as_ref()
            .map(ago)
            .or_else(|| self.compare.taking.as_ref().map(ago));
        if age != self.compare.age {
            self.compare.age = age;
            changed = true;
        }
        changed
    }

    /// Lands what the Compare workers finished. True when anything landed.
    pub fn land_compare(&mut self) -> bool {
        let mut landed = false;
        let mut reread = false;
        let now = self.now;
        while let Ok(l) = self.compare.rx.try_recv() {
            landed = true;
            let c = &mut self.compare;
            match l {
                Landed::List { job, result } => {
                    if !c.reading || job != c.list_job {
                        continue;
                    }
                    c.reading = false;
                    match result {
                        Ok(list) => {
                            // A choice whose snapshot is gone (pruned) is dropped.
                            let known = |id: &Option<String>| {
                                id.as_ref().is_none_or(|i| list.iter().any(|s| &s.id == i))
                            };
                            if !known(&c.from) {
                                c.from = None;
                            }
                            if !known(&c.to) {
                                c.to = None;
                            }
                            c.list = Some(list);
                            c.list_error = None;
                        }
                        Err(why) => c.list_error = Some(why),
                    }
                }
                Landed::Diffed {
                    job,
                    from,
                    to,
                    result,
                } => {
                    if c.diffing.as_ref().is_none_or(|d| d.job != job) {
                        continue;
                    }
                    c.diffing = None;
                    match result {
                        Ok(diff) => {
                            c.diff_error = None;
                            c.shown = Some(Shown {
                                from,
                                to,
                                diff,
                                at: now,
                            });
                            c.expanded.clear();
                        }
                        Err(why) => c.diff_error = Some(why),
                    }
                }
                Landed::Taken { job, result } => {
                    if c.taking.as_ref().is_none_or(|t| t.job != job) {
                        continue;
                    }
                    c.taking = None;
                    reread = result.is_ok();
                    c.taken = Some(result);
                }
            }
        }
        // The new snapshot is in the list now. Off the page nothing is read: opening it reads.
        if reread && self.nav == crate::groups::Nav::Compare {
            self.open_compare();
        }
        landed
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    use crate::sample_compare::{CREATE, DIFF, DIFF_EMPTY, LIST};

    /// Answers from a script and records every command.
    pub struct Fake {
        pub calls: Mutex<Vec<Vec<String>>>,
        pub list: Result<Ran, String>,
        pub diff: Result<Ran, String>,
        pub create: Result<Ran, String>,
    }

    pub fn ok(stdout: &str) -> Result<Ran, String> {
        Ok(Ran {
            code: Some(0),
            stdout: stdout.into(),
            stderr: String::new(),
        })
    }

    pub fn failed(code: i32, stderr: &str) -> Result<Ran, String> {
        Ok(Ran {
            code: Some(code),
            stdout: String::new(),
            stderr: stderr.into(),
        })
    }

    impl Fake {
        pub fn new() -> Fake {
            Fake {
                calls: Mutex::new(Vec::new()),
                list: ok(LIST),
                diff: ok(DIFF),
                create: ok(CREATE),
            }
        }

        pub fn calls(&self) -> Vec<Vec<String>> {
            self.calls.lock().unwrap().clone()
        }

        pub fn count(&self, sub: &str) -> usize {
            self.calls().iter().filter(|c| c[1] == sub).count()
        }
    }

    impl Runner for Fake {
        fn run(&self, args: &[String], _: Duration, _: &AtomicBool) -> Result<Ran, String> {
            self.calls.lock().unwrap().push(args.to_vec());
            match args[1].as_str() {
                "list" => self.list.clone(),
                "diff" => self.diff.clone(),
                _ => self.create.clone(),
            }
        }
    }

    fn app(fake: &Arc<Fake>) -> App {
        let mut s = crate::app::init();
        s.compare.runner = fake.clone();
        s.compare.threaded = false;
        s.visible = true;
        s.nav = crate::groups::Nav::Compare;
        s
    }

    fn change(kind: Kind, key: &str, before: &str, after: &str) -> Change {
        Change {
            kind,
            key: key.into(),
            before: before.into(),
            after: after.into(),
        }
    }

    fn texts(d: &Diff, id: &str) -> Vec<String> {
        groups(d)
            .into_iter()
            .find(|g| g.id == id)
            .unwrap_or_else(|| panic!("no group {id}"))
            .lines
            .into_iter()
            .map(|l| l.text)
            .collect()
    }

    #[test]
    fn the_list_reads_newest_first_with_baselines_named() {
        let list = parse_list(LIST).unwrap();
        assert_eq!(list.len(), 7);
        assert!(list.windows(2).all(|w| w[0].taken_at >= w[1].taken_at));
        assert_eq!(list[0].id, "snap-20261122T121540Z-1001");
        assert_eq!(list[0].when, "2026-11-22 12:15 UTC");
        assert_eq!(list[0].apps, 49);
        assert!(!list[0].baseline);
        let b = list.iter().find(|s| s.baseline).unwrap();
        assert_eq!(label(b), "2026-11-22 12:14 UTC · Before the Xcode update");
    }

    #[test]
    fn no_snapshots_stored_reads_as_an_empty_list() {
        for none in ["", "  \n", "null", "[]"] {
            assert_eq!(parse_list(none), Ok(Vec::new()), "{none:?}");
        }
        assert!(parse_list("{}").is_err());
        assert!(parse_list("<html>").is_err());
        // Rows without an id are no use to a diff; extra fields and other cases are fine.
        let l = parse_list(
            r#"[{"Kind":"live"},{"id":"s1","taken_at":"2026-01-02T03:04:05Z","extra":1}]"#,
        )
        .unwrap();
        assert_eq!(l.len(), 1);
        assert_eq!(l[0].when, "2026-01-02 03:04 UTC");
    }

    #[test]
    fn a_diff_reads_with_null_sections_and_unknown_kinds() {
        let d = parse_diff(DIFF).unwrap();
        assert_eq!(d.a, "snap-20260515T011539Z-1007");
        assert_eq!(d.sections.len(), 11);
        assert!(d.total() > 40);
        let empty = parse_diff(DIFF_EMPTY).unwrap();
        assert_eq!(empty.total(), 0);
        assert!(groups(&empty).is_empty());
        let odd = parse_diff(
            r#"{"Sections":[{"Name":"new_area","Changes":[{"Kind":"moved","Key":"x","Before":"1"}]},{"Name":"","Changes":null}]}"#,
        )
        .unwrap();
        assert_eq!(odd.sections.len(), 1);
        assert_eq!(odd.sections[0].changes[0].kind, Kind::Changed);
        assert!(parse_diff("[]").is_err());
        assert!(parse_diff("").is_err());
    }

    #[test]
    fn the_diff_is_grouped_in_plain_words() {
        let d = parse_diff(DIFF).unwrap();
        let g = groups(&d);
        let ids: Vec<&str> = g.iter().map(|g| g.id.as_str()).collect();
        assert_eq!(ids[0], "apps", "apps first");
        assert!(
            !ids.contains(&"vpn"),
            "groups with nothing in them are left out"
        );
        assert!(!ids.contains(&"active_runtimes"));
        let apps = texts(&d, "apps");
        assert!(apps.contains(&"com.example.terminal added (version 0.1.0)".to_owned()));
        assert!(apps.contains(&"com.example.mailer removed (was version 4.97.1)".to_owned()));
        assert!(apps.contains(&"com.example.editor updated: 0.212.6 → 1.12.0".to_owned()));
        assert!(apps.contains(&"com.example.browser now uses the hardened runtime".to_owned()));
        // Added first, then removed, then changed.
        assert_eq!(g[0].lines[0].kind, Kind::Added);
        assert_eq!(g[0].lines.last().unwrap().kind, Kind::Changed);
        assert_eq!(g[0].summary, "4 added · 4 removed · 6 changed");
        assert!(
            texts(&d, "listening_ports")
                .contains(&"Port 6379 (tcp) is no longer listening".to_owned())
        );
        assert!(texts(&d, "system_limits").contains(&"Processes in use: 0.0% → 11.2%".to_owned()));
        assert!(texts(&d, "host").contains(&"macOS version: 14.5 → 15.2".to_owned()));
        assert!(texts(&d, "host").contains(&"Data held compressed: 0.0 MB → 5.0 GB".to_owned()));
        assert!(
            texts(&d, "sysctls")[0]
                .starts_with("Last restart: Mon Mar  4 08:00:00 2024 → Fri Nov 20")
        );
        assert!(texts(&d, "jdks").iter().any(|t| {
            t.starts_with("Java 23.0.2/Example Vendor@manual installed at /Users/me/")
        }));
        assert!(
            texts(&d, "brew_formulae")
                .contains(&"python@3.11 removed (was version 3.11.9)".to_owned())
        );
    }

    #[test]
    fn small_wordings() {
        let mut d = Diff::default();
        d.sections.push(Section {
            name: "apps".into(),
            changes: vec![
                change(
                    Kind::Changed,
                    "/Applications/Old Thing.app:team-id",
                    "(unsigned)",
                    "ABC123",
                ),
                change(Kind::Changed, "com.x.y:hardened-runtime", "true", "false"),
                change(Kind::Changed, "com.x.y:gatekeeper", "accepted", "rejected"),
                change(Kind::Added, "/Applications/Plain.app", "", ""),
            ],
        });
        d.sections.push(Section {
            name: "vpn".into(),
            changes: vec![change(Kind::Changed, "vpn_active", "false", "true")],
        });
        d.sections.push(Section {
            name: "path_dirs".into(),
            changes: vec![change(Kind::Changed, "/opt/bin", "pos 3", "pos 1")],
        });
        d.sections.push(Section {
            name: "some_new_thing".into(),
            changes: vec![change(Kind::Added, "k", "", "v")],
        });
        let apps = texts(&d, "apps");
        assert!(apps.contains(
            &"Old Thing is signed by a different developer ((unsigned) → ABC123)".to_owned()
        ));
        assert!(apps.contains(&"com.x.y no longer uses the hardened runtime".to_owned()));
        assert!(apps.contains(&"com.x.y: Gatekeeper accepted → rejected".to_owned()));
        assert!(apps.contains(&"Plain added".to_owned()));
        assert_eq!(texts(&d, "vpn"), vec!["VPN: off → on".to_owned()]);
        assert_eq!(
            texts(&d, "path_dirs"),
            vec!["/opt/bin moved on PATH: pos 3 → pos 1".to_owned()]
        );
        let g = groups(&d);
        assert_eq!(
            g.last().unwrap().title,
            "some new thing",
            "an unknown area still shows"
        );
    }

    #[test]
    fn host_sizes_read_as_sizes() {
        let d = Diff {
            sections: vec![Section {
                name: "host".into(),
                changes: vec![change(
                    Kind::Changed,
                    "ram_bytes",
                    "17179869184",
                    "34359738368",
                )],
            }],
            ..Diff::default()
        };
        assert_eq!(
            texts(&d, "host"),
            vec!["Memory installed: 16.0 GB → 32.0 GB".to_owned()]
        );
    }

    #[test]
    fn arguments_put_the_flags_before_the_ids() {
        assert_eq!(list_args(), ["snapshot", "list", "--json"]);
        assert_eq!(
            diff_args("a", Some("b")),
            ["snapshot", "diff", "--json", "a", "b"]
        );
        assert_eq!(
            diff_args("a", None),
            ["snapshot", "diff", "--json", "a", "live"]
        );
        assert_eq!(take_args(false), ["snapshot", "create"]);
        assert_eq!(
            take_args(true),
            ["snapshot", "create", "--baseline", "--name", BASELINE_NAME]
        );
    }

    #[test]
    fn a_taken_snapshot_is_named_by_its_id() {
        assert_eq!(
            parse_taken(CREATE).as_deref(),
            Some("snap-20261122T121540Z-1001")
        );
        assert_eq!(parse_taken("nothing useful"), None);
        assert_eq!(parse_taken("id:   \n"), None);
    }

    #[test]
    fn failures_say_why() {
        let ran = |code, err: &str| Ran {
            code: Some(code),
            stdout: String::new(),
            stderr: err.into(),
        };
        assert!(
            failure("snapshot diff", &ran(2, "unknown command \"snapshot\""))
                .contains("Update Spectra")
        );
        let sudo = failure(
            "snapshot create",
            &ran(1, "open /var/db: permission denied"),
        );
        assert!(sudo.contains("never runs sudo") && sudo.contains("permission denied"));
        assert_eq!(
            failure("snapshot diff", &ran(1, "\nsnapshot \"x\": not found\n")),
            "snapshot \"x\": not found"
        );
        assert_eq!(
            failure("snapshot list", &ran(7, "")),
            "spectra snapshot list stopped with exit status 7"
        );
    }

    #[test]
    fn opening_reads_only_the_list() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.open_compare();
        assert!(s.compare.reading);
        assert!(s.land_compare());
        assert_eq!(fake.calls(), vec![list_args()], "no diff, no snapshot");
        assert_eq!(s.compare.list.as_ref().unwrap().len(), 7);
        assert!(!s.compare.reading && s.compare.list_error.is_none());
    }

    #[test]
    fn nothing_is_read_while_the_window_is_hidden() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.visible = false;
        s.open_compare();
        s.tick_compare();
        assert!(fake.calls().is_empty());
        s.visible = true;
        assert!(s.tick_compare());
        s.land_compare();
        assert_eq!(fake.count("list"), 1);
    }

    #[test]
    fn a_comparison_runs_only_on_a_press_with_an_earlier_one_chosen() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.open_compare();
        s.land_compare();
        s.run_compare();
        assert_eq!(fake.count("diff"), 0, "nothing chosen yet");
        let id = s.compare.list.as_ref().unwrap()[3].id.clone();
        s.pick_from(&id);
        s.pick_to(Some(&id));
        s.run_compare();
        assert_eq!(fake.count("diff"), 0, "not against itself");
        s.pick_to(None);
        s.run_compare();
        assert!(s.compare.diffing.is_some());
        s.land_compare();
        assert_eq!(fake.calls().last().unwrap(), &diff_args(&id, None));
        let shown = s.compare.shown.as_ref().unwrap();
        assert_eq!(
            (shown.from.as_str(), shown.to.as_deref()),
            (id.as_str(), None)
        );
        assert!(s.compare.diffing.is_none() && s.compare.diff_error.is_none());
        // Two stored ones.
        let other = s.compare.list.as_ref().unwrap()[0].id.clone();
        s.pick_to(Some(&other));
        s.run_compare();
        s.land_compare();
        assert_eq!(fake.calls().last().unwrap(), &diff_args(&id, Some(&other)));
    }

    #[test]
    fn a_failed_diff_keeps_the_one_before() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.open_compare();
        s.land_compare();
        s.pick_from("snap-20260515T011539Z-1007");
        s.run_compare();
        s.land_compare();
        assert!(s.compare.shown.is_some());
        let bad = Arc::new(Fake {
            diff: failed(1, "snapshot \"x\" has no JSON blob"),
            ..Fake::new()
        });
        s.compare.runner = bad;
        s.run_compare();
        s.land_compare();
        assert!(s.compare.shown.is_some(), "the earlier comparison stays");
        assert_eq!(
            s.compare.diff_error.as_deref(),
            Some("snapshot \"x\" has no JSON blob")
        );
    }

    #[test]
    fn taking_a_snapshot_waits_for_a_press_says_where_it_went_and_rereads_the_list() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.open_compare();
        s.land_compare();
        assert_eq!(fake.count("create"), 0);
        s.take_snapshot(false);
        s.take_snapshot(false);
        s.land_compare();
        assert_eq!(fake.count("create"), 1);
        let taken = s.compare.taken.clone().unwrap().unwrap();
        assert_eq!(taken.id, "snap-20261122T121540Z-1001");
        s.land_compare();
        assert_eq!(fake.count("list"), 2, "the new snapshot is listed");
    }

    #[test]
    fn a_snapshot_that_could_not_be_saved_is_a_failure() {
        let fake = Arc::new(Fake {
            create: Ok(Ran {
                code: Some(0),
                stdout: CREATE.into(),
                stderr: "warning: could not persist snapshot: unable to open database file\n"
                    .into(),
            }),
            ..Fake::new()
        });
        let mut s = app(&fake);
        s.take_snapshot(true);
        s.land_compare();
        let why = s.compare.taken.clone().unwrap().unwrap_err();
        assert!(
            why.contains("couldn't save it") && why.contains("unable to open database file"),
            "{why}"
        );
        assert_eq!(fake.count("list"), 0, "nothing new to list");
    }

    #[test]
    fn leaving_the_page_stops_a_comparison_but_not_a_snapshot() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.compare.diffing = Some(Going::shown(0.0));
        s.compare.taking = Some(Going::shown(0.0));
        s.cancel_compare();
        assert!(s.compare.diffing.is_none());
        assert!(s.compare.taking.is_some(), "a snapshot is left to finish");
    }

    #[test]
    fn the_result_names_what_the_diff_ran_with_not_the_choice_now() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.open_compare();
        s.land_compare();
        let (a, b) = {
            let l = s.compare.list.as_ref().unwrap();
            (l[6].id.clone(), l[2].id.clone())
        };
        s.pick_from(&a);
        s.run_compare();
        s.pick_from(&b);
        s.land_compare();
        let shown = s.compare.shown.clone().unwrap();
        assert_eq!(
            (shown.from.as_str(), shown.to.as_deref()),
            (a.as_str(), None)
        );
        // The chosen one dropped by a list reread mid-diff: still the original.
        s.pick_from(&a);
        let diffing = Arc::clone(&s.compare.runner);
        s.compare.runner = Arc::new(Fake {
            list: ok("[]"),
            ..Fake::new()
        });
        s.open_compare();
        s.compare.runner = diffing;
        s.run_compare();
        s.land_compare();
        assert_eq!(s.compare.from, None, "the list no longer has it");
        let shown = s.compare.shown.clone().unwrap();
        assert_eq!(shown.from, a);
    }

    #[test]
    fn a_snapshot_landing_off_the_page_reads_nothing() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.nav = crate::groups::Nav::All;
        s.take_snapshot(false);
        s.land_compare();
        assert!(s.compare.taken.as_ref().unwrap().is_ok());
        assert_eq!(fake.count("list"), 0);
        assert!(!s.compare.reading);
        s.tick_compare();
        assert_eq!(fake.count("list"), 0);
        s.show(crate::groups::Nav::Compare);
        s.land_compare();
        assert_eq!(fake.count("list"), 1, "opening the page reads it");
    }

    #[test]
    fn a_choice_whose_snapshot_was_pruned_is_dropped() {
        let fake = Arc::new(Fake::new());
        let mut s = app(&fake);
        s.pick_from("snap-gone");
        s.open_compare();
        s.land_compare();
        assert_eq!(s.compare.from, None);
    }

    #[test]
    fn a_failed_list_says_why() {
        let fake = Arc::new(Fake {
            list: failed(1, "open /Users/me/.spectra/spectra.db: permission denied"),
            ..Fake::new()
        });
        let mut s = app(&fake);
        s.open_compare();
        s.land_compare();
        assert!(s.compare.list.is_none());
        assert!(
            s.compare
                .list_error
                .as_deref()
                .unwrap()
                .contains("never runs sudo")
        );
    }
}
