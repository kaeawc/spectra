//! What an open page runs: its tools, on demand and one at a time; its live network and energy
//! readings, one round at a time while it's open; and an app page's bundle, read once per
//! opening. Everything runs Spectra off the UI thread with a deadline, and lands only on the
//! page it was for.

use std::collections::HashMap;
use std::process::Command;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::app::App;
use crate::bundle::{self, Bundle};
use crate::live::{self, NetPoint, Round, Series};
use crate::procs::{self, Process, ProcSample, Role};
use crate::run::{self, Ended};
use crate::tools::{Report, Tool};

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

/// What the worker hands back, numbered by the job it was.
#[derive(Clone, Debug)]
pub enum Landed {
    Tool { job: u64, tool: Tool, result: Result<Report, Failure> },
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

    /// Forgets the last page's work, stopping whatever of it still runs.
    fn reset(&mut self, subject: Option<Subject>) {
        for r in self.runs.values() {
            r.cancel.store(true, Ordering::Relaxed);
        }
        for f in [self.live.take(), self.bundle_job.take()].into_iter().flatten() {
            f.cancel.store(true, Ordering::Relaxed);
        }
        self.runs.clear();
        self.net = Series::default();
        self.energy = Series::default();
        self.next_live = 0.0;
        self.bundle = None;
        self.subject = subject;
    }

    fn next_job(&mut self) -> (u64, Arc<AtomicBool>) {
        self.job += 1;
        (self.job, Arc::new(AtomicBool::new(false)))
    }
}

impl Default for Inspector {
    fn default() -> Inspector {
        Inspector::new()
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
        if tool.reads_task()
            && let (Some(p), Some(me)) = (self.process(*pid), self.me.uid)
            && p.uid != me
        {
            let who = if p.user.is_empty() { format!("uid {}", p.uid) } else { p.user.clone() };
            return Some(format!("Owned by {who}; reading its threads or memory needs root"));
        }
        None
    }

    /// Runs `tool` on the open page's process, unless another tool is running or it can't.
    pub fn run_tool(&mut self, tool: Tool) {
        if self.tool_refusal(tool).is_some() || self.inspect.busy().is_some() {
            return;
        }
        let Some(Subject::Process { pid, .. }) = self.inspect.subject.clone() else { return };
        let (job, cancel) = self.inspect.next_job();
        let spectra = Arc::clone(&self.inspect.spectra);
        let flag = Arc::clone(&cancel);
        self.inspect.runs.insert(tool, ToolRun { state: RunState::Running { since: self.now }, job, cancel });
        self.submit_inspect(
            Box::new(move || {
                let result = spectra
                    .run(&tool.args(pid), tool.deadline(), &flag)
                    .and_then(|out| tool.parse(&out).map_err(Failure::Failed));
                Landed::Tool { job, tool, result }
            }),
            Landed::Tool { job, tool, result: Err(Failure::Failed("Spectra Desktop hit an internal error running it.".into())) },
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

    /// The pids a live round reads energy for, and the pid whose network (with its
    /// descendants') it reads. `None` when there's nothing running to read.
    fn live_targets(&self) -> Option<(Vec<u32>, u32)> {
        match self.inspect.subject.as_ref()? {
            Subject::Process { pid, .. } => {
                let listed = self.process(*pid).is_some() && self.ended_page().is_none();
                (listed || self.jvm(*pid).is_some()).then(|| (vec![*pid], *pid))
            }
            Subject::App(path) => {
                let pids: Vec<u32> = self.app_procs(path).iter().map(|p| p.pid).collect();
                let lead = self.app_lead(path)?;
                Some((pids, lead))
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
        let i = &mut self.inspect;
        match l {
            Landed::Tool { job, tool, result } => {
                let Some(r) = i.runs.get_mut(&tool).filter(|r| r.job == job) else { return };
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
                i.next_live = now + live::REST;
                match round.net {
                    Some(Ok(p)) => i.net.push(p),
                    Some(Err(why)) => i.net.off = Some(why),
                    None => {}
                }
                match round.energy {
                    Some(Ok(Some(v))) => i.energy.push(v),
                    Some(Err(why)) => i.energy.off = Some(why),
                    Some(Ok(None)) | None => {}
                }
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
            match self.replies.get(args[0].as_str()) {
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
    fn a_failed_live_series_stops_and_says_why() {
        let (mut s, fake) = faked(Fake::with(&[("network", Ok(crate::live::tests::NET)), ("impact", Err("impact: not supported on linux"))]));
        s.open(900);
        s.tick_inspector();
        s.run_held_inspect();
        assert_eq!(s.inspect.energy.off.as_deref(), Some("impact: not supported on linux"));
        s.now += live::REST;
        s.tick_inspector();
        s.run_held_inspect();
        assert_eq!(s.inspect.net.points.len(), 2);
        assert_eq!(fake.calls().iter().filter(|c| c[0] == "impact").count(), 1, "not asked again");
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

    /// Needs Spectra and runs it on a `sleep` this test starts: `cargo test -- --ignored`.
    #[test]
    #[ignore]
    fn runs_the_real_tools_on_a_sleep_of_its_own() {
        let mut child = Command::new("sleep").arg("120").spawn().unwrap();
        let pid = child.id();
        let never = AtomicBool::new(false);
        let calc = System.run(&bundle::args("/System/Applications/Calculator.app"), bundle::DEADLINE, &never);
        println!("bundle     {:?}", calc.map_err(|e| format!("{e:?}")).and_then(|o| bundle::parse(&o)).map(|b| b.built_with()));
        for tool in Tool::ALL.into_iter().filter(|t| t.needs_root().is_none()) {
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
