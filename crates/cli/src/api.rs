//! The command layer's seams and per-invocation state. Commands take a [`Cli`] instead of
//! touching the process: the OS, environment, clock, subprocesses, filesystem and output all
//! come from here, so tests (and the desktop, which calls the command layer directly) supply
//! their own.

use std::io::Write;
use std::sync::Arc;

use spectra_base::clock::{Clock, Sleeper, SystemClock, SystemSleeper};
use spectra_base::env::{Env, SystemEnv};
use spectra_base::fs::{Fs, OsFs};
use spectra_base::hostos::{Kind, Unsupported};
use spectra_base::logger::{self, Format, Level, Logger};
use spectra_base::proc::{OsRunner, Runner};

/// The version a source build reports (Go's `buildversion.Dev`).
pub const DEV_VERSION: &str = "dev";

/// The seams a command runs against.
#[derive(Clone)]
pub struct Ctx {
    /// The OS to behave as; [`Kind::Unknown`] means the host.
    pub os: Kind,
    /// The architecture to report, as Go's `runtime.GOARCH` names it; empty means the host's.
    pub arch: String,
    pub env: Arc<dyn Env>,
    pub clock: Arc<dyn Clock>,
    pub sleeper: Arc<dyn Sleeper>,
    pub runner: Arc<dyn Runner>,
    pub fs: Arc<dyn Fs>,
    /// What `spectra version` prints (Go's ldflags-stamped `main.version`).
    pub version: String,
    /// Receives debug records when `--verbose`, `--debug` or `SPECTRA_DEBUG` is given; without
    /// them commands log to a discarding logger.
    pub debug_logger: Arc<dyn Logger>,
}

impl Ctx {
    /// The real host: process environment, wall clock, real subprocesses and filesystem, and
    /// debug records as text on stderr (Go's `enableVerbose`).
    pub fn system(version: impl Into<String>) -> Ctx {
        let clock: Arc<dyn Clock> = Arc::new(SystemClock);
        let debug_logger =
            logger::new(logger::Config { writer: None, format: Format::Text, level: Some(Level::Debug), clock: Some(Arc::clone(&clock)) });
        Ctx {
            os: Kind::Unknown,
            arch: String::new(),
            env: Arc::new(SystemEnv),
            clock,
            sleeper: Arc::new(SystemSleeper),
            runner: Arc::new(OsRunner),
            fs: Arc::new(OsFs),
            version: version.into(),
            debug_logger,
        }
    }
}

/// One invocation: the seams, the resolved OS, the logger `--verbose` chose, and the output.
pub struct Cli<'a> {
    pub ctx: &'a Ctx,
    /// `ctx.os`, resolved to the host when it is unknown.
    pub os: Kind,
    /// Go's `cliLogger`: discards unless verbose output was asked for.
    pub logger: Arc<dyn Logger>,
    pub stdout: &'a mut dyn Write,
    pub stderr: &'a mut dyn Write,
}

impl<'a> Cli<'a> {
    pub fn new(ctx: &'a Ctx, verbose: bool, stdout: &'a mut dyn Write, stderr: &'a mut dyn Write) -> Cli<'a> {
        let logger = if verbose { Arc::clone(&ctx.debug_logger) } else { logger::discard() };
        Cli { ctx, os: ctx.os.resolve(), logger, stdout, stderr }
    }
}

/// What `spectra version` reports.
pub fn version(ctx: &Ctx) -> &str {
    &ctx.version
}

/// The help text `spectra help` prints.
pub fn help() -> String {
    let mut out = Vec::new();
    crate::help::write(&mut out);
    String::from_utf8_lossy(&out).into_owned()
}

/// Why `command` cannot run on `os`, when it is macOS-only and `os` is not macOS: the report
/// `spectra <command> --json` prints there, with its hint.
pub fn unsupported(command: &str, os: Kind) -> Option<spectra_base::hostos::CommandReport> {
    let os = os.resolve();
    let d = crate::unsupported::darwin_only(command)?;
    (os != Kind::Darwin).then(|| Unsupported::new(d.feature, os).command_report(command, d.hint))
}

/// Go's `runtime.GOARCH` for the host (Rust names the same two architectures `aarch64` and
/// `x86_64`).
pub fn host_arch() -> &'static str {
    match std::env::consts::ARCH {
        "aarch64" => "arm64",
        "x86_64" => "amd64",
        other => other,
    }
}

/// What `spectra capabilities` reports: the manifest of the machine-readable interfaces this
/// build supports on `ctx.os` (Go's `capabilities.Build(version, GOOS, GOARCH)`).
pub fn capabilities(ctx: &Ctx) -> spectra_protocol::SpectraCapabilities {
    let arch = if ctx.arch.is_empty() { host_arch() } else { ctx.arch.as_str() };
    crate::capabilities::build(&ctx.version, ctx.os.resolve(), arch)
}
