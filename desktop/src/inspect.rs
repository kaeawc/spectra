//! What an open page runs: its tools, on demand and one at a time; its live network and energy
//! readings, one round at a time while it's open; and an app page's bundle, read once per
//! opening. Everything runs Spectra off the UI thread with a deadline, and lands only on the
//! page it was for.

use std::collections::{HashMap, HashSet};
use std::process::Command;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use rune::TextEdit;

use crate::app::App;
use crate::bundle::{self, Bundle};
use crate::jvm_tools::{self, HistogramReport, Recording, ThreadState};
use crate::live::{self, NetPoint, Round, Series};
use crate::procs::{self, Process, ProcSample, Role};
use crate::run::{self, Ended};
use crate::tools::{Kind, Paths, Report, Tool};

/// How long stopping a flight recording may take.
const STOP_DEADLINE: Duration = Duration::from_secs(15);

/// The most a tool may print: a sample of a busy process with every stack is a few MB.
const MAX_OUTPUT: usize = 32 << 20;

/// Why a run gave no output.
#[derive(Clone, Debug, PartialEq)]
pub enum Failure {
    /// The person cancelled it, or left the page.
    Cancelled,
    Failed(String),
}

/// Runs Spectra. Tests swap in a fake.
pub trait Spectra: Send + Sync {
    /// Runs `spectra <args>` and returns what it printed, stopping it at `deadline` or once
    /// `cancel` is set.
    fn run(&self, args: &[String], deadline: Duration, cancel: &AtomicBool) -> Result<String, Failure>;
}

/// The real Spectra, from `SPECTRA_BIN` or `PATH`.
pub struct System;

impl Spectra for System {
    fn run(&self, args: &[String], deadline: Duration, cancel: &AtomicBool) -> Result<String, Failure> {
        let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
        let mut cmd = Command::new(&bin);
        cmd.args(args);
        let out = run::run_until(cmd, deadline, MAX_OUTPUT, cancel).map_err(|e| match e {
            Ended::Cancelled => Failure::Cancelled,
            Ended::Failed(e) => Failure::Failed(procs::run_failure(&bin, e)),
        })?;
        if !out.status.success() {
            return Err(Failure::Failed(failure_line(&bin, args, &out.stderr, out.status.code())));
        }
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    }
}

/// What a failed run says: Spectra's own first line, else its exit status.
pub fn failure_line(bin: &str, args: &[String], stderr: &[u8], code: Option<i32>) -> String {
    let stderr = String::from_utf8_lossy(stderr);
    match stderr.lines().map(str::trim).find(|l| !l.is_empty()) {
        Some(line) => line.to_owned(),
        None => {
            let what = args.first().map_or("", String::as_str);
            match code {
                Some(c) => format!("{bin} {what} stopped with exit status {c}"),
                None => format!("{bin} {what} was stopped"),
            }
        }
    }
}

/// Stands in for Spectra where the pids are made up (the sample scenes): runs nothing.
pub struct Inert;

impl Spectra for Inert {
    fn run(&self, _: &[String], _: Duration, _: &AtomicBool) -> Result<String, Failure> {
        Err(Failure::Failed("This is sample data; there is no process to look at.".into()))
    }
}

/// What a page is about.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Subject {
    /// A process, by pid and start, so a reused pid is someone else.
    Process { pid: u32, started: String },
    /// An app, by its bundle's path.
    App(String),
}

#[derive(Clone, Debug, PartialEq)]
pub enum RunState {
    /// Started at the window clock's `since`.
    Running { since: f64 },
    /// Cancel was pressed; waiting for Spectra to be stopped.
    Cancelling,
    Done(Box<Report>),
    Failed(String),
    Cancelled,
}

pub struct ToolRun {
    pub state: RunState,
    job: u64,
    cancel: Arc<AtomicBool>,
}

