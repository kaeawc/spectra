//! `spectra procdiag` (`cmd/spectra/procdiag.go`): why a Linux process is slow or stuck, from
//! two `/proc` samples an interval apart ([`spectra_host::procdiag`]). Exits 3 when a finding
//! about the process itself is worse than info; cgroup-wide findings are context only. Off
//! Linux it refuses, pointing at `spectra sample` and `spectra hang`.

use std::collections::BTreeMap;
use std::fmt;
use std::io::Write;

use spectra_base::gojson::{Encoder, GoDuration};
use spectra_base::hostos::{Kind, Unsupported};
use spectra_host::procdiag::{self as diag, Report};

use crate::api::{Cli, Ctx};
use crate::flags::FlagSet;
use crate::flags::goconv;

pub use spectra_host::procdiag::{Fds, Finding, Io, Memory, Severity, Thread};

/// What [`crate::api::procdiag`] samples, through `ctx.fs`, `ctx.clock` and `ctx.sleeper`.
#[derive(Clone, Debug)]
pub struct Options {
    pub pid: i64,
    /// Time between the two samples; 1s when not positive.
    pub interval: GoDuration,
    pub proc_root: String,
    pub cgroup_root: String,
}

impl Default for Options {
    fn default() -> Self {
        Options { pid: 0, interval: GoDuration(1_000_000_000), proc_root: "/proc".to_string(), cgroup_root: "/sys/fs/cgroup".to_string() }
    }
}

/// Why there is no report.
#[derive(Debug)]
pub enum Error {
    /// Not Linux.
    Unsupported(Unsupported),
    /// The process is gone, or its `stat` could not be read.
    Inspect(diag::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Unsupported(u) => u.fmt(f),
            Error::Inspect(e) => e.fmt(f),
        }
    }
}

impl std::error::Error for Error {}

/// The feature named when the command runs off Linux.
const FEATURE: &str = "/proc process diagnosis";

/// Go's `procdiag.Inspect` with the command's seams: `ctx.arch` picks the syscall table.
pub fn collect(ctx: &Ctx, opts: &Options) -> Result<Report, Error> {
    let os = ctx.os.resolve();
    if os != Kind::Linux {
        return Err(Error::Unsupported(Unsupported::new(FEATURE, os)));
    }
    let arch = if ctx.arch.is_empty() { crate::api::host_arch().to_string() } else { ctx.arch.clone() };
    diag::inspect(
        opts.pid,
        &diag::Options {
            fs: ctx.fs.as_ref(),
            clock: ctx.clock.as_ref(),
            sleeper: ctx.sleeper.as_ref(),
            proc_root: opts.proc_root.clone(),
            cgroup_root: opts.cgroup_root.clone(),
            interval: opts.interval,
            arch,
        },
    )
    .map_err(Error::Inspect)
}

/// Go's `runProcdiag`.
pub fn run(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let mut fs = FlagSet::new("spectra procdiag");
    let as_json = fs.bool("json", false, "Emit JSON instead of a human summary");
    let interval = fs.duration("interval", GoDuration(1_000_000_000), "Time between the two samples (longer separates leaks from noise)");
    if fs.parse(args, cli.stderr).is_err() {
        return 2;
    }
    if cli.os != Kind::Linux {
        let _ = writeln!(
            cli.stderr,
            "spectra procdiag: {} (use `spectra sample` or `spectra hang` on macOS)",
            Unsupported::new(FEATURE, cli.os)
        );
        return 1;
    }
    if fs.narg() != 1 {
        let _ = writeln!(cli.stderr, "usage: spectra procdiag [--interval <d>] [--json] <pid>");
        return 2;
    }
    let pid = match goconv::parse_int(fs.arg(0)) {
        Ok(p) if p > 0 => p,
        _ => {
            let _ = writeln!(cli.stderr, "invalid PID {}", goconv::quote(fs.arg(0)));
            return 2;
        }
    };
    let interval = interval.get();
    if interval.0 <= 0 {
        let _ = writeln!(cli.stderr, "--interval must be positive");
        return 2;
    }
    let rep = match crate::api::procdiag(cli.ctx, Options { pid, interval, ..Options::default() }) {
        Ok(r) => r,
        Err(Error::Inspect(e)) if e.is_no_process() => {
            let _ = writeln!(cli.stderr, "spectra procdiag: no such process {pid}");
            return 1;
        }
        Err(e) => {
            let _ = writeln!(cli.stderr, "spectra procdiag: {e}");
            return 1;
        }
    };
    if as_json.get() {
        // Go's json.NewEncoder(stdout) with SetIndent("", "  ").
        let _ = Encoder { indent: true, escape_html: true }.encode(&mut *cli.stdout, &rep);
    } else {
        write_text(cli.stdout, &rep);
    }
    rep.exit_code()
}

