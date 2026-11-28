//! `spectra lineage <pid>` (`cmd/spectra/lineage.go`): who started a process and what it
//! started, from one process snapshot (see [`crate::process`] for where it comes from).

use std::io::{self, Write};

use spectra_base::gojson::{Encoder, GoTime};
use spectra_host::lineage::{self, Lineage, Node, NotFound};
use spectra_host::process::{CollectOptions, SystemCollector};

use crate::api::{Cli, Ctx};
use crate::flags::{FlagSet, goconv};

/// What [`crate::api::lineage`] returns.
pub type Report = Lineage;

/// What [`crate::api::lineage`] reads.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Options {
    pub pid: i64,
}

/// Snapshots the processes for `ctx.os` and builds `opts.pid`'s lineage.
pub fn collect(ctx: &Ctx, opts: &Options) -> Result<Report, NotFound> {
    let collector = SystemCollector(ctx.process_seams(ctx.os.resolve()));
    lineage::collect(&collector, &CollectOptions::default(), opts.pid)
}

/// Go's `runLineageWithIO`.
pub fn run(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let mut fs = FlagSet::new("spectra lineage");
    let as_json = fs.bool("json", false, "Emit JSON instead of a human tree");
    if fs.parse(args, cli.stderr).is_err() {
        return 2;
    }
    if fs.narg() != 1 {
        let _ = writeln!(cli.stderr, "usage: spectra lineage [--json] <pid>");
        return 2;
    }
    let raw = fs.arg(0).to_string();
    let pid = match spectra_host::goerr::parse_int(&raw) {
        Ok(p) if p > 0 => p,
        _ => {
            let _ = writeln!(cli.stderr, "invalid PID {}", goconv::quote(&raw));
            return 2;
        }
    };
    let res = match crate::api::lineage(cli.ctx, Options { pid }) {
        Ok(r) => r,
        Err(e) => {
            let _ = writeln!(cli.stderr, "lineage: {e}");
            return 1;
        }
    };
    if as_json.get() {
        // Go's json.NewEncoder(stdout) with SetIndent("", "  ").
        if let Err(e) = (Encoder { indent: true, escape_html: true }).encode(&mut *cli.stdout, &res) {
            let _ = writeln!(cli.stderr, "{e}");
            return 1;
        }
        return 0;
    }
    if let Err(e) = write_lineage(cli.stdout, &res) {
        let _ = writeln!(cli.stderr, "lineage: write output: {e}");
        return 1;
    }
    0
}

/// Go's `printLineage`; the first write error stops it.
pub fn write_lineage(w: &mut dyn Write, r: &Lineage) -> io::Result<()> {
    writeln!(w, "Ancestors of pid {} (nearest first):", r.pid)?;
    for a in &r.ancestors {
        writeln!(w, "  {:>6}  {:<12}  {}  {}", a.pid, a.user, date_time(&a.start_time), a.command)?;
    }
    if !r.flags.is_empty() {
        writeln!(w, "Flags: {} (heuristic: per-user launchd agents also have PPID 1)", r.flags.join(", "))?;
    }
    writeln!(w, "Descendants:")?;
    write_node(w, &r.tree, 1)?;
    writeln!(w, "Subtree: {} processes, {} KiB RSS, {:.1}% CPU", r.tree.count, r.tree.subtree_rss_kib, r.tree.subtree_cpu_pct)
}

fn write_node(w: &mut dyn Write, n: &Node, depth: usize) -> io::Result<()> {
    let flags = if n.flags.is_empty() { String::new() } else { format!(" [{}]", n.flags.join(",")) };
    writeln!(w, "{}{} ({})  rss {} KiB  cpu {:.1}%{flags}", "  ".repeat(depth), n.command, n.pid, n.rss_kib, n.cpu_pct)?;
    for c in &n.children {
        write_node(w, c, depth + 1)?;
    }
    Ok(())
}

/// Go's `t.Format(time.DateTime)` in the time's own zone, or `-` for the zero time.
fn date_time(t: &GoTime) -> String {
    if t.is_zero() {
        return "-".into();
    }
    let dt = t.offset.to_datetime(t.instant);
    format!("{:04}-{:02}-{:02} {:02}:{:02}:{:02}", dt.year(), dt.month(), dt.day(), dt.hour(), dt.minute(), dt.second())
}

#[cfg(test)]
mod tests;
