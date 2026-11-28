//! `spectra process` (`cmd/spectra/process_cmd.go`, `process_classify.go`, `process_family.go`):
//! running processes sorted by memory, filtered, as rows, a tree or families, optionally with
//! role, runtime, owning app and (macOS) signing per process. The table comes from
//! [`spectra_host::process`] for `ctx.os`: `ps` through `ctx.runner` and libproc facts through
//! `ctx.processes` on macOS, `/proc` through `ctx.fs` on Linux.

use std::fmt;
use std::io::Write;
use std::sync::Arc;

use spectra_base::gojson::{Encoder, GoDuration, GoTime};
use spectra_base::hostos::Kind;
use spectra_base::proc::CancelToken;
use spectra_host::classify::{self, Classifier};
use spectra_host::gosort;
use spectra_host::process::{self, CollectOptions, Family, Info, TreeNode};

use crate::api::{Cli, Ctx};
use crate::flags::{FlagSet, goconv};

/// What [`crate::api::process`] reads (the flags of `spectra process`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Options {
    /// rss, vsz, cpu, mem, elapsed, startedat, pid or cmd.
    pub sort: String,
    /// Only the first N rows (or families); 0 means all.
    pub top: i64,
    /// Open FDs and listening ports via one `lsof`.
    pub deep: bool,
    /// Typed FD columns; implies `deep`.
    pub fd_breakdown: bool,
    pub tree: bool,
    /// Minimum RSS, such as `500M`.
    pub min_rss: String,
    /// Minimum VSZ, such as `1G`.
    pub min_vsz: String,
    pub min_elapsed: GoDuration,
    /// Only direct children of this pid; -1 means any.
    pub ppid: i64,
    /// Only processes owned by this user name.
    pub user: String,
    /// `family`, or empty for rows.
    pub group_by: String,
    pub classify: bool,
}

impl Default for Options {
    fn default() -> Self {
        Options {
            sort: "rss".into(),
            top: 0,
            deep: false,
            fd_breakdown: false,
            tree: false,
            min_rss: String::new(),
            min_vsz: String::new(),
            min_elapsed: GoDuration(0),
            ppid: -1,
            user: String::new(),
            group_by: String::new(),
            classify: false,
        }
    }
}

/// What [`crate::api::process`] returns.
#[derive(Clone, Debug, PartialEq)]
pub struct Report {
    pub view: View,
    /// Classification stopped early (Go prints it and leaves the rest unknown).
    pub classify_error: Option<String>,
}

/// The shape of a [`Report`].
#[derive(Clone, Debug, PartialEq)]
pub enum View {
    /// Nothing matched (Go's "no processes found").
    Empty,
    Rows(Vec<Info>),
    Tree(Vec<TreeNode>),
    Families(Vec<Family>),
}

/// Why [`crate::api::process`] refused; the texts are Go's.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Error {
    GroupBy(String),
    MinRss,
    MinVsz,
    Sort(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::GroupBy(g) => write!(f, "invalid --group-by {} (supported: family)", goconv::quote(g)),
            Error::MinRss => f.write_str("invalid --min-rss: expected non-negative size"),
            Error::MinVsz => f.write_str("invalid --min-vsz: expected non-negative size"),
            Error::Sort(s) => write!(f, "invalid --sort {}", goconv::quote(s)),
        }
    }
}

impl std::error::Error for Error {}

struct Filters {
    min_rss: u64,
    min_vsz: u64,
    min_elapsed: GoDuration,
    ppid: i64,
    user: String,
}

