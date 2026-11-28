//! The code view: on a "Show code" press, the code of a frame whose source isn't public,
//! decompiled where a decompiler is installed, else disassembled (or `javap`'s bytecode), else
//! the source file when the runtime keeps one. These are local developer tools, not Spectra:
//! they run here, off the UI thread, one step at a time within one deadline and output cap,
//! only on a press, and stop when the panel closes or the page changes. Only the processes
//! started here are stopped (their own process group, see `run.rs`). The view is one panel any
//! page can open with a [`Request`].

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use crate::app::App;
use crate::codeview_parse::{demangle, mangled, minified, parse};
use crate::codeview_plan::{
    NoView, Output, Parse, Request, Step, TMP, Target, plan, resolve_until,
};
use crate::codeview_tools::{Probe, SystemProbe, Tool, Tools, detect};
use crate::groups::Nav;
use crate::inspect::Failure;
use crate::run::{Ended, RunError};

/// How long one view may take, all its steps together.
pub const DEADLINE: Duration = Duration::from_secs(30);
/// The most any step may print.
pub const CAP: usize = 2 << 20;

/// Runs code tools and reads files for the view. Tests swap in a fake.
pub trait Runner: Send + Sync {
    /// Runs `program` with `args`, returning its stdout; stopped at `deadline`, past `cap` bytes,
    /// or once `cancel` is set. A non-zero exit is a failure with its stderr's first line.
    fn run(
        &self,
        program: &Path,
        args: &[String],
        deadline: Duration,
        cap: usize,
        cancel: &AtomicBool,
    ) -> Result<String, Failure>;
    /// A file's text, at most `cap` bytes of it.
    fn read(&self, path: &Path, cap: usize) -> Result<String, String>;
    /// A new empty folder in the system temp folder.
    fn temp_dir(&self) -> Result<PathBuf, String>;
    fn write(&self, path: &Path, text: &str) -> Result<(), String>;
    /// Makes a folder inside a temp folder.
    fn make_dir(&self, path: &Path) -> Result<(), String>;
    /// Removes a folder [`Runner::temp_dir`] made, and what's in it.
    fn remove_temp(&self, dir: &Path);
    /// The first file under `dir` with extension `ext`.
    fn find(&self, dir: &Path, ext: &str) -> Option<PathBuf>;
}

/// The real tools and filesystem.
pub struct System;

/// What every temp folder here is called, so removal touches nothing else.
const TEMP_PREFIX: &str = "spectra-desktop-codeview-";

static TEMPS: AtomicU64 = AtomicU64::new(0);

impl Runner for System {
    fn run(
        &self,
        program: &Path,
        args: &[String],
        deadline: Duration,
        cap: usize,
        cancel: &AtomicBool,
    ) -> Result<String, Failure> {
        let name = program.file_name().map_or_else(
            || program.display().to_string(),
            |n| n.to_string_lossy().into_owned(),
        );
        let mut cmd = std::process::Command::new(program);
        cmd.args(args);
        let out = crate::run::run_until(cmd, deadline, cap, cancel).map_err(|e| match e {
            Ended::Cancelled => Failure::Cancelled,
            Ended::Failed(e) => Failure::Failed(run_failure(&name, e)),
        })?;
        if !out.status.success() {
            let stderr = String::from_utf8_lossy(&out.stderr);
            let stdout = String::from_utf8_lossy(&out.stdout);
            let line = stderr
                .lines()
                .chain(stdout.lines())
                .map(str::trim)
                .find(|l| !l.is_empty())
                .map_or_else(
                    || {
                        format!(
                            "{name} stopped with exit status {}",
                            out.status.code().unwrap_or(-1)
                        )
                    },
                    |l| format!("{name}: {l}"),
                );
            return Err(Failure::Failed(line));
        }
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    }

