//! The command layer's seams and per-invocation state. Commands take a [`Cli`] instead of
//! touching the process: the OS, environment, clock, subprocesses, filesystem and output all
//! come from here, so tests (and the desktop, which calls the command layer directly) supply
//! their own.

use std::io::Write;
use std::sync::Arc;

use jiff::tz::TimeZone;
use spectra_base::clock::{Clock, Sleeper, SystemClock, SystemSleeper};
use spectra_base::env::{Env, SystemEnv};
use spectra_base::fs::{Fs, OsFs};
use spectra_base::hostos::{Kind, Unsupported};
use spectra_base::logger::{self, Format, Level, Logger};
use spectra_base::proc::{OsRunner, Runner};
use spectra_host::memstate::{self, HostMemory, MemorySource};
use spectra_host::process::{self, HostProcesses, ProcessSource};
use spectra_host::syslimits::{HostSystem, SystemSource};

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
    /// macOS memory readings (Mach VM statistics and sysctls); recorded cases replay a
    /// [`memstate::FixedMemory`].
    pub memory: Arc<dyn MemorySource>,
    /// macOS process facts (libproc details, `hw.memsize`, `kern.procargs2`) and the page size;
    /// recorded cases replay a [`spectra_host::process::FixedProcesses`].
    pub processes: Arc<dyn ProcessSource>,
    /// Who is running (Go's `os.Getuid`); recorded cases replay a
    /// [`spectra_host::syslimits::FixedSystem`].
    pub system: Arc<dyn SystemSource>,
    /// The zone process start times are shown in (Go's `time.Local`).
    pub tz: TimeZone,
    /// What `spectra version` prints (Go's ldflags-stamped `main.version`).
    pub version: String,
    /// Receives debug records when `--verbose`, `--debug` or `SPECTRA_DEBUG` is given; without
    /// them commands log to a discarding logger.
    pub debug_logger: Arc<dyn Logger>,
}

impl Ctx {
    /// The real host: process environment, wall clock, real subprocesses and filesystem, the
    /// local time zone, and debug records as text on stderr (Go's `enableVerbose`).
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
            memory: Arc::new(HostMemory),
            processes: Arc::new(HostProcesses),
            system: Arc::new(HostSystem),
            tz: spectra_host::localtz::local(&SystemEnv, &OsFs),
            version: version.into(),
            debug_logger,
        }
    }

    /// The process-collection seams for this context on `os`.
    pub fn process_seams(&self, os: Kind) -> process::Seams<'_> {
        process::Seams {
            os,
            runner: self.runner.as_ref(),
            env: self.env.as_ref(),
            fs: self.fs.as_ref(),
            clock: self.clock.as_ref(),
            source: self.processes.as_ref(),
            tz: &self.tz,
            proc_fs: "",
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

/// What `spectra memory` reports: one host memory sample for `ctx.os` (Go's `memstate.Collect`).
pub fn memory(ctx: &Ctx, opts: crate::memory::Options) -> Result<crate::memory::Report, memstate::Error> {
    crate::memory::collect(ctx, &opts)
}

/// What `spectra pressure` reports: host PSI and, for `opts.pid`, its cgroup v2 accounting, with
/// the findings drawn from them. Linux only; elsewhere [`crate::pressure::Error::Unsupported`].
pub fn pressure(ctx: &Ctx, opts: crate::pressure::Options) -> Result<crate::pressure::Report, crate::pressure::Error> {
    crate::pressure::collect(ctx, &opts)
}

/// What `spectra procdiag` reports: two `/proc` samples of `opts.pid`, `opts.interval` apart
/// (through `ctx.fs`, `ctx.clock` and `ctx.sleeper`), and the findings drawn from them. Linux
/// only; elsewhere [`crate::procdiag::Error::Unsupported`].
pub fn procdiag(ctx: &Ctx, opts: crate::procdiag::Options) -> Result<spectra_host::procdiag::Report, crate::procdiag::Error> {
    crate::procdiag::collect(ctx, &opts)
}

/// What `spectra process` reports: the process table for `ctx.os` (`ps` and libproc facts on
/// macOS, `/proc` on Linux), filtered, then as rows, a tree or families (Go's `runProcess`).
pub fn process(ctx: &Ctx, opts: crate::process::Options) -> Result<crate::process::Report, crate::process::Error> {
    crate::process::collect(ctx, &opts)
}

/// What `spectra lineage` reports: `opts.pid`'s ancestor chain and descendant subtree with
/// RSS and CPU totals, from one process snapshot (Go's `lineage.Collect`).
pub fn lineage(ctx: &Ctx, opts: crate::lineage::Options) -> Result<crate::lineage::Report, spectra_host::lineage::NotFound> {
    crate::lineage::collect(ctx, &opts)
}

/// What `spectra system limits` reports: kernel pty, file and process limits against current
/// usage, and with `opts.top` the processes holding the saturated ones (Go's `runSystemLimits`).
pub fn system(ctx: &Ctx, opts: crate::system::Options) -> crate::system::Report {
    crate::system::collect(ctx, &opts)
}
