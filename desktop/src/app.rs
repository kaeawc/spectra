//! App state: both feeds' latest readings (every process, and the JVMs in detail), each one's
//! recent history, the pinned JVM baseline, and what's on screen.

use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use rune::TextEdit;
use rune_kit::shell::PaneState;
use rune_kit::{SortDir, TableState};

use crate::data::{Jvm, Sample};
use crate::findings::{Finding, Link, Severity, diagnose};
use crate::groups::{self, Category, Group, Nav, Row, Sort, SortKey};
use crate::inspect::Inspector;
use crate::proc_findings;
use crate::procs::{ProcError, ProcFeed, ProcSample, Process, Role, Runtime};
use crate::signal::{self, Attempt, Identity, Me, Signal, Signals, Target};
use crate::tree::{Tree, View};

pub const HISTORY: usize = 120;
/// Readings kept per process: three minutes at one every 2 s.
pub const PROC_HISTORY: usize = 90;
/// How long each feed rests after a reading lands before the next. JVM readings take Spectra
/// about 17 s; process readings about 0.2 s.
pub const REFRESH: Duration = Duration::from_secs(2);
/// How long the JVM feed rests between readings while no JVM's page is open. A reading runs a
/// JDK tool against every JVM: about 17 s and as many CPU-seconds with a dozen JVMs.
pub const JVM_REST: Duration = Duration::from_secs(30);
/// How long to wait before asking a Spectra that's too old again, in case it was updated.
pub const RETRY_OLD: Duration = Duration::from_secs(30);
/// How old the process numbers may get, in seconds, before the app says when they're from.
pub const STALE_AFTER: f64 = 3.0 * REFRESH.as_secs_f64();
/// How long, in seconds of readings, a process asked to quit may take before the app says it
/// hasn't and offers Force quit.
pub const STILL_AFTER: f64 = 5.0;

pub type Feed = Result<Vec<Jvm>, String>;

/// When each feed reads next. The window asks for every reading, one at a time, and asks for
/// none while it's hidden; the feed threads only answer.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Feeds {
    /// A process reading was asked for and hasn't landed.
    pub proc_out: bool,
    /// The window clock at which the next process reading is due.
    pub proc_next: f64,
    /// A reading was asked for (after a Quit) while one was out: ask again once it lands.
    pub proc_again: bool,
    pub jvm_out: bool,
    /// When the last JVM reading landed; `None` before the first.
    pub jvm_landed: Option<f64>,
}

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

/// A page to go back to: a process, by pid and when it started so a reused pid isn't taken for
/// it, or an app, by its bundle's path (`pid` is then 0).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PageRef {
    pub pid: u32,
    pub started: String,
    pub app: Option<String>,
}

impl PageRef {
    fn app(path: String) -> PageRef {
        PageRef {
            pid: 0,
            started: String::new(),
            app: Some(path),
        }
    }
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
    /// Confirmed; the worker is checking the pid is still the process confirmed, then sending.
    Checking,
    /// Sent at the window clock's `at`; no reading since has shown it gone.
    Asked { at: f64 },
    /// `kill` ran at `at` but how it went is unknown (`why`), so it's watched as if sent.
    MaybeSent { at: f64, why: String },
    /// A later reading no longer lists it.
    Ended,
    /// Still listed `STILL_AFTER` seconds after it was sent.
    StillRunning,
    /// Still listed, but `ps` says it has exited (state `Z`) and waits for `parent` to collect
    /// it. No signal can help.
    Waiting { parent: String },
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
            (Status::Checking, _) => {
                format!("Checking that pid {} is still {name}…", self.target.pid)
            }
            (Status::MaybeSent { why, .. }, signal) => {
                format!(
                    "{} may have been sent to {name} ({why}); watching whether it ends",
                    signal.verb()
                )
            }
            (Status::Waiting { parent }, _) => {
                format!(
                    "{name} has exited and is waiting for {parent} to collect it; Force quit can't help"
                )
            }
            (Status::Asked { .. }, Signal::Term) => format!("Asked {name} to quit"),
            (Status::Asked { .. }, Signal::Kill) => format!("Stopping {name}"),
            (Status::Ended, Signal::Term) => format!("{name} quit"),
            (Status::Ended, Signal::Kill) => format!("{name} was stopped"),
            (Status::StillRunning, Signal::Term) => {
                format!("{name} is still running {STILL_AFTER:.0} s after being asked to quit")
            }
            (Status::StillRunning, Signal::Kill) => {
                format!(
                    "{name} is still running after Force quit; it may be stuck waiting on the system"
                )
            }
            (Status::Refused(why) | Status::Failed(why), _) => why.clone(),
        }
    }
}

/// What the worker hands back.
pub enum Done {
    /// A confirmed Quit's check and send, for outcome number `job`.
    Quit { job: u64, attempt: Attempt },
    /// What `ps` says of a process still listed after its signal, for outcome number `job`.
    Probe {
        job: u64,
        now: Result<Option<Identity>, String>,
    },
}

/// Work that runs `ps` or `kill`, off the UI thread.
pub type Job = Box<dyn FnOnce() -> Done + Send>;

/// What a job lands as when it never answers (it panicked, or its thread never started):
/// outcome number `job` and the reason.
type Failure = fn(u64, String) -> Done;

fn quit_failed(job: u64, why: String) -> Done {
    Done::Quit {
        job,
        attempt: Attempt::Failed(format!(
            "{why} Check whether the process is still running before trying again."
        )),
    }
}

fn probe_failed(job: u64, why: String) -> Done {
    Done::Probe { job, now: Err(why) }
}

/// Runs `job`, turning a panic into the job's failure so the outcome never waits on an answer
/// that isn't coming.
fn run_guarded(job: Job, id: u64, failed: Failure) -> Done {
    std::panic::catch_unwind(std::panic::AssertUnwindSafe(job))
        .unwrap_or_else(|_| failed(id, "Spectra hit an internal error while checking.".into()))
}