    fn read(&self, path: &Path, cap: usize) -> Result<String, String> {
        use std::io::Read;
        let f = std::fs::File::open(path)
            .map_err(|e| format!("Couldn't open {}: {e}", path.display()))?;
        let mut buf = Vec::new();
        f.take(cap as u64 + 1)
            .read_to_end(&mut buf)
            .map_err(|e| format!("Couldn't read {}: {e}", path.display()))?;
        if buf.len() > cap {
            return Err(format!("{} is over {} MB", path.display(), cap >> 20));
        }
        Ok(String::from_utf8_lossy(&buf).into_owned())
    }

    fn temp_dir(&self) -> Result<PathBuf, String> {
        let n = TEMPS.fetch_add(1, Ordering::SeqCst);
        let dir = std::env::temp_dir().join(format!("{TEMP_PREFIX}{}-{n}", std::process::id()));
        std::fs::create_dir(&dir).map_err(|e| format!("Couldn't make a temp folder: {e}"))?;
        Ok(dir)
    }

    fn write(&self, path: &Path, text: &str) -> Result<(), String> {
        std::fs::write(path, text).map_err(|e| format!("Couldn't write {}: {e}", path.display()))
    }

    fn make_dir(&self, path: &Path) -> Result<(), String> {
        std::fs::create_dir(path).map_err(|e| format!("Couldn't make {}: {e}", path.display()))
    }

    fn remove_temp(&self, dir: &Path) {
        // Only a folder of ours, directly in the temp folder.
        let ours = dir.parent() == Some(std::env::temp_dir().as_path())
            && dir
                .file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| n.starts_with(TEMP_PREFIX));
        if ours {
            let _ = std::fs::remove_dir_all(dir);
        }
    }

    fn find(&self, dir: &Path, ext: &str) -> Option<PathBuf> {
        let mut stack = vec![dir.to_path_buf()];
        let mut seen = 0;
        while let Some(d) = stack.pop() {
            let Ok(entries) = std::fs::read_dir(&d) else {
                continue;
            };
            let mut entries: Vec<PathBuf> =
                entries.filter_map(|e| e.ok().map(|e| e.path())).collect();
            entries.sort();
            for e in entries {
                seen += 1;
                if seen > 10_000 {
                    return None;
                }
                if e.is_dir() {
                    stack.push(e);
                } else if e.extension().is_some_and(|x| x == ext) {
                    return Some(e);
                }
            }
        }
        None
    }
}

/// What a failed run says, by tool name.
fn run_failure(name: &str, e: RunError) -> String {
    match e {
        RunError::Spawn(e) => format!("Couldn't run {name}: {e}"),
        RunError::Timeout(d) => format!(
            "{name} took longer than {} s and was stopped",
            d.as_secs_f64().round()
        ),
        RunError::TooLarge(cap) if cap >= 1 << 20 => {
            format!("{name} printed more than {} MB and was stopped", cap >> 20)
        }
        RunError::TooLarge(cap) => {
            format!("{name} printed more than {} KB and was stopped", cap >> 10)
        }
        RunError::Io(e) => format!("Couldn't read {name}'s output: {e}"),
    }
}

/// What the panel shows once a step worked.
#[derive(Clone, Debug, PartialEq)]
pub struct Shown {
    /// What it is and which tool made it: "Disassembly from lldb".
    pub what: String,
    pub lines: Vec<String>,
    pub mark: Option<usize>,
    /// What the mark is, or why there isn't one.
    pub note: String,
    /// Steps that came first and didn't work, and why.
    pub tried: Vec<String>,
}

#[derive(Clone, Debug, PartialEq)]
pub enum State {
    Running,
    Shown(Shown),
    /// No code view, and why: no tool, nothing to look up, or every step failed.
    Nothing(String),
}