impl ToolRun {
    pub fn busy(&self) -> bool {
        matches!(self.state, RunState::Running { .. } | RunState::Cancelling)
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum BundleState {
    Loading,
    Loaded(Box<Bundle>),
    Failed(String),
}

/// What a JVM's page keeps between runs: the last histogram, the recording, and how the thread
/// dump is filtered. A new page starts over.
#[derive(Default)]
pub struct JvmPage {
    /// The histogram the latest one is compared with.
    pub prev_histogram: Option<HistogramReport>,
    /// A flight recording running on this page's JVM. Leaving the page stops it.
    pub recording: Option<Recording>,
    pub threads_query: TextEdit,
    pub threads_state: Option<ThreadState>,
    /// The thread groups shown open, by their first thread's name.
    pub threads_open: HashSet<String>,
}

/// What the worker hands back, numbered by the job it was.
#[derive(Clone, Debug)]
pub enum Landed {
    /// `pid` is the process it ran on: a recording that started late is stopped there.
    Tool { job: u64, tool: Tool, pid: u32, result: Result<Report, Failure> },
    Live { job: u64, round: Round },
    Bundle { job: u64, result: Result<Box<Bundle>, String> },
}

pub type Work = Box<dyn FnOnce() -> Landed + Send>;

/// Where the inspector's work runs: a thread each, or (in tests) held until the test runs it.
pub enum Exec {
    Thread,
    #[cfg_attr(not(test), allow(dead_code))]
    Held(Arc<Mutex<Vec<Work>>>),
}

/// A job in flight: its number and its cancel flag.
struct Flight {
    job: u64,
    cancel: Arc<AtomicBool>,
}

pub struct Inspector {
    pub subject: Option<Subject>,
    pub runs: HashMap<Tool, ToolRun>,
    pub net: Series<NetPoint>,
    pub energy: Series<f64>,
    live: Option<Flight>,
    /// The window clock when the next live round may start.
    next_live: f64,
    pub bundle: Option<BundleState>,
    bundle_job: Option<Flight>,
    pub jvm: JvmPage,
    /// Recording starts not yet landed, job to pid, whichever page they were for: each may have
    /// started a recording that quitting must stop.
    starts: HashMap<u64, u32>,
    /// Recording stops not yet landed, job to pid: the name is fixed, so a start waits for them.
    stops: HashMap<u64, u32>,
    job: u64,
    pub spectra: Arc<dyn Spectra>,
    pub exec: Exec,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
    /// Whether live readings and the bundle start on their own. Sample scenes turn it off.
    pub auto: bool,
}

impl Inspector {
    pub fn new() -> Inspector {
        let (tx, rx) = channel();
        Inspector {
            subject: None,
            runs: HashMap::new(),
            net: Series::default(),
            energy: Series::default(),
            live: None,
            next_live: 0.0,
            bundle: None,
            bundle_job: None,
            jvm: JvmPage::default(),
            starts: HashMap::new(),
            stops: HashMap::new(),
            job: 0,
            spectra: Arc::new(System),
            exec: Exec::Thread,
            tx,
            rx,
            auto: true,
        }
    }

    /// The tool running (or being cancelled), if any: one runs at a time.
    pub fn busy(&self) -> Option<Tool> {
        Tool::ALL.into_iter().find(|t| self.runs.get(t).is_some_and(ToolRun::busy))
    }

    pub fn state(&self, tool: Tool) -> Option<&RunState> {
        self.runs.get(&tool).map(|r| &r.state)
    }

    /// Shows `tool` as `state` without running anything: for the sample scenes.
    pub fn put(&mut self, tool: Tool, state: RunState) {
        self.runs.insert(tool, ToolRun { state, job: 0, cancel: Arc::new(AtomicBool::new(false)) });
    }

    /// Asks every job in flight (tools, live round, bundle) to stop.
    pub fn cancel_all(&mut self) {
        for r in self.runs.values() {
            r.cancel.store(true, Ordering::Relaxed);
        }
        for f in [self.live.take(), self.bundle_job.take()].into_iter().flatten() {
            f.cancel.store(true, Ordering::Relaxed);
        }
    }

    /// Forgets the last page's work, stopping whatever of it still runs.
    fn reset(&mut self, subject: Option<Subject>) {
        self.cancel_all();
        self.runs.clear();
        self.net = Series::default();
        self.energy = Series::default();
        self.next_live = 0.0;
        self.bundle = None;
        self.jvm = JvmPage::default();
        self.subject = subject;
    }

    fn next_job(&mut self) -> (u64, Arc<AtomicBool>) {
        self.job += 1;
        (self.job, Arc::new(AtomicBool::new(false)))
    }
}

impl Drop for Inspector {
    /// The app state going away stops its jobs, if nothing asked sooner.
    fn drop(&mut self) {
        self.cancel_all();
    }
}

impl Default for Inspector {
    fn default() -> Inspector {
        Inspector::new()
    }
}

/// Runs `tool`'s Spectra commands on `pid` in order and reads the answer. Native memory reads
/// NMT and the VM's own view, and says plainly when NMT is off. Stopping a recording always
/// stops it, even when writing it out failed or was cancelled; a start that was cancelled or
/// failed (a deadline, or an attach error after `JFR.start` ran) is stopped again at once.
fn perform(tool: Tool, pid: u32, paths: &Paths, spectra: &dyn Spectra, flag: &AtomicBool) -> Result<Report, Failure> {
    let steps = tool.steps(pid, paths);
    let deadline = tool.deadline();
    let never = AtomicBool::new(false);
    let why = |f: Failure| match f {
        Failure::Cancelled => "Stopped.".to_owned(),
        Failure::Failed(w) => w,
    };
    match tool {
        Tool::NativeMemory => {
            let nmt = spectra.run(&steps[0], deadline, flag);
            if matches!(nmt, Err(Failure::Cancelled)) {
                return Err(Failure::Cancelled);
            }
            let vm = spectra.run(&steps[1], deadline, flag);
            if matches!(vm, Err(Failure::Cancelled)) {
                return Err(Failure::Cancelled);
            }
            jvm_tools::native_memory(nmt.map_err(why), vm.map_err(why)).map(Report::NativeMemory).map_err(Failure::Failed)
        }
        Tool::JfrStart => {
            let started = spectra.run(&steps[0], deadline, flag);
            let cancelled = flag.load(Ordering::Relaxed) || matches!(started, Err(Failure::Cancelled));
            if cancelled || started.is_err() {
                let _ = spectra.run(&jvm_tools::stop_args(pid), STOP_DEADLINE, &never);
            }
            if cancelled {
                return Err(Failure::Cancelled);
            }
            tool.parse(&started?).map_err(Failure::Failed)
        }
        Tool::JfrStop => {
            let dumped = spectra.run(&steps[0], deadline, flag);
            let _ = spectra.run(&steps[1], STOP_DEADLINE, &never);
            dumped?;
            let out = spectra.run(&steps[2], deadline, flag)?;
            match tool.parse(&out).map_err(Failure::Failed)? {
                Report::Jfr(mut r) => {
                    r.path = paths.out.clone();
                    Ok(Report::Jfr(r))
                }
                other => Ok(other),
            }
        }
        _ => {
            let mut out = String::new();
            for step in &steps {
                out = spectra.run(step, deadline, flag)?;
            }
            tool.parse(&out).map_err(Failure::Failed)
        }
    }
}

/// Runs `work` and turns a panic into a failure, so the page never waits on an answer that
/// isn't coming.
fn guarded(work: Work, failed: Landed) -> Landed {
    std::panic::catch_unwind(std::panic::AssertUnwindSafe(work)).unwrap_or(failed)
}

/// A readings history summed across processes, aligned on the latest reading: a process that
/// started later adds nothing to the readings before it.
pub fn summed(tracks: &[&[ProcSample]]) -> Vec<ProcSample> {
    let n = tracks.iter().map(|t| t.len()).max().unwrap_or(0);
    let mut out = vec![ProcSample::default(); n];
    for t in tracks {
        for (i, x) in t.iter().enumerate() {
            let o = &mut out[n - t.len() + i];
            o.cpu += x.cpu;
            o.rss_kib += x.rss_kib;
            o.threads += x.threads;
        }
    }
    out
}

impl App {
    /// What the open page is about.
    pub fn subject(&self) -> Option<Subject> {
        if let Some(pid) = self.selected {
            return Some(Subject::Process { pid, started: self.selected_started.clone() });
        }
        self.app_page.clone().map(Subject::App)
    }

    /// Starts the inspector over when the page changed; an app page starts reading its bundle.
    pub fn sync_inspector(&mut self) {
        let subject = self.subject();
        if self.inspect.subject == subject {
            return;
        }
        // A recording is the page's: leaving it stops the recording, and keeps nothing.
        if let Some(rec) = self.inspect.jvm.recording.take() {
            self.stop_recording(rec.pid);
        }
        self.inspect.reset(subject);
        if self.inspect.auto && matches!(self.inspect.subject, Some(Subject::App(_))) {
            self.read_bundle();
        }
    }

    /// An app page's processes, busiest first.
    pub fn app_procs(&self, path: &str) -> Vec<&Process> {
        let mut v: Vec<&Process> = self.procs.iter().filter(|p| p.bundle_path() == Some(path)).collect();
        v.sort_by(|a, b| crate::groups::Sort::default().processes(a, b));
        v
    }

    /// An app's main process: its `App` role, else its lowest pid.
    pub fn app_lead(&self, path: &str) -> Option<u32> {
        let procs = self.app_procs(path);
        procs.iter().filter(|p| p.role == Role::App).map(|p| p.pid).min().or_else(|| procs.iter().map(|p| p.pid).min())
    }

    /// What an app is called: its processes' app name, else its bundle's file name.
    pub fn app_name(&self, path: &str) -> String {
        self.procs
            .iter()
            .filter(|p| p.bundle_path() == Some(path))
            .find_map(|p| p.app.as_ref().map(|a| a.name.clone()).filter(|n| !n.is_empty()))
            .unwrap_or_else(|| {
                let file = path.trim_end_matches('/').rsplit('/').next().unwrap_or(path);
                file.trim_end_matches(".app").to_owned()
            })
    }

    /// An app's readings, its processes' summed.
    pub fn app_history(&self, path: &str) -> Vec<ProcSample> {
        let tracks: Vec<&[ProcSample]> = self.app_procs(path).iter().map(|p| self.proc_history(p.pid)).collect();
        summed(&tracks)
    }

    /// Why `tool` can't run on the open page's process, if it can't.
    pub fn tool_refusal(&self, tool: Tool) -> Option<String> {
        if let Some(why) = tool.needs_root() {
            return Some(why.into());
        }
        let Some(Subject::Process { pid, .. }) = &self.inspect.subject else {
            return Some("Open a process to run this".into());
        };
        if self.ended_page().is_some() {
            return Some("That process has ended".into());
        }
        if tool.kind() == Kind::Jvm {
            if self.jvm(*pid).is_none() {
                return Some("Not a JVM".into());
            }
            let recording = self.inspect.jvm.recording.is_some();
            match tool {
                Tool::LeakSuspects | Tool::Dominators if self.heap_dump_path().is_none() => return Some("Take a heap dump first".into()),
                Tool::JfrStart if self.inspect.starts.values().chain(self.inspect.stops.values()).any(|p| p == pid) => {
                    return Some("A recording is still starting or stopping on this JVM. Try again in a moment.".into());
                }
                Tool::JfrStart if recording => return Some("Already recording".into()),
                Tool::JfrStop if !recording => return Some("Not recording".into()),
                _ => {}
            }
        }
        if (tool.reads_task() || tool.attaches())
            && let (Some(p), Some(me)) = (self.process(*pid), self.me.uid)
            && p.uid != me
        {
            let who = if p.user.is_empty() { format!("uid {}", p.uid) } else { p.user.clone() };
            return Some(if tool.attaches() {
                format!("Owned by {who}; only that user can attach to its JVM")
            } else {
                format!("Owned by {who}; reading its threads or memory needs root")
            });
        }
        None
    }

    /// The heap dump taken on this page, if one was.
    pub fn heap_dump_path(&self) -> Option<String> {
        match self.inspect.state(Tool::HeapDump)? {
            RunState::Done(r) => match &**r {
                Report::HeapDump(h) => Some(h.path.clone()),
                _ => None,
            },
            _ => None,
        }
    }

    /// The files `tool` reads or writes on `pid`: a new heap dump or recording in Downloads,
    /// named for the JVM, and the page's heap dump for its analyses.
    fn tool_paths(&self, tool: Tool, pid: u32) -> Paths {
        let name = self.jvm(pid).map(|j| j.name()).or_else(|| self.process(pid).map(|p| p.name().into_owned())).unwrap_or_default();
        let out = match tool {
            Tool::HeapDump => jvm_tools::out_path(&jvm_tools::out_dir(), &name, pid, (self.wall)(), "hprof"),
            Tool::JfrStop => jvm_tools::out_path(&jvm_tools::out_dir(), &name, pid, (self.wall)(), "jfr"),
            _ => String::new(),
        };
        Paths { out, hprof: self.heap_dump_path().unwrap_or_default() }
    }

    /// Stops the flight recording on `pid` off the UI thread; nothing waits for the answer but
    /// quitting, which waits a moment for it from the moment it's queued.
    fn stop_recording(&mut self, pid: u32) {
        let (job, _) = self.inspect.next_job();
        let spectra = Arc::clone(&self.inspect.spectra);
        let pending = run::pending();
        self.inspect.stops.insert(job, pid);
        self.submit_inspect(
            Box::new(move || {
                let _pending = pending;
                let never = AtomicBool::new(false);
                let result = spectra.run(&jvm_tools::stop_args(pid), STOP_DEADLINE, &never).map(|_| Report::JfrStarted);
                Landed::Tool { job, tool: Tool::JfrStop, pid, result }
            }),
            Landed::Tool { job, tool: Tool::JfrStop, pid, result: Err(Failure::Cancelled) },
        );
    }

    /// Stops the page's flight recording before the app quits, waiting for Spectra, and any
    /// recording a start still in flight (or done but not landed) may have begun.
    pub fn stop_recording_now(&mut self) {
        if let Some(r) = self.inspect.runs.get(&Tool::JfrStart) {
            r.cancel.store(true, Ordering::Relaxed);
        }
        let mut pids: Vec<u32> = self.inspect.jvm.recording.take().map(|r| r.pid).into_iter().collect();
        pids.extend(self.inspect.starts.drain().map(|(_, pid)| pid));
        pids.sort_unstable();
        pids.dedup();
        let never = AtomicBool::new(false);
        let spectra = &*self.inspect.spectra;
        std::thread::scope(|s| {
            for pid in pids {
                let never = &never;
                s.spawn(move || {
                    let _ = spectra.run(&jvm_tools::stop_args(pid), STOP_DEADLINE, never);
                });
            }
        });
    }

    /// Runs `tool` on the open page's process, unless another tool is running or it can't.
    pub fn run_tool(&mut self, tool: Tool) {
        if self.tool_refusal(tool).is_some() || self.inspect.busy().is_some() {
            return;
        }
        let Some(Subject::Process { pid, .. }) = self.inspect.subject.clone() else { return };
        let paths = self.tool_paths(tool, pid);
        match tool {
            // The histogram this one will be compared with.
            Tool::Histogram => {
                if let Some(RunState::Done(r)) = self.inspect.state(tool)
                    && let Report::Histogram(h) = &**r
                {
                    self.inspect.jvm.prev_histogram = Some(h.clone());
                }
            }
            // A new dump's analyses start over.
            Tool::HeapDump => {
                self.inspect.runs.remove(&Tool::LeakSuspects);
                self.inspect.runs.remove(&Tool::Dominators);
            }
            // The recording ends with this run, whatever it finds.
            Tool::JfrStop => self.inspect.jvm.recording = None,
            _ => {}
        }
        let (job, cancel) = self.inspect.next_job();
        let spectra = Arc::clone(&self.inspect.spectra);
        let flag = Arc::clone(&cancel);
        self.inspect.runs.insert(tool, ToolRun { state: RunState::Running { since: self.now }, job, cancel });
        if tool == Tool::JfrStart {
            self.inspect.starts.insert(job, pid);
        }
        // Both may run `jfr stop` after their first command ends: quitting waits for that too.
        if tool == Tool::JfrStop {
            self.inspect.stops.insert(job, pid);
        }
        let pending = matches!(tool, Tool::JfrStart | Tool::JfrStop).then(run::pending);
        self.submit_inspect(
            Box::new(move || {
                let _pending = pending;
                let result = perform(tool, pid, &paths, &*spectra, &flag);
                Landed::Tool { job, tool, pid, result }
            }),
            Landed::Tool { job, tool, pid, result: Err(Failure::Failed("Spectra Desktop hit an internal error running it.".into())) },
        );
    }

    /// Asks a running tool to stop. It reads as cancelled once Spectra has been stopped.
    pub fn cancel_tool(&mut self, tool: Tool) {
        if let Some(r) = self.inspect.runs.get_mut(&tool)
            && matches!(r.state, RunState::Running { .. })
        {
            r.cancel.store(true, Ordering::Relaxed);
            r.state = RunState::Cancelling;
        }
    }

    /// Reads the open app page's bundle (again, after a failure).
    pub fn read_bundle(&mut self) {
        let Some(Subject::App(path)) = self.inspect.subject.clone() else { return };
        if self.inspect.bundle_job.is_some() {
            return;
        }
        let (job, cancel) = self.inspect.next_job();
        let spectra = Arc::clone(&self.inspect.spectra);
        let flag = Arc::clone(&cancel);
        self.inspect.bundle = Some(BundleState::Loading);
        self.inspect.bundle_job = Some(Flight { job, cancel });
        self.submit_inspect(
            Box::new(move || {
                let result = match spectra.run(&bundle::args(&path), bundle::DEADLINE, &flag) {
                    Ok(out) => bundle::parse(&out).map(Box::new),
                    Err(Failure::Cancelled) => Err("Stopped.".into()),
                    Err(Failure::Failed(why)) => Err(why),
                };
                Landed::Bundle { job, result }
            }),
            Landed::Bundle { job, result: Err("Spectra Desktop hit an internal error reading it.".into()) },
        );
    }

    /// `pids` and everything they started, so energy covers what network does.
    fn with_descendants(&self, pids: &[u32]) -> Vec<u32> {
        let mut all: Vec<u32> = pids.to_vec();
        let mut i = 0;
        while i < all.len() {
            let parent = all[i];
            for c in self.children(parent) {
                if !all.contains(&c.pid) {
                    all.push(c.pid);
                }
            }
            i += 1;
        }
        all
    }

    /// The pids a live round reads energy for, and the pid whose network (with its
    /// descendants') it reads. `None` when there's nothing running to read.
    fn live_targets(&self) -> Option<(Vec<u32>, u32)> {
        match self.inspect.subject.as_ref()? {
            Subject::Process { pid, .. } => {
                let listed = self.process(*pid).is_some() && self.ended_page().is_none();
                (listed || self.jvm(*pid).is_some()).then(|| (self.with_descendants(&[*pid]), *pid))
            }
            Subject::App(path) => {
                let own: Vec<u32> = self.app_procs(path).iter().map(|p| p.pid).collect();
                let lead = self.app_lead(path)?;
                Some((self.with_descendants(&own), lead))
            }
        }
    }

    /// Starts the next live round when the page is open, none is in flight and it has rested.
    pub fn tick_inspector(&mut self) {
        self.sync_inspector();
        let i = &self.inspect;
        if !i.auto || i.live.is_some() || self.now < i.next_live || (i.net.off.is_some() && i.energy.off.is_some()) {
            return;
        }
        let Some((pids, net_pid)) = self.live_targets() else { return };
        let (want_net, want_energy) = (i.net.off.is_none(), i.energy.off.is_none());
        let (job, cancel) = self.inspect.next_job();
        let spectra = Arc::clone(&self.inspect.spectra);
        let flag = Arc::clone(&cancel);
        self.inspect.live = Some(Flight { job, cancel });
        self.submit_inspect(
            Box::new(move || {
                let failed = |f: Failure| match f {
                    Failure::Cancelled => "Stopped.".to_owned(),
                    Failure::Failed(why) => why,
                };
                let net = want_net.then(|| {
                    spectra.run(&live::net_args(net_pid), live::NET_DEADLINE, &flag).map_err(failed).and_then(|o| live::parse_net(&o))
                });
                let energy = (want_energy && !flag.load(Ordering::Relaxed)).then(|| {
                    spectra
                        .run(&live::impact_args(), live::IMPACT_DEADLINE, &flag)
                        .map_err(failed)
                        .and_then(|o| live::parse_impact(&o, &pids))
                });
                Landed::Live { job, round: Round { net, energy } }
            }),
            Landed::Live {
                job,
                round: Round {
                    net: Some(Err("Spectra Desktop hit an internal error reading it.".into())),
                    energy: Some(Err("Spectra Desktop hit an internal error reading it.".into())),
                },
            },
        );
    }

    /// Hands `work` to the worker; `failed` is what it lands as if it panics or can't start.
    fn submit_inspect(&self, work: Work, failed: Landed) {
        match &self.inspect.exec {
            Exec::Thread => {
                let (tx, wake) = (self.inspect.tx.clone(), self.wake.clone());
                let fallback = failed.clone();
                let spawned = std::thread::Builder::new().name("spectra-tool".into()).spawn({
                    let (tx, wake) = (tx.clone(), wake.clone());
                    move || {
                        let _ = tx.send(guarded(work, failed));
                        if let Some(wake) = wake {
                            wake();
                        }
                    }
                });
                if spawned.is_err() {
                    let _ = tx.send(fallback);
                    if let Some(wake) = wake {
                        wake();
                    }
                }
            }
            Exec::Held(q) => q.lock().expect("the held work").push(Box::new(move || guarded(work, failed))),
        }
    }

    /// Runs the work a test's `Exec::Held` is holding, on this thread, and lands it.
    #[cfg(test)]
    pub fn run_held_inspect(&mut self) {
        let work = match &self.inspect.exec {
            Exec::Held(q) => std::mem::take(&mut *q.lock().unwrap()),
            Exec::Thread => Vec::new(),
        };
        for w in work {
            self.land_inspected(w());
        }
    }

    /// Lands what the inspector's work finished. True when anything landed.
    pub fn land_inspect(&mut self) -> bool {
        let mut landed = false;
        while let Ok(l) = self.inspect.rx.try_recv() {
            self.land_inspected(l);
            landed = true;
        }
        landed
    }

    pub fn land_inspected(&mut self, l: Landed) {
        let now = self.now;
        if let Landed::Tool { job, tool: Tool::JfrStop, .. } = &l {
            self.inspect.stops.remove(job);
        }
        if let Landed::Tool { job, tool: Tool::JfrStart, pid, result } = &l {
            self.inspect.starts.remove(job);
            // A start that got through after its run was cancelled or its page was left: nothing
            // will keep or stop that recording, so stop it now.
            let wanted = self.inspect.runs.get(&Tool::JfrStart).is_some_and(|r| r.job == *job && !matches!(r.state, RunState::Cancelling));
            if result.is_ok() && !wanted {
                self.stop_recording(*pid);
            }
        }
        let i = &mut self.inspect;
        match l {
            Landed::Tool { job, tool, pid, result } => {
                let Some(r) = i.runs.get_mut(&tool).filter(|r| r.job == job) else { return };
                if tool == Tool::JfrStart && result.is_ok() && !matches!(r.state, RunState::Cancelling) {
                    i.jvm.recording = Some(Recording { pid, since: now });
                }
                r.state = match (&r.state, result) {
                    (RunState::Cancelling, _) | (_, Err(Failure::Cancelled)) => RunState::Cancelled,
                    (_, Ok(report)) => RunState::Done(Box::new(report)),
                    (_, Err(Failure::Failed(why))) => RunState::Failed(why),
                };
            }
            Landed::Live { job, round } => {
                if i.live.as_ref().is_none_or(|f| f.job != job) {
                    return;
                }
                i.live = None;
                let mut extra = 0f64;
                match round.net {
                    Some(Ok(p)) => i.net.push(p),
                    Some(Err(why)) => extra = extra.max(i.net.fail(why)),
                    None => {}
                }
                match round.energy {
                    Some(Ok(Some(v))) => i.energy.push(v),
                    Some(Err(why)) => extra = extra.max(i.energy.fail(why)),
                    Some(Ok(None)) | None => {}
                }
                i.next_live = now + live::REST + extra;
            }
            Landed::Bundle { job, result } => {
                if i.bundle_job.as_ref().is_none_or(|f| f.job != job) {
                    return;
                }
                i.bundle_job = None;
                i.bundle = Some(match result {
                    Ok(b) => BundleState::Loaded(b),
                    Err(why) => BundleState::Failed(why),
                });
            }
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use crate::app::init;
    use crate::groups::tests::{app, proc};
    use crate::jvm_tools::tests as jx;
    use crate::tools::tests as fx;

    /// A Spectra that answers from fixtures by subcommand and records what it was asked.
    #[derive(Default)]
    pub struct Fake {
        pub replies: HashMap<&'static str, Result<String, String>>,
        pub calls: Mutex<Vec<Vec<String>>>,
    }

    impl Fake {
        pub fn with(replies: &[(&'static str, Result<&str, &str>)]) -> Fake {
            Fake {
                replies: replies.iter().map(|(k, v)| (*k, v.map(str::to_owned).map_err(str::to_owned))).collect(),
                calls: Mutex::new(Vec::new()),
            }
        }

        /// Every tool's fixture, and live readings.
        pub fn all() -> Fake {
            Fake::with(&[
                ("sample", Ok(fx::SAMPLE)),
                ("hang", Ok(fx::HANG)),
                ("vmmap", Ok(fx::VMMAP)),
                ("vmregions", Ok(fx::VMREGIONS)),
                ("memtrace", Ok(fx::MEMTRACE)),
                ("lineage", Ok(fx::LINEAGE)),
                ("owner", Ok(fx::OWNER)),
                ("runtime", Ok(fx::RUNTIME)),
                ("network", Ok(crate::live::tests::NET)),
                ("impact", Ok(crate::live::tests::IMPACT)),
                ("--json", Ok(crate::bundle::tests::CHAT)),
                ("jvm thread-dump", Ok(jx::THREADS)),
                ("jvm heap-histogram", Ok(jx::HISTOGRAM)),
                ("jvm gc-stats", Ok(jx::GC)),
                ("jvm nmt", Ok(jx::NMT)),
                ("jvm vm-memory", Ok(jx::VM_MEMORY)),
                ("jvm explain", Ok(jx::EXPLAIN)),
                ("jvm heap-dump", Ok(jx::HEAP_DUMP_OUT)),
                ("jvm heap-hprof", Ok(jx::HPROF)),
                ("jvm dominators", Ok(jx::DOMINATORS)),
                ("jvm jfr start", Ok(jx::JFR_STARTED)),
                ("jvm jfr dump", Ok("/Users/user/Downloads/x.jfr\n")),
                ("jvm jfr stop", Ok("JFR recording \"spectra-desktop\" stopped on PID 7\n")),
                ("jvm jfr summary", Ok(jx::JFR)),
            ])
        }

        pub fn calls(&self) -> Vec<Vec<String>> {
            self.calls.lock().unwrap().clone()
        }
    }

    impl Spectra for Fake {
        fn run(&self, args: &[String], _: Duration, cancel: &AtomicBool) -> Result<String, Failure> {
            self.calls.lock().unwrap().push(args.to_vec());
            if cancel.load(Ordering::Relaxed) {
                return Err(Failure::Cancelled);
            }
            // `jvm jfr start`, then `jvm nmt`, then the subcommand alone.
            let key = |n: usize| args.iter().take(n).map(String::as_str).collect::<Vec<_>>().join(" ");
            let reply = [3, 2, 1].into_iter().find_map(|n| (args.len() >= n).then(|| self.replies.get(key(n).as_str())).flatten());
            match reply {
                Some(Ok(out)) => Ok(out.clone()),
                Some(Err(why)) => Err(Failure::Failed(why.clone())),
                None => Err(Failure::Failed(format!("no fake for {}", args[0]))),
            }
        }
    }

    /// An app with `fake` for Spectra and its work held until the test runs it.
    pub fn faked(fake: Fake) -> (App, Arc<Fake>) {
        let mut s = init();
        let fake = Arc::new(fake);
        s.inspect.spectra = fake.clone();
        s.inspect.exec = Exec::Held(Arc::new(Mutex::new(Vec::new())));
        let mine = |p: Process| Process { uid: 501, ..p };
        let mut other = proc(8, "WindowServer", 1.0, 1200.0);
        other.uid = 88;
        other.user = "_windowserver".into();
        let chat = app(proc(900, "Chat", 2.0, 1000.0), "Chat", "/Applications/Chat.app", Role::App);
        let helper = app(proc(901, "Chat Helper", 1.0, 500.0), "Chat", "/Applications/Chat.app", Role::AppHelper);
        s.me.uid = Some(501);
        s.land_procs(Ok(vec![mine(proc(7, "sleep", 0.0, 1200.0)), other, mine(chat), mine(helper)]));
        (s, fake)
    }

    fn held(s: &App) -> usize {
        match &s.inspect.exec {
            Exec::Held(q) => q.lock().unwrap().len(),
            Exec::Thread => 0,
        }
    }

    #[test]
    fn a_tool_runs_then_lands_its_report() {
        let (mut s, fake) = faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Sample);
        assert!(matches!(s.inspect.state(Tool::Sample), Some(RunState::Running { .. })));
        s.run_held_inspect();
        let Some(RunState::Done(r)) = s.inspect.state(Tool::Sample) else { panic!("{:?}", s.inspect.state(Tool::Sample)) };
        assert!(matches!(**r, Report::Sample(_)));
        assert_eq!(fake.calls(), [vec!["sample", "-json", "7"]]);
    }

    #[test]
    fn one_tool_runs_at_a_time() {
        let (mut s, fake) = faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Memtrace);
        s.run_tool(Tool::Lineage);
        s.run_tool(Tool::Memtrace);
        assert_eq!(held(&s), 1, "nothing else started while memtrace runs");
        assert_eq!(s.inspect.busy(), Some(Tool::Memtrace));
        assert!(s.inspect.state(Tool::Lineage).is_none());
        s.run_held_inspect();
        s.run_tool(Tool::Lineage);
        s.run_held_inspect();
        assert_eq!(fake.calls().len(), 2);
        assert!(matches!(s.inspect.state(Tool::Lineage), Some(RunState::Done(_))));
    }

    #[test]
    fn a_cancelled_tool_reads_cancelled_once_spectra_stopped_and_blocks_others_meanwhile() {
        let (mut s, _) = faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Hang);
        s.cancel_tool(Tool::Hang);
        assert_eq!(s.inspect.state(Tool::Hang), Some(&RunState::Cancelling));
        s.run_tool(Tool::Sample);
        assert_eq!(held(&s), 1, "nothing starts until the cancelled one has stopped");
        s.run_held_inspect();
        assert_eq!(s.inspect.state(Tool::Hang), Some(&RunState::Cancelled));
        s.run_tool(Tool::Hang);
        assert!(matches!(s.inspect.state(Tool::Hang), Some(RunState::Running { .. })), "and it can run again");
    }

    #[test]
    fn a_failed_tool_says_why() {
        let (mut s, _) = faked(Fake::with(&[("vmmap", Err("vmmap: cannot examine process 7")), ("lineage", Ok("not json"))]));
        s.open(7);
        s.run_tool(Tool::Vmmap);
        s.run_held_inspect();
        assert_eq!(s.inspect.state(Tool::Vmmap), Some(&RunState::Failed("vmmap: cannot examine process 7".into())));
        s.run_tool(Tool::Lineage);
        s.run_held_inspect();
        assert!(matches!(s.inspect.state(Tool::Lineage), Some(RunState::Failed(why)) if why.starts_with("Couldn't read")));
    }

    #[test]
    fn root_tools_and_other_users_task_tools_are_off_with_the_reason() {
        let (mut s, fake) = faked(Fake::all());
        s.open(8);
        assert_eq!(s.tool_refusal(Tool::Spindump).as_deref(), Some("Needs root; Spectra Desktop doesn't run sudo"));
        assert_eq!(
            s.tool_refusal(Tool::Sample).as_deref(),
            Some("Owned by _windowserver; reading its threads or memory needs root")
        );
        assert_eq!(s.tool_refusal(Tool::Lineage), None, "lineage reads only the process table");
        s.run_tool(Tool::Spindump);
        s.run_tool(Tool::Sample);
        s.run_tool(Tool::Lsmp);
        s.run_held_inspect();
        assert!(fake.calls().is_empty(), "nothing ran");
    }

    #[test]
    fn leaving_the_page_cancels_its_work_and_drops_late_answers() {
        let (mut s, _) = faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Sample);
        s.open(900);
        assert!(s.inspect.runs.is_empty(), "a new page starts with no runs");
        s.back();
        s.run_held_inspect();
        assert!(s.inspect.runs.is_empty(), "the old page's sample lands nowhere");
    }

    #[test]
    fn a_late_answer_does_not_land_on_the_same_tool_run_again_on_the_returned_page() {
        let (mut s, _) = faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Sample);
        s.open(900);
        s.open(7);
        s.run_tool(Tool::Sample);
        let work = match &s.inspect.exec {
            Exec::Held(q) => std::mem::take(&mut *q.lock().unwrap()),
            Exec::Thread => unreachable!(),
        };
        assert_eq!(work.len(), 2, "the old sample and the new one");
        let mut work = work.into_iter();
        // The old job's answer arrives while the new one is still running: only the job number
        // tells them apart.
        s.land_inspected((work.next().unwrap())());
        assert!(matches!(s.inspect.state(Tool::Sample), Some(RunState::Running { .. })), "the old answer was dropped");
        s.land_inspected((work.next().unwrap())());
        assert!(matches!(s.inspect.state(Tool::Sample), Some(RunState::Done(_))));
    }

    #[test]
    fn live_rounds_run_one_at_a_time_while_the_page_is_open() {
        let (mut s, fake) = faked(Fake::all());
        s.open(7);
        s.now = 10.0;
        s.tick_inspector();
        s.tick_inspector();
        assert_eq!(held(&s), 1, "never two rounds at once");
        s.run_held_inspect();
        assert_eq!(s.inspect.net.points.len(), 1);
        assert!(s.inspect.energy.points.is_empty(), "pid 7 isn't in the impact list");
        s.tick_inspector();
        assert_eq!(held(&s), 0, "it rests after a round");
        s.now += live::REST;
        s.tick_inspector();
        assert_eq!(held(&s), 1);
        let calls = fake.calls();
        assert_eq!(calls[0], ["network", "top", "--pid", "7", "--json", "--duration", "2s"]);
        assert_eq!(calls[1][0], "impact");
        s.show(crate::groups::Nav::All);
        s.run_held_inspect();
        assert!(s.inspect.net.points.is_empty(), "closing the page dropped the round");
        s.now += 60.0;
        s.tick_inspector();
        assert_eq!(held(&s), 0, "nothing runs without a page");
    }

    #[test]
    fn a_failed_live_series_is_retried_with_backoff_before_it_stops() {
        let (mut s, fake) = faked(Fake::with(&[("network", Ok(crate::live::tests::NET)), ("impact", Err("impact: not supported on linux"))]));
        s.open(900);
        let impacts = |f: &Fake| f.calls().iter().filter(|c| c[0] == "impact").count();
        let mut waits = Vec::new();
        for _ in 0..=live::RETRIES {
            s.tick_inspector();
            s.run_held_inspect();
            waits.push(s.inspect.next_live - s.now);
            s.now = s.inspect.next_live;
        }
        assert_eq!(impacts(&fake), 4, "the first try and three retries");
        assert_eq!(waits, [15.0, 20.0, 30.0, 50.0]);
        assert_eq!(s.inspect.energy.off.as_deref(), Some("impact: not supported on linux"));
        s.tick_inspector();
        s.run_held_inspect();
        assert_eq!(s.inspect.net.points.len(), 5, "network kept going");
        assert_eq!(impacts(&fake), 4, "not asked again");
    }

    #[test]
    fn energy_sums_the_process_and_its_descendants() {
        let (mut s, _) = faked(Fake::all());
        let mine = |p: Process| Process { uid: 501, ..p };
        let child = |pid, ppid| mine(Process { ppid, ..proc(pid, "x", 0.0, 1.0) });
        s.land_procs(Ok(vec![mine(proc(900, "Chat", 2.0, 1000.0)), child(901, 900), child(77, 901), child(5, 1)]));
        s.open(900);
        s.tick_inspector();
        s.run_held_inspect();
        assert_eq!(s.inspect.energy.points.back(), Some(&27.85), "900 + child 901 + grandchild 77, not the unrelated pid");
    }

    #[test]
    fn cancel_all_stops_every_job_in_flight() {
        let (mut s, _) = faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Memtrace);
        s.now = 5.0;
        s.tick_inspector();
        let mut flags: Vec<_> = s.inspect.runs.values().map(|r| Arc::clone(&r.cancel)).collect();
        flags.push(Arc::clone(&s.inspect.live.as_ref().expect("a live round").cancel));
        assert_eq!(flags.len(), 2);
        assert!(flags.iter().all(|f| !f.load(Ordering::Relaxed)));
        s.inspect.cancel_all();
        assert!(flags.iter().all(|f| f.load(Ordering::Relaxed)));
    }

