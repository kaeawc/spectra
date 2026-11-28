//! Where disk space went: how much room each disk has left (from the Machine page's own reading,
//! never read twice), the biggest folders in ~/Library and inside any folder the person picks
//! (`spectra du`, only on a press: it walks every file and can take minutes), and the Time
//! Machine snapshots kept on this Mac (`spectra timemachine`, read when the page opens).
//!
//! Read-only: nothing here deletes, moves or writes anything.

use std::collections::HashMap;
use std::process::Command;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::Value;

use crate::app::App;
use crate::run::{Ended, run_until};
use crate::system::{VOLUME_WARN, Volume};

/// How long sizing one folder may take. A full ~/Library took over five minutes on a busy Mac;
/// past this the walk is stopped and the page suggests a smaller folder.
pub const DU_DEADLINE: Duration = Duration::from_secs(15 * 60);
/// How long the Time Machine read may take: it answers in well under a second.
pub const TM_DEADLINE: Duration = Duration::from_secs(30);
/// The most a command may print. Both print a few KB.
const MAX_OUTPUT: usize = 4 << 20;
/// Folders and files listed per measurement.
pub const TOP: usize = 15;
/// A disk with less than this free is low: macOS needs room for updates, swap and caches.
pub const LOW_FREE: f64 = 20.0 * 1024.0 * 1024.0 * 1024.0;

/// What a command printed and how it exited.
#[derive(Clone, Debug, Default)]
pub struct Ran {
    pub code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
}

impl Ran {
    /// Spectra's first line on stderr, if it said anything.
    fn said(&self) -> Option<&str> {
        self.stderr.lines().map(str::trim).find(|l| !l.is_empty())
    }
}

/// Runs `spectra <args>` until it ends, the deadline passes or `cancel` is set. Tests swap in a
/// fake that records the args and runs nothing.
pub trait Runner: Send + Sync {
    fn run(&self, args: &[String], deadline: Duration, cancel: &AtomicBool) -> Result<Ran, String>;
}

/// The real Spectra, from `SPECTRA_BIN` or `PATH`, with a deadline and an output cap.
pub struct SpectraBin;

impl Runner for SpectraBin {
    fn run(&self, args: &[String], deadline: Duration, cancel: &AtomicBool) -> Result<Ran, String> {
        let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
        let mut cmd = Command::new(&bin);
        cmd.args(args);
        let out = run_until(cmd, deadline, MAX_OUTPUT, cancel).map_err(|e| match e {
            Ended::Cancelled => format!("{bin} was stopped"),
            Ended::Failed(e) => crate::procs::run_failure(&bin, e),
        })?;
        Ok(Ran {
            code: out.status.code(),
            stdout: String::from_utf8_lossy(&out.stdout).into_owned(),
            stderr: String::from_utf8_lossy(&out.stderr).into_owned(),
        })
    }
}

/// `spectra du` for `path`: one level of folders, the biggest files, and `--` before the path
/// so a folder named like a flag is still read as a path.
pub fn du_args(path: &str) -> Vec<String> {
    let top = TOP.to_string();
    ["du", "--json", "--depth", "1", "--top", &top, "--", path]
        .map(String::from)
        .to_vec()
}

pub fn tm_args() -> Vec<String> {
    ["timemachine", "--json"].map(String::from).to_vec()
}

/// A folder or file and the space it takes on disk.
#[derive(Clone, Debug, PartialEq)]
pub struct Entry {
    pub path: String,
    pub bytes: f64,
}

/// What `spectra du` found under one folder.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Du {
    pub root: String,
    pub total: f64,
    pub files: u64,
    pub dirs: u64,
    /// The biggest folders one level down, biggest first.
    pub dirs_top: Vec<Entry>,
    /// The biggest files anywhere under it, biggest first.
    pub files_top: Vec<Entry>,
    /// Folders Spectra couldn't open (macOS privacy protection, mostly).
    pub unreadable: usize,
    /// Other disks mounted inside it, which weren't walked.
    pub skipped: usize,
    /// The walk stopped early: the totals are a floor.
    pub partial: bool,
}