/// The page a panel was opened on; another page closes it.
#[derive(Clone, Debug, PartialEq)]
pub struct Origin {
    pub nav: Nav,
    pub selected: Option<u32>,
    pub app: Option<String>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Panel {
    pub title: String,
    pub request: Request,
    pub origin: Origin,
    pub state: State,
    /// Every line, not only from a little above the mark.
    pub show_all: bool,
}

struct Landed {
    job: u64,
    answer: Option<State>,
}

/// The code view's state.
pub struct CodeView {
    pub open: Option<Panel>,
    pub runner: Arc<dyn Runner>,
    pub probe: Arc<dyn Probe>,
    /// False in tests: a job runs on the calling thread.
    pub threaded: bool,
    /// What was found, kept for the app's lifetime.
    tools: Arc<Mutex<Option<Arc<Tools>>>>,
    flight: Option<(u64, Arc<AtomicBool>)>,
    job: u64,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
}

impl Default for CodeView {
    fn default() -> CodeView {
        CodeView::new()
    }
}

impl CodeView {
    pub fn new() -> CodeView {
        let (tx, rx) = channel();
        CodeView {
            open: None,
            runner: Arc::new(System),
            probe: Arc::new(SystemProbe),
            threaded: true,
            tools: Arc::new(Mutex::new(None)),
            flight: None,
            job: 0,
            tx,
            rx,
        }
    }

    /// Sets what was found, for tests.
    #[cfg(test)]
    pub fn set_tools(&self, t: Tools) {
        *self.tools.lock().unwrap_or_else(|e| e.into_inner()) = Some(Arc::new(t));
    }

    /// Stops the job on its way; its answer is dropped.
    pub fn cancel(&mut self) {
        if let Some((_, c)) = self.flight.take() {
            c.store(true, Ordering::SeqCst);
        }
    }

    /// Closes the panel, stopping what it runs.
    pub fn close(&mut self) {
        self.cancel();
        self.open = None;
    }
}

/// The cached tools, looked for on first use, and again when `refresh`.
fn tools(cache: &Mutex<Option<Arc<Tools>>>, probe: &dyn Probe, refresh: bool) -> Arc<Tools> {
    let mut c = cache.lock().unwrap_or_else(|e| e.into_inner());
    match &*c {
        Some(t) if !refresh => Arc::clone(t),
        _ => {
            let t = Arc::new(detect(probe));
            *c = Some(Arc::clone(&t));
            t
        }
    }
}

/// Swaps the temp folder's place-holder for the folder.
fn with_temp(args: &[String], dir: Option<&Path>) -> Vec<String> {
    match dir {
        Some(d) => args
            .iter()
            .map(|a| a.replace(TMP, &d.to_string_lossy()))
            .collect(),
        None => args.to_vec(),
    }
}

/// A step's temp folder, removed when this goes, even by a panic.
struct Temp<'a> {
    r: &'a dyn Runner,
    dir: PathBuf,
}

impl Drop for Temp<'_> {
    fn drop(&mut self) {
        self.r.remove_temp(&self.dir);
    }
}

/// Runs one step: its temp folder, files and command, and where its answer is. The folder is
/// removed whatever happens.
fn run_step(
    r: &dyn Runner,
    st: &Step,
    left: Duration,
    cancel: &AtomicBool,
) -> Result<String, Failure> {
    if let Output::Read(p) = &st.output {
        return r.read(p, CAP).map_err(Failure::Failed);
    }
    let temp = if st.needs_temp() {
        Some(Temp {
            r,
            dir: r.temp_dir().map_err(Failure::Failed)?,
        })
    } else {
        None
    };
    let dir = temp.as_ref().map(|t| t.dir.as_path());
    if let Some(d) = dir {
        for sub in &st.dirs {
            r.make_dir(&d.join(sub)).map_err(Failure::Failed)?;
        }
        for (name, text) in &st.files {
            r.write(&d.join(name), text).map_err(Failure::Failed)?;
        }
    }
    let args = with_temp(&st.args, dir);
    let out = r.run(&st.program, &args, left, CAP, cancel)?;
    match (&st.output, dir) {
        (Output::File(name), Some(d)) => r.read(&d.join(name), CAP).map_err(Failure::Failed),
        (Output::JavaIn(sub), Some(d)) => {
            let f = r
                .find(&d.join(sub), "java")
                .ok_or_else(|| Failure::Failed(format!("{} wrote no Java file", st.label())))?;
            r.read(&f, CAP).map_err(Failure::Failed)
        }
        _ => Ok(out),
    }
}