/// Go's `formatStateCounts`: ` (R=1 S=3)`, sorted by state, or `""` when there are none.
pub fn format_state_counts(counts: &BTreeMap<String, i64>) -> String {
    if counts.is_empty() {
        return String::new();
    }
    let parts: Vec<String> = counts.iter().map(|(k, v)| format!("{k}={v}")).collect();
    format!(" ({})", parts.join(" "))
}

/// Go's `truncate`: at most `n` runes, the last replaced by `…` when cut.
fn truncate(s: &str, n: usize) -> String {
    if s.chars().count() <= n {
        return s.to_string();
    }
    let mut out: String = s.chars().take(n.saturating_sub(1)).collect();
    out.push('…');
    out
}

/// Go's `printProcdiag`.
pub fn write_text(w: &mut dyn Write, r: &Report) {
    let _ = writeln!(w, "=== pid {} ({}) — {} ===", r.pid, r.comm, r.verdict);
    if !r.cmdline.is_empty() {
        let _ = writeln!(w, "  cmd:      {}", truncate(&r.cmdline, 100));
    }
    let _ = writeln!(w, "  sampled:  {:.1}s, {} thread(s){}", r.interval_sec, r.thread_count, format_state_counts(&r.state_counts));
    let _ = writeln!(w, "  cpu:      {:.0}% of one core", r.cpu_pct);
    let m = &r.memory;
    let _ = writeln!(
        w,
        "  memory:   rss {} MiB ({:+.1} MiB/s), anon {} MiB, swap {} MiB",
        m.rss_kib / 1024,
        m.rss_rate_kib_ps / 1024.0,
        m.anon_kib / 1024,
        m.swap_kib / 1024
    );
    let limit = if r.fds.limit > 0 { r.fds.limit.to_string() } else { "unlimited".to_string() };
    let _ = writeln!(w, "  fds:      {} / {limit} ({:+.1}/s)", r.fds.count, r.fds.rate_per_sec);
    if let Some(io) = &r.io {
        let _ = writeln!(w, "  disk io:  read {} KiB, wrote {} KiB", io.read_bytes / 1024, io.write_bytes / 1024);
    }
    if let Some(cg) = &r.cgroup {
        let _ = writeln!(w, "  cgroup:   {}", cg.path);
    }
    let _ = writeln!(w, "  top threads:");
    for t in r.threads.iter().take(5) {
        let mut place = t.syscall.clone();
        if !t.wchan.is_empty() {
            place.push_str(" @");
            place.push_str(&t.wchan);
        }
        let _ = writeln!(w, "    {:7} {:<15} {} {:5.1}%  {place}", t.tid, truncate(&t.comm, 15), t.state, t.cpu_pct);
    }
    if r.findings.is_empty() {
        let _ = writeln!(w, "  no problems found in this window");
    }
    for f in &r.findings {
        let scope = if f.process_scoped() { String::new() } else { format!(" ({})", f.scope) };
        let _ = writeln!(w, "  [{}]{scope} {}: {}", f.severity.as_str(), f.kind, f.summary);
        for e in &f.evidence {
            let _ = writeln!(w, "         · {e}");
        }
    }
    for n in &r.notes {
        let _ = writeln!(w, "  note: {n}");
    }
}

#[cfg(test)]
mod tests;