fn num(v: &Value, key: &str) -> f64 {
    v.get(key)
        .and_then(|x| {
            x.as_f64()
                .or_else(|| x.as_str().and_then(|s| s.trim().parse().ok()))
        })
        .filter(|x| x.is_finite() && *x >= 0.0)
        .unwrap_or(0.0)
}

fn st(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

fn list<'a>(v: &'a Value, key: &str) -> &'a [Value] {
    v.get(key)
        .and_then(Value::as_array)
        .map_or(&[], Vec::as_slice)
}

fn object(json: &str, what: &str) -> Result<Value, String> {
    let v: Value = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's {what} output didn't read as JSON: {e}"))?;
    if v.is_object() {
        Ok(v)
    } else {
        Err(format!("Spectra's {what} output wasn't a JSON object"))
    }
}

fn entries(v: &Value, key: &str) -> Vec<Entry> {
    let mut out: Vec<Entry> = list(v, key)
        .iter()
        .map(|e| Entry {
            path: st(e, "path"),
            bytes: num(e, "allocated_bytes"),
        })
        .filter(|e| !e.path.is_empty())
        .collect();
    out.sort_by(|a, b| b.bytes.total_cmp(&a.bytes));
    out
}

/// Reads `spectra du --json`. Missing or null fields read as nothing, never as a failure.
pub fn parse_du(json: &str) -> Result<Du, String> {
    let v = object(json, "du")?;
    Ok(Du {
        root: st(&v, "root"),
        total: num(&v, "total_bytes"),
        files: num(&v, "file_count") as u64,
        dirs: num(&v, "dir_count") as u64,
        dirs_top: entries(&v, "top_dirs"),
        files_top: entries(&v, "top_files"),
        unreadable: list(&v, "unreadable").len(),
        skipped: list(&v, "skipped").len(),
        partial: v.get("partial").and_then(Value::as_bool).unwrap_or(false),
    })
}