/// Names a demangler can turn readable in disassembly lldb didn't print.
fn demangled(
    r: &dyn Runner,
    t: &Tools,
    parse: Parse,
    lines: &mut [String],
    left: &dyn Fn() -> Duration,
    cancel: &AtomicBool,
) {
    if !matches!(parse, Parse::Objdump | Parse::Otool) {
        return;
    }
    for (tool, swift) in [(Tool::SwiftDemangle, true), (Tool::CxxFilt, false)] {
        let Some(bin) = t.get(tool) else {
            continue;
        };
        // Measured again for each: the step before may have used it all.
        let left = left();
        if left.is_zero() {
            return;
        }
        let names = mangled(lines, swift);
        if names.is_empty() {
            continue;
        }
        let mut args: Vec<String> = if swift {
            vec!["--compact".into()]
        } else {
            Vec::new()
        };
        args.extend(names.iter().cloned());
        if let Ok(out) = r.run(bin, &args, left.min(Duration::from_secs(5)), CAP, cancel) {
            demangle(lines, &names, &out);
        }
    }
}

/// The whole job: resolve the request, plan, and try each step until one shows code. `None`
/// when cancelled.
pub fn work(
    req: &Request,
    runner: &dyn Runner,
    probe: &dyn Probe,
    cache: &Mutex<Option<Arc<Tools>>>,
    cancel: &AtomicBool,
) -> Option<State> {
    work_within(req, runner, probe, cache, cancel, DEADLINE)
}

/// [`work`], within `budget` rather than [`DEADLINE`].
fn work_within(
    req: &Request,
    runner: &dyn Runner,
    probe: &dyn Probe,
    cache: &Mutex<Option<Arc<Tools>>>,
    cancel: &AtomicBool,
    budget: Duration,
) -> Option<State> {
    let start = Instant::now();
    let time_left = || budget.saturating_sub(start.elapsed());
    let target = resolve_until(req, probe, &|| {
        cancel.load(Ordering::SeqCst) || time_left().is_zero()
    });
    if cancel.load(Ordering::SeqCst) {
        return None;
    }
    if time_left().is_zero() {
        return Some(State::Nothing(format!(
            "Looking the frame up took longer than {} s and was stopped.",
            budget.as_secs_f64().round()
        )));
    }
    let mut t = tools(cache, probe, false);
    let mut planned = plan(&target, &t);
    // Nothing found to run: look again, in case something was installed since.
    if let Err(NoView {
        missing_tool: true, ..
    }) = planned
    {
        t = tools(cache, probe, true);
        planned = plan(&target, &t);
    }
    let steps = match planned {
        Ok(s) => s,
        Err(no) => return Some(State::Nothing(no.reason)),
    };
    let mut tried = Vec::new();
    for st in &steps {
        if cancel.load(Ordering::SeqCst) {
            return None;
        }
        let left = time_left();
        if left.is_zero() {
            tried.push(format!("{}: out of time", st.label()));
            break;
        }
        if st.only_if_minified
            && let Target::Script(s) = &target
            && let Some(p) = &s.path
            && !runner.read(p, CAP).is_ok_and(|text| minified(&text))
        {
            continue;
        }
        let out = match run_step(runner, st, left, cancel) {
            Ok(out) => out,
            Err(Failure::Cancelled) => return None,
            Err(Failure::Failed(why)) => {
                tried.push(format!("{}: {why}", st.label()));
                continue;
            }
        };
        match parse(st.parse, &out, &target) {
            Ok(mut p) => {
                demangled(runner, &t, st.parse, &mut p.lines, &time_left, cancel);
                return Some(State::Shown(Shown {
                    what: st.parse.what(st.label()),
                    lines: p.lines,
                    mark: p.mark,
                    note: p.note,
                    tried,
                }));
            }
            Err(why) => tried.push(format!("{}: {why}", st.label())),
        }
    }
    if cancel.load(Ordering::SeqCst) {
        return None;
    }
    Some(State::Nothing(format!(
        "Every tool tried failed. {}",
        tried.join(". ")
    )))
}