#[cfg_attr(not(test), allow(dead_code))]
/// A job a test's `Runner::Held` holds, with what it lands as if it panics.
pub struct Held {
    job: Job,
    id: u64,
    failed: Failure,
}

/// Where jobs run: a thread each, or (in tests) held until the test runs them, so a test can
/// see the app while one is in flight.
pub enum Runner {
    Thread,
    #[cfg_attr(not(test), allow(dead_code))]
    Held(Arc<Mutex<Vec<Held>>>),
    /// A machine that can't start another thread.
    #[cfg(test)]
    NoThreads,
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
        s.memory = Category::ALL
            .iter()
            .copied()
            .zip(memory)
            .filter(|(_, kib)| *kib > 0.0)
            .collect();
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
    /// The app (by bundle path) whose page is open, when no process page is.
    pub app_page: Option<String>,
    /// The pages "back" returns to, most recent last.
    pub back_stack: Vec<PageRef>,
    /// The open page's tools, live readings and bundle.
    pub inspect: Inspector,
    pub expanded: Option<String>,
    pub args_open: bool,
    /// Whether a process page shows its whole command line.
    pub cmd_all: bool,
    pub loaded: bool,
    pub error: Option<String>,
    pub feed: Option<Receiver<Feed>>,
    pub feed_tx: Option<Sender<Feed>>,
    /// Asks the JVM feed for a reading.
    pub jvm_go: Option<Sender<()>>,
    pub jvm_go_rx: Option<Receiver<()>>,
    pub feeds: Feeds,
    /// Whether the window is on screen; the feeds rest while it isn't.
    pub visible: bool,

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
    /// Grouped or as a tree; kept while the app runs.
    pub view: View,
    /// The tree as it stands, `nav` applied; a tree row's `group` indexes its nodes.
    pub tree: Tree,
    /// Pids whose children the tree shows, kept across readings while they run.
    pub tree_open: HashSet<u32>,
    /// Pids closed during the current search, which otherwise opens down to every match.
    pub tree_closed: HashSet<u32>,
    /// The search `tree_closed` belongs to.
    tree_query: String,
    /// Which tree nodes the search keeps; `None` without one.
    tree_keep: Option<Vec<bool>>,
    /// Processes the search matched in the tree, or all of them without one.
    pub tree_matches: usize,

    /// Asks `ps` about a pid and sends signals; tests swap in a recorder.
    pub signals: Arc<dyn Signals>,
    pub runner: Runner,
    pub done_tx: Sender<Done>,
    pub done_rx: Receiver<Done>,
    /// Wakes the window when a job is done; `None` headless.
    pub wake: Option<rune::Waker>,
    /// Numbers outcomes, so a job's answer lands only on the outcome it was for.
    pub job: u64,
    pub me: Me,
    pub confirm: Option<Confirm>,
    /// The last Quit or Force quit, until another replaces it or it's dismissed.
    pub outcome: Option<Outcome>,
    /// Asks the process feed for a reading: `true` for one taken now, after a signal, rather
    /// than the daemon's shared snapshot.
    pub poke: Option<Sender<bool>>,
    pub poke_rx: Option<Receiver<bool>>,
    /// The machine-wide readings the System page shows.
    pub system: crate::system::System,
    /// Processes starting and exiting, for the Activity page.
    pub activity: crate::spawns::Activity,
    /// Which processes move bytes, for the Network page.
    pub network: crate::network::NetworkPage,
    /// What starts at login or boot, for the Startup page.
    pub startup: crate::startup::Startup,
    /// "Right now" on the Overview: Spectra's look at why the machine may be slow.
    pub whatswrong: crate::whatswrong::WhatsWrong,
    /// Where disk space went, for the Storage page.
    pub storage: crate::storage::Storage,
    /// Stored snapshots and what differs from one, for the Compare page.
    pub compare: crate::compare::Compare,
    /// The installed developer toolchains, for the Toolchains page.
    pub toolchains: crate::toolchains::ToolchainsPage,
    /// Memory jumps and the daemon's alerts, for the Memory and alerts page.
    pub growth: crate::growth::Growth,
    /// What changed before it got slow, for the Timeline page.
    pub timeline: crate::timeline::TimelinePage,
    /// What has been crashing, for the Crashes page.
    pub crashes: crate::crashes::CrashesPage,
    /// The code view a "Show code" press opens.
    pub codeview: crate::codeview::CodeView,
    /// Other Macs through Spectra Remote, for the Hosts page and each host's page.
    pub hosts: crate::remote::Hosts,
}