    #[test]
    fn an_app_page_reads_its_bundle_once_and_sums_its_processes() {
        let (mut s, fake) = faked(Fake::all());
        s.open_app("/Applications/Chat.app");
        assert_eq!(s.inspect.bundle, Some(BundleState::Loading));
        s.sync_inspector();
        s.run_held_inspect();
        assert!(matches!(&s.inspect.bundle, Some(BundleState::Loaded(b)) if b.bundle_id == "com.example.chat"));
        assert_eq!(fake.calls(), [vec!["--json", "/Applications/Chat.app"]], "read once");
        assert_eq!(s.app_lead("/Applications/Chat.app"), Some(900));
        assert_eq!(s.app_name("/Applications/Chat.app"), "Chat");
        let h = s.app_history("/Applications/Chat.app");
        assert_eq!(h.last().map(|x| (x.cpu, x.rss_kib, x.threads)), Some((3.0, 1500.0, 4)));
        s.now = 5.0;
        s.tick_inspector();
        s.run_held_inspect();
        assert_eq!(s.inspect.energy.points.back(), Some(&7.75), "both of its processes' impact");
        assert_eq!(fake.calls()[1][3], "900", "its network is its main process's, helpers included");
    }

    #[test]
    fn readings_sum_aligned_on_the_latest() {
        let x = |cpu| ProcSample { cpu, rss_kib: 1.0, threads: 1 };
        let a = [x(1.0), x(2.0), x(3.0)];
        let b = [x(10.0)];
        let s = summed(&[&a, &b]);
        assert_eq!(s.iter().map(|p| p.cpu).collect::<Vec<_>>(), [1.0, 2.0, 13.0]);
        assert_eq!(s[2].threads, 2);
        assert!(summed(&[]).is_empty());
    }

