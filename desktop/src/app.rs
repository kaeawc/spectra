//! App state: both feeds' latest readings (every process, and the JVMs in detail), each one's
//! recent history, the pinned JVM baseline, and what's on screen.

use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::time::Duration;

use rune::TextEdit;
use rune_kit::shell::PaneState;
use rune_kit::{SortDir, TableState};

use crate::data::{Jvm, Sample};
use crate::findings::{Finding, Link, Severity, diagnose};
use crate::groups::{self, Category, Group, Nav, Row, Sort, SortKey};
use crate::proc_findings;
use crate::procs::{ProcError, ProcFeed, ProcSample, Process, Role, Runtime};
use crate::signal::{self, Attempt, Me, Signal, Signals, Target};

pub const HISTORY: usize = 120;
/// Readings kept per process: three minutes at one every 2 s.
pub const PROC_HISTORY: usize = 90;
/// How long each feed rests after a reading lands before the next. JVM readings take Spectra
/// about 17 s; process readings about 0.2 s.
pub const REFRESH: Duration = Duration::from_secs(2);
/// How old the process numbers may get, in seconds, before the app says when they're from.
pub const STALE_AFTER: f64 = 3.0 * REFRESH.as_secs_f64();
/// How long, in seconds of readings, a process asked to quit may take before the app says it
/// hasn't and offers Force quit.
pub const STILL_AFTER: f64 = 5.0;

pub type Feed = Result<Vec<Jvm>, String>;

/// A reading pinned by "Pin baseline", and which reading it was, so the timeline can mark it.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Baseline {
    pub sample: Sample,
    /// How many readings of this JVM had landed when it was pinned (the pinned one included).
    pub reading: u64,
}

/// One process's recent readings, oldest first. `started` tells a reused pid from the old one.
#[derive(Clone, Debug, Default)]
pub struct Track {
    pub started: String,
    pub samples: Vec<ProcSample>,
}

/// A process page: the pid, and when that process started, so a reused pid isn't taken for it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PageRef {
    pub pid: u32,
    pub started: String,
}

/// A Quit or Force quit waiting for the person to confirm it.
#[derive(Clone, Debug, PartialEq)]
pub struct Confirm {
    pub target: Target,
    pub signal: Signal,
}

/// Where a Quit or Force quit stands.
#[derive(Clone, Debug, PartialEq)]
pub enum Status {
    /// Sent at the window clock's `at`; no reading since has shown it gone.
    Asked { at: f64 },
    /// A later reading no longer lists it.
    Ended,
    /// Still listed `STILL_AFTER` seconds after it was sent.
    StillRunning,
    /// Nothing was sent: the pid is gone or someone else's.
    Refused(String),
    /// `ps` or `kill` failed.
    Failed(String),
}

#[derive(Clone, Debug, PartialEq)]
pub struct Outcome {
    pub target: Target,
    pub signal: Signal,
    pub status: Status,
}

impl Outcome {
    /// The outcome in a line, as it stands.
    pub fn line(&self) -> String {
        let name = &self.target.name;
        match (&self.status, self.signal) {
            (Status::Asked { .. }, Signal::Term) => format!("Asked {name} to quit"),
            (Status::Asked { .. }, Signal::Kill) => format!("Stopping {name}"),
            (Status::Ended, Signal::Term) => format!("{name} quit"),
            (Status::Ended, Signal::Kill) => format!("{name} was stopped"),
            (Status::StillRunning, Signal::Term) => {
                format!("{name} is still running {STILL_AFTER:.0} s after being asked to quit")
            }
            (Status::StillRunning, Signal::Kill) => {
                format!("{name} is still running after Force quit; it may be stuck waiting on the system")
            }
            (Status::Refused(why) | Status::Failed(why), _) => why.clone(),
        }
    }
}