/// The table, filtered, then grouped, or sorted, cut to `top` and classified, as Go's
/// `runProcess` does it.
pub fn collect(ctx: &Ctx, opts: &Options) -> Result<Report, Error> {
    if !opts.group_by.is_empty() && opts.group_by != "family" {
        return Err(Error::GroupBy(opts.group_by.clone()));
    }
    let filters = Filters {
        min_rss: parse_process_size(&opts.min_rss).map_err(|SizeError| Error::MinRss)?,
        min_vsz: parse_process_size(&opts.min_vsz).map_err(|SizeError| Error::MinVsz)?,
        min_elapsed: opts.min_elapsed,
        ppid: opts.ppid,
        user: opts.user.clone(),
    };
    let os = ctx.os.resolve();
    let seams = ctx.process_seams(os);
    let deep = opts.deep || opts.fd_breakdown;
    let procs: Vec<Info> = process::collect_all(&seams, &CollectOptions { deep, ..CollectOptions::default() })
        .into_iter()
        .filter(|p| matches(p, &filters))
        .collect();
    let mut report = Report { view: View::Empty, classify_error: None };
    if procs.is_empty() {
        return Ok(report);
    }
    if opts.group_by == "family" {
        let mut fams = process::group_families(&procs, ctx.clock.now(), 0);
        if opts.top > 0 && (opts.top as usize) < fams.len() {
            fams.truncate(opts.top as usize);
        }
        report.view = View::Families(fams);
        return Ok(report);
    }
    let mut procs = procs;
    if !sort_processes(&mut procs, &opts.sort) {
        return Err(Error::Sort(opts.sort.clone()));
    }
    if opts.top > 0 && (opts.top as usize) < procs.len() {
        procs.truncate(opts.top as usize);
    }
    if opts.classify {
        report.classify_error = classify_processes(ctx, os, &mut procs);
    }
    report.view = if opts.tree { View::Tree(process::build_tree(&procs)) } else { View::Rows(procs) };
    Ok(report)
}

/// Go's `processClassify`: a fresh classifier over the context's runner and filesystem. Rust
/// has no port of Go's on-disk `internal/cache` yet, so it runs without a store (the output is
/// the same; a second invocation is not warmer).
fn classify_processes(ctx: &Ctx, os: Kind, procs: &mut [Info]) -> Option<String> {
    let c = Classifier::new(classify::Options {
        os,
        runner: Arc::clone(&ctx.runner),
        fs: Arc::clone(&ctx.fs),
        store: None,
        workers: 8,
        max_scan_bytes: 0,
        proc_fs: String::new(),
        arch: ctx.arch.clone(),
    });
    let seams = ctx.process_seams(os);
    let argv = |pid: i64, full: &str| process::host_argv_for(&seams, pid, full);
    // Go cancels on SIGINT; the Rust build installs no signal handler, so it runs to the end.
    process::classify(procs, &c, &argv, &CancelToken::new()).map(|e| e.to_string())
}

fn matches(p: &Info, f: &Filters) -> bool {
    if f.min_rss > 0 && rss_bytes(p) < f.min_rss {
        return false;
    }
    if f.min_vsz > 0 && vsz_bytes(p) < f.min_vsz {
        return false;
    }
    if f.min_elapsed.0 > 0 && p.elapsed < f.min_elapsed {
        return false;
    }
    if f.ppid >= 0 && p.ppid != f.ppid {
        return false;
    }
    f.user.is_empty() || p.user == f.user
}

/// Go's `sortProcesses` (an unstable `sort.Slice`, ported so ties land where Go puts them).
pub fn sort_processes(procs: &mut [Info], by: &str) -> bool {
    match by {
        "pid" => gosort::slice(procs, |a, b| a.pid < b.pid),
        "cmd" => gosort::slice(procs, |a, b| a.command < b.command),
        "rss" => gosort::slice(procs, |a, b| rss_bytes(a) > rss_bytes(b)),
        "vsz" => gosort::slice(procs, |a, b| vsz_bytes(a) > vsz_bytes(b)),
        "cpu" => gosort::slice(procs, |a, b| a.cpu_percent > b.cpu_percent),
        "mem" => gosort::slice(procs, |a, b| a.mem_percent > b.mem_percent),
        "elapsed" => gosort::slice(procs, |a, b| a.elapsed > b.elapsed),
        "startedat" => gosort::slice(procs, |a, b| a.started_at.instant < b.started_at.instant),
        _ => return false,
    }
    true
}