/// A local Time Machine snapshot: a copy of the disk as it was, kept on the disk itself.
#[derive(Clone, Debug, PartialEq)]
pub struct Snapshot {
    pub name: String,
    /// `2026-11-21 09:30 UTC`, or empty when Spectra gave no date.
    pub when: String,
    pub volume: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct TimeMachine {
    pub snapshots: Vec<Snapshot>,
    /// A backup is being written now, and how far along it is (0 to 100) when Spectra says.
    pub running: bool,
    pub percent: Option<f64>,
    pub auto_backup: bool,
    pub destinations: usize,
}

/// `2026-11-21T09:30:12Z` as `2026-11-21 09:30 UTC`; anything else as it came.
fn when(date: &str) -> String {
    let d = date.trim();
    if d.is_empty() || d.starts_with("0001-") {
        return String::new();
    }
    match (d.get(..10), d.get(11..16)) {
        (Some(day), Some(time)) if d.as_bytes().get(10) == Some(&b'T') => {
            let utc = if d.ends_with('Z') { " UTC" } else { "" };
            format!("{day} {time}{utc}")
        }
        _ => d.to_owned(),
    }
}

/// Reads `spectra timemachine --json`. Missing or null fields read as nothing.
pub fn parse_timemachine(json: &str) -> Result<TimeMachine, String> {
    let v = object(json, "timemachine")?;
    let status = v.get("status").cloned().unwrap_or(Value::Null);
    let running = status
        .get("running")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    // Spectra gives a fraction (0.42) while a backup runs and -1 when it doesn't know.
    let percent = status
        .get("percent")
        .and_then(Value::as_f64)
        .filter(|p| (0.0..=1.0).contains(p))
        .map(|p| p * 100.0);
    let snapshots = list(&v, "local_snapshots")
        .iter()
        .map(|s| Snapshot {
            name: st(s, "name"),
            when: when(&st(s, "date")),
            volume: st(s, "volume"),
        })
        .filter(|s| !s.name.is_empty())
        .collect();
    Ok(TimeMachine {
        snapshots,
        running,
        percent: percent.filter(|_| running),
        auto_backup: v
            .get("auto_backup_enabled")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        destinations: list(&v, "destinations").len(),
    })
}

/// Whether a disk is low on room: under [`LOW_FREE`] free, or as full as the Machine page's
/// full-disk warning.
pub fn low_free(v: &Volume) -> bool {
    v.avail < LOW_FREE || v.used_pct() >= VOLUME_WARN
}

/// Why a command printed nothing to read.
fn failure(what: &str, ran: &Ran) -> String {
    let said = ran.said().unwrap_or("");
    let lower = said.to_ascii_lowercase();
    if lower.contains("unknown command") {
        return format!("This Spectra has no `spectra {what}`. Update Spectra to use it.");
    }
    match (said.is_empty(), ran.code) {
        (false, _) => said.to_owned(),
        (true, Some(c)) => format!("spectra {what} stopped with exit status {c}"),
        (true, None) => format!("spectra {what} was stopped"),
    }
}

fn measure(runner: &dyn Runner, path: &str, cancel: &AtomicBool) -> Result<Du, String> {
    let ran = runner.run(&du_args(path), DU_DEADLINE, cancel)?;
    if ran.code != Some(0) || ran.stdout.trim().is_empty() {
        return Err(failure("du", &ran));
    }
    parse_du(&ran.stdout)
}

fn read_tm(runner: &dyn Runner, cancel: &AtomicBool) -> Result<TimeMachine, String> {
    let ran = runner.run(&tm_args(), TM_DEADLINE, cancel)?;
    if ran.code != Some(0) || ran.stdout.trim().is_empty() {
        return Err(failure("timemachine", &ran));
    }
    parse_timemachine(&ran.stdout)
}

pub enum Landed {
    Measured {
        job: u64,
        path: String,
        result: Result<Du, String>,
    },
    TimeMachine {
        job: u64,
        result: Result<TimeMachine, String>,
    },
}

/// A finished measurement and when it landed, on the window clock.
#[derive(Clone, Debug)]
pub struct Measured {
    pub du: Du,
    pub at: f64,
}

/// The measurement on its way: one at a time, since each walks a lot of disk.
pub struct Measuring {
    pub path: String,
    pub started: f64,
    job: u64,
    cancel: Arc<AtomicBool>,
}

impl Measuring {
    /// A measurement shown as on its way, for the sample scenes: nothing runs.
    pub fn shown(path: &str, started: f64) -> Measuring {
        Measuring {
            path: path.to_owned(),
            started,
            job: 0,
            cancel: Arc::new(AtomicBool::new(false)),
        }
    }
}

/// The Storage page's state.
pub struct Storage {
    pub runner: Arc<dyn Runner>,
    /// Off in tests: work runs on the calling thread and lands on the next `land_storage`.
    pub threaded: bool,
    /// The person's home folder; ~/Library is measured under it.
    pub home: String,
    /// The last result for each folder measured, kept until measured again.
    pub results: HashMap<String, Measured>,
    /// Why the last measurement of a folder failed; its result from before stays.
    pub errors: HashMap<String, String>,
    pub measuring: Option<Measuring>,
    /// The folders looked inside, the open one last; empty when none is open.
    pub trail: Vec<String>,
    pub tm: Option<TimeMachine>,
    pub tm_reading: bool,
    pub tm_error: Option<String>,
    tm_cancel: Arc<AtomicBool>,
    tm_job: u64,
    /// The page opened while the window was hidden: Time Machine is read once it's shown.
    tm_wanted: bool,
    job: u64,
    /// The "started … ago" last shown while measuring, so a tick redraws only when it changes.
    shown: Option<String>,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
}

impl Storage {
    pub fn new() -> Storage {
        let (tx, rx) = channel();
        Storage {
            runner: Arc::new(SpectraBin),
            threaded: true,
            home: std::env::var("HOME").unwrap_or_default(),
            results: HashMap::new(),
            errors: HashMap::new(),
            measuring: None,
            trail: Vec::new(),
            tm: None,
            tm_reading: false,
            tm_error: None,
            tm_cancel: Arc::new(AtomicBool::new(false)),
            tm_job: 0,
            tm_wanted: false,
            job: 0,
            shown: None,
            tx,
            rx,
        }
    }

