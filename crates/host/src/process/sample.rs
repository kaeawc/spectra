//! Bounded call-stack captures with macOS `sample(1)` (Go's `sample.go`).

use std::fmt;
use std::io;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use serde::Serialize;
use spectra_base::clock::Clock;
use spectra_base::fs::{Fs, OsFs};
use spectra_base::gojson::{GoDuration, GoTime};
use spectra_base::hostos::{Kind, Unsupported};
use spectra_base::proc::{Cmd, Runner};

/// Runs a command and returns its stdout, with an error when it failed; what a failing command
/// printed is kept (Go's `CommandRunner` over `exec.Cmd.Output`).
pub trait CommandRunner {
    fn run(&self, name: &str, args: &[String], timeout: Duration) -> (Vec<u8>, Option<String>);
}

/// A [`CommandRunner`] over a [`Runner`] (Go's `ExecRunner`).
pub struct RunnerCommands<'a>(pub &'a dyn Runner);

impl CommandRunner for RunnerCommands<'_> {
    fn run(&self, name: &str, args: &[String], timeout: Duration) -> (Vec<u8>, Option<String>) {
        match self.0.run(&Cmd::new(name, args.iter().cloned()).timeout(timeout)) {
            Ok(o) if o.exit_code == 0 => (o.stdout, None),
            Ok(o) => (o.stdout, Some(format!("exit status {}", o.exit_code))),
            Err(e) => (Vec::new(), Some(e.to_string())),
        }
    }
}

/// Stores raw sample output (Go's `SampleStore`).
pub trait SampleStore {
    fn put_sample(&self, sample: &SampleResult) -> Result<(), String>;
}

/// A bounded capture (Go's `SampleOptions`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SampleOptions {
    pub pid: i64,
    pub duration: GoDuration,
    pub interval_ms: i64,
    pub store: bool,
    pub taken_at: Option<GoTime>,
}

/// One capture's output and metadata (Go's `SampleResult`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct SampleResult {
    pub pid: i64,
    pub taken_at: GoTime,
    pub duration_ms: i64,
    pub interval_ms: i64,
    pub output: String,
}

/// Why a capture failed; the texts are Go's.
#[derive(Debug)]
pub enum SampleError {
    Unsupported(Unsupported),
    Message(String),
}

impl fmt::Display for SampleError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            SampleError::Unsupported(u) => u.fmt(f),
            SampleError::Message(m) => f.write_str(m),
        }
    }
}

impl std::error::Error for SampleError {}

/// The file `sample(1)` writes its report to (Go's `ReportFiles`).
pub trait ReportFiles {
    /// Creates an empty file matching `pattern` (one `*`) and returns its path.
    fn create_temp(&self, pattern: &str) -> io::Result<String>;
    fn read_file(&self, path: &str) -> io::Result<Vec<u8>>;
    fn remove(&self, path: &str) -> io::Result<()>;
}

/// [`ReportFiles`] on the real filesystem; `dir` empty means the system temp directory (Go's
/// `OSReportFiles`).
#[derive(Clone, Debug, Default)]
pub struct OsReportFiles {
    pub dir: String,
}

static TEMP_SEQ: AtomicU64 = AtomicU64::new(0);

impl ReportFiles for OsReportFiles {
    fn create_temp(&self, pattern: &str) -> io::Result<String> {
        let dir = if self.dir.is_empty() { std::env::temp_dir() } else { self.dir.clone().into() };
        let (prefix, suffix) = pattern.split_once('*').unwrap_or((pattern, ""));
        for _ in 0..10_000 {
            let seq = TEMP_SEQ.fetch_add(1, Ordering::Relaxed);
            let path = dir.join(format!("{prefix}{}{seq}{suffix}", std::process::id()));
            match std::fs::OpenOptions::new().write(true).create_new(true).open(&path) {
                Ok(_) => return Ok(path.to_string_lossy().into_owned()),
                Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
                Err(e) => return Err(e),
            }
        }
        Err(io::Error::new(io::ErrorKind::AlreadyExists, "createtemp: too many collisions"))
    }