/// A `--min-rss`/`--min-vsz` value that is not a non-negative size.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SizeError;

impl fmt::Display for SizeError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("expected non-negative size")
    }
}

impl std::error::Error for SizeError {}

/// Go's `parseProcessSize`: a number with an optional K, M, G or T (binary) suffix.
pub fn parse_process_size(raw: &str) -> Result<u64, SizeError> {
    let raw = raw.trim();
    let Some(&unit) = raw.as_bytes().last() else { return Ok(0) };
    let (multiplier, number) = match unit {
        b'k' | b'K' => (1u64 << 10, &raw[..raw.len() - 1]),
        b'm' | b'M' => (1 << 20, &raw[..raw.len() - 1]),
        b'g' | b'G' => (1 << 30, &raw[..raw.len() - 1]),
        b't' | b'T' => (1 << 40, &raw[..raw.len() - 1]),
        _ => (1, raw),
    };
    match spectra_host::goerr::parse_float(number) {
        Ok(v) if v >= 0.0 => Ok((v * multiplier as f64) as u64),
        _ => Err(SizeError),
    }
}

fn rss_bytes(p: &Info) -> u64 {
    if p.rss_kib <= 0 { 0 } else { (p.rss_kib as u64).wrapping_mul(1024) }
}

fn vsz_bytes(p: &Info) -> u64 {
    if p.vsz_bytes > 0 {
        return p.vsz_bytes;
    }
    if p.vsize_kib <= 0 { 0 } else { (p.vsize_kib as u64).wrapping_mul(1024) }
}

/// Go's `runProcess`.
pub fn run(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let mut fs = FlagSet::new("spectra process");
    let as_json = fs.bool("json", false, "Emit JSON instead of a human summary");
    let sort = fs.string("sort", "rss", "Sort by: rss, vsz, cpu, mem, elapsed, startedat, pid, or cmd");
    let top = fs.int("top", 0, "Show only top N processes (0 = all)");
    let deep = fs.bool("deep", false, "Enrich with open FD count and listening ports via lsof (slower)");
    let fd_breakdown = fs.bool("fd-breakdown", false, "Show typed FD columns (implies --deep)");
    let tree = fs.bool("tree", false, "Render a parent/child process tree");
    let min_rss = fs.string("min-rss", "", "Minimum RSS (for example 500M, 2G)");
    let min_vsz = fs.string("min-vsz", "", "Minimum VSZ (for example 1G)");
    let min_elapsed = fs.duration("min-elapsed", GoDuration(0), "Minimum elapsed runtime");
    let ppid = fs.int("ppid", -1, "Only direct children of PID");
    let user = fs.string("user", "", "Only processes owned by user name");
    let group_by = fs.string("group-by", "", "Group processes: family (sums CPU per simulator device, Xcode, build tools, browsers)");
    let classify_flag = fs.bool("classify", false, "Add role, runtime, owning app and (macOS) code signing per process");
    if fs.parse(args, cli.stderr).is_err() {
        return 2;
    }
    let group_by = group_by.get();
    if !group_by.is_empty()
        && let Some(msg) = group_by_conflict(&fs, &group_by)
    {
        let _ = writeln!(cli.stderr, "{msg}");
        return 2;
    }
    let opts = Options {
        sort: sort.get(),
        top: top.get(),
        deep: deep.get() || fd_breakdown.get(),
        fd_breakdown: fd_breakdown.get(),
        tree: tree.get(),
        min_rss: min_rss.get(),
        min_vsz: min_vsz.get(),
        min_elapsed: min_elapsed.get(),
        ppid: ppid.get(),
        user: user.get(),
        group_by,
        classify: classify_flag.get(),
    };
    let report = match crate::api::process(cli.ctx, opts.clone()) {
        Ok(r) => r,
        Err(e) => {
            let _ = writeln!(cli.stderr, "{e}");
            return 2;
        }
    };
    if let Some(e) = &report.classify_error {
        let _ = writeln!(cli.stderr, "classify: {e} (remaining processes left unknown)");
    }
    write_report(cli, &report, &opts, as_json.get())
}

