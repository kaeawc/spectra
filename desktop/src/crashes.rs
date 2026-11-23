//! What has been crashing on this Mac, and why: `spectra crash signatures` (repeated crashes
//! grouped by process, exception and top frame), `spectra crash list` (recent crash and hang
//! reports) and `spectra crash oom` (memory-limit and low-memory kills), read from the reports
//! folder. Read when the Crashes page opens with nothing read yet or a reading older than
//! [`STALE`], and on Refresh; never on a timer. One report is decoded with
//! `spectra crash inspect` only when "Open report" is pressed.

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::time::Duration;

use serde::Deserialize;

use crate::app::App;
use crate::groups::Nav;
use crate::inspect::{Failure, Spectra};
use crate::procs::parse_rfc3339;

/// How long each of the three listings may take: each one reads every report in the folder.
pub const DEADLINE: Duration = Duration::from_secs(45);
/// How long decoding one report may take.
pub const INSPECT_DEADLINE: Duration = Duration::from_secs(30);
/// The most a listing may print; Spectra stops at 25 rows, a few kilobytes.
const MAX_OUTPUT: usize = 4 << 20;
/// The most one decoded report may print: every thread's frames.
const INSPECT_OUTPUT: usize = 16 << 20;
/// Opening the page reads again only once its last reading is this old, in seconds; Refresh
/// always reads.
pub const STALE: i64 = 10 * 60;
/// Frames of the crashed thread an opened report shows.
pub const TOP_FRAMES: usize = 8;

/// Repeated crashes of one process with one exception and top frame.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Signature {
    pub process: String,
    /// `EXC_CRASH (SIGKILL)`, as the report has it; empty when it had none.
    pub exception: String,
    pub top_frame: String,
    pub count: u64,
    /// Unix seconds, when the times could be read.
    pub first: Option<i64>,
    pub last: Option<i64>,
    /// The latest report of this kind, to open; empty when Spectra named none.
    pub file: String,
}

/// One crash or hang report.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Report {
    pub at: Option<i64>,
    pub process: String,
    /// `crash`, `hang`, or Spectra's longer label for an unknown kind.
    pub kind: String,
    pub exception: String,
    pub file: String,
}

/// A process ended for using too much memory.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct MemoryKill {
    pub at: Option<i64>,
    pub process: String,
    pub reason: String,
    pub limit: String,
    pub observed: String,
    pub file: String,
}

/// One thread of a decoded report.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Thread {
    pub index: i64,
    pub name: String,
    pub queue: String,
    pub crashed: bool,
    pub frames: Vec<String>,
}

/// A resource limit the process went over.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Resource {
    pub flavor: String,
    pub explanation: String,
    pub limit: String,
    pub observed: String,
}

/// One report as `spectra crash inspect --json` decodes it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Inspected {
    pub process: String,
    /// The crashed process's executable, as the report names it.
    pub path: String,
    pub version: String,
    pub pid: Option<i64>,
    pub at: Option<i64>,
    pub kind: String,
    pub os_version: String,
    pub exception: String,
    /// Spectra's one-line reading of the exception type.
    pub detail: String,
    /// How the system said it ended: `Trace/BPT trap: 5`.
    pub termination: String,
    pub resource: Option<Resource>,
    /// The thread that crashed, when the report has one.
    pub crashed: Option<Thread>,
    pub threads: usize,
}

/// One reading of the three listings.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Crashes {
    /// Most often first, then most recent.
    pub signatures: Vec<Signature>,
    /// Newest first.
    pub reports: Vec<Report>,
    pub kills: Vec<MemoryKill>,
    /// Why the recent reports couldn't be read, when the signatures could.
    pub reports_error: Option<String>,
    /// Why the memory kills couldn't be read, when the signatures could.
    pub kills_error: Option<String>,
}

impl Crashes {
    pub fn is_empty(&self) -> bool {
        self.signatures.is_empty() && self.reports.is_empty() && self.kills.is_empty()
    }
}