    #[test]
    fn a_failed_run_says_spectras_first_line() {
        let args = vec!["spindump".to_owned()];
        assert_eq!(failure_line("spectra", &args, b"\nspindump requires root\nmore", Some(1)), "spindump requires root");
        assert_eq!(failure_line("spectra", &args, b"", Some(2)), "spectra spindump stopped with exit status 2");
    }

    /// `faked`, with pid 7 also a JVM.
    fn jvm_faked(fake: Fake) -> (App, Arc<Fake>) {
        let (mut s, fake) = faked(fake);
        s.land(Ok(vec![crate::data::Jvm {
            pid: 7,
            main_class: "com.acme.App".into(),
            ..Default::default()
        }]));
        (s, fake)
    }

    fn jvm_calls(fake: &Fake, sub: &str) -> usize {
        fake.calls()
            .iter()
            .filter(|c| c.len() > 1 && c[0] == "jvm" && c[1..].join(" ").starts_with(sub))
            .count()
    }

    #[test]
    fn jvm_tools_are_refused_on_a_page_that_isnt_a_jvm() {
        let (mut s, fake) = faked(Fake::all());
        s.open(7);
        for tool in Tool::ALL.into_iter().filter(|t| t.kind() == Kind::Jvm) {
            assert_eq!(
                s.tool_refusal(tool).as_deref(),
                Some("Not a JVM"),
                "{tool:?}"
            );
            s.run_tool(tool);
        }
        s.run_held_inspect();
        assert!(fake.calls().is_empty(), "nothing ran");
        assert_eq!(
            s.tool_refusal(Tool::Lineage),
            None,
            "its other tools still run"
        );
    }