fn write_report(cli: &mut Cli<'_>, report: &Report, opts: &Options, as_json: bool) -> i32 {
    // Go's json.NewEncoder(os.Stdout) with SetIndent("", "  ").
    let enc = Encoder { indent: true, escape_html: true };
    let cols = ClassColumns::new(opts.classify, cli.os);
    match &report.view {
        View::Empty => {
            let _ = writeln!(cli.stderr, "no processes found");
        }
        View::Families(fams) if as_json => {
            if enc.encode(&mut *cli.stdout, fams).is_err() {
                return 1;
            }
        }
        View::Families(fams) => write_families(cli.stdout, fams),
        View::Tree(roots) if as_json => {
            let _ = enc.encode(&mut *cli.stdout, roots);
        }
        View::Tree(roots) => write_tree(cli.stdout, roots, &cols),
        View::Rows(procs) if as_json => {
            let _ = enc.encode(&mut *cli.stdout, procs);
        }
        View::Rows(procs) => write_rows(cli.stdout, procs, opts.deep, opts.fd_breakdown, &cols),
    }
    0
}

/// Go's `groupByConflict`: `--group-by family` with a flag that changes or breaks family
/// attribution (filters drop ancestors; `--sort` and `--tree` do not apply to families).
fn group_by_conflict(fs: &FlagSet, group_by: &str) -> Option<String> {
    if group_by != "family" {
        return Some(Error::GroupBy(group_by.to_string()).to_string());
    }
    let mut bad = Vec::new();
    fs.visit(|f| {
        if ["sort", "tree", "ppid", "user", "min-rss", "min-vsz", "min-elapsed", "classify"].contains(&f.name.as_str()) {
            bad.push(format!("--{}", f.name));
        }
    });
    (!bad.is_empty()).then(|| format!("--group-by family cannot be combined with {}", bad.join(", ")))
}

/// The bound on the APP column (Go's `classAppWidth`).
pub const CLASS_APP_WIDTH: usize = 16;

/// The columns `--classify` adds before COMMAND; empty when it is off. SIGN is macOS only
/// (Go's `processClassColumns`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ClassColumns {
    pub on: bool,
    pub sign: bool,
}

impl ClassColumns {
    pub fn new(on: bool, os: Kind) -> Self {
        ClassColumns { on, sign: on && os == Kind::Darwin }
    }

    pub fn header(&self) -> String {
        self.cells("ROLE", "RUNTIME", "SIGN", "APP")
    }

    pub fn row(&self, p: &Info) -> String {
        let sign = p.signing.as_ref().map_or("-", |s| s.status.as_str());
        let app = p.app.as_ref().map_or_else(|| "-".to_string(), |a| dash_if_empty(&truncate(&a.name, CLASS_APP_WIDTH)));
        self.cells(&dash_if_empty(p.role.as_str()), &dash_if_empty(p.runtime.as_str()), sign, &app)
    }

    fn cells(&self, role: &str, runtime: &str, sign: &str, app: &str) -> String {
        if !self.on {
            return String::new();
        }
        let mut out = format!("{role:<10}  {runtime:<8}  ");
        if self.sign {
            out.push_str(&format!("{sign:<12}  "));
        }
        out + &format!("{app:<CLASS_APP_WIDTH$}  ")
    }

    pub fn width(&self) -> usize {
        match (self.on, self.sign) {
            (_, true) => 10 + 2 + 8 + 2 + 12 + 2 + CLASS_APP_WIDTH + 2,
            (true, false) => 10 + 2 + 8 + 2 + CLASS_APP_WIDTH + 2,
            _ => 0,
        }
    }
}