pub fn init() -> App {
    let (tx, rx) = channel();
    let (ptx, prx) = channel();
    let (poke, poke_rx) = channel();
    let (jvm_go, jvm_go_rx) = channel();
    let (done_tx, done_rx) = channel();
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
        app_page: None,
        back_stack: Vec::new(),
        inspect: Inspector::new(),
        expanded: None,
        args_open: false,
        cmd_all: false,
        loaded: false,
        error: None,
        feed: Some(rx),
        feed_tx: Some(tx),
        jvm_go: Some(jvm_go),
        jvm_go_rx: Some(jvm_go_rx),
        feeds: Feeds::default(),
        visible: true,
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
        view: View::Grouped,
        tree: Tree::default(),
        tree_open: HashSet::new(),
        tree_closed: HashSet::new(),
        tree_query: String::new(),
        tree_keep: None,
        tree_matches: 0,
        signals: Arc::new(signal::System),
        runner: Runner::Thread,
        done_tx,
        done_rx,
        wake: None,
        job: 0,
        me: Me {
            pid: std::process::id(),
            uid: None,
        },
        confirm: None,
        outcome: None,
        poke: Some(poke),
        poke_rx: Some(poke_rx),
        system: crate::system::System::new(),
        activity: crate::spawns::Activity::new(),
        network: crate::network::NetworkPage::new(),
        startup: crate::startup::Startup::new(),
        whatswrong: crate::whatswrong::WhatsWrong::new(),
        storage: crate::storage::Storage::new(),
        compare: crate::compare::Compare::new(),
        toolchains: crate::toolchains::ToolchainsPage::new(),
        growth: crate::growth::Growth::new(),
        timeline: crate::timeline::TimelinePage::new(),
        crashes: crate::crashes::CrashesPage::new(),
        codeview: crate::codeview::CodeView::new(),
        hosts: crate::remote::Hosts::new(),
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
        self.feeds.jvm_out = false;
        self.feeds.jvm_landed = Some(self.now);
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
        self.feeds.proc_out = false;
        let rest = if matches!(feed, Err(ProcError::TooOld(_))) {
            RETRY_OLD
        } else {
            REFRESH
        };
        self.feeds.proc_next = self.now + rest.as_secs_f64();
        if std::mem::take(&mut self.feeds.proc_again) {
            self.ask_procs(true);
        }
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
            let mut t = old
                .filter(|t| t.started == p.started_at)
                .unwrap_or_else(|| Track {
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
        let index = &self.proc_index;
        self.tree_open.retain(|pid| index.contains_key(pid));
        self.tree_closed.retain(|pid| index.contains_key(pid));
    }

    /// Whatever was keyed by a pid that now belongs to another process: its JVM numbers and
    /// baseline, an open finding, the table's cursor.
    fn forget_reused(&mut self, reused: &[u32]) {
        for pid in reused {
            self.history.remove(pid);
            self.readings.remove(pid);
            self.baseline.remove(pid);
            let prefix = format!("{pid}/");
            if self
                .expanded
                .as_deref()
                .is_some_and(|id| id.starts_with(&prefix))
            {
                self.expanded = None;
            }
            self.tree_open.remove(pid);
            self.tree_closed.remove(pid);
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
        let ids: HashSet<String> = groups::group(&all, self.sort)
            .into_iter()
            .map(|g| g.id)
            .collect();
        self.open_groups.retain(|id| ids.contains(id));
    }

    pub(crate) fn refresh_findings(&mut self) {
        let mut all: Vec<Finding> = self
            .jvms
            .iter()
            .flat_map(|j| diagnose(j, &self.history(j.pid)))
            .chain(self.proc_findings.iter().cloned())
            .chain(crate::system_findings::diagnose(&self.system, self.now))
            .chain(self.activity.findings((self.wall)() as f64))
            .collect();
        all.sort_by_key(|f| (f.severity, f.pid));
        self.findings = all;
    }

    /// Whether `page` still names a running process (or a JVM the process feed doesn't list).
    fn is_live(&self, page: &PageRef) -> bool {
        // An app page shows its bundle whether or not it runs.
        if page.app.is_some() {
            return true;
        }
        match self.process(page.pid) {
            Some(p) => page.started.is_empty() || p.started_at == page.started,
            None => self.jvm(page.pid).is_some(),
        }
    }

    /// Closes the open page once its process is in neither feed, or its pid is someone else's.
    /// A page whose process the app was asked to stop stays, to say it ended.
    fn drop_gone_selection(&mut self) {
        if let Some(pid) = self.selected
            && !self.is_live(&PageRef {
                pid,
                started: self.selected_started.clone(),
                app: None,
            })
            && self.ended_page().is_none()
        {
            self.selected = None;
            self.selected_started.clear();
            self.back_stack.clear();
        }
        let stack = std::mem::take(&mut self.back_stack);
        self.back_stack = stack.into_iter().filter(|p| self.is_live(p)).collect();
        self.sync_inspector();
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
            Some(ProcError::Failed(why)) => Some(format!(
                "{when} · last reading failed: {}",
                why.trim_end_matches('.')
            )),
            _ if ago > STALE_AFTER => Some(when),
            _ => None,
        }
    }

    pub fn process(&self, pid: u32) -> Option<&Process> {
        self.proc_index.get(&pid).and_then(|&i| self.procs.get(i))
    }

    pub fn children(&self, pid: u32) -> Vec<&Process> {
        self.procs
            .iter()
            .filter(|p| p.ppid == pid && p.pid != pid)
            .collect()
    }

    pub fn proc_history(&self, pid: u32) -> &[ProcSample] {
        self.tracks.get(&pid).map_or(&[], |t| t.samples.as_slice())
    }

    /// Re-cuts the list after a reading, a sidebar choice, a search, a sort or a group opening.
    pub fn rebuild(&mut self) {
        let q = self.query.text.trim().to_lowercase();
        let nav = if matches!(
            self.nav,
            Nav::Overview | Nav::System | Nav::Activity | Nav::Startup
        ) {
            Nav::All
        } else {
            self.nav
        };
        let nav = if nav == Nav::Network { Nav::All } else { nav };
        let nav = if nav == Nav::Storage { Nav::All } else { nav };
        let nav = if nav == Nav::Compare { Nav::All } else { nav };
        let nav = if nav == Nav::Growth { Nav::All } else { nav };
        let nav = if nav == Nav::Timeline { Nav::All } else { nav };
        let nav = if nav == Nav::Crashes { Nav::All } else { nav };
        if self.view == View::Tree {
            self.rebuild_tree(nav, q);
            return;
        }
        let kept: Vec<&Process> = self
            .procs
            .iter()
            .filter(|p| nav.holds(p) && groups::matches_query(p, &q))
            .collect();
        self.groups = groups::group(&kept, self.sort);
        self.rows = groups::flatten(&self.groups, &self.open_groups);
    }

    /// The tree over `nav`'s processes; a search keeps its matches and the processes above them.
    fn rebuild_tree(&mut self, nav: Nav, q: String) {
        self.groups.clear();
        let kept: Vec<&Process> = self.procs.iter().filter(|p| nav.holds(p)).collect();
        self.tree = Tree::build(&kept, self.sort);
        if q != self.tree_query {
            self.tree_closed.clear();
            self.tree_query = q;
        }
        let q = &self.tree_query;
        if q.is_empty() {
            self.tree_matches = self.tree.nodes.len();
            self.tree_keep = None;
        } else {
            let (keep, hits) = self.tree.keep(|pid| {
                self.process(pid)
                    .is_some_and(|p| groups::matches_query(p, q))
            });
            self.tree_matches = hits;
            self.tree_keep = Some(keep);
        }
        self.rows = self.tree.flatten(
            &self.tree_open,
            self.tree_keep.as_deref(),
            &self.tree_closed,
        );
    }

    /// Lays the list out grouped or as a tree.
    pub fn set_view(&mut self, view: View) {
        if self.view != view {
            self.view = view;
            self.table.clear_selection();
            self.rebuild();
        }
    }

    /// Whether the tree shows `pid`'s children.
    pub fn tree_is_open(&self, pid: u32) -> bool {
        self.tree.index_of(pid).is_some_and(|i| {
            self.tree.is_open(
                i,
                &self.tree_open,
                self.tree_keep.as_deref(),
                &self.tree_closed,
            )
        })
    }

    /// Whether `pid` has children the tree would show: with a search, only kept ones.
    pub fn tree_has_children(&self, pid: u32) -> bool {
        self.tree
            .index_of(pid)
            .is_some_and(|i| self.tree.has_shown_children(i, self.tree_keep.as_deref()))
    }

    /// Opens or closes `pid`'s branch in the tree.
    pub fn toggle_tree(&mut self, pid: u32) {
        let open = self.tree_is_open(pid);
        self.set_tree_open(pid, !open);
    }

    fn set_tree_open(&mut self, pid: u32, open: bool) {
        if self.tree_query.is_empty() {
            if open {
                self.tree_open.insert(pid);
            } else {
                self.tree_open.remove(&pid);
            }
        } else if open {
            self.tree_closed.remove(&pid);
        } else {
            self.tree_closed.insert(pid);
        }
        self.rebuild();
    }

    /// Left and Right on the tree's cursor row: Right opens a closed branch, then steps to its
    /// first child; Left closes an open one, then steps to the parent. Returns the row the
    /// cursor should move to, if it moves.
    pub fn tree_step(&mut self, right: bool) -> Option<String> {
        let at = self
            .table
            .cursor()?
            .strip_prefix("p:")?
            .parse::<u32>()
            .ok()?;
        self.tree.index_of(at)?;
        let has_children = self.tree_has_children(at);
        // A branch whose children all ended shows no triangle: Left goes straight to the parent.
        let open = has_children && self.tree_is_open(at);
        let parent = self.tree.parent_of(at);
        match (right, open) {
            (true, false) if has_children => {
                self.set_tree_open(at, true);
                None
            }
            (true, true) => {
                let i = self.rows.iter().position(|r| r.id == format!("p:{at}"))?;
                self.rows
                    .get(i + 1)
                    .filter(|r| r.depth > self.rows[i].depth)
                    .map(|r| r.id.clone())
            }
            (false, true) => {
                self.set_tree_open(at, false);
                None
            }
            (false, false) => parent
                .map(|p| format!("p:{p}"))
                .filter(|id| self.rows.iter().any(|r| &r.id == id)),
            _ => None,
        }
    }

    /// Shows `nav` in the main pane, closing any open page.
    pub fn show(&mut self, nav: Nav) {
        // Leaving the Storage page stops a measurement on its way.
        if self.nav == Nav::Storage && nav != Nav::Storage {
            self.cancel_storage();
        }
        // Leaving Compare stops a comparison on its way.
        if self.nav == Nav::Compare && nav != Nav::Compare {
            self.cancel_compare();
        }
        // Leaving Toolchains drops a read on its way.
        if self.nav == Nav::Toolchains && nav != Nav::Toolchains {
            self.cancel_toolchains();
        }
        // The Timeline page reads each time it opens; leaving it stops a reading on its way.
        self.timeline_nav(self.nav, nav);
        // The Crashes page reads when it opens with nothing read or an old reading.
        self.crashes_nav(self.nav, nav);
        self.nav = nav;
        self.select(None);
        self.table.clear_selection();
        self.rebuild();
        // The Startup page reads what starts at login each time it opens.
        if nav == Nav::Startup {
            self.read_startup();
        }
        // The Storage page reads Time Machine's snapshots each time it opens; sizing waits for a press.
        if nav == Nav::Storage {
            self.open_storage();
        }
        // The Compare page reads the stored snapshots each time it opens; the rest waits for a press.
        if nav == Nav::Compare {
            self.open_compare();
        }
        // The Toolchains page reads once, when first opened; Refresh reads again.
        if nav == Nav::Toolchains {
            self.open_toolchains();
        }
        // The Memory and alerts page looks at once each time it opens.
        if nav == Nav::Growth {
            self.open_growth();
        }
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
        self.table.set_sort(
            column,
            if descending {
                SortDir::Descending
            } else {
                SortDir::Ascending
            },
        );
        self.rebuild();
    }

    /// A list row was opened (Return or a double click): an app opens its page, another group
    /// opens or closes, a process opens its page. Moving the cursor onto a row does neither, and
    /// the triangle only opens or closes the group.
    pub fn open_row(&mut self, id: &str) {
        if let Some(group) = id.strip_prefix("g:") {
            match self.app_path_of(group) {
                Some(path) => self.open_app(&path),
                None => self.toggle_group(group),
            }
        } else if let Some(pid) = id.strip_prefix("p:").and_then(|p| p.parse().ok()) {
            self.open(pid);
        }
    }

    /// The bundle path of an app group, by its id.
    fn app_path_of(&self, group: &str) -> Option<String> {
        let g = self
            .groups
            .iter()
            .find(|g| g.id == group && g.kind == groups::GroupKind::App)?;
        g.pids.iter().find_map(|pid| {
            self.process(*pid)
                .and_then(|p| p.bundle_path())
                .map(str::to_owned)
        })
    }

    pub fn follow(&mut self, link: Link) {
        match link {
            Link::Process(pid) => self.open(pid),
            Link::Unidentified => self.show(Nav::Unidentified),
            Link::System => self.show(Nav::System),
            Link::Activity(ppid) => self.show_activity_of(ppid),
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
        self.findings
            .iter()
            .filter(|f| f.severity < Severity::Info)
            .count()
    }

    /// One process's findings, its JVM's included, worst first.
    pub fn findings_for(&self, pid: u32) -> Vec<&Finding> {
        self.findings
            .iter()
            .filter(|f| f.link == Link::Process(pid))
            .collect()
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
            started: self
                .process(pid)
                .map_or_else(String::new, |p| p.started_at.clone()),
            app: None,
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
        self.app_page = None;
        self.selected = pid;
        self.selected_started = pid.map_or_else(String::new, |pid| self.page_ref(pid).started);
        self.sync_inspector();
    }

    /// Remembers the open page, if any, for "back".
    fn push_current(&mut self) {
        if let Some(cur) = self.selected {
            self.back_stack.push(PageRef {
                pid: cur,
                started: self.selected_started.clone(),
                app: None,
            });
        } else if let Some(path) = self.app_page.take() {
            self.back_stack.push(PageRef::app(path));
        }
    }

    /// Opens `pid`'s page from wherever we are, so "back" returns here.
    pub fn open(&mut self, pid: u32) {
        if self.selected == Some(pid) {
            return;
        }
        self.push_current();
        self.set_page(Some(pid));
    }

    /// Opens the page of the app whose bundle is at `path`, so "back" returns here.
    pub fn open_app(&mut self, path: &str) {
        if self.selected.is_none() && self.app_page.as_deref() == Some(path) {
            return;
        }
        self.push_current();
        self.show_app(path.to_owned());
    }

    fn show_app(&mut self, path: String) {
        self.set_page(None);
        self.app_page = Some(path);
        self.sync_inspector();
    }

    /// Whether a page (a process's or an app's) is over the list.
    pub fn on_page(&self) -> bool {
        self.selected.is_some() || self.app_page.is_some()
    }

    /// Returns to the page before this one, or to the list once there is none.
    pub fn back(&mut self) {
        while let Some(page) = self.back_stack.pop() {
            if self.is_live(&page) {
                match page.app {
                    Some(path) => self.show_app(path),
                    None => self.set_page(Some(page.pid)),
                }
                return;
            }
        }
        self.set_page(None);
    }

    /// The process page "back" would return to.
    pub fn back_to(&self) -> Option<u32> {
        self.back_stack
            .last()
            .filter(|p| p.app.is_none())
            .map(|p| p.pid)
    }

    /// The page "back" would return to.
    pub fn back_page(&self) -> Option<&PageRef> {
        self.back_stack.last()
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
        g.pids
            .iter()
            .copied()
            .filter(|pid| self.process(*pid).is_some_and(|p| p.role == Role::App))
            .min()
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
            // An app page hides the list and its cursor.
            None if self.app_page.is_some() => None,
            // The Network page has no list cursor either.
            None if self.nav == Nav::Network => None,
            // The Storage page lists folders, not processes.
            None if self.nav == Nav::Storage => None,
            None if self.nav == Nav::Compare => None,
            // The Memory and alerts page lists alerts and jumps, not a table.
            None if self.nav == Nav::Growth => None,
            // Nor the Timeline page.
            None if self.nav == Nav::Timeline => None,
            None if self.nav == Nav::Crashes => None,
            None if !matches!(
                self.nav,
                Nav::Overview | Nav::System | Nav::Activity | Nav::Startup
            ) =>
            {
                self.cursor_target()
            }
            None => None,
        }
    }

    /// Why `pid` can't be quit from here, if it can't. A pid the reading doesn't list has ended.
    pub fn refusal(&self, pid: u32) -> Option<String> {
        let Some(p) = self.process(pid) else {
            return Some("That process has already ended.".into());
        };
        if let Some(o) = &self.outcome
            && o.target.is(p)
            && let Status::Waiting { parent } = &o.status
        {
            return Some(format!(
                "It has exited and is waiting for {parent} to collect it."
            ));
        }
        signal::refusal(p, self.me, &|pid| self.process(pid).map(|p| p.ppid))
    }

    /// Whether a Quit for `pid` was sent (or may have been) and hasn't settled yet, so asking
    /// again adds nothing.
    pub fn quit_pending(&self, pid: u32) -> bool {
        self.outcome.as_ref().is_some_and(|o| {
            o.target.pid == pid
                && o.signal == Signal::Term
                && matches!(o.status, Status::Asked { .. } | Status::MaybeSent { .. })
        })
    }

    /// Whether a confirmed Quit or Force quit is still being checked and sent. Nothing else
    /// may be asked meanwhile.
    pub fn checking(&self) -> bool {
        self.outcome
            .as_ref()
            .is_some_and(|o| o.status == Status::Checking)
    }

    /// Opens the confirmation for `signal` to `pid` as it is now. It never sends anything itself.
    pub fn ask(&mut self, pid: u32, signal: Signal) {
        if let Some(p) = self.process(pid) {
            self.ask_target(Target::of(p), signal);
        }
    }

    /// Opens the confirmation for `signal` to `t`, a process as it was on screen, only while the
    /// current reading still lists that process (the same pid and start). A pid that changed
    /// hands since the screen was drawn gets no confirmation.
    pub fn ask_target(&mut self, t: Target, signal: Signal) {
        if self.confirm.is_some() || self.checking() || self.refusal(t.pid).is_some() {
            return;
        }
        if signal == Signal::Term && self.quit_pending(t.pid) {
            return;
        }
        if let Some(p) = self.process(t.pid).filter(|p| t.is(p)) {
            self.confirm = Some(Confirm {
                target: Target::of(p),
                signal,
            });
        }
    }

    pub fn cancel(&mut self) {
        self.confirm = None;
    }

    /// The confirmation's button: hands the check and the send to the worker and says it's
    /// checking. The confirmation is used up on the first press, so a second sends nothing.
    pub fn confirm(&mut self) {
        let Some(Confirm { target, signal }) = self.confirm.take() else {
            return;
        };
        self.job += 1;
        let job = self.job;
        let signals = Arc::clone(&self.signals);
        let t = target.clone();
        self.outcome = Some(Outcome {
            target,
            signal,
            status: Status::Checking,
        });
        self.submit(
            job,
            quit_failed,
            Box::new(move || Done::Quit {
                job,
                attempt: signal::quit(signals.as_ref(), &t, signal),
            }),
        );
    }

    /// Hands `work`, outcome number `id`'s job, to the worker. If it panics or can't be started,
    /// `failed` says so, so the outcome still settles.
    fn submit(&self, id: u64, failed: Failure, work: Job) {
        let start_failed = |e: std::io::Error| {
            let _ = self.done_tx.send(failed(
                id,
                format!("Couldn't start a thread for the check: {e}."),
            ));
            if let Some(wake) = &self.wake {
                wake();
            }
        };
        match &self.runner {
            Runner::Thread => {
                let tx = self.done_tx.clone();
                let wake = self.wake.clone();
                let spawned = std::thread::Builder::new()
                    .name("spectra-quit".into())
                    .spawn(move || {
                        let _ = tx.send(run_guarded(work, id, failed));
                        if let Some(wake) = wake {
                            wake();
                        }
                    });
                if let Err(e) = spawned {
                    start_failed(e);
                }
            }
            #[cfg(test)]
            Runner::NoThreads => start_failed(std::io::Error::other("out of threads")),
            Runner::Held(q) => q.lock().expect("the held jobs").push(Held {
                job: work,
                id,
                failed,
            }),
        }
    }

    /// Runs the jobs a test's `Runner::Held` is holding, on this thread, and lands them.
    #[cfg(test)]
    pub fn run_held(&mut self) {
        let jobs = match &self.runner {
            Runner::Held(q) => std::mem::take(&mut *q.lock().unwrap()),
            _ => Vec::new(),
        };
        for job in jobs {
            self.land_done(run_guarded(job.job, job.id, job.failed));
        }
    }

    /// Lands what the worker finished. True when anything landed.
    pub fn land_work(&mut self) -> bool {
        let mut landed = false;
        while let Ok(done) = self.done_rx.try_recv() {
            self.land_done(done);
            landed = true;
        }
        landed
    }

    fn land_done(&mut self, done: Done) {
        match done {
            Done::Quit { job, attempt } => self.land_attempt(job, attempt),
            Done::Probe { job, now } => self.land_probe(job, now),
        }
    }

    fn land_attempt(&mut self, job: u64, attempt: Attempt) {
        let now = self.now;
        if job != self.job {
            return;
        }
        let Some(o) = self
            .outcome
            .as_mut()
            .filter(|o| o.status == Status::Checking)
        else {
            return;
        };
        o.status = match attempt {
            Attempt::Sent => Status::Asked { at: now },
            Attempt::Unsure(why) => Status::MaybeSent { at: now, why },
            Attempt::Refused(why) => Status::Refused(why),
            Attempt::Failed(why) => Status::Failed(why),
        };
        let sent = matches!(o.status, Status::Asked { .. } | Status::MaybeSent { .. });
        if sent {
            self.poke_feed();
        }
    }

    fn land_probe(&mut self, job: u64, now: Result<Option<Identity>, String>) {
        if job != self.job {
            return;
        }
        let Some(o) = self
            .outcome
            .as_ref()
            .filter(|o| o.status == Status::StillRunning)
        else {
            return;
        };
        match now {
            Ok(Some(id)) if signal::waits_for_parent(&o.target, Some(&id)) => {
                let ppid = self
                    .process(o.target.pid)
                    .filter(|p| o.target.is(p))
                    .map_or(o.target.process.ppid, |p| p.ppid);
                let parent = match self.process(ppid) {
                    Some(p) => format!("{} (pid {ppid})", signal::display_name(p)),
                    None => format!("its parent (pid {ppid})"),
                };
                if let Some(o) = &mut self.outcome {
                    o.status = Status::Waiting { parent };
                }
            }
            // Gone already: the next reading will say so.
            Ok(None) => self.poke_feed(),
            Ok(Some(_)) | Err(_) => {}
        }
    }

    /// A process reading now, after a signal: at once, or as soon as the one out lands.
    fn poke_feed(&mut self) {
        if self.feeds.proc_out {
            self.feeds.proc_again = true;
        } else {
            self.ask_procs(true);
        }
    }

    /// Asks for a process reading: `fresh` for one taken now, not the shared snapshot.
    fn ask_procs(&mut self, fresh: bool) {
        if let Some(poke) = &self.poke
            && poke.send(fresh).is_ok()
        {
            self.feeds.proc_out = true;
        }
    }

    /// Whether the window is on screen: not hidden, minimised, or covered whole (another Space
    /// included, where the system reports it). Every recurring feed rests while it isn't, except
    /// Activity's "Keep recording".
    pub fn window_shown(&self) -> bool {
        self.visible
    }

    /// Whether the JVM feed is worth reading: before the process list says otherwise, while the
    /// list has a JVM, or while the last JVM reading found one.
    pub fn jvm_wanted(&self) -> bool {
        let jvm = Runtime::ALL
            .iter()
            .position(|r| *r == Runtime::Jvm)
            .map_or(0, |i| self.stats.runtimes[i]);
        !self.has_processes() || jvm > 0 || !self.jvms.is_empty()
    }

    /// How long the JVM feed rests after a reading: briefly while a JVM's page is open.
    fn jvm_rest(&self) -> Duration {
        if self.selected.is_some_and(|pid| self.jvm(pid).is_some()) {
            REFRESH
        } else {
            JVM_REST
        }
    }

    /// An open page's live rounds, which rest while the window isn't shown.
    pub fn tick_live(&mut self) {
        if self.window_shown() {
            self.tick_inspector();
        } else {
            self.sync_inspector();
        }
    }

    /// Asks each feed for its next reading once it's due, and none while the window is hidden.
    /// Returns when the next one falls due, for the window to look again then.
    pub fn tick_feeds(&mut self) -> Option<f64> {
        if !self.window_shown() {
            return None;
        }
        let mut due: Option<f64> = None;
        if !self.feeds.proc_out {
            if self.now >= self.feeds.proc_next {
                self.ask_procs(false);
            } else {
                due = Some(self.feeds.proc_next);
            }
        }
        if self.jvm_wanted() && !self.feeds.jvm_out {
            let next = self
                .feeds
                .jvm_landed
                .map_or(0.0, |t| t + self.jvm_rest().as_secs_f64());
            if self.now >= next {
                if let Some(go) = &self.jvm_go
                    && go.send(()).is_ok()
                {
                    self.feeds.jvm_out = true;
                }
            } else {
                due = Some(due.map_or(next, |d| d.min(next)));
            }
        }
        due
    }

    /// Asks `ps`, off the UI thread, whether a process still listed after its signal has in fact
    /// exited and waits for its parent.
    fn probe(&mut self) {
        let Some(o) = &self.outcome else { return };
        let (job, pid) = (self.job, o.target.pid);
        let signals = Arc::clone(&self.signals);
        self.submit(
            job,
            probe_failed,
            Box::new(move || Done::Probe {
                job,
                now: signals.identify(pid),
            }),
        );
    }

    /// Dismisses the outcome, except while it's being checked: the answer has to land somewhere.
    pub fn dismiss_outcome(&mut self) {
        if self.checking() {
            return;
        }
        self.outcome = None;
        self.job += 1;
    }

    /// After a reading: a process that was sent a signal and is gone (or whose pid now has
    /// another start) has ended; one still there `STILL_AFTER` seconds on is still running, and
    /// `ps` is asked whether it's a zombie.
    fn track_outcome(&mut self) {
        let Some(o) = &mut self.outcome else { return };
        let listed = self
            .proc_index
            .get(&o.target.pid)
            .and_then(|&i| self.procs.get(i));
        let gone = !listed.is_some_and(|p| o.target.is(p));
        let mut probe = false;
        match o.status {
            Status::Asked { at } | Status::MaybeSent { at, .. } => {
                if gone {
                    o.status = Status::Ended;
                } else if self.now - at >= STILL_AFTER {
                    o.status = Status::StillRunning;
                    probe = true;
                }
            }
            Status::StillRunning | Status::Waiting { .. } if gone => o.status = Status::Ended,
            _ => {}
        }
        if probe {
            self.probe();
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

    /// How many readings each feed was asked for since the last look.
    fn asked(s: &App) -> (usize, usize) {
        fn n<T>(rx: &Option<Receiver<T>>) -> usize {
            rx.as_ref().map_or(0, |rx| rx.try_iter().count())
        }
        (n(&s.poke_rx), n(&s.jvm_go_rx))
    }

    #[test]
    fn a_reading_after_a_signal_is_taken_now_and_the_cadence_reads_the_snapshot() {
        let mut s = init();
        let fresh = |s: &App| s.poke_rx.as_ref().unwrap().try_iter().collect::<Vec<_>>();
        s.tick_feeds();
        assert_eq!(
            fresh(&s),
            vec![false],
            "every {REFRESH:?}, the daemon's snapshot"
        );
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        s.poke_feed();
        assert_eq!(fresh(&s), vec![true], "after a Quit, one taken now");
        // Asked while one is out, the follow-up is taken now too.
        s.poke_feed();
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        assert_eq!(fresh(&s), vec![true]);
    }

    fn jvm_proc(pid: u32) -> Process {
        Process {
            runtime: Runtime::Jvm,
            ..proc(pid, "java", 1.0, 1.0)
        }
    }

    /// What landing a saved feed costs: `PERF_PROCS_JSON=<file> cargo test --release -- --ignored land_cost --nocapture`.
    #[test]
    #[ignore]
    fn land_cost() {
        let Ok(path) = std::env::var("PERF_PROCS_JSON") else {
            return;
        };
        let procs = crate::procs::parse(&std::fs::read_to_string(path).unwrap()).unwrap();
        let mut s = init();
        let t = std::time::Instant::now();
        for _ in 0..20 {
            s.land_procs(Ok(procs.clone()));
        }
        println!(
            "land_procs of {} processes: {:?}",
            procs.len(),
            t.elapsed() / 20
        );
    }

    #[test]
    fn no_reading_is_asked_for_while_the_window_is_hidden() {
        let mut s = init();
        s.visible = false;
        for t in 0..100 {
            s.now = f64::from(t);
            assert_eq!(s.tick_feeds(), None);
        }
        assert_eq!(asked(&s), (0, 0), "nothing ran while hidden");
        // Back on screen, both read at once.
        s.visible = true;
        s.tick_feeds();
        assert_eq!(asked(&s), (1, 1));
    }

    #[test]
    fn each_feed_has_one_reading_out_and_rests_after_it_lands() {
        let mut s = init();
        s.tick_feeds();
        assert_eq!(asked(&s), (1, 1));
        s.now = 5.0;
        s.tick_feeds();
        assert_eq!(asked(&s), (0, 0), "nothing more while both are out");
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        s.now = 6.0;
        assert_eq!(
            s.tick_feeds(),
            Some(5.0 + REFRESH.as_secs_f64()),
            "due a rest after it landed"
        );
        assert_eq!(asked(&s), (0, 0));
        s.now = 7.0;
        s.tick_feeds();
        assert_eq!(asked(&s), (1, 0));
    }

    #[test]
    fn the_jvm_feed_rests_longer_unless_a_jvms_page_is_open() {
        let mut s = init();
        s.land_procs(Ok(vec![jvm_proc(7), proc(8, "b", 0.0, 1.0)]));
        s.tick_feeds();
        asked(&s);
        s.land(Ok(vec![data::Jvm {
            pid: 7,
            ..data::Jvm::default()
        }]));
        s.feeds.proc_out = true;
        s.now = REFRESH.as_secs_f64();
        s.tick_feeds();
        assert_eq!(
            asked(&s),
            (0, 0),
            "off its page the JVM feed rests {JVM_REST:?}"
        );
        s.now = JVM_REST.as_secs_f64();
        s.tick_feeds();
        assert_eq!(asked(&s), (0, 1));
        s.land(Ok(vec![data::Jvm {
            pid: 7,
            ..data::Jvm::default()
        }]));
        s.open(7);
        s.now += REFRESH.as_secs_f64();
        s.tick_feeds();
        assert_eq!(asked(&s), (0, 1), "on its page it reads every {REFRESH:?}");
    }

    #[test]
    fn the_jvm_feed_stops_once_neither_feed_shows_a_jvm() {
        let mut s = init();
        s.tick_feeds();
        assert_eq!(asked(&s), (1, 1), "before the list lands it looks");
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        s.land(Ok(Vec::new()));
        assert!(!s.jvm_wanted());
        s.feeds.proc_out = true;
        s.now = 1000.0;
        s.tick_feeds();
        assert_eq!(asked(&s), (0, 0));
        // A JVM starts.
        s.land_procs(Ok(vec![jvm_proc(9)]));
        s.feeds.proc_out = true;
        s.tick_feeds();
        assert_eq!(asked(&s), (0, 1));
    }

    #[test]
    fn a_reading_asked_for_while_one_is_out_follows_it() {
        let mut s = init();
        s.tick_feeds();
        asked(&s);
        s.poke_feed();
        assert_eq!(asked(&s), (0, 0), "one at a time");
        s.land_procs(Ok(vec![proc(1, "a", 1.0, 1.0)]));
        assert_eq!(asked(&s), (1, 0), "asked again as soon as it landed");
        assert!(s.feeds.proc_out);
    }

    #[test]
    fn process_history_is_bounded_and_forgets_ended_processes() {
        let mut s = init();
        for i in 0..(PROC_HISTORY + 5) {
            s.land_procs(Ok(vec![
                proc(1, "a", i as f64, 1.0),
                proc(2, "b", 0.0, 1.0),
            ]));
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
        let jvm = Jvm {
            pid: 1,
            main_class: "a.B".into(),
            gc: Some(data::Gc::default()),
            ..Jvm::default()
        };
        s.land(Ok(vec![jvm.clone()]));
        s.land_procs(Ok(vec![
            started(proc(1, "java", 1.0, 1.0), "09:00"),
            started(proc(2, "zsh", 1.0, 1.0), "09:00"),
        ]));
        s.toggle_baseline();
        s.open(2);
        s.open(1);
        assert_eq!(s.back_to(), Some(2));
        s.expanded = Some("1/jvm-healthy".into());
        // Pid 1 is now another program.
        s.land_procs(Ok(vec![
            started(proc(1, "node", 1.0, 1.0), "10:00"),
            started(proc(2, "zsh", 1.0, 1.0), "09:00"),
        ]));
        assert_eq!(s.selected, None, "the page was for the old process");
        assert!(
            s.baseline.is_empty() && s.history(1).is_empty(),
            "its JVM numbers went with it"
        );
        assert_eq!(s.expanded, None);
        // And a reused pid in the back stack is skipped.
        s.open(2);
        s.open(1);
        s.land_procs(Ok(vec![
            started(proc(1, "node", 1.0, 1.0), "10:00"),
            started(proc(2, "zsh", 1.0, 1.0), "11:00"),
        ]));
        assert_eq!(s.selected, Some(1));
        s.back();
        assert_eq!(s.selected, None, "pid 2's page was for the zsh that ended");
    }

    #[test]
    fn open_groups_that_no_longer_exist_are_forgotten() {
        let mut s = init();
        s.land_procs(Ok(vec![
            proc(1, "zsh", 1.0, 1.0),
            proc(2, "zsh", 1.0, 1.0),
            proc(3, "a", 1.0, 1.0),
        ]));
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
        assert_eq!(
            s.feed_status().as_deref(),
            Some("Updated 12 s ago"),
            "a hung reading leaves it old"
        );
        s.land_procs(Err(ProcError::Failed(
            "spectra process failed: no processes found".into(),
        )));
        assert_eq!(
            s.feed_status().as_deref(),
            Some(
                "Updated 12 s ago · last reading failed: spectra process failed: no processes found"
            )
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
        assert_eq!(
            s.stats.memory,
            [(Category::Apps, 100.0), (Category::DevTools, 50.0)]
        );
    }

    #[test]
    fn back_walks_the_pages_opened() {
        let mut s = init();
        s.land_procs(Ok(vec![
            proc(1, "a", 1.0, 1.0),
            proc(2, "b", 1.0, 1.0),
            proc(3, "c", 1.0, 1.0),
        ]));
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
        assert_eq!(
            s.sort,
            Sort {
                key: SortKey::Memory,
                descending: true
            }
        );
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