impl App {
    pub(crate) fn origin(&self) -> Origin {
        Origin {
            nav: self.nav,
            selected: self.selected,
            app: self.app_page.clone(),
        }
    }

    /// Opens the code view for `req`, stopping any view already on its way.
    pub fn show_code(&mut self, req: Request) {
        let origin = self.origin();
        let wake = self.wake.clone();
        let c = &mut self.codeview;
        c.cancel();
        c.job += 1;
        let job = c.job;
        let cancel = Arc::new(AtomicBool::new(false));
        c.flight = Some((job, Arc::clone(&cancel)));
        c.open = Some(Panel {
            title: req.title(),
            request: req.clone(),
            origin,
            state: State::Running,
            show_all: false,
        });
        let (runner, probe, cache, tx) = (
            Arc::clone(&c.runner),
            Arc::clone(&c.probe),
            Arc::clone(&c.tools),
            c.tx.clone(),
        );
        let go = move || {
            // A panic in a tool's handling still lands, so the panel doesn't wait forever.
            let answer = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                work(&req, runner.as_ref(), probe.as_ref(), &cache, &cancel)
            }))
            .unwrap_or_else(|_| Some(State::Nothing("The code view stopped unexpectedly.".into())));
            let _ = tx.send(Landed { job, answer });
        };
        if !c.threaded {
            go();
            return;
        }
        let hold = crate::run::pending();
        let started = std::thread::Builder::new()
            .name("codeview".into())
            .spawn(move || {
                go();
                drop(hold);
                if let Some(wake) = wake {
                    wake();
                }
            });
        if started.is_err() {
            let _ = c.tx.send(Landed {
                job,
                answer: Some(State::Nothing(
                    "Couldn't start a thread to run the tools.".into(),
                )),
            });
        }
    }

    /// The code of a crash report's frame.
    pub fn show_crash_code(&mut self, frame: &str, process_path: &str) {
        self.show_code(Request::Crash {
            frame: frame.to_owned(),
            process_path: process_path.to_owned(),
        });
    }

    /// The code of a function a sample of `pid` caught.
    pub fn show_sampled_code(&mut self, pid: u32, symbol: &str) {
        let exe = self
            .process(pid)
            .map(|p| p.executable_path.clone())
            .unwrap_or_default();
        self.show_code(Request::Sampled {
            symbol: symbol.to_owned(),
            exe,
        });
    }

    /// The code of a Java frame in JVM `pid`.
    pub fn show_java_code(&mut self, pid: u32, frame: &str) {
        let (command_line, exe) = self
            .process(pid)
            .map(|p| (p.full_command_line.clone(), p.executable_path.clone()))
            .unwrap_or_default();
        self.show_code(Request::Java {
            frame: frame.to_owned(),
            command_line,
            exe,
        });
    }

    pub fn close_code(&mut self) {
        self.codeview.close();
    }

    pub fn show_all_code(&mut self) {
        if let Some(p) = &mut self.codeview.open {
            p.show_all = true;
        }
    }

    /// Lands a finished view, and closes the panel once its page is gone. True when the panel
    /// changed.
    pub fn land_codeview(&mut self) -> bool {
        let mut changed = false;
        if self
            .codeview
            .open
            .as_ref()
            .is_some_and(|p| p.origin != self.origin())
        {
            self.codeview.close();
            changed = true;
        }
        while let Ok(l) = self.codeview.rx.try_recv() {
            let c = &mut self.codeview;
            if c.flight.as_ref().is_none_or(|(j, _)| *j != l.job) {
                continue;
            }
            c.flight = None;
            if let (Some(p), Some(state)) = (&mut c.open, l.answer) {
                p.state = state;
                changed = true;
            }
        }
        changed
    }
}

#[cfg(test)]
#[path = "codeview_tests.rs"]
pub(crate) mod tests;
