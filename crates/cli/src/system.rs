//! `spectra system limits` (`cmd/spectra/system.go`): kernel pty, file and process limits
//! against current usage, optionally with the processes behind each saturated resource. Like
//! Go, it asks the macOS tools (`sysctl`, `lsof`, `ps`) through `ctx.runner` on every OS and
//! reports what fails as partial failures; the process table for `--top` comes from
//! [`spectra_host::process`] for `ctx.os`.

use std::io::Write;

use serde::Serialize;
use spectra_base::gojson::{self, Encoder};
use spectra_host::process::{self, CollectOptions};
use spectra_host::syslimits::{self, ResourceUsage, SystemLimits, TopHolders};

use crate::api::{Cli, Ctx};
use crate::flags::FlagSet;
use crate::process::truncate;

pub use spectra_host::syslimits::TopHolder;

/// What [`crate::api::system`] reads (the flags of `spectra system limits`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Options {
    /// Attribute saturated resources to the processes holding them (a deep process table).
    pub top: bool,
}

/// What [`crate::api::system`] returns. `--json` prints `limits` alone, or both fields with
/// `--top`.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Report {
    pub limits: SystemLimits,
    /// Present only with `--top`.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub top_holders: TopHolders,
}

/// How many holders each resource lists.
const TOP_ROWS: usize = 10;

/// The limits for `ctx.runner`'s host, and with `opts.top` their top holders.
pub fn collect(ctx: &Ctx, opts: &Options) -> Report {
    let uid = ctx.system.uid();
    let limits = syslimits::collect(&syslimits::Options { runner: ctx.runner.as_ref(), clock: ctx.clock.as_ref(), uid });
    let top_holders = if opts.top {
        let seams = ctx.process_seams(ctx.os.resolve());
        let procs = process::collect_all(&seams, &CollectOptions { deep: true, ..CollectOptions::default() });
        syslimits::top_holders_of(&procs, TOP_ROWS, uid)
    } else {
        TopHolders::new()
    };
    Report { limits, top_holders }
}

/// Go's `runSystem`: `limits` is the one subcommand.
pub fn run(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    if let Some(first) = args.first()
        && !first.starts_with('-')
        && first == "limits"
    {
        return run_limits(cli, &args[1..]);
    }
    let _ = writeln!(cli.stderr, "usage: spectra system limits [--json] [--top]");
    2
}

/// Go's `runSystemLimits`.
fn run_limits(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let mut fs = FlagSet::new("spectra system limits");
    let as_json = fs.bool("json", false, "Emit JSON");
    let show_top = fs.bool("top", false, "Show top process holders for saturated resources");
    if fs.parse(args, cli.stderr).is_err() {
        return 2;
    }
    let report = crate::api::system(cli.ctx, Options { top: show_top.get() });
    let code = i32::from(report.limits.any_critical());
    if as_json.get() {
        // Go's json.NewEncoder(os.Stdout) with SetIndent("", "  "); the error is ignored there.
        let encoder = Encoder { indent: true, escape_html: true };
        let _ = if show_top.get() { encoder.encode(&mut *cli.stdout, &report) } else { encoder.encode(&mut *cli.stdout, &report.limits) };
        return code;
    }
    write_limits(cli.stdout, cli.stderr, &report.limits);
    if show_top.get() {
        write_top(cli.stdout, &report.limits, &report.top_holders);
    }
    code
}

/// Go's `printSystemLimits`; the partial-failure warning goes to `stderr`.
pub fn write_limits(out: &mut dyn Write, err: &mut dyn Write, limits: &SystemLimits) {
    let _ = writeln!(out, "{:<14}  {:<8}  {:<8}  {:<7}  STATE", "RESOURCE", "USED", "LIMIT", "PCT");
    let _ = writeln!(out, "{}", "-".repeat(52));
    write_usage_row(out, "pty slots", &limits.pty);
    write_usage_row(out, "open files", &limits.files);
    write_usage_row(out, "processes", &limits.procs);
    write_usage_row(out, "processes/uid", &limits.procs_per_uid);
    let _ = writeln!(out, "{:<14}  {:<8}  {:<8}  {:<7}  ", "files/proc", "-", limits.files_per_proc, "-");
    if !limits.partial_failures.is_empty() {
        let _ = writeln!(err, "warning: partial collection failures: {}", limits.partial_failures.join(", "));
    }
}

fn write_usage_row(out: &mut dyn Write, label: &str, usage: &ResourceUsage) {
    let state = if usage.critical {
        "CRITICAL"
    } else if usage.warn {
        "WARN"
    } else {
        ""
    };
    let pct = if usage.limit > 0 { format!("{:.0}%", usage.pct) } else { "-".to_string() };
    let _ = writeln!(out, "{:<14}  {:<8}  {:<8}  {:<7}  {}", label, usage.current, usage.limit, pct, state);
}

/// Go's `printSystemLimitTop`.
pub fn write_top(out: &mut dyn Write, limits: &SystemLimits, top: &TopHolders) {
    write_top_for(out, "pty slots", "pty", &limits.pty, top);
    write_top_for(out, "open files", "files", &limits.files, top);
    write_top_for(out, "processes", "procs", &limits.procs, top);
    write_top_for(out, "processes/uid", "procs_per_uid", &limits.procs_per_uid, top);
}

fn write_top_for(out: &mut dyn Write, label: &str, key: &str, usage: &ResourceUsage, top: &TopHolders) {
    if !usage.warn && !usage.critical {
        return;
    }
    let Some(rows) = top.get(key).filter(|r| !r.is_empty()) else { return };
    let state = if usage.critical { "CRITICAL" } else { "WARN" };
    let _ = writeln!(out, "\nRESOURCE: {label} ({}/{}, {state})", usage.current, usage.limit);
    let _ = writeln!(out, "  {:<7}  {:<7}  COMMAND", "PID", "COUNT");
    for row in rows {
        let _ = writeln!(out, "  {:<7}  {:<7}  {}", row.pid, row.count, truncate(&row.command, 48));
    }
}

#[cfg(test)]
mod tests;