fn write_rows(out: &mut dyn Write, procs: &[Info], deep: bool, fd_breakdown: bool, cols: &ClassColumns) {
    let (h, w) = (cols.header(), cols.width());
    if fd_breakdown {
        let _ = writeln!(
            out,
            "{:<7}  {:<6}  {:<8}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {h}COMMAND",
            "PID", "THR", "RSS", "FDS", "PTY", "SOCK", "REG", "PIPE", "CHR", "KQ", "OTH"
        );
        let _ = writeln!(out, "{}", "-".repeat(105 + w));
    } else if deep {
        let _ = writeln!(out, "{:<7}  {:<6}  {:<10}  {:<10}  {:<5}  {h}COMMAND", "PID", "THR", "RSS", "CPU%", "FDS");
        let _ = writeln!(out, "{}", "-".repeat(82 + w));
    } else {
        let _ = writeln!(
            out,
            "{:<7}  {:<7}  {:<10}  {:<10}  {:<7}  {:<7}  {:<20}  {h}COMMAND",
            "PID", "PPID", "RSS", "VSZ", "CPU%", "MEM%", "STARTED"
        );
        let _ = writeln!(out, "{}", "-".repeat(112 + w));
    }
    for p in procs {
        let thr = if p.thread_count > 0 { p.thread_count.to_string() } else { "-".into() };
        let cpu = if p.cpu_pct > 0.0 { format!("{:.1}%", p.cpu_pct) } else { "-".into() };
        let row = cols.row(p);
        if fd_breakdown {
            let b = p.fd_breakdown.unwrap_or_default();
            let _ = writeln!(
                out,
                "{:<7}  {thr:<6}  {:<8}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {:<5}  {row}{}",
                p.pid,
                human_size_kib(p.rss_kib),
                fd_string(p),
                b.pty,
                b.socket,
                b.regular,
                b.pipe,
                b.char,
                b.kqueue,
                b.other,
                truncate(&p.command, 40)
            );
        } else if deep {
            let _ = writeln!(
                out,
                "{:<7}  {thr:<6}  {:<10}  {cpu:<10}  {:<5}  {row}{}",
                p.pid,
                human_size_kib(p.rss_kib),
                fd_string(p),
                truncate(&p.command, 45)
            );
        } else {
            let _ = writeln!(
                out,
                "{:<7}  {:<7}  {:<10}  {:<10}  {cpu:<7}  {:<7}  {:<20}  {row}{}",
                p.pid,
                p.ppid,
                human_size_kib(p.rss_kib),
                human_size_bytes(vsz_bytes(p)),
                percent_string(p.mem_percent),
                started_string(&p.started_at),
                truncate(&p.command, 40)
            );
        }
    }
}

fn write_tree(out: &mut dyn Write, roots: &[TreeNode], cols: &ClassColumns) {
    let _ = writeln!(out, "{:<7}  {:<7}  {:<20}  {}COMMAND", "PID", "PPID", "STARTED", cols.header());
    let _ = writeln!(out, "{}", "-".repeat(76 + cols.width()));
    for r in roots {
        write_tree_node(out, r, 0, cols);
    }
}

fn write_tree_node(out: &mut dyn Write, node: &TreeNode, depth: usize, cols: &ClassColumns) {
    let p = &node.info;
    let width = 50usize.saturating_sub(depth * 2).max(20);
    let _ = writeln!(
        out,
        "{:<7}  {:<7}  {:<20}  {}{}{}",
        p.pid,
        p.ppid,
        started_string(&p.started_at),
        cols.row(p),
        "  ".repeat(depth),
        truncate(&p.command, width)
    );
    for c in &node.children {
        write_tree_node(out, c, depth + 1, cols);
    }
}