    pub fn library(&self) -> String {
        format!("{}/Library", self.home.trim_end_matches('/'))
    }

    /// `path` with the home folder written as `~`.
    pub fn short(&self, path: &str) -> String {
        let home = self.home.trim_end_matches('/');
        if home.is_empty() {
            return path.to_owned();
        }
        match path.strip_prefix(home) {
            Some("") => "~".to_owned(),
            Some(rest) if rest.starts_with('/') => format!("~{rest}"),
            _ => path.to_owned(),
        }
    }

    /// The folder being looked inside, if one is open.
    pub fn inside(&self) -> Option<&str> {
        self.trail.last().map(String::as_str)
    }

    pub fn busy(&self) -> bool {
        self.measuring.is_some()
    }

    pub fn measuring_path(&self) -> Option<&str> {
        self.measuring.as_ref().map(|m| m.path.as_str())
    }
}

impl Default for Storage {
    fn default() -> Storage {
        Storage::new()
    }
}

/// Runs `work` on a thread of its own (or here, in tests), turning a panic into `failed`.
fn submit(
    st: &Storage,
    wake: Option<rune::Waker>,
    work: impl FnOnce() -> Landed + Send + 'static,
    failed: Landed,
) {
    let tx = st.tx.clone();
    if !st.threaded {
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
        .name("spectra-storage".into())
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

impl App {
    /// The page opened: reads the Time Machine snapshots (quick), or once the window is shown
    /// when it's hidden. Nothing is measured until a press asks for it.
    pub fn open_storage(&mut self) {
        let shown = self.window_shown();
        let st = &mut self.storage;
        if st.tm_reading {
            return;
        }
        if !shown {
            st.tm_wanted = true;
            return;
        }
        st.tm_wanted = false;
        st.tm_reading = true;
        st.tm_job += 1;
        let job = st.tm_job;
        st.tm_cancel = Arc::new(AtomicBool::new(false));
        let cancel = Arc::clone(&st.tm_cancel);
        let runner = Arc::clone(&st.runner);
        let failed = Landed::TimeMachine {
            job,
            result: Err(
                "Couldn't ask Spectra about Time Machine: the reading thread failed.".into(),
            ),
        };
        submit(
            st,
            self.wake.clone(),
            move || Landed::TimeMachine {
                job,
                result: read_tm(runner.as_ref(), &cancel),
            },
            failed,
        );
    }

    /// Measures `path` with `spectra du`, unless a measurement is already on its way.
    pub fn measure_storage(&mut self, path: &str) {
        let now = self.now;
        let st = &mut self.storage;
        if st.measuring.is_some() || path.is_empty() {
            return;
        }
        st.job += 1;
        let job = st.job;
        let cancel = Arc::new(AtomicBool::new(false));
        st.measuring = Some(Measuring {
            path: path.to_owned(),
            started: now,
            job,
            cancel: Arc::clone(&cancel),
        });
        let runner = Arc::clone(&st.runner);
        let owned = path.to_owned();
        let failed = Landed::Measured {
            job,
            path: owned.clone(),
            result: Err("Couldn't ask Spectra to measure it: the measuring thread failed.".into()),
        };
        submit(
            st,
            self.wake.clone(),
            move || Landed::Measured {
                job,
                result: measure(runner.as_ref(), &owned, &cancel),
                path: owned,
            },
            failed,
        );
    }

    /// Opens `path`'s biggest folders and files, measuring it unless it was measured before.
    /// From the top of the page the trail starts over; from inside a folder it goes one deeper.
    pub fn look_inside(&mut self, path: &str, deeper: bool) {
        let st = &mut self.storage;
        if !deeper {
            st.trail.clear();
        }
        if st.inside() != Some(path) {
            st.trail.push(path.to_owned());
        }
        if !st.results.contains_key(path) {
            self.measure_storage(path);
        }
    }

    /// Back out of the open folder, to the one before it or to the top of the page.
    pub fn storage_back(&mut self) {
        let Some(left) = self.storage.trail.pop() else {
            return;
        };
        // A measurement of the folder just left, or of one inside it, has nothing on screen to
        // show it: stop it rather than leave the page busy with no Stop.
        let inside_it = |p: &str| {
            p == left
                || p.strip_prefix(left.as_str())
                    .is_some_and(|r| r.starts_with('/'))
        };
        if self.storage.measuring_path().is_some_and(inside_it) {
            self.stop_measuring();
        }
    }

    /// Stops what's on its way: on leaving the page and on quit. Results from before stay.
    pub fn cancel_storage(&mut self) {
        let st = &mut self.storage;
        if let Some(m) = st.measuring.take() {
            m.cancel.store(true, Ordering::SeqCst);
        }
        st.tm_cancel.store(true, Ordering::SeqCst);
        st.tm_reading = false;
        st.tm_wanted = false;
    }

    /// Stops the measurement on its way, keeping what was measured before.
    pub fn stop_measuring(&mut self) {
        if let Some(m) = self.storage.measuring.take() {
            m.cancel.store(true, Ordering::SeqCst);
        }
    }

    /// Lands what finished, and says whether anything shown changed: a landing, or the
    /// "started … ago" of a measurement on its way now reading differently. While the window is
    /// hidden a measurement may finish, but nothing new starts and the age isn't redrawn; a
    /// Time Machine read the page asked for while hidden starts once it's shown.
    pub fn tick_storage(&mut self) -> bool {
        let mut changed = self.land_storage();
        if !self.window_shown() {
            self.storage.shown = None;
            return changed;
        }
        if self.storage.tm_wanted {
            self.open_storage();
            changed = true;
        }
        let age = self
            .storage
            .measuring
            .as_ref()
            .map(|m| crate::app::fmt_ago(self.now - m.started));
        if age != self.storage.shown {
            self.storage.shown = age;
            changed = true;
        }
        changed
    }

    /// Lands what the Storage workers finished. True when anything landed.
    pub fn land_storage(&mut self) -> bool {
        let mut landed = false;
        let now = self.now;
        while let Ok(l) = self.storage.rx.try_recv() {
            landed = true;
            let st = &mut self.storage;
            match l {
                Landed::Measured { job, path, result } => {
                    if st.measuring.as_ref().is_none_or(|m| m.job != job) {
                        continue;
                    }
                    st.measuring = None;
                    match result {
                        Ok(du) => {
                            st.errors.remove(&path);
                            st.results.insert(path, Measured { du, at: now });
                        }
                        Err(why) => {
                            st.errors.insert(path, why);
                        }
                    }
                }
                Landed::TimeMachine { job, result } => {
                    if !st.tm_reading || job != st.tm_job {
                        continue;
                    }
                    st.tm_reading = false;
                    match result {
                        Ok(tm) => {
                            st.tm = Some(tm);
                            st.tm_error = None;
                        }
                        Err(why) => st.tm_error = Some(why),
                    }
                }
            }
        }
        landed
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    use crate::sample_storage::{DU_LIBRARY, DU_LOGS, STORAGE, TM, TM_SNAPSHOTS};

    /// Records every command and answers from a script: `du` with `du`, `timemachine` with
    /// `tm`.
    pub struct Fake {
        pub calls: Mutex<Vec<Vec<String>>>,
        pub du: Result<Ran, String>,
        pub tm: Result<Ran, String>,
    }

    pub fn ok(stdout: &str) -> Result<Ran, String> {
        Ok(Ran {
            code: Some(0),
            stdout: stdout.into(),
            stderr: String::new(),
        })
    }

    impl Fake {
        pub fn new() -> Fake {
            Fake {
                calls: Mutex::new(Vec::new()),
                du: ok(DU_LIBRARY),
                tm: ok(TM_SNAPSHOTS),
            }
        }

        pub fn calls(&self) -> Vec<Vec<String>> {
            self.calls.lock().unwrap().clone()
        }

        pub fn dus(&self) -> Vec<Vec<String>> {
            self.calls().into_iter().filter(|c| c[0] == "du").collect()
        }
    }

    impl Runner for Fake {
        fn run(&self, args: &[String], _: Duration, _: &AtomicBool) -> Result<Ran, String> {
            self.calls.lock().unwrap().push(args.to_vec());
            match args[0].as_str() {
                "du" => self.du.clone(),
                _ => self.tm.clone(),
            }
        }
    }

    fn vol(used: f64, avail: f64) -> Volume {
        Volume {
            mount: "/Volumes/Disk".into(),
            device: String::new(),
            fs_type: "apfs".into(),
            read_only: false,
            total: used + avail,
            used,
            avail,
        }
    }

    const GB: f64 = 1024.0 * 1024.0 * 1024.0;

    #[test]
    fn a_real_du_reads_its_folders_files_and_what_it_couldnt_open() {
        let du = parse_du(DU_LIBRARY).unwrap();
        assert_eq!(du.root, "/Users/me/Library");
        assert_eq!(du.total, 412316860416.0);
        assert_eq!(du.files, 2841977);
        assert_eq!(du.dirs_top.len(), 7);
        assert_eq!(du.dirs_top[0].path, "/Users/me/Library/Developer");
        assert!(
            du.dirs_top.windows(2).all(|w| w[0].bytes >= w[1].bytes),
            "biggest first"
        );
        assert_eq!(du.files_top.len(), 2);
        assert_eq!(du.unreadable, 1);
        assert_eq!(du.skipped, 0);
        assert!(!du.partial);
        assert_eq!(parse_du(DU_LOGS).unwrap().dirs_top.len(), 2);
    }

    #[test]
    fn missing_and_null_fields_never_fail_a_read() {
        let du = parse_du(r#"{"root": null, "top_dirs": [{"path": "/a"}, {"allocated_bytes": 5}, null], "top_files": null, "partial": true}"#).unwrap();
        assert_eq!(du.root, "");
        assert_eq!(du.total, 0.0);
        assert_eq!(
            du.dirs_top,
            vec![Entry {
                path: "/a".into(),
                bytes: 0.0
            }]
        );
        assert!(du.files_top.is_empty());
        assert!(du.partial);
        assert_eq!(parse_du("{}").unwrap(), Du::default());
        assert!(parse_du("[]").is_err());
        assert!(parse_du("not json").is_err());

        let tm = parse_timemachine(TM_SNAPSHOTS).unwrap();
        assert_eq!(tm.snapshots.len(), 3);
        assert_eq!(tm.snapshots[0].when, "2026-11-21 09:30 UTC");
        assert_eq!(tm.snapshots[0].volume, "/");
        assert_eq!(tm.snapshots[1].when, "", "no date");
        assert_eq!(tm.snapshots[2].volume, "", "a null volume");
        assert!(tm.running);
        assert_eq!(tm.percent, Some(42.0));
        assert!(tm.auto_backup);
        assert_eq!(tm.destinations, 1);

        let idle = parse_timemachine(TM).unwrap();
        assert!(idle.snapshots.is_empty());
        assert!(!idle.running);
        assert_eq!(idle.percent, None, "-1 is not a percentage");
        assert_eq!(parse_timemachine("{}").unwrap(), TimeMachine::default());
        assert_eq!(
            parse_timemachine(r#"{"status": null, "local_snapshots": null}"#).unwrap(),
            TimeMachine::default()
        );
    }

    #[test]
    fn the_volumes_come_from_the_machine_pages_reading() {
        let vols = crate::system::parse_storage(STORAGE).unwrap();
        let shown: Vec<&Volume> = vols.iter().filter(|v| v.is_storage()).collect();
        assert_eq!(shown.len(), 1, "only the data volume: {shown:?}");
        assert_eq!(shown[0].label(), "Startup disk");
        assert!(low_free(shown[0]), "92% used is low");
    }

    #[test]
    fn a_disk_is_low_under_20_gb_free_or_once_the_machine_page_calls_it_full() {
        assert!(!low_free(&vol(100.0 * GB, 100.0 * GB)));
        assert!(low_free(&vol(10.0 * GB, 19.9 * GB)), "under 20 GB free");
        assert!(!low_free(&vol(10.0 * GB, 20.0 * GB)), "20 GB is enough");
        assert!(low_free(&vol(900.0 * GB, 100.0 * GB)), "90% used");
        assert!(!low_free(&vol(890.0 * GB, 110.0 * GB)), "89% used");
    }

    #[test]
    fn du_ends_its_flags_before_the_path() {
        assert_eq!(
            du_args("/Users/me/-rf"),
            [
                "du",
                "--json",
                "--depth",
                "1",
                "--top",
                "15",
                "--",
                "/Users/me/-rf"
            ]
        );
        let args = du_args("--help");
        assert_eq!(args[args.len() - 2], "--");
        assert_eq!(
            args[args.len() - 1],
            "--help",
            "a path that looks like a flag stays a path"
        );
        assert_eq!(tm_args(), ["timemachine", "--json"]);
    }

    #[test]
    fn a_failed_measurement_says_why() {
        let fake = Fake {
            du: Ok(Ran {
                code: Some(1),
                stdout: String::new(),
                stderr: "du: stat /nope: no such file or directory\n".into(),
            }),
            ..Fake::new()
        };
        let never = AtomicBool::new(false);
        assert_eq!(
            measure(&fake, "/nope", &never).unwrap_err(),
            "du: stat /nope: no such file or directory"
        );
        let old = Fake {
            du: Ok(Ran {
                code: Some(2),
                stdout: String::new(),
                stderr: "spectra: unknown command \"du\"\n".into(),
            }),
            ..Fake::new()
        };
        assert!(
            measure(&old, "/a", &never)
                .unwrap_err()
                .contains("Update Spectra")
        );
        let silent = Fake {
            tm: Ok(Ran {
                code: None,
                ..Ran::default()
            }),
            ..Fake::new()
        };
        assert_eq!(
            read_tm(&silent, &never).unwrap_err(),
            "spectra timemachine was stopped"
        );
    }

    fn app(fake: Arc<Fake>) -> App {
        let mut s = crate::app::init();
        s.storage.runner = fake;
        s.storage.threaded = false;
        s.storage.home = "/Users/me".into();
        s
    }

    #[test]
    fn one_measurement_at_a_time_kept_with_when_it_landed() {
        let fake = Arc::new(Fake::new());
        let mut s = app(fake.clone());
        s.now = 100.0;
        s.measure_storage("/Users/me/Library");
        assert!(s.storage.busy());
        s.measure_storage("/Users/me");
        assert_eq!(fake.dus().len(), 1, "the second waits for the first");
        assert!(s.land_storage());
        assert!(!s.storage.busy());
        let m = &s.storage.results["/Users/me/Library"];
        assert_eq!(m.at, 100.0);
        assert_eq!(m.du.dirs_top.len(), 7);
    }

    #[test]
    fn looking_inside_reuses_a_measurement_and_back_walks_the_trail() {
        let fake = Arc::new(Fake::new());
        let mut s = app(fake.clone());
        s.look_inside("/Users/me/Library/Developer", false);
        s.land_storage();
        s.look_inside("/Users/me/Library/Developer/Xcode", true);
        s.land_storage();
        assert_eq!(s.storage.trail.len(), 2);
        assert_eq!(fake.dus().len(), 2);
        s.storage_back();
        assert_eq!(s.storage.inside(), Some("/Users/me/Library/Developer"));
        s.look_inside("/Users/me/Library/Developer/Xcode", true);
        assert_eq!(fake.dus().len(), 2, "measured before: not measured again");
        s.look_inside("/Users/me/Library/Caches", false);
        assert_eq!(
            s.storage.trail,
            ["/Users/me/Library/Caches"],
            "from the top it starts over"
        );
        s.storage_back();
        assert_eq!(s.storage.inside(), None);
    }

    #[test]
    fn leaving_the_page_stops_the_measurement_and_drops_its_answer() {
        let fake = Arc::new(Fake::new());
        let mut s = app(fake);
        s.show(crate::groups::Nav::Storage);
        s.measure_storage("/Users/me/Library");
        let cancel = Arc::clone(&s.storage.measuring.as_ref().unwrap().cancel);
        s.show(crate::groups::Nav::Overview);
        assert!(cancel.load(Ordering::SeqCst), "the walk is killed");
        assert!(!s.storage.busy());
        s.land_storage();
        assert!(
            s.storage.results.is_empty(),
            "a stopped walk's answer is dropped"
        );
        assert!(s.storage.tm.is_none(), "so is the Time Machine read's");
    }

    #[test]
    fn back_stops_a_measurement_of_the_folder_left_or_inside_it() {
        let fake = Arc::new(Fake::new());
        let mut s = app(fake);
        s.storage.trail = vec!["/Users/me".into()];
        s.storage.measuring = Some(Measuring::shown("/Users/me", 0.0));
        let cancel = Arc::clone(&s.storage.measuring.as_ref().unwrap().cancel);
        s.storage_back();
        assert!(!s.storage.busy());
        assert!(cancel.load(Ordering::SeqCst), "the walk is killed");
        // Backing out of a deeper folder leaves a measurement of its parent running.
        s.storage.trail = vec!["/Users/me".into(), "/Users/me/Library".into()];
        s.storage.measuring = Some(Measuring::shown("/Users/me", 0.0));
        s.storage_back();
        assert!(s.storage.busy());
    }

    #[test]
    fn the_tick_redraws_only_when_the_age_shown_changes() {
        let fake = Arc::new(Fake::new());
        let mut s = app(fake);
        s.now = 100.0;
        s.storage.measuring = Some(Measuring::shown("/Users/me/Library", 100.0));
        assert!(s.tick_storage(), "first look at the age");
        assert!(!s.tick_storage(), "same string");
        s.now = 101.0;
        assert!(s.tick_storage(), "a second later it reads differently");
        s.now = 101.2;
        assert!(!s.tick_storage());
    }

    #[test]
    fn while_hidden_a_walk_may_finish_but_nothing_new_starts_and_the_age_rests() {
        let fake = Arc::new(Fake::new());
        let mut s = app(fake.clone());
        s.now = 100.0;
        s.measure_storage("/Users/me/Library");
        s.visible = false;
        // The page opening while hidden waits to read Time Machine.
        s.show(crate::groups::Nav::Storage);
        assert!(s.tick_storage(), "the walk on its way lands");
        assert!(s.storage.results.contains_key("/Users/me/Library"));
        s.storage.measuring = Some(Measuring::shown("/Users/me", 100.0));
        for t in 101..110 {
            s.now = f64::from(t);
            assert!(!s.tick_storage(), "the age isn't redrawn while hidden");
        }
        assert_eq!(
            fake.calls().iter().filter(|c| c[0] != "du").count(),
            0,
            "Time Machine isn't read while hidden"
        );
        // Shown again, Time Machine is read and the age is drawn.
        s.visible = true;
        assert!(s.tick_storage());
        assert_eq!(fake.calls().iter().filter(|c| c[0] != "du").count(), 1);
        s.land_storage();
        assert!(s.storage.tm.is_some());
        s.tick_storage();
        assert_eq!(
            fake.calls().iter().filter(|c| c[0] != "du").count(),
            1,
            "read once"
        );
    }

    #[test]
    fn a_failed_remeasure_keeps_the_sizes_from_before() {
        let mut fake = Fake::new();
        let mut s = app(Arc::new(Fake::new()));
        s.measure_storage("/a");
        s.land_storage();
        fake.du = Err("spectra took longer than 900 s and was stopped".into());
        s.storage.runner = Arc::new(fake);
        s.measure_storage("/a");
        s.land_storage();
        assert!(s.storage.results.contains_key("/a"));
        assert!(s.storage.errors["/a"].contains("longer than"));
    }

    #[test]
    fn home_is_written_as_a_tilde() {
        let mut st = Storage::new();
        st.home = "/Users/me".into();
        assert_eq!(st.short("/Users/me"), "~");
        assert_eq!(st.short("/Users/me/Library"), "~/Library");
        assert_eq!(st.short("/Users/meg/Library"), "/Users/meg/Library");
        assert_eq!(st.library(), "/Users/me/Library");
    }
}