/// A report's time: `2026-11-20 06:48:10.00 -0500`, or RFC 3339.
pub fn parse_time(s: &str) -> Option<i64> {
    let s = s.trim();
    if let Some(t) = parse_rfc3339(s) {
        return Some(t);
    }
    // `… 06:48:10.00 -0500` → `…T06:48:10.00-05:00`.
    let (when, zone) = s.rsplit_once(' ')?;
    let b = zone.as_bytes();
    if b.len() != 5
        || !matches!(b[0], b'+' | b'-')
        || !zone[1..].bytes().all(|c| c.is_ascii_digit())
    {
        return None;
    }
    let when = when.replacen(' ', "T", 1);
    parse_rfc3339(&format!("{when}{}:{}", &zone[..3], &zone[3..]))
}

/// The exception in plain words, where it's obvious: a resource limit first (it ends in SIGKILL),
/// then the signal, then the exception type.
pub fn plain(exception: &str) -> Option<&'static str> {
    let e = exception.to_ascii_uppercase();
    let has = |w: &str| e.contains(w);
    Some(if has("EXC_RESOURCE") {
        "went over a limit"
    } else if has("SIGKILL") {
        "killed"
    } else if has("SIGABRT") {
        "aborted itself"
    } else if has("SIGSEGV") || has("SIGBUS") || has("EXC_BAD_ACCESS") {
        "bad memory access"
    } else if has("SIGTRAP") || has("EXC_BREAKPOINT") {
        "stopped on a check"
    } else if has("SIGILL") || has("EXC_BAD_INSTRUCTION") {
        "ran a bad instruction"
    } else if has("SIGFPE") || has("EXC_ARITHMETIC") {
        "bad arithmetic"
    } else if has("EXC_GUARD") {
        "broke a guarded resource"
    } else {
        return None;
    })
}

/// What happened, short: the plain words, else the exception as written, else the kind.
pub fn what(exception: &str, kind: &str) -> String {
    if let Some(p) = plain(exception) {
        return p.to_owned();
    }
    if !exception.trim().is_empty() {
        return exception.trim().to_owned();
    }
    match kind.trim() {
        "hang" => "stopped responding".into(),
        "" => "crashed".into(),
        k if k.starts_with("crash") => "crashed".into(),
        k => k.to_owned(),
    }
}