/// Go's `printProcessFamilies`.
fn write_families(out: &mut dyn Write, fams: &[Family]) {
    let _ = writeln!(out, "{:<48}  {:<6}  {:<9}  {:<6}  {:<9}  HEAVIEST", "FAMILY", "PROCS", "CPU%", "SHARE", "AGE");
    let _ = writeln!(out, "{}", "-".repeat(110));
    for f in fams {
        let age = if f.age.0 > 0 { format_duration(f.age) } else { "-".into() };
        let _ = writeln!(
            out,
            "{:<48}  {:<6}  {:<9}  {:<6}  {age:<9}  {}",
            truncate(&f.name, 48),
            f.members,
            format!("{:.1}%", f.cpu_pct),
            format!("{:.0}%", f.share_pct),
            format_members(&f.heaviest)
        );
    }
}

/// Go's `whatswrong.FormatMembers`.
pub fn format_members(ms: &[process::Member]) -> String {
    ms.iter().map(|m| format!("{} (pid {}, {:.0}%)", m.command, m.pid, m.cpu_pct)).collect::<Vec<_>>().join(", ")
}

fn fd_string(p: &Info) -> String {
    if p.open_fds > 0 { p.open_fds.to_string() } else { "-".into() }
}

/// Go's `humanSizeKiB`.
pub fn human_size_kib(kib: i64) -> String {
    const MIB: i64 = 1024;
    const GIB: i64 = 1024 * MIB;
    match kib {
        0 => "-".into(),
        k if k >= GIB => format!("{:.1}G", k as f64 / GIB as f64),
        k if k >= MIB => format!("{:.1}M", k as f64 / MIB as f64),
        k => format!("{k}K"),
    }
}

/// Go's `humanSizeBytes`.
pub fn human_size_bytes(bytes: u64) -> String {
    const KIB: u64 = 1024;
    const MIB: u64 = 1024 * KIB;
    const GIB: u64 = 1024 * MIB;
    match bytes {
        0 => "-".into(),
        b if b >= GIB => format!("{:.1}G", b as f64 / GIB as f64),
        b if b >= MIB => format!("{:.1}M", b as f64 / MIB as f64),
        b if b >= KIB => format!("{}K", b / KIB),
        b => format!("{b}B"),
    }
}

fn percent_string(v: f64) -> String {
    if v <= 0.0 { "-".into() } else { format!("{v:.1}%") }
}

/// Go's `startedString`: `2006-01-02 15:04` in the time's own zone.
fn started_string(t: &GoTime) -> String {
    if t.is_zero() {
        return "-".into();
    }
    let dt = t.offset.to_datetime(t.instant);
    format!("{:04}-{:02}-{:02} {:02}:{:02}", dt.year(), dt.month(), dt.day(), dt.hour(), dt.minute())
}

/// Go's `truncate`: at most `n` runes, the last replaced by `…` when cut.
pub fn truncate(s: &str, n: usize) -> String {
    if s.chars().count() <= n {
        return s.to_string();
    }
    let mut out: String = s.chars().take(n.saturating_sub(1)).collect();
    out.push('…');
    out
}

/// Go's `dashIfEmpty`.
pub fn dash_if_empty(s: &str) -> String {
    if s.is_empty() { "-".into() } else { s.to_string() }
}

/// Go's `formatDuration` (`cmd/spectra/inspect.go`): `1d2h`, `3h4m`, `5m6s`, `7s`.
pub fn format_duration(d: GoDuration) -> String {
    const S: i64 = 1_000_000_000;
    const M: i64 = 60 * S;
    const H: i64 = 60 * M;
    let d = d.0;
    // Go's Duration.Hours/Minutes/Seconds, truncated by int().
    let hours = ((d / H) as f64 + (d % H) as f64 / (H as f64)) as i64;
    let minutes = ((d / M) as f64 + (d % M) as f64 / (M as f64)) as i64;
    let seconds = ((d / S) as f64 + (d % S) as f64 / 1e9) as i64;
    if d >= 24 * H {
        format!("{}d{}h", hours / 24, hours % 24)
    } else if d >= H {
        format!("{hours}h{}m", minutes % 60)
    } else if d >= M {
        format!("{minutes}m{}s", seconds % 60)
    } else {
        format!("{seconds}s")
    }
}

#[cfg(test)]
mod tests;