/// What the sidebar and the overview count, worked out once per reading.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Stats {
    /// Processes per `Category::ALL`.
    pub kinds: [usize; Category::ALL.len()],
    /// Processes per `Runtime::ALL`.
    pub runtimes: [usize; Runtime::ALL.len()],
    pub cpu: f64,
    pub apps: usize,
    /// Memory per category, most first, the empty ones left out.
    pub memory: Vec<(Category, f64)>,
    /// Whether any process carries a signature (none do on Linux).
    pub signed: bool,
}

impl Stats {
    pub fn of(procs: &[Process]) -> Stats {
        let mut s = Stats::default();
        let mut memory = [0.0; Category::ALL.len()];
        for p in procs {
            for (i, c) in Category::ALL.iter().enumerate() {
                if c.holds(p.role) {
                    s.kinds[i] += 1;
                    memory[i] += p.rss_kib;
                }
            }
            if let Some(i) = Runtime::ALL.iter().position(|r| *r == p.runtime) {
                s.runtimes[i] += 1;
            }
            s.cpu += p.cpu_pct;
            s.apps += usize::from(p.role == Role::App);
            s.signed |= p.signing.is_some();
        }
        s.memory = Category::ALL.iter().copied().zip(memory).filter(|(_, kib)| *kib > 0.0).collect();
        s.memory.sort_by(|a, b| b.1.total_cmp(&a.1));
        s
    }
}

/// Unix seconds now, for telling how long a process has run.
fn system_time() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| d.as_secs() as i64)
}

pub struct App {
    pub sidebar: PaneState,
    pub jvms: Vec<Jvm>,
    pub history: HashMap<u32, VecDeque<Sample>>,
    /// Readings landed per JVM since it appeared, including those rolled out of `history`.
    pub readings: HashMap<u32, u64>,
    /// The readings the evidence compares against.
    pub baseline: HashMap<u32, Baseline>,
    /// The process (or JVM) whose page is open, over whatever `nav` shows.
    pub selected: Option<u32>,
    /// When the open page's process started, empty for a JVM the process feed doesn't list.
    pub selected_started: String,
    /// The pages "back" returns to, most recent last.
    pub back_stack: Vec<PageRef>,
    pub expanded: Option<String>,
    pub args_open: bool,
    /// Whether a process page shows its whole command line.
    pub cmd_all: bool,
    pub loaded: bool,
    pub error: Option<String>,
    pub feed: Option<Receiver<Feed>>,
    pub feed_tx: Option<Sender<Feed>>,

    pub procs: Vec<Process>,
    proc_index: HashMap<u32, usize>,
    pub tracks: HashMap<u32, Track>,
    pub proc_findings: Vec<Finding>,
    pub proc_loaded: bool,
    /// What went wrong with the latest process reading; the list before it stays.
    pub proc_error: Option<ProcError>,
    /// The window clock (`now`) when the last good process reading landed.
    pub proc_updated: Option<f64>,
    pub proc_feed: Option<Receiver<ProcFeed>>,
    pub proc_tx: Option<Sender<ProcFeed>>,
    /// Logical cores, to put total CPU in proportion.
    pub cores: usize,
    /// The window's clock in seconds, as `poll` last saw it.
    pub now: f64,
    /// The wall clock in Unix seconds, to tell a process that just started. Tests pin it.
    pub wall: fn() -> i64,

    /// Every finding, worst first, as of the last reading of either feed.
    pub findings: Vec<Finding>,
    pub stats: Stats,

    pub nav: Nav,
    pub sort: Sort,
    pub table: TableState,
    pub query: TextEdit,
    /// Ids of the groups shown open.
    pub open_groups: HashSet<String>,
    /// The list as it stands: `nav` and the search applied, grouped and sorted.
    pub groups: Vec<Group>,
    pub rows: Vec<Row>,

    /// Asks `ps` about a pid and sends signals; tests swap in a recorder.
    pub signals: Box<dyn Signals>,
    pub me: Me,
    pub confirm: Option<Confirm>,
    /// The last Quit or Force quit, until another replaces it or it's dismissed.
    pub outcome: Option<Outcome>,
    /// Wakes the process feed for a reading now, after a signal.
    pub poke: Option<Sender<()>>,
    pub poke_rx: Option<Receiver<()>>,
}