/// `null` and a missing field read as nothing.
fn text(v: Option<String>) -> String {
    v.unwrap_or_default().trim().to_owned()
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawSignature {
    process: Option<String>,
    exception: Option<String>,
    top_frame: Option<String>,
    count: Option<u64>,
    first_seen: Option<String>,
    last_seen: Option<String>,
    sample_file: Option<String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawReport {
    time: Option<String>,
    process: Option<String>,
    kind: Option<String>,
    exception: Option<String>,
    file: Option<String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawKill {
    time: Option<String>,
    process: Option<String>,
    reason: Option<String>,
    limit: Option<String>,
    observed: Option<String>,
    file: Option<String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawThread {
    index: Option<i64>,
    name: Option<String>,
    queue: Option<String>,
    triggered: Option<bool>,
    frames: Option<Vec<Option<String>>>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawResource {
    flavor: Option<String>,
    explanation: Option<String>,
    limit: Option<String>,
    observed: Option<String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawInspected {
    process: Option<String>,
    path: Option<String>,
    version: Option<String>,
    pid: Option<i64>,
    time: Option<String>,
    kind: Option<String>,
    os_version: Option<String>,
    exception: Option<String>,
    exception_detail: Option<String>,
    termination: Option<String>,
    resource: Option<RawResource>,
    faulting_thread: Option<i64>,
    threads: Option<Vec<RawThread>>,
}

/// A JSON list, `null` read as empty.
fn list<T: for<'de> Deserialize<'de>>(json: &str, what: &str) -> Result<Vec<T>, String> {
    let raw: Option<Vec<T>> = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's {what} couldn't be read: {e}"))?;
    Ok(raw.unwrap_or_default())
}

/// Most often first, then the most recent, then by name.
pub fn sort_signatures(sigs: &mut [Signature]) {
    sigs.sort_by(|a, b| {
        b.count
            .cmp(&a.count)
            .then(b.last.cmp(&a.last))
            .then_with(|| a.process.cmp(&b.process))
    });
}

/// `spectra crash signatures --json`, sorted. One without a process is left out.
pub fn parse_signatures(json: &str) -> Result<Vec<Signature>, String> {
    let mut out: Vec<Signature> = list::<RawSignature>(json, "crash signatures")?
        .into_iter()
        .map(|r| Signature {
            process: text(r.process),
            exception: text(r.exception),
            top_frame: text(r.top_frame),
            count: r.count.unwrap_or(1).max(1),
            first: r.first_seen.as_deref().and_then(parse_time),
            last: r.last_seen.as_deref().and_then(parse_time),
            file: text(r.sample_file),
        })
        .filter(|s| !s.process.is_empty())
        .collect();
    sort_signatures(&mut out);
    Ok(out)
}

/// `spectra crash list --json`, newest first (a report without a time last).
pub fn parse_reports(json: &str) -> Result<Vec<Report>, String> {
    let mut out: Vec<Report> = list::<RawReport>(json, "crash reports")?
        .into_iter()
        .map(|r| Report {
            at: r.time.as_deref().and_then(parse_time),
            process: text(r.process),
            kind: text(r.kind),
            exception: text(r.exception),
            file: text(r.file),
        })
        .filter(|r| !r.process.is_empty())
        .collect();
    out.sort_by_key(|a| std::cmp::Reverse(a.at));
    Ok(out)
}

/// `spectra crash oom --json`, newest first.
pub fn parse_kills(json: &str) -> Result<Vec<MemoryKill>, String> {
    let mut out: Vec<MemoryKill> = list::<RawKill>(json, "memory kills")?
        .into_iter()
        .map(|r| MemoryKill {
            at: r.time.as_deref().and_then(parse_time),
            process: text(r.process),
            reason: text(r.reason),
            limit: text(r.limit),
            observed: text(r.observed),
            file: text(r.file),
        })
        .filter(|k| !k.process.is_empty())
        .collect();
    out.sort_by_key(|a| std::cmp::Reverse(a.at));
    Ok(out)
}

/// `spectra crash inspect --json <file>`.
pub fn parse_inspect(json: &str) -> Result<Inspected, String> {
    let r: RawInspected = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's decoded report couldn't be read: {e}"))?;
    let threads: Vec<Thread> = r
        .threads
        .unwrap_or_default()
        .into_iter()
        .enumerate()
        .map(|(i, t)| Thread {
            index: t.index.unwrap_or(i as i64),
            name: text(t.name),
            queue: text(t.queue),
            crashed: t.triggered.unwrap_or(false),
            frames: t
                .frames
                .unwrap_or_default()
                .into_iter()
                .map(text)
                .filter(|f| !f.is_empty())
                .collect(),
        })
        .collect();
    let crashed = r
        .faulting_thread
        .and_then(|i| threads.iter().find(|t| t.index == i))
        .or_else(|| threads.iter().find(|t| t.crashed))
        .cloned();
    Ok(Inspected {
        process: text(r.process),
        path: text(r.path),
        version: text(r.version),
        pid: r.pid.filter(|p| *p > 0),
        at: r.time.as_deref().and_then(parse_time),
        kind: text(r.kind),
        os_version: text(r.os_version),
        exception: text(r.exception),
        detail: text(r.exception_detail),
        termination: text(r.termination),
        resource: r.resource.map(|x| Resource {
            flavor: text(x.flavor),
            explanation: text(x.explanation),
            limit: text(x.limit),
            observed: text(x.observed),
        }),
        crashed,
        threads: threads.len(),
    })
}

/// The three listings, as the page runs them.
pub fn signatures_args() -> Vec<String> {
    ["crash", "signatures", "--json"].map(String::from).to_vec()
}

pub fn list_args() -> Vec<String> {
    ["crash", "list", "--json"].map(String::from).to_vec()
}

pub fn oom_args() -> Vec<String> {
    ["crash", "oom", "--json"].map(String::from).to_vec()
}

pub fn inspect_args(file: &str) -> Vec<String> {
    vec![
        "crash".into(),
        "inspect".into(),
        "--json".into(),
        file.to_owned(),
    ]
}

/// Reads all three. The signatures must read; the other two failing only note it.
pub fn read(spectra: &dyn Spectra, cancel: &AtomicBool) -> Result<Crashes, Failure> {
    let out = spectra.run_capped(&signatures_args(), DEADLINE, MAX_OUTPUT, cancel)?;
    let signatures = parse_signatures(&out).map_err(Failure::Failed)?;
    let mut c = Crashes {
        signatures,
        ..Crashes::default()
    };
    match spectra
        .run_capped(&list_args(), DEADLINE, MAX_OUTPUT, cancel)
        .and_then(|o| parse_reports(&o).map_err(Failure::Failed))
    {
        Ok(r) => c.reports = r,
        Err(Failure::Cancelled) => return Err(Failure::Cancelled),
        Err(Failure::Failed(why)) => c.reports_error = Some(why),
    }
    match spectra
        .run_capped(&oom_args(), DEADLINE, MAX_OUTPUT, cancel)
        .and_then(|o| parse_kills(&o).map_err(Failure::Failed))
    {
        Ok(k) => c.kills = k,
        Err(Failure::Cancelled) => return Err(Failure::Cancelled),
        Err(Failure::Failed(why)) => c.kills_error = Some(why),
    }
    Ok(c)
}

/// Decodes one report.
pub fn inspect(
    spectra: &dyn Spectra,
    file: &str,
    cancel: &AtomicBool,
) -> Result<Inspected, Failure> {
    let out = spectra.run_capped(
        &inspect_args(file),
        INSPECT_DEADLINE,
        INSPECT_OUTPUT,
        cancel,
    )?;
    parse_inspect(&out).map_err(Failure::Failed)
}

/// Which list a picked row is in.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Section {
    Signatures,
    Reports,
    Kills,
}

impl Section {
    pub fn id(self) -> &'static str {
        match self {
            Section::Signatures => "sig",
            Section::Reports => "rep",
            Section::Kills => "oom",
        }
    }
}

/// What a page job answered.
enum Answer {
    Read(Result<Crashes, Failure>),
    Opened(String, Result<Box<Inspected>, Failure>),
}

struct Landed {
    job: u64,
    answer: Answer,
}

struct Flight {
    job: u64,
    cancel: Arc<AtomicBool>,
}

/// The Crashes page's state.
pub struct CrashesPage {
    /// The last good reading and the wall clock when it landed.
    pub data: Option<(Crashes, i64)>,
    pub error: Option<String>,
    /// The picked row: its list and its report file.
    pub picked: Option<(Section, String)>,
    /// Decoded reports by file, each the answer to an "Open report" press.
    pub opened: HashMap<String, Result<Inspected, String>>,
    /// The row whose Open process / Sample now found the process had exited.
    pub gone: Option<(Section, String)>,
    /// The window the strip keeps the lists to, from and to in Unix seconds.
    pub range: Option<(i64, i64)>,
    /// The strip's bars a drag has covered so far: where it started and where it is.
    pub drag: Option<(usize, usize)>,
    /// The strip's bar under the pointer.
    pub hover: Option<usize>,
    pub spectra: Arc<dyn Spectra>,
    /// False in tests: a job runs on the calling thread.
    pub threaded: bool,
    reading: Option<Flight>,
    /// The file being decoded, and its job.
    opening: Option<(String, Flight)>,
    job: u64,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
}

impl Default for CrashesPage {
    fn default() -> CrashesPage {
        CrashesPage::new()
    }
}

impl CrashesPage {
    pub fn new() -> CrashesPage {
        let (tx, rx) = channel();
        CrashesPage {
            data: None,
            error: None,
            picked: None,
            opened: HashMap::new(),
            gone: None,
            range: None,
            drag: None,
            hover: None,
            spectra: Arc::new(crate::inspect::System),
            threaded: true,
            reading: None,
            opening: None,
            job: 0,
            tx,
            rx,
        }
    }

    pub fn reading(&self) -> bool {
        self.reading.is_some()
    }

    /// The report being decoded, if any.
    pub fn opening(&self) -> Option<&str> {
        self.opening.as_ref().map(|(f, _)| f.as_str())
    }

    /// A reading that never lands and runs nothing, so a test sees the page reading.
    #[cfg(test)]
    pub fn sample_flight(&mut self) {
        self.job += 1;
        self.reading = Some(Flight {
            job: self.job,
            cancel: Arc::new(AtomicBool::new(false)),
        });
    }

    /// Stops the reading and any decoding on their way; their answers are dropped.
    pub fn cancel(&mut self) {
        if let Some(f) = self.reading.take() {
            f.cancel.store(true, Ordering::SeqCst);
        }
        if let Some((_, f)) = self.opening.take() {
            f.cancel.store(true, Ordering::SeqCst);
        }
    }

    /// Whether opening the page should read: nothing read yet, or the reading is [`STALE`].
    pub fn wants_read(&self, wall: i64) -> bool {
        if self.reading.is_some() {
            return false;
        }
        match &self.data {
            None => true,
            Some((_, at)) => wall - at >= STALE,
        }
    }

    fn flight(&mut self) -> (u64, Arc<AtomicBool>) {
        self.job += 1;
        (self.job, Arc::new(AtomicBool::new(false)))
    }

    /// Runs `work` off the UI thread (on it in tests), sending its answer back as job `job`.
    fn spawn(
        &self,
        job: u64,
        name: &str,
        wake: Option<rune::Waker>,
        work: impl FnOnce() -> Answer + Send + 'static,
        failed: Answer,
    ) {
        let tx = self.tx.clone();
        if !self.threaded {
            let _ = tx.send(Landed {
                job,
                answer: work(),
            });
            return;
        }
        // Quit waits for this from now until it's done, even before Spectra starts.
        let hold = crate::run::pending();
        let thread_tx = tx.clone();
        let started = std::thread::Builder::new()
            .name(name.into())
            .spawn(move || {
                let answer = work();
                let _ = thread_tx.send(Landed { job, answer });
                drop(hold);
                if let Some(wake) = wake {
                    wake();
                }
            });
        if started.is_err() {
            let _ = tx.send(Landed {
                job,
                answer: failed,
            });
        }
    }
}

fn no_thread(e: &str) -> Failure {
    Failure::Failed(format!("Couldn't start a thread to {e}."))
}

impl App {
    /// Reads the three listings, stopping any reading already on its way.
    pub fn read_crashes(&mut self) {
        let wake = self.wake.clone();
        let c = &mut self.crashes;
        if let Some(f) = c.reading.take() {
            f.cancel.store(true, Ordering::SeqCst);
        }
        let (job, cancel) = c.flight();
        c.reading = Some(Flight {
            job,
            cancel: Arc::clone(&cancel),
        });
        let spectra = Arc::clone(&c.spectra);
        c.spawn(
            job,
            "spectra-crashes",
            wake,
            move || Answer::Read(read(spectra.as_ref(), &cancel)),
            Answer::Read(Err(no_thread("read the crash reports"))),
        );
    }

    /// Picks a row; picking it again leaves it picked.
    pub fn pick_crash(&mut self, section: Section, file: &str) {
        let row = (section, file.to_owned());
        if self.crashes.gone.as_ref() != Some(&row) {
            self.crashes.gone = None;
        }
        self.crashes.picked = Some(row);
    }

    /// Decodes the picked row's report, stopping any other decoding on its way.
    pub fn open_crash_report(&mut self) {
        let Some((_, file)) = self.crashes.picked.clone() else {
            return;
        };
        if file.is_empty() {
            return;
        }
        let wake = self.wake.clone();
        let c = &mut self.crashes;
        if let Some((_, f)) = c.opening.take() {
            f.cancel.store(true, Ordering::SeqCst);
        }
        let (job, cancel) = c.flight();
        c.opening = Some((
            file.clone(),
            Flight {
                job,
                cancel: Arc::clone(&cancel),
            },
        ));
        let spectra = Arc::clone(&c.spectra);
        let failed = Answer::Opened(file.clone(), Err(no_thread("open the report")));
        c.spawn(
            job,
            "spectra-crash-inspect",
            wake,
            move || {
                let r = inspect(spectra.as_ref(), &file, &cancel).map(Box::new);
                Answer::Opened(file, r)
            },
            failed,
        );
    }

    /// Lands finished jobs. True when one changed the page.
    pub fn land_crashes(&mut self) -> bool {
        let mut changed = false;
        while let Ok(l) = self.crashes.rx.try_recv() {
            let c = &mut self.crashes;
            match l.answer {
                Answer::Read(result) => {
                    if c.reading.as_ref().is_none_or(|f| f.job != l.job) {
                        continue;
                    }
                    c.reading = None;
                    match result {
                        Ok(data) => {
                            c.data = Some((data, (self.wall)()));
                            c.error = None;
                        }
                        Err(Failure::Cancelled) => {}
                        Err(Failure::Failed(why)) => c.error = Some(why),
                    }
                }
                Answer::Opened(file, result) => {
                    if c.opening.as_ref().is_none_or(|(_, f)| f.job != l.job) {
                        continue;
                    }
                    c.opening = None;
                    match result {
                        Ok(r) => {
                            c.opened.insert(file, Ok(*r));
                        }
                        Err(Failure::Cancelled) => {}
                        Err(Failure::Failed(why)) => {
                            c.opened.insert(file, Err(why));
                        }
                    }
                }
            }
            changed = true;
        }
        changed
    }

    /// Leaving the page stops what's on its way; opening it reads only when [`CrashesPage::wants_read`].
    pub fn crashes_nav(&mut self, from: Nav, to: Nav) {
        if from == Nav::Crashes && to != Nav::Crashes {
            self.crashes.cancel();
            self.crashes.range = None;
            self.crashes.drag = None;
        }
        if to == Nav::Crashes && from != Nav::Crashes && self.crashes.wants_read((self.wall)()) {
            self.read_crashes();
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::sync::Mutex;

    pub const SIGNATURES: &str = include_str!("../tests/fixtures/crashes/signatures.json");
    pub const SIGNATURES_MISSING: &str =
        include_str!("../tests/fixtures/crashes/signatures-missing.json");
    pub const LIST: &str = include_str!("../tests/fixtures/crashes/list.json");
    pub const OOM: &str = include_str!("../tests/fixtures/crashes/oom.json");
    pub const INSPECT: &str = include_str!("../tests/fixtures/crashes/inspect.json");
    pub const INSPECT_RESOURCE: &str =
        include_str!("../tests/fixtures/crashes/inspect-resource.json");
    pub const EMPTY: &str = include_str!("../tests/fixtures/crashes/empty.json");
    pub const NULL: &str = include_str!("../tests/fixtures/crashes/null.json");

    /// Spectra standing in with set answers by `crash` subcommand (and, for `inspect`, by file),
    /// recording what it was asked.
    pub struct Fake {
        pub replies: HashMap<String, Result<String, String>>,
        pub calls: Mutex<Vec<Vec<String>>>,
    }

    impl Fake {
        pub fn new(pairs: &[(&str, Result<&str, &str>)]) -> Fake {
            Fake {
                replies: pairs
                    .iter()
                    .map(|(k, v)| ((*k).to_owned(), v.map(str::to_owned).map_err(str::to_owned)))
                    .collect(),
                calls: Mutex::new(Vec::new()),
            }
        }

        /// Every listing answers from the fixtures; every report decodes as [`INSPECT`].
        pub fn ok() -> Fake {
            Fake::new(&[
                ("signatures", Ok(SIGNATURES)),
                ("list", Ok(LIST)),
                ("oom", Ok(OOM)),
                ("inspect", Ok(INSPECT)),
            ])
        }

        pub fn calls(&self) -> Vec<Vec<String>> {
            self.calls.lock().unwrap().clone()
        }
    }

    impl Spectra for Fake {
        fn run(
            &self,
            args: &[String],
            _: Duration,
            cancel: &AtomicBool,
        ) -> Result<String, Failure> {
            self.calls.lock().unwrap().push(args.to_vec());
            if cancel.load(Ordering::Relaxed) {
                return Err(Failure::Cancelled);
            }
            let sub = args.get(1).map_or("", String::as_str);
            let by_file = args.last().map(|f| format!("inspect {f}"));
            let reply = by_file
                .filter(|_| sub == "inspect")
                .and_then(|k| self.replies.get(&k))
                .or_else(|| self.replies.get(sub));
            match reply {
                Some(Ok(out)) => Ok(out.clone()),
                Some(Err(why)) => Err(Failure::Failed(why.clone())),
                None => Err(Failure::Failed(format!("no fake for {sub}"))),
            }
        }
    }

    #[test]
    fn report_times_read_with_their_offset() {
        // 06:48:10 at five hours behind UTC is 11:48:10Z.
        assert_eq!(
            parse_time("2026-11-20 06:48:10.00 -0500"),
            parse_rfc3339("2026-11-20T11:48:10Z")
        );
        assert_eq!(
            parse_time("2026-11-20 12:00:00.00 +0130"),
            parse_rfc3339("2026-11-20T10:30:00Z")
        );
        assert!(parse_time("2026-11-20T11:48:10Z").is_some());
        assert_eq!(parse_time("not a time"), None);
        assert_eq!(parse_time(""), None);
        assert_eq!(parse_time("2026-11-20 06:48:10.00 -05"), None);
    }

    #[test]
    fn signatures_lead_with_the_most_often_then_the_most_recent() {
        let sigs = parse_signatures(SIGNATURES).unwrap();
        let order: Vec<&str> = sigs.iter().map(|s| s.process.as_str()).collect();
        // Two at 14: syncbridged was seen later; two at 3: WidgetHostService later.
        assert_eq!(
            order,
            [
                "syncbridged",
                "searchindexerd",
                "Sketchpad",
                "WidgetHostService",
                "thumbnailagent",
                "notehelper"
            ]
        );
        let top = &sigs[0];
        assert_eq!(top.count, 14);
        assert_eq!(top.exception, "EXC_CRASH (SIGKILL)");
        assert_eq!(top.top_frame, "libsystem_kernel.dylib`mach_msg2_trap + 8");
        assert!(top.file.ends_with("syncbridged-2026-11-20-064810.ips"));
        assert!(top.first < top.last);
    }

    #[test]
    fn missing_and_null_fields_dont_fail() {
        let sigs = parse_signatures(SIGNATURES_MISSING).unwrap();
        // The empty object has no process and is left out.
        assert_eq!(sigs.len(), 2);
        let print = sigs.iter().find(|s| s.process == "printqueued").unwrap();
        assert_eq!(print.count, 1, "a null count is one report");
        assert_eq!(print.exception, "");
        assert!(print.last.is_some());
        let scan = sigs.iter().find(|s| s.process == "mediascand").unwrap();
        assert_eq!((scan.last, scan.first), (None, None));
        assert_eq!(scan.file, "");
        // Two reports outrank one, whatever the times.
        assert_eq!(sigs[0].process, "mediascand");

        let reports = parse_reports(LIST).unwrap();
        assert_eq!(reports.len(), 7);
        assert_eq!(reports[0].process, "syncbridged");
        let lost = reports.last().unwrap();
        assert_eq!((lost.process.as_str(), lost.at), ("lostreportd", None));
        let hang = reports.iter().find(|r| r.kind == "hang").unwrap();
        assert_eq!(hang.exception, "");

        let kills = parse_kills(OOM).unwrap();
        assert_eq!(kills.len(), 2);
        assert_eq!(
            (kills[0].limit.as_str(), kills[0].observed.as_str()),
            ("300", "512")
        );
        assert_eq!(kills[1].limit, "");

        // Spectra prints `null` for no memory kills at all.
        for json in [NULL, EMPTY] {
            assert!(parse_signatures(json).unwrap().is_empty());
            assert!(parse_reports(json).unwrap().is_empty());
            assert!(parse_kills(json).unwrap().is_empty());
        }
        assert!(
            parse_reports("{oops")
                .unwrap_err()
                .contains("crash reports")
        );
    }

    #[test]
    fn an_inspected_report_names_its_crashed_thread() {
        let r = parse_inspect(INSPECT).unwrap();
        assert_eq!(r.process, "WidgetHostService");
        assert_eq!(r.pid, Some(4821));
        assert_eq!(r.threads, 3);
        let t = r.crashed.unwrap();
        assert_eq!((t.index, t.name.as_str()), (2, "render"));
        assert_eq!(t.frames.len(), 12);
        assert!(t.frames[1].contains("TimelineCache.entry"));
        assert_eq!(r.termination, "Trace/BPT trap: 5");

        let m = parse_inspect(INSPECT_RESOURCE).unwrap();
        assert_eq!(m.crashed, None, "null threads: no crashed thread");
        assert_eq!(m.threads, 0);
        let res = m.resource.unwrap();
        assert_eq!((res.flavor.as_str(), res.limit.as_str()), ("MEMORY", "300"));
        assert_eq!(parse_inspect("{}").unwrap(), Inspected::default());
    }

    #[test]
    fn exceptions_read_in_plain_words() {
        assert_eq!(plain("EXC_CRASH (SIGKILL)"), Some("killed"));
        assert_eq!(plain("EXC_RESOURCE (SIGKILL)"), Some("went over a limit"));
        assert_eq!(plain("EXC_BAD_ACCESS (SIGSEGV)"), Some("bad memory access"));
        assert_eq!(plain("EXC_BAD_ACCESS (SIGBUS)"), Some("bad memory access"));
        assert_eq!(
            plain("EXC_BREAKPOINT (SIGTRAP)"),
            Some("stopped on a check")
        );
        assert_eq!(plain("EXC_BREAKPOINT"), Some("stopped on a check"));
        assert_eq!(plain("EXC_CRASH (SIGABRT)"), Some("aborted itself"));
        assert_eq!(plain("EXC_GUARD"), Some("broke a guarded resource"));
        assert_eq!(plain("EXC_SOMETHING_NEW"), None);
        assert_eq!(what("EXC_SOMETHING_NEW", "crash"), "EXC_SOMETHING_NEW");
        assert_eq!(what("", "hang"), "stopped responding");
        assert_eq!(what("", "crash report (bug_type 211)"), "crashed");
    }

    #[test]
    fn a_failed_listing_beside_good_signatures_only_notes_it() {
        let fake = Fake::new(&[
            ("signatures", Ok(SIGNATURES)),
            ("list", Err("list reports: permission denied")),
            ("oom", Ok(NULL)),
        ]);
        let c = read(&fake, &AtomicBool::new(false)).unwrap();
        assert_eq!(c.signatures.len(), 6);
        assert_eq!(
            c.reports_error.as_deref(),
            Some("list reports: permission denied")
        );
        assert!(c.kills.is_empty() && c.kills_error.is_none());
        assert_eq!(fake.calls(), [signatures_args(), list_args(), oom_args()]);

        let broken = Fake::new(&[(
            "signatures",
            Err("spectra took longer than 45 s and was stopped"),
        )]);
        assert_eq!(
            read(&broken, &AtomicBool::new(false)),
            Err(Failure::Failed(
                "spectra took longer than 45 s and was stopped".into()
            ))
        );
        assert_eq!(
            broken.calls().len(),
            1,
            "nothing more runs after the signatures fail"
        );
    }
}