    #[test]
    fn a_jvm_tool_runs_spectras_jvm_command_and_lands() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Threads);
        s.run_held_inspect();
        assert!(
            matches!(s.inspect.state(Tool::Threads), Some(RunState::Done(r)) if matches!(**r, Report::Threads(_)))
        );
        assert_eq!(fake.calls(), [vec!["jvm", "thread-dump", "--json", "7"]]);
        assert_eq!(
            s.tool_refusal(Tool::LeakSuspects).as_deref(),
            Some("Take a heap dump first")
        );
        assert_eq!(
            s.tool_refusal(Tool::JfrStop).as_deref(),
            Some("Not recording")
        );
    }

    #[test]
    fn a_second_histogram_keeps_the_first_to_compare() {
        let (mut s, _) = jvm_faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::Histogram);
        s.run_held_inspect();
        assert!(
            s.inspect.jvm.prev_histogram.is_none(),
            "nothing to compare with yet"
        );
        s.run_tool(Tool::Histogram);
        assert!(
            s.inspect.jvm.prev_histogram.is_some(),
            "the first is kept as the run starts"
        );
        s.run_held_inspect();
        s.open(900);
        assert!(
            s.inspect.jvm.prev_histogram.is_none(),
            "a new page starts over"
        );
    }

    #[test]
    fn native_memory_with_nmt_off_is_an_answer_not_a_failure() {
        let mut fake = Fake::all();
        fake.replies.insert(
            "jvm nmt",
            Err(crate::jvm_tools::tests::NMT_OFF_ERR.trim().to_owned()),
        );
        let (mut s, fake) = jvm_faked(fake);
        s.open(7);
        s.run_tool(Tool::NativeMemory);
        s.run_held_inspect();
        let Some(RunState::Done(r)) = s.inspect.state(Tool::NativeMemory) else {
            panic!("{:?}", s.inspect.state(Tool::NativeMemory))
        };
        let Report::NativeMemory(n) = &**r else {
            panic!()
        };
        assert!(n.off().unwrap().contains("NativeMemoryTracking"));
        assert!(n.vm.is_some());
        assert_eq!(fake.calls().len(), 2, "nmt and vm-memory");
    }

    #[test]
    fn a_heap_dump_goes_to_a_named_file_and_its_analyses_read_it() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::HeapDump);
        s.run_held_inspect();
        let call = &fake.calls()[0];
        assert_eq!(&call[..3], ["jvm", "heap-dump", "--out"]);
        let out = std::path::Path::new(&call[3]);
        assert!(
            out.file_name()
                .unwrap()
                .to_str()
                .unwrap()
                .starts_with("App-7-"),
            "{out:?}"
        );
        assert_eq!(out.extension().unwrap(), "hprof");
        assert_eq!(call[4], "7");
        let path = s.heap_dump_path().expect("the path Spectra printed");
        s.run_tool(Tool::LeakSuspects);
        s.run_held_inspect();
        s.run_tool(Tool::Dominators);
        s.run_held_inspect();
        assert_eq!(
            fake.calls()[1],
            [
                "jvm",
                "heap-hprof",
                "--json",
                "--leak-suspects",
                path.as_str()
            ]
        );
        assert_eq!(
            fake.calls()[2],
            ["jvm", "dominators", "--json", path.as_str()]
        );
        assert!(matches!(
            s.inspect.state(Tool::Dominators),
            Some(RunState::Done(_))
        ));
        s.run_tool(Tool::HeapDump);
        assert!(
            s.inspect.state(Tool::LeakSuspects).is_none(),
            "a new dump's analyses start over"
        );
    }

    #[test]
    fn stop_and_analyze_dumps_stops_and_summarises_the_recording() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.now = 3.0;
        s.run_tool(Tool::JfrStart);
        s.run_held_inspect();
        assert_eq!(
            s.inspect.jvm.recording,
            Some(Recording { pid: 7, since: 3.0 })
        );
        assert_eq!(
            s.tool_refusal(Tool::JfrStart).as_deref(),
            Some("Already recording")
        );
        s.run_tool(Tool::JfrStop);
        assert!(s.inspect.jvm.recording.is_none());
        s.run_held_inspect();
        let calls = fake.calls();
        assert_eq!(
            calls[0],
            ["jvm", "jfr", "start", "--name", "spectra-desktop", "7"]
        );
        assert_eq!(
            &calls[1][..5],
            ["jvm", "jfr", "dump", "--name", "spectra-desktop"]
        );
        let out = calls[1][6].clone();
        assert!(out.ends_with(".jfr"));
        assert_eq!(
            calls[2],
            ["jvm", "jfr", "stop", "--name", "spectra-desktop", "7"]
        );
        assert_eq!(calls[3], ["jvm", "jfr", "summary", "--json", out.as_str()]);
        let Some(RunState::Done(r)) = s.inspect.state(Tool::JfrStop) else {
            panic!()
        };
        assert!(
            matches!(&**r, Report::Jfr(j) if j.path == out),
            "the path is where it was written"
        );
    }

    #[test]
    fn a_failed_dump_still_stops_the_recording() {
        let mut fake = Fake::all();
        fake.replies
            .insert("jvm jfr dump", Err("JFR.dump failed".into()));
        let (mut s, fake) = jvm_faked(fake);
        s.open(7);
        s.run_tool(Tool::JfrStart);
        s.run_held_inspect();
        s.run_tool(Tool::JfrStop);
        s.run_held_inspect();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1);
        assert_eq!(jvm_calls(&fake, "jfr summary"), 0);
        assert_eq!(
            s.inspect.state(Tool::JfrStop),
            Some(&RunState::Failed("JFR.dump failed".into()))
        );
    }

    #[test]
    fn leaving_the_page_stops_its_recording() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::JfrStart);
        s.run_held_inspect();
        assert!(s.inspect.jvm.recording.is_some());
        s.open(900);
        assert!(s.inspect.jvm.recording.is_none());
        s.run_held_inspect();
        assert_eq!(
            fake.calls().last().unwrap(),
            &["jvm", "jfr", "stop", "--name", "spectra-desktop", "7"]
        );
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1);
        assert!(s.inspect.runs.is_empty(), "its answer lands nowhere");
    }

    #[test]
    fn a_recording_started_as_the_page_is_left_is_stopped_at_once() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::JfrStart);
        s.open(900);
        s.run_held_inspect();
        assert_eq!(
            jvm_calls(&fake, "jfr stop"),
            1,
            "the start was cancelled, and stopped to be sure"
        );
        assert!(s.inspect.jvm.recording.is_none());
    }

    #[test]
    fn quitting_stops_the_recording_and_waits_for_it() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::JfrStart);
        s.run_held_inspect();
        s.stop_recording_now();
        assert_eq!(
            jvm_calls(&fake, "jfr stop"),
            1,
            "run there and then, not handed to a worker"
        );
        s.stop_recording_now();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1, "only once");
    }

    /// The work a test's `Exec::Held` is holding, to run and land in an order of its own.
    fn take_held(s: &App) -> Vec<Work> {
        match &s.inspect.exec {
            Exec::Held(q) => std::mem::take(&mut *q.lock().unwrap()),
            Exec::Thread => Vec::new(),
        }
    }

    /// Runs a recording start to the end, so `jfr start` succeeded, without landing it.
    fn started_not_landed(s: &mut App) -> Landed {
        s.run_tool(Tool::JfrStart);
        let mut work = take_held(s);
        assert_eq!(work.len(), 1);
        let landed = (work.pop().unwrap())();
        assert!(matches!(&landed, Landed::Tool { pid: 7, result: Ok(_), .. }), "{landed:?}");
        landed
    }

    const STOP_7: [&str; 6] = ["jvm", "jfr", "stop", "--name", "spectra-desktop", "7"];

    #[test]
    fn a_start_that_lands_after_the_page_was_left_is_stopped() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        let landed = started_not_landed(&mut s);
        s.open(900);
        s.run_held_inspect();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 0, "nothing knew of it yet");
        s.land_inspected(landed);
        s.run_held_inspect();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1);
        assert_eq!(fake.calls().last().unwrap(), &STOP_7);
        assert!(s.inspect.jvm.recording.is_none());
    }

    #[test]
    fn a_start_that_lands_after_cancel_is_stopped() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        let landed = started_not_landed(&mut s);
        s.cancel_tool(Tool::JfrStart);
        s.land_inspected(landed);
        assert_eq!(s.inspect.state(Tool::JfrStart), Some(&RunState::Cancelled));
        assert!(s.inspect.jvm.recording.is_none());
        s.run_held_inspect();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1);
        assert_eq!(fake.calls().last().unwrap(), &STOP_7);
        assert_eq!(s.tool_refusal(Tool::JfrStart), None, "it can start again");
    }

    #[test]
    fn a_start_is_refused_while_one_is_pending_or_its_stop_is() {
        const WHY: &str = "A recording is still starting or stopping on this JVM. Try again in a moment.";
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        let landed = started_not_landed(&mut s);
        s.open(900);
        s.open(7);
        assert_eq!(s.tool_refusal(Tool::JfrStart).as_deref(), Some(WHY), "the start hasn't landed");
        s.run_tool(Tool::JfrStart);
        assert_eq!(take_held(&s).len(), 0, "no second start was queued");
        assert_eq!(jvm_calls(&fake, "jfr start"), 1);
        // It lands as unwanted; its stop is queued and still holds the name.
        s.land_inspected(landed);
        assert_eq!(s.tool_refusal(Tool::JfrStart).as_deref(), Some(WHY), "a stop is pending");
        s.run_held_inspect();
        assert_eq!(jvm_calls(&fake, "jfr start"), 1, "only the first start ran");
        assert_eq!(s.tool_refusal(Tool::JfrStart), None, "allowed once start and stop landed");
    }

    #[test]
    fn quitting_with_two_pending_stops_stops_both() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.inspect.starts.insert(1, 7);
        s.inspect.starts.insert(2, 8);
        s.stop_recording_now();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 2);
        assert!(s.inspect.starts.is_empty());
    }

    #[test]
    fn quitting_with_a_start_done_but_not_landed_stops_it() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        let _landed = started_not_landed(&mut s);
        s.stop_recording_now();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1, "stopped there and then");
        assert_eq!(fake.calls().last().unwrap(), &STOP_7);
    }

    #[test]
    fn quitting_with_a_start_in_flight_cancels_and_stops_it() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        s.run_tool(Tool::JfrStart);
        s.stop_recording_now();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1, "stopped there and then");
        let landed = (take_held(&s).pop().unwrap())();
        assert!(matches!(landed, Landed::Tool { result: Err(Failure::Cancelled), .. }), "the start itself was cancelled");
    }

    #[test]
    fn quitting_with_a_start_from_a_page_already_left_stops_it() {
        let (mut s, fake) = jvm_faked(Fake::all());
        s.open(7);
        let _landed = started_not_landed(&mut s);
        s.open(900);
        s.stop_recording_now();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1);
        assert_eq!(fake.calls().last().unwrap(), &STOP_7);
    }

    #[test]
    fn a_failed_start_is_stopped_too() {
        let mut fake = Fake::all();
        fake.replies.insert("jvm jfr start", Err("timed out after 30s".into()));
        let (mut s, fake) = jvm_faked(fake);
        s.open(7);
        s.run_tool(Tool::JfrStart);
        s.run_held_inspect();
        assert_eq!(jvm_calls(&fake, "jfr stop"), 1, "JFR.start may have run before it failed");
        assert_eq!(s.inspect.state(Tool::JfrStart), Some(&RunState::Failed("timed out after 30s".into())));
        assert!(s.inspect.jvm.recording.is_none());
    }

    /// Needs Spectra and runs it on a `sleep` this test starts: `cargo test -- --ignored`.
    #[test]
    #[ignore]
    fn runs_the_real_tools_on_a_sleep_of_its_own() {
        let mut child = Command::new("sleep").arg("120").spawn().unwrap();
        let pid = child.id();
        let never = AtomicBool::new(false);
        let calc = System.run(&bundle::args("/System/Applications/Calculator.app"), bundle::DEADLINE, &never);
        println!("bundle     {:?}", calc.map_err(|e| format!("{e:?}")).and_then(|o| bundle::parse(&o)).map(|b| b.built_with()));
        for tool in Tool::ALL.into_iter().filter(|t| t.needs_root().is_none() && t.kind() != Kind::Jvm) {
            let out = System.run(&tool.args(pid), tool.deadline(), &never);
            let parsed = out.map_err(|e| format!("{e:?}")).and_then(|o| tool.parse(&o));
            println!("{:<10} {}", tool.id(), if parsed.is_ok() { "ok".into() } else { format!("{parsed:?}") });
            assert!(parsed.is_ok(), "{tool:?}");
        }
        let net = System.run(&live::net_args(pid), live::NET_DEADLINE, &never).unwrap();
        println!("network {:?}", live::parse_net(&net));
        let imp = System.run(&live::impact_args(), live::IMPACT_DEADLINE, &never).unwrap();
        println!("impact {:?}", live::parse_impact(&imp, &[pid]));
        // Only the sleep this test started.
        let _ = child.kill();
        let _ = child.wait();
    }
}