pub fn init() -> App {
    let (tx, rx) = channel();
    let (ptx, prx) = channel();
    let (poke, poke_rx) = channel();
    let mut table = TableState::new().key("procs");
    table.set_sort("cpu", SortDir::Descending);
    App {
        sidebar: PaneState::new(240.0).range(160.0, 420.0),
        jvms: Vec::new(),
        history: HashMap::new(),
        readings: HashMap::new(),
        baseline: HashMap::new(),
        selected: None,
        selected_started: String::new(),
        back_stack: Vec::new(),
        expanded: None,
        args_open: false,
        cmd_all: false,
        loaded: false,
        error: None,
        feed: Some(rx),
        feed_tx: Some(tx),
        procs: Vec::new(),
        proc_index: HashMap::new(),
        tracks: HashMap::new(),
        proc_findings: Vec::new(),
        proc_loaded: false,
        proc_error: None,
        proc_updated: None,
        proc_feed: Some(prx),
        proc_tx: Some(ptx),
        cores: std::thread::available_parallelism().map_or(1, |n| n.get()),
        now: 0.0,
        wall: system_time,
        findings: Vec::new(),
        stats: Stats::default(),
        nav: Nav::Overview,
        sort: Sort::default(),
        table,
        query: TextEdit::new(""),
        open_groups: HashSet::new(),
        groups: Vec::new(),
        rows: Vec::new(),
        signals: Box::new(signal::System),
        me: Me { pid: std::process::id(), uid: None },
        confirm: None,
        outcome: None,
        poke: Some(poke),
        poke_rx: Some(poke_rx),
    }
}