    fn read_file(&self, path: &str) -> io::Result<Vec<u8>> {
        OsFs.read(Path::new(path))
    }

    fn remove(&self, path: &str) -> io::Result<()> {
        std::fs::remove_file(path)
    }
}

/// Runs a named command with arguments: stdout, and an error when it failed.
pub type RunFn<'a> = dyn Fn(&str, &[String]) -> (Vec<u8>, Option<String>) + 'a;

/// Go's `RunSampleReport`: runs `sample <args...> -file <temp>` and returns the report read back
/// from that file, plus what `sample` printed (its progress lines, useful in errors). The temp
/// file is always removed; without `-file`, `sample` would leave one in /tmp.
pub fn run_sample_report(files: &dyn ReportFiles, run: &RunFn<'_>, args: &[String]) -> (Vec<u8>, Vec<u8>, Option<String>) {
    let path = match files.create_temp("spectra-*.sample.txt") {
        Ok(p) => p,
        Err(e) => return (Vec::new(), Vec::new(), Some(format!("sample report file: {e}"))),
    };
    let mut full = args.to_vec();
    full.extend(["-file".to_string(), path.clone()]);
    let (stdout, err) = run("sample", &full);
    let result = match err {
        Some(e) => (Vec::new(), stdout, Some(e)),
        None => match files.read_file(&path) {
            Ok(report) => (report, stdout, None),
            Err(e) => (Vec::new(), stdout, Some(format!("read sample report: {e}"))),
        },
    };
    let _ = files.remove(&path);
    result
}

/// Captures call stacks with `sample(1)` (Go's `Sampler`).
pub struct Sampler<'a> {
    pub os: Kind,
    pub runner: &'a dyn CommandRunner,
    pub store: Option<&'a dyn SampleStore>,
    pub clock: &'a dyn Clock,
    pub files: &'a dyn ReportFiles,
}

impl Sampler<'_> {
    /// Go's `Capture`: `sample <pid> <duration-seconds> <interval-ms>`.
    pub fn capture(&self, mut opts: SampleOptions) -> Result<SampleResult, SampleError> {
        // sample(1) is a macOS developer tool; Linux profiling is perf.
        let os = self.os.resolve();
        if os != Kind::Darwin {
            return Err(SampleError::Unsupported(Unsupported::new("process sampling (sample(1))", os)));
        }
        if opts.pid <= 0 {
            return Err(SampleError::Message("sample requires positive pid".to_string()));
        }
        if opts.duration.0 <= 0 {
            opts.duration = GoDuration(1_000_000_000);
        }
        if opts.interval_ms <= 0 {
            opts.interval_ms = 10;
        }
        let taken_at = opts.taken_at.unwrap_or_else(|| GoTime::utc(self.clock.now()));
        let timeout = Duration::from_nanos(u64::try_from(opts.duration.0).unwrap_or(0)) + Duration::from_secs(5);
        let seconds = match super::round_second(opts.duration.0) / 1_000_000_000 {
            s if s <= 0 => 1,
            s => s,
        };
        let run = |name: &str, args: &[String]| self.runner.run(name, args, timeout);
        let args = [opts.pid.to_string(), seconds.to_string(), opts.interval_ms.to_string()];
        let (report, _, err) = run_sample_report(self.files, &run, &args);
        if let Some(e) = err {
            return Err(SampleError::Message(format!("sample pid {}: {e}", opts.pid)));
        }
        let result = SampleResult {
            pid: opts.pid,
            taken_at,
            duration_ms: opts.duration.0 / 1_000_000,
            interval_ms: opts.interval_ms,
            output: String::from_utf8_lossy(&report).into_owned(),
        };
        if opts.store
            && let Some(store) = self.store
        {
            store.put_sample(&result).map_err(|e| SampleError::Message(format!("store sample: {e}")))?;
        }
        Ok(result)
    }
}

#[cfg(test)]
mod tests;