/// `12 s`, `3 min`, `2 h`.
pub fn fmt_ago(secs: f64) -> String {
    let s = secs.max(0.0).round() as u64;
    match s {
        0..60 => format!("{s} s"),
        60..3600 => format!("{} min", s / 60),
        _ => format!("{} h", s / 3600),
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
                self.jvms = jvms;
                self.drop_gone_selection();
                self.refresh_findings();
            }
            Err(e) => self.error = Some(e),
        }
    }

    /// Takes a reading of every process: extends each one's history, forgets the ones that
    /// ended, and rebuilds the list, the counts and the findings. A failed or empty reading
    /// keeps everything from the last good one and says so.
    pub fn land_procs(&mut self, feed: ProcFeed) {
        self.proc_loaded = true;
        let procs = match feed {
            Ok(procs) if procs.is_empty() => {
                self.proc_error = Some(ProcError::Failed("Spectra listed no processes.".into()));
                return;
            }
            Ok(procs) => procs,
            Err(e) => {
                self.proc_error = Some(e);
                return;
            }
        };
        self.proc_error = None;
        self.proc_updated = Some(self.now);
        if self.me.uid.is_none() {
            self.me.uid = procs.iter().find(|p| p.pid == self.me.pid).map(|p| p.uid);
        }
        let mut tracks = HashMap::with_capacity(procs.len());
        let mut reused = Vec::new();
        for p in &procs {
            let old = self.tracks.remove(&p.pid);
            if old.as_ref().is_some_and(|t| t.started != p.started_at) {
                reused.push(p.pid);
            }
            let mut t = old.filter(|t| t.started == p.started_at).unwrap_or_else(|| Track {
                started: p.started_at.clone(),
                samples: Vec::new(),
            });
            t.samples.push(p.sample());
            if t.samples.len() > PROC_HISTORY {
                t.samples.remove(0);
            }
            tracks.insert(p.pid, t);
        }
        self.tracks = tracks;
        self.proc_index = procs.iter().enumerate().map(|(i, p)| (p.pid, i)).collect();
        self.procs = procs;
        self.forget_reused(&reused);
        self.track_outcome();
        self.stats = Stats::of(&self.procs);
        let tracks = &self.tracks;
        let empty: &[ProcSample] = &[];
        self.proc_findings = proc_findings::diagnose(
            &self.procs,
            &|pid| tracks.get(&pid).map_or(empty, |t| t.samples.as_slice()),
            (self.wall)(),
        );
        self.drop_gone_selection();
        self.refresh_findings();
        self.rebuild();
        self.prune_open_groups();
    }

    /// Whatever was keyed by a pid that now belongs to another process: its JVM numbers and
    /// baseline, an open finding, the table's cursor.
    fn forget_reused(&mut self, reused: &[u32]) {
        for pid in reused {
            self.history.remove(pid);
            self.readings.remove(pid);
            self.baseline.remove(pid);
            let prefix = format!("{pid}/");
            if self.expanded.as_deref().is_some_and(|id| id.starts_with(&prefix)) {
                self.expanded = None;
            }
            if self.table.cursor() == Some(format!("p:{pid}").as_str()) {
                self.table.clear_selection();
            }
        }
    }

    /// Forgets open groups that no longer exist in any list.
    fn prune_open_groups(&mut self) {
        if self.open_groups.is_empty() {
            return;
        }
        let all: Vec<&Process> = self.procs.iter().collect();
        let ids: HashSet<String> = groups::group(&all, self.sort).into_iter().map(|g| g.id).collect();
        self.open_groups.retain(|id| ids.contains(id));
    }

    fn refresh_findings(&mut self) {
        let mut all: Vec<Finding> = self
            .jvms
            .iter()
            .flat_map(|j| diagnose(j, &self.history(j.pid)))
            .chain(self.proc_findings.iter().cloned())
            .collect();
        all.sort_by_key(|f| (f.severity, f.pid));
        self.findings = all;
    }

    /// Whether `page` still names a running process (or a JVM the process feed doesn't list).
    fn is_live(&self, page: &PageRef) -> bool {
        match self.process(page.pid) {
            Some(p) => page.started.is_empty() || p.started_at == page.started,
            None => self.jvm(page.pid).is_some(),
        }
    }

    /// Closes the open page once its process is in neither feed, or its pid is someone else's.
    /// A page whose process the app was asked to stop stays, to say it ended.
    fn drop_gone_selection(&mut self) {
        if let Some(pid) = self.selected
            && !self.is_live(&PageRef { pid, started: self.selected_started.clone() })
            && self.ended_page().is_none()
        {
            self.selected = None;
            self.selected_started.clear();
            self.back_stack.clear();
        }
        let stack = std::mem::take(&mut self.back_stack);
        self.back_stack = stack.into_iter().filter(|p| self.is_live(p)).collect();
    }

    /// Whether the process feed has given a list; without one the app shows only JVMs. A failed
    /// reading keeps the last list.
    pub fn has_processes(&self) -> bool {
        !self.procs.is_empty() && !matches!(self.proc_error, Some(ProcError::TooOld(_)))
    }

    /// When the process numbers are from, once that's worth saying: the last reading failed, or
    /// none has landed for a while.
    pub fn feed_status(&self) -> Option<String> {
        if !self.has_processes() {
            return None;
        }
        let ago = self.now - self.proc_updated?;
        let when = format!("Updated {} ago", fmt_ago(ago));
        match &self.proc_error {
            Some(ProcError::Failed(why)) => Some(format!("{when} · last reading failed: {}", why.trim_end_matches('.'))),
            _ if ago > STALE_AFTER => Some(when),
            _ => None,
        }
    }

    pub fn process(&self, pid: u32) -> Option<&Process> {
        self.proc_index.get(&pid).and_then(|&i| self.procs.get(i))
    }

    pub fn children(&self, pid: u32) -> Vec<&Process> {
        self.procs.iter().filter(|p| p.ppid == pid && p.pid != pid).collect()
    }

    pub fn proc_history(&self, pid: u32) -> &[ProcSample] {
        self.tracks.get(&pid).map_or(&[], |t| t.samples.as_slice())
    }

    /// Re-cuts the list after a reading, a sidebar choice, a search, a sort or a group opening.
    pub fn rebuild(&mut self) {
        let q = self.query.text.trim().to_lowercase();
        let nav = if self.nav == Nav::Overview { Nav::All } else { self.nav };
        let kept: Vec<&Process> = self
            .procs
            .iter()
            .filter(|p| nav.holds(p) && groups::matches_query(p, &q))
            .collect();
        self.groups = groups::group(&kept, self.sort);
        self.rows = groups::flatten(&self.groups, &self.open_groups);
    }

    /// Shows `nav` in the main pane, closing any open page.
    pub fn show(&mut self, nav: Nav) {
        self.nav = nav;
        self.select(None);
        self.table.clear_selection();
        self.rebuild();
    }

    pub fn toggle_group(&mut self, id: &str) {
        if !self.open_groups.remove(id) {
            self.open_groups.insert(id.to_owned());
        }
        self.rebuild();
    }

    /// A header click: CPU and memory start busiest first, names A to Z.
    pub fn sort_by(&mut self, column: &str, dir: SortDir) {
        let key = match column {
            "mem" => SortKey::Memory,
            "name" => SortKey::Name,
            _ => SortKey::Cpu,
        };
        let descending = if key != self.sort.key && key != SortKey::Name {
            true
        } else {
            dir == SortDir::Descending
        };
        self.sort = Sort { key, descending };
        self.table.set_sort(column, if descending { SortDir::Descending } else { SortDir::Ascending });
        self.rebuild();
    }

    /// A list row was opened (Return or a double click): a group opens or closes, a process
    /// opens its page. Moving the cursor onto a row does neither.
    pub fn open_row(&mut self, id: &str) {
        if let Some(group) = id.strip_prefix("g:") {
            self.toggle_group(group);
        } else if let Some(pid) = id.strip_prefix("p:").and_then(|p| p.parse().ok()) {
            self.open(pid);
        }
    }

    pub fn follow(&mut self, link: Link) {
        match link {
            Link::Process(pid) => self.open(pid),
            Link::Unidentified => self.show(Nav::Unidentified),
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

    /// Findings that need a look (worse than information).
    pub fn problems(&self) -> usize {
        self.findings.iter().filter(|f| f.severity < Severity::Info).count()
    }

    /// One process's findings, its JVM's included, worst first.
    pub fn findings_for(&self, pid: u32) -> Vec<&Finding> {
        self.findings.iter().filter(|f| f.link == Link::Process(pid)).collect()
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

    fn page_ref(&self, pid: u32) -> PageRef {
        PageRef {
            pid,
            started: self.process(pid).map_or_else(String::new, |p| p.started_at.clone()),
        }
    }

    /// Opens `pid`'s page, or closes the page, without history: what the sidebar does.
    pub fn select(&mut self, pid: Option<u32>) {
        self.back_stack.clear();
        self.set_page(pid);
    }

    fn set_page(&mut self, pid: Option<u32>) {
        if self.selected != pid {
            self.cmd_all = false;
        }
        self.selected = pid;
        self.selected_started = pid.map_or_else(String::new, |pid| self.page_ref(pid).started);
    }

    /// Opens `pid`'s page from wherever we are, so "back" returns here.
    pub fn open(&mut self, pid: u32) {
        if let Some(cur) = self.selected {
            if cur == pid {
                return;
            }
            self.back_stack.push(PageRef { pid: cur, started: self.selected_started.clone() });
        }
        self.set_page(Some(pid));
    }

    /// Returns to the page before this one, or to the list once there is none.
    pub fn back(&mut self) {
        while let Some(page) = self.back_stack.pop() {
            if self.is_live(&page) {
                self.set_page(Some(page.pid));
                return;
            }
        }
        self.set_page(None);
    }

    /// The page "back" would return to.
    pub fn back_to(&self) -> Option<u32> {
        self.back_stack.last().map(|p| p.pid)
    }

    /// The process a list row's Quit acts on: the process itself, or an app group's main
    /// process. A group of several that isn't an app, or an app without its main process,
    /// has none; its processes are quit one at a time.
    pub fn row_target(&self, id: &str) -> Option<u32> {
        if let Some(pid) = id.strip_prefix("p:") {
            return pid.parse().ok().filter(|pid| self.process(*pid).is_some());
        }
        let id = id.strip_prefix("g:")?;
        let g = self.groups.iter().find(|g| g.id == id)?;
        if g.kind != groups::GroupKind::App {
            return None;
        }
        g.pids.iter().copied().filter(|pid| self.process(*pid).is_some_and(|p| p.role == Role::App)).min()
    }

    /// What the list's Quit acts on: the row under the cursor's target.
    pub fn cursor_target(&self) -> Option<u32> {
        self.table.cursor().and_then(|id| self.row_target(id))
    }

    /// What ⌥⌘Q acts on: the open page's process, else the list cursor's.
    pub fn shortcut_target(&self) -> Option<u32> {
        match self.selected {
            Some(pid) if self.ended_page().is_none() => self.process(pid).map(|p| p.pid),
            Some(_) => None,
            None if self.nav != Nav::Overview => self.cursor_target(),
            None => None,
        }
    }

    /// Why `pid` can't be quit from here, if it can't.
    pub fn refusal(&self, pid: u32) -> Option<String> {
        match self.process(pid) {
            Some(p) => signal::refusal(p, self.me),
            None => Some("That process has already ended.".into()),
        }
    }

    /// Whether a Quit for `pid` was sent and hasn't settled yet, so asking again adds nothing.
    pub fn quit_pending(&self, pid: u32) -> bool {
        self.outcome.as_ref().is_some_and(|o| {
            o.target.pid == pid && o.signal == Signal::Term && matches!(o.status, Status::Asked { .. })
        })
    }

    /// Opens the confirmation for `signal` to `pid`. It never sends anything itself.
    pub fn ask(&mut self, pid: u32, signal: Signal) {
        if self.confirm.is_some() || self.refusal(pid).is_some() {
            return;
        }
        if signal == Signal::Term && self.quit_pending(pid) {
            return;
        }
        if let Some(p) = self.process(pid) {
            self.confirm = Some(Confirm { target: Target::of(p), signal });
        }
    }

    pub fn cancel(&mut self) {
        self.confirm = None;
    }

    /// The confirmation's button: checks the pid is still the process confirmed, then sends.
    /// The confirmation is used up on the first press, so a second sends nothing.
    pub fn confirm(&mut self) {
        let Some(Confirm { target, signal }) = self.confirm.take() else {
            return;
        };
        let status = match signal::quit(self.signals.as_ref(), &target, signal) {
            Attempt::Sent => Status::Asked { at: self.now },
            Attempt::Refused(why) => Status::Refused(why),
            Attempt::Failed(why) => Status::Failed(why),
        };
        let sent = matches!(status, Status::Asked { .. });
        self.outcome = Some(Outcome { target, signal, status });
        if sent && let Some(poke) = &self.poke {
            let _ = poke.send(());
        }
    }

    pub fn dismiss_outcome(&mut self) {
        self.outcome = None;
    }

    /// After a reading: a process that was sent a signal and is gone has ended; one still
    /// there `STILL_AFTER` seconds on is still running.
    fn track_outcome(&mut self) {
        let Some(o) = &mut self.outcome else { return };
        let Status::Asked { at } = o.status else { return };
        let listed = self.proc_index.get(&o.target.pid).and_then(|&i| self.procs.get(i));
        if !listed.is_some_and(|p| o.target.is(p)) {
            o.status = Status::Ended;
        } else if self.now - at >= STILL_AFTER {
            o.status = Status::StillRunning;
        }
    }

    /// The open page's process, when the app stopped it (or was stopping it) and it's gone.
    pub fn ended_page(&self) -> Option<&Target> {
        let o = self.outcome.as_ref()?;
        let t = &o.target;
        let gone = self.process(t.pid).is_none_or(|p| !t.is(p));
        (self.selected == Some(t.pid) && self.selected_started == t.started && gone).then_some(t)
    }
}

/// The index in a history of `len` readings of the reading that was number `pinned` (1-based)
/// when `landed` readings have landed in all. `None` once it has rolled out, or before any landed.
pub fn baseline_index(landed: u64, len: usize, pinned: u64) -> Option<usize> {
    let first = landed.checked_sub(len as u64)?; // readings that rolled out before the window
    let idx = pinned.checked_sub(1)?.checked_sub(first)?;
    (idx < len as u64).then_some(idx as usize)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data;
    use crate::groups::tests::proc;

    fn started(mut p: Process, at: &str) -> Process {
        p.started_at = at.into();
        p
    }

    #[test]
    fn process_history_is_bounded_and_forgets_ended_processes() {
        let mut s = init();
        for i in 0..(PROC_HISTORY + 5) {
            s.land_procs(Ok(vec![proc(1, "a", i as f64, 1.0), proc(2, "b", 0.0, 1.0)]));
        }
        assert_eq!(s.proc_history(1).len(), PROC_HISTORY);
        assert_eq!(s.proc_history(1)[0].cpu, 5.0, "the oldest rolled out");
        s.land_procs(Ok(vec![proc(1, "a", 0.0, 1.0)]));
        assert!(s.proc_history(2).is_empty(), "pid 2 ended");
        assert!(!s.tracks.contains_key(&2));
    }

    #[test]
    fn a_reused_pid_starts_a_new_history() {
        let mut s = init();
        s.land_procs(Ok(vec![started(proc(1, "a", 1.0, 1.0), "09:00")]));
        s.land_procs(Ok(vec![started(proc(1, "a", 2.0, 1.0), "09:00")]));
        assert_eq!(s.proc_history(1).len(), 2);
        s.land_procs(Ok(vec![started(proc(1, "b", 3.0, 1.0), "10:00")]));
        assert_eq!(s.proc_history(1).len(), 1);
    }

    #[test]
    fn a_reused_pid_closes_the_page_and_forgets_what_was_keyed_by_it() {
        let mut s = init();
        let jvm = Jvm { pid: 1, main_class: "a.B".into(), gc: Some(data::Gc::default()), ..Jvm::default() };
        s.land(Ok(vec![jvm.clone()]));
        s.land_procs(Ok(vec![started(proc(1, "java", 1.0, 1.0), "09:00"), started(proc(2, "zsh", 1.0, 1.0), "09:00")]));
        s.toggle_baseline();
        s.open(2);
        s.open(1);
        assert_eq!(s.back_to(), Some(2));
        s.expanded = Some("1/jvm-healthy".into());
        // Pid 1 is now another program.
        s.land_procs(Ok(vec![started(proc(1, "node", 1.0, 1.0), "10:00"), started(proc(2, "zsh", 1.0, 1.0), "09:00")]));
        assert_eq!(s.selected, None, "the page was for the old process");
        assert!(s.baseline.is_empty() && s.history(1).is_empty(), "its JVM numbers went with it");
        assert_eq!(s.expanded, None);
        // And a reused pid in the back stack is skipped.
        s.open(2);
        s.open(1);
        s.land_procs(Ok(vec![started(proc(1, "node", 1.0, 1.0), "10:00"), started(proc(2, "zsh", 1.0, 1.0), "11:00")]));
        assert_eq!(s.selected, Some(1));
        s.back();
        assert_eq!(s.selected, None, "pid 2's page was for the zsh that ended");
    }

    #[test]
    fn open_groups_that_no_longer_exist_are_forgotten() {
        let mut s = init();
        s.land_procs(Ok(vec![proc(1, "zsh", 1.0, 1.0), proc(2, "zsh", 1.0, 1.0), proc(3, "a", 1.0, 1.0)]));
        s.toggle_group("exe:zsh");
        assert!(s.open_groups.contains("exe:zsh"));
        s.land_procs(Ok(vec![proc(1, "zsh", 1.0, 1.0), proc(3, "a", 1.0, 1.0)]));
        assert!(s.open_groups.is_empty(), "one zsh is no group");
    }

    #[test]
    fn a_failed_reading_keeps_the_last_list_and_an_ended_page_closes() {
        let mut s = init();
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0), proc(2, "b", 1.0, 1.0)]));
        s.select(Some(2));
        s.land_procs(Err(ProcError::Failed("boom".into())));
        assert_eq!(s.procs.len(), 2);
        assert_eq!(s.selected, Some(2));
        assert!(s.has_processes(), "a failed reading keeps the list");
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        assert_eq!(s.selected, None);
        assert!(s.has_processes());
    }

    #[test]
    fn an_empty_reading_is_a_failure_that_keeps_the_last_list() {
        let mut s = init();
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0), proc(2, "b", 1.0, 1.0)]));
        s.select(Some(2));
        s.land_procs(Ok(vec![]));
        assert_eq!(s.procs.len(), 2);
        assert_eq!(s.proc_history(1).len(), 1, "history kept");
        assert_eq!(s.selected, Some(2), "the page stays open");
        assert!(matches!(s.proc_error, Some(ProcError::Failed(_))));
    }

    #[test]
    fn the_status_says_when_the_numbers_are_from_once_that_matters() {
        let mut s = init();
        s.now = 100.0;
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        assert_eq!(s.feed_status(), None, "fresh");
        s.now = 100.0 + STALE_AFTER - 1.0;
        assert_eq!(s.feed_status(), None, "still recent");
        s.now = 112.0;
        assert_eq!(s.feed_status().as_deref(), Some("Updated 12 s ago"), "a hung reading leaves it old");
        s.land_procs(Err(ProcError::Failed("spectra process failed: no processes found".into())));
        assert_eq!(
            s.feed_status().as_deref(),
            Some("Updated 12 s ago · last reading failed: spectra process failed: no processes found")
        );
        s.now = 400.0;
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        assert_eq!(s.feed_status(), None, "a good reading clears it");
        assert_eq!(fmt_ago(185.0), "3 min");
    }

    #[test]
    fn counts_are_worked_out_once_per_reading() {
        let mut s = init();
        let mut a = proc(1, "Slack", 3.0, 100.0);
        a.role = Role::App;
        a.runtime = Runtime::Electron;
        s.land_procs(Ok(vec![a, proc(2, "gh", 1.0, 50.0)]));
        assert_eq!(s.stats.kinds, [1, 0, 0, 1, 0]);
        assert_eq!(s.stats.runtimes[0], 1, "Electron");
        assert_eq!(s.stats.runtimes[Runtime::ALL.len() - 1], 1, "Unknown");
        assert_eq!((s.stats.cpu, s.stats.apps, s.stats.signed), (4.0, 1, false));
        assert_eq!(s.stats.memory, [(Category::Apps, 100.0), (Category::DevTools, 50.0)]);
    }

    #[test]
    fn back_walks_the_pages_opened() {
        let mut s = init();
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0), proc(2, "b", 1.0, 1.0), proc(3, "c", 1.0, 1.0)]));
        s.open(3);
        s.open(2);
        s.open(1);
        s.back();
        assert_eq!(s.selected, Some(2));
        s.back();
        assert_eq!(s.selected, Some(3));
        s.back();
        assert_eq!(s.selected, None, "then the list");
        s.open(3);
        s.open(2);
        s.show(Nav::All);
        assert!(s.back_stack.is_empty(), "a sidebar choice starts over");
    }

    #[test]
    fn sorting_by_a_new_column_starts_busiest_first() {
        let mut s = init();
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 5.0), proc(2, "b", 9.0, 1.0)]));
        s.show(Nav::All);
        assert_eq!(s.rows[0].id, "p:2", "CPU, busiest first");
        s.sort_by("mem", SortDir::Ascending);
        assert_eq!(s.sort, Sort { key: SortKey::Memory, descending: true });
        assert_eq!(s.rows[0].id, "p:1");
        s.sort_by("mem", SortDir::Ascending);
        assert_eq!(s.rows[0].id, "p:2", "a second click flips it");
        s.sort_by("name", SortDir::Ascending);
        assert_eq!(s.rows[0].id, "p:1", "names start A to Z");
    }

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
