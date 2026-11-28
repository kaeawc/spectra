//! A point-in-time snapshot of every running process (Go's `internal/process`).
//!
//! On macOS one `ps` listing (plus a `comm` listing for executables whose path has a space) is
//! enriched with libproc details through a [`ProcessSource`]; on Linux `/proc` is read through
//! [`Fs`]. `--deep` adds one batched `lsof`. The OS comes from [`Seams::os`], so both paths run,
//! against fixtures, on any host.

mod argv;
mod deep;
mod family;
mod launch;
mod linux;
mod lstart;
mod sample;

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::time::Duration;

use jiff::Timestamp;
use jiff::tz::TimeZone;
use serde::{Deserialize, Serialize};
use spectra_base::clock::Clock;
use spectra_base::env::{Env, stable_time_env};
use spectra_base::fs::Fs;
use spectra_base::gojson::{self, GoDuration, GoTime};
use spectra_base::hostos::Kind;
use spectra_base::proc::{CancelToken, Cmd, Runner};

use crate::classify::{self, App, Cancelled, Classifier, Role, Runtime, Signing};
use crate::goerr;

pub use argv::{MAX_ARGV_BYTES, MAX_ARGV_COUNT, argv as host_argv_for, argv_matches, host_argv, parse_proc_args2, split_cmdline};
pub use deep::{is_log_shaped_path, parse_lsof_deep};
pub use family::{
    FAMILY_BROWSER, FAMILY_BUILD, FAMILY_EMULATOR, FAMILY_OTHER, FAMILY_SIMULATOR, FAMILY_XCODE, Family, Member, group_families,
};
pub use launch::{CwdReader, parse_lsof_name};
pub use linux::{LINUX_CLK_TCK, lookup_passwd_uid, parse_btime, parse_linux_cpu_times};
pub use lstart::parse_lstart;
pub use sample::{
    CommandRunner, OsReportFiles, ReportFiles, RunFn, RunnerCommands, SampleError, SampleOptions, SampleResult, SampleStore, Sampler,
    run_sample_report,
};

/// One running process at snapshot time (Go's `Info`); field order and `omitempty` match its
/// JSON.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Info {
    pub pid: i64,
    pub ppid: i64,
    pub uid: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub user: String,
    /// Short name: the executable's base name.
    pub command: String,
    /// `p_comm` from libproc, when available.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub bsd_name: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub executable_path: String,
    /// Linux: `/proc/<pid>/exe` reports the file as deleted; `executable_path` is where it was.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub executable_deleted: bool,
    pub full_command_line: String,
    /// The argument vector with its boundaries intact; read only during classification and only
    /// for interpreters.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub argv: Vec<String>,
    pub rss_kib: i64,
    pub vsize_kib: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub vsz_bytes: u64,
    pub thread_count: i64,
    pub cpu_pct: f64,
    pub cpu_percent: f64,
    pub mem_percent: f64,
    /// Go's `omitempty` never omits a `time.Time`; the zero time prints.
    pub start_time: GoTime,
    pub started_at: GoTime,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub elapsed: GoDuration,

    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub open_fds: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub fd_breakdown: Option<FdBreakdown>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub listening_ports: Vec<i64>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub outbound_connections: Vec<String>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub log_files: Vec<String>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub cwd: String,

    /// The known `.app` bundle the executable lives in, when bundle paths were given.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub app_path: String,

    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub role: Role,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub runtime: Runtime,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub app: Option<App>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub signing: Option<Signing>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub evidence: Vec<String>,
}

/// Open descriptors grouped by `lsof` TYPE and path (Go's `FDBreakdown`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
pub struct FdBreakdown {
    pub total: i64,
    pub pty: i64,
    pub socket: i64,
    pub regular: i64,
    pub dir: i64,
    pub pipe: i64,
    pub char: i64,
    pub kqueue: i64,
    pub other: i64,
}

/// Per-process metadata libproc gives without extra tools (Go's `Details`). Recorded cases hold
/// one per pid in this shape.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Details {
    pub thread_count: i64,
    pub bsd_name: String,
    pub executable_path: String,
}

/// Where the macOS-only process facts come from, so recorded cases replay without a live
/// host. The real one is [`HostProcesses`]; [`FixedProcesses`] answers from a recording.
pub trait ProcessSource: Send + Sync {
    /// libproc's thread count, BSD name and executable path per pid (Go's
    /// `collectProcessDetails`): only pids with at least one of them.
    fn details(&self, pids: &[i64]) -> BTreeMap<i64, Details>;
    /// `hw.memsize`, 0 when unknown (Go's `systemMemoryBytes`; 0 off macOS).
    fn memory_bytes(&self) -> u64;
    /// The kernel page size the Linux reader converts RSS pages with.
    fn page_size(&self) -> u64;
    /// `pid`'s argv from `kern.procargs2` (macOS).
    fn argv(&self, pid: i64) -> Result<Vec<String>, String>;
}

/// The live host, through `spectra_sys`. Off macOS only the page size is real, as in Go's
/// non-cgo build.
#[derive(Clone, Copy, Debug, Default)]
pub struct HostProcesses;

impl ProcessSource for HostProcesses {
    #[cfg(target_os = "macos")]
    fn details(&self, pids: &[i64]) -> BTreeMap<i64, Details> {
        use spectra_sys::darwin as sys;
        let mut out = BTreeMap::new();
        for &pid in pids {
            let Ok(p) = i32::try_from(pid) else { continue };
            if p <= 0 {
                continue;
            }
            let mut d = Details::default();
            if let Ok(info) = sys::proc_task_all_info(p) {
                if info.thread_count > 0 {
                    d.thread_count = i64::from(info.thread_count);
                }
                d.bsd_name = info.bsd_name;
            }
            if let Ok(path) = sys::proc_pidpath(p) {
                d.executable_path = path;
            }
            if d.thread_count > 0 || !d.bsd_name.is_empty() || !d.executable_path.is_empty() {
                out.insert(pid, d);
            }
        }
        out
    }

    #[cfg(not(target_os = "macos"))]
    fn details(&self, _pids: &[i64]) -> BTreeMap<i64, Details> {
        BTreeMap::new()
    }

    #[cfg(target_os = "macos")]
    fn memory_bytes(&self) -> u64 {
        spectra_sys::darwin::sysctl_u64("hw.memsize").unwrap_or(0)
    }

    #[cfg(not(target_os = "macos"))]
    fn memory_bytes(&self) -> u64 {
        0
    }

    #[cfg(unix)]
    fn page_size(&self) -> u64 {
        spectra_sys::page_size()
    }

    #[cfg(not(unix))]
    fn page_size(&self) -> u64 {
        4096
    }

    #[cfg(target_os = "macos")]
    fn argv(&self, pid: i64) -> Result<Vec<String>, String> {
        let p = i32::try_from(pid).map_err(|e| e.to_string())?;
        let raw = spectra_sys::darwin::procargs2(p).map_err(|e| format!("kern.procargs2 pid {pid}: {}", goerr::errno_text(&e)))?;
        parse_proc_args2(&raw)
    }

    #[cfg(not(target_os = "macos"))]
    fn argv(&self, _pid: i64) -> Result<Vec<String>, String> {
        Err("kern.procargs2 is macOS only".to_string())
    }
}

/// A recorded set of macOS process facts (and the Linux page size).
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
#[serde(default)]
pub struct FixedProcesses {
    pub details: BTreeMap<i64, Details>,
    pub memory_bytes: u64,
    pub page_size: u64,
    /// argv per pid; a pid without one fails to read.
    pub argv: BTreeMap<i64, Vec<String>>,
}

impl ProcessSource for FixedProcesses {
    fn details(&self, pids: &[i64]) -> BTreeMap<i64, Details> {
        pids.iter().filter_map(|p| self.details.get(p).map(|d| (*p, d.clone()))).collect()
    }

    fn memory_bytes(&self) -> u64 {
        self.memory_bytes
    }

    fn page_size(&self) -> u64 {
        self.page_size
    }

    fn argv(&self, pid: i64) -> Result<Vec<String>, String> {
        self.argv.get(&pid).cloned().ok_or_else(|| format!("kern.procargs2 pid {pid}: no such process"))
    }
}

/// What collection runs against.
#[derive(Clone, Copy)]
pub struct Seams<'a> {
    /// [`Kind::Unknown`] means the host.
    pub os: Kind,
    /// Runs `ps` and `lsof`.
    pub runner: &'a dyn Runner,
    /// The environment `ps` inherits (with `TZ=UTC0 LC_ALL=C` forced).
    pub env: &'a dyn Env,
    /// Linux `/proc` and `/etc/passwd`.
    pub fs: &'a dyn Fs,
    pub clock: &'a dyn Clock,
    pub source: &'a dyn ProcessSource,
    /// Go's `time.Local`: start times are shown in it.
    pub tz: &'a TimeZone,
    /// The Linux procfs root; empty means `/proc`.
    pub proc_fs: &'a str,
}

/// What to collect (Go's `CollectOptions` without its seams).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CollectOptions {
    /// Each process whose executable lives inside one of these `.app` paths gets `app_path`.
    pub bundle_paths: Vec<String>,
    /// Enrich with open FDs, listening ports and connections from one batched `lsof`.
    pub deep: bool,
}

/// The timeout Go's `defaultRunner` gives `ps` and `lsof`.
const COMMAND_TIMEOUT: Duration = Duration::from_secs(5);

/// The `ps` columns: pid ppid pcpu rss vsz uid user lstart(5 tokens) command...
pub const PS_COLUMNS: &str = "pid=,ppid=,pcpu=,rss=,vsz=,uid=,user=,lstart=,command=";

/// Go's `CollectAll`: every running process. Rows that do not parse are skipped.
pub fn collect_all(s: &Seams<'_>, opts: &CollectOptions) -> Vec<Info> {
    let now = s.clock.now();
    if s.os.resolve() == Kind::Linux {
        let root = if s.proc_fs.is_empty() { "/proc" } else { s.proc_fs };
        let mut procs = linux::collect(s, root, now);
        apply_derived_fields(&mut procs, now, s.source.memory_bytes());
        finish(s, opts, &mut procs);
        return procs;
    }
    let Ok(out) = run(s, &Cmd::new("ps", ["-axwwo", PS_COLUMNS]).env(stable_time_env(s.env))) else {
        return Vec::new();
    };
    let mut procs = parse_ps(&String::from_utf8_lossy(&out), s.tz);
    apply_comm_listing(s, &mut procs);
    apply_derived_fields(&mut procs, now, s.source.memory_bytes());
    apply_process_details(&mut procs, s.source);
    finish(s, opts, &mut procs);
    procs
}

fn finish(s: &Seams<'_>, opts: &CollectOptions, procs: &mut [Info]) {
    if !opts.bundle_paths.is_empty() {
        attribute_bundles(procs, &opts.bundle_paths);
    }
    if opts.deep && !procs.is_empty() {
        deep::enrich(procs, |cmd| run_partial(s, cmd));
    }
}

/// Go's `defaultRunner` (`exec.Cmd.Output`): stdout, and an error for a failed start or a
/// non-zero exit. `ps` always runs with `TZ=UTC0 LC_ALL=C`.
fn run(s: &Seams<'_>, cmd: &Cmd) -> Result<Vec<u8>, String> {
    let (out, err) = run_partial(s, cmd);
    match err {
        Some(e) => Err(e),
        None => Ok(out),
    }
}

/// Like [`run`], but keeps what a failing command printed (`lsof` exits 1 when one pid of many
/// has gone, and Go still parses its output).
fn run_partial(s: &Seams<'_>, cmd: &Cmd) -> (Vec<u8>, Option<String>) {
    let mut cmd = cmd.clone();
    if cmd.name == "ps" && cmd.env.is_none() {
        cmd = cmd.env(stable_time_env(s.env));
    }
    match s.runner.run(&cmd.timeout(COMMAND_TIMEOUT)) {
        Ok(out) if out.exit_code == 0 => (out.stdout, None),
        Ok(out) => {
            let code = out.exit_code;
            (out.stdout, Some(format!("exit status {code}")))
        }
        Err(e) => (Vec::new(), Some(e.to_string())),
    }
}

/// Go's `applyDerivedFields`: VSZ in bytes, the CPU and start-time aliases, elapsed time and the
/// share of physical memory.
pub fn apply_derived_fields(procs: &mut [Info], now: Timestamp, mem_bytes: u64) {
    let now_ns = now.as_nanosecond();
    for p in procs {
        if p.vsize_kib > 0 {
            p.vsz_bytes = kib_to_bytes(p.vsize_kib);
        }
        p.cpu_percent = p.cpu_pct;
        p.started_at = p.start_time;
        if !p.started_at.is_zero() && now_ns > p.started_at.instant.as_nanosecond() {
            p.elapsed = GoDuration(round_second(sub_nanos(now_ns, p.started_at.instant.as_nanosecond())));
        }
        if mem_bytes > 0 && p.rss_kib > 0 {
            p.mem_percent = kib_to_bytes(p.rss_kib) as f64 / mem_bytes as f64 * 100.0;
        }
    }
}

/// Go's `t.Sub(u)`: the difference in nanoseconds, saturating at `Duration`'s range.
pub(crate) fn sub_nanos(t: i128, u: i128) -> i64 {
    let d = t - u;
    i64::try_from(d).unwrap_or(if d < 0 { i64::MIN } else { i64::MAX })
}

/// Go's `d.Round(time.Second)`.
pub(crate) fn round_second(d: i64) -> i64 {
    const M: i64 = 1_000_000_000;
    let less_than_half = |x: i64| (x as u64).wrapping_add(x as u64) < M as u64;
    let r = d % M;
    if d < 0 {
        let r = -r;
        if less_than_half(r) {
            return d + r;
        }
        return d.checked_sub(M - r).unwrap_or(i64::MIN);
    }
    if less_than_half(r) {
        return d - r;
    }
    d.checked_add(M - r).unwrap_or(i64::MAX)
}

/// Go's `Duration.Seconds`.
pub(crate) fn duration_seconds(d: i64) -> f64 {
    let (sec, nsec) = (d / 1_000_000_000, d % 1_000_000_000);
    sec as f64 + nsec as f64 / 1e9
}

/// `instant` in the zone `tz` (Go's `t.In(time.Local)`).
pub fn in_zone(tz: &TimeZone, instant: Timestamp) -> GoTime {
    GoTime { instant, offset: tz.to_offset(instant) }
}

fn kib_to_bytes(kib: i64) -> u64 {
    if kib <= 0 {
        return 0;
    }
    (kib as u64).wrapping_mul(1024)
}

fn apply_process_details(procs: &mut [Info], source: &dyn ProcessSource) {
    if procs.is_empty() {
        return;
    }
    let pids: Vec<i64> = procs.iter().map(|p| p.pid).collect();
    let details = source.details(&pids);
    for p in procs {
        let Some(d) = details.get(&p.pid) else { continue };
        if d.thread_count > 0 {
            p.thread_count = d.thread_count;
        }
        if !d.bsd_name.is_empty() {
            p.bsd_name = d.bsd_name.clone();
        }
        if !d.executable_path.is_empty() {
            p.executable_path = d.executable_path.clone();
        }
    }
}

/// Go's `strconv.Atoi` with the error ignored: 0 for a syntax error, the clamped limit for a
/// range error.
pub(crate) fn atoi(s: &str) -> i64 {
    goerr::parse_int(s).unwrap_or_else(|e| e.value)
}

/// Go's `parsePS`.
pub fn parse_ps(raw: &str, tz: &TimeZone) -> Vec<Info> {
    raw.split('\n').map(str::trim).filter(|l| !l.is_empty()).map(|l| parse_row(l, tz)).filter(|p| p.pid > 0).collect()
}

/// Go's `parseRow`: pid ppid pcpu rss vsz uid user <lstart: 5 tokens> command...
pub fn parse_row(line: &str, tz: &TimeZone) -> Info {
    let fields: Vec<&str> = line.split_whitespace().collect();
    if fields.len() < 13 {
        return Info::default();
    }
    let cpu = goerr::parse_float(fields[2]).unwrap_or_else(|e| e.value);
    let vsz = atoi(fields[4]);
    // ps ran with TZ=UTC0 LC_ALL=C; present the instant in the local zone.
    let start = parse_lstart(&fields[7..12].join(" ")).map_or_else(GoTime::zero, |t| in_zone(tz, t));
    let full = fields[12..].join(" ");
    Info {
        pid: atoi(fields[0]),
        ppid: atoi(fields[1]),
        cpu_pct: cpu,
        cpu_percent: cpu,
        rss_kib: atoi(fields[3]),
        vsize_kib: vsz,
        vsz_bytes: kib_to_bytes(vsz),
        uid: atoi(fields[5]),
        user: fields[6].to_string(),
        command: short_name(&full),
        full_command_line: full,
        start_time: start,
        started_at: start,
        ..Info::default()
    }
}

/// Go's `shortName`: the first word's last path component.
pub fn short_name(cmd: &str) -> String {
    let Some(exe) = cmd.split_whitespace().next() else { return String::new() };
    exe.rsplit('/').next().unwrap_or(exe).to_string()
}

/// Go's `applyCommListing`: `command=` cannot separate an executable path with a space from its
/// arguments; `comm=` (argv[0] alone, untruncated as the last column) can.
fn apply_comm_listing(s: &Seams<'_>, procs: &mut [Info]) {
    if procs.is_empty() {
        return;
    }
    if let Ok(out) = run(s, &Cmd::new("ps", ["-axwwo", "pid=,comm="]).env(stable_time_env(s.env))) {
        apply_exec_names(procs, &parse_ps_comm(&String::from_utf8_lossy(&out)));
    }
}

/// Go's `parsePSComm`: pid to argv[0].
pub fn parse_ps_comm(raw: &str) -> HashMap<i64, String> {
    let mut out = HashMap::new();
    for line in raw.split('\n') {
        let Some((pid, comm)) = line.trim().split_once(' ') else { continue };
        let comm = comm.trim_start_matches(' ');
        let Ok(pid) = goerr::parse_int(pid) else { continue };
        if pid <= 0 || comm.is_empty() {
            continue;
        }
        out.insert(pid, comm.to_string());
    }
    out
}

/// Go's `applyExecNames`.
pub fn apply_exec_names(procs: &mut [Info], comms: &HashMap<i64, String>) {
    for p in procs {
        let argv0 = comms.get(&p.pid).map_or("", String::as_str);
        let name = exec_name(&p.full_command_line, argv0);
        if !name.is_empty() {
            p.command = name;
        }
    }
}

/// Go's `execName`: argv[0]'s base name, trusted only when it begins the same pid's command line
/// (which also rejects a pid recycled between the two listings).
pub fn exec_name(full: &str, argv0: &str) -> String {
    let norm = argv0.split_whitespace().collect::<Vec<_>>().join(" ");
    if norm.is_empty() || (full != norm && !full.starts_with(&format!("{norm} "))) {
        return String::new();
    }
    argv0.rsplit('/').next().unwrap_or(argv0).to_string()
}

/// Go's `attributeBundles`.
pub fn attribute_bundles(procs: &mut [Info], bundles: &[String]) {
    for p in procs.iter_mut() {
        if let Some(b) = bundles.iter().find(|b| process_belongs_to_bundle(p, b)) {
            p.app_path = b.clone();
        }
    }
}

fn process_belongs_to_bundle(p: &Info, bundle: &str) -> bool {
    path_inside_bundle(&p.executable_path, bundle) || p.full_command_line.starts_with(bundle)
}

fn path_inside_bundle(path: &str, bundle: &str) -> bool {
    if path.is_empty() || bundle.is_empty() {
        return false;
    }
    path == bundle || path.starts_with(&format!("{}/", bundle.trim_end_matches('/')))
}

/// Go's `GroupByApp`: processes by `app_path` ("" for none).
pub fn group_by_app(procs: &[Info]) -> BTreeMap<String, Vec<Info>> {
    let mut m: BTreeMap<String, Vec<Info>> = BTreeMap::new();
    for p in procs {
        m.entry(p.app_path.clone()).or_default().push(p.clone());
    }
    m
}

/// Go's `TotalRSS`.
pub fn total_rss(procs: &[Info]) -> i64 {
    procs.iter().map(|p| p.rss_kib).sum()
}

/// One process in the parent-child hierarchy (Go's `TreeNode`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct TreeNode {
    pub info: Info,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub children: Vec<TreeNode>,
}

/// Go's `BuildTree`: a process whose parent is not listed, or is itself, is a root. Children
/// keep the listing's order.
pub fn build_tree(procs: &[Info]) -> Vec<TreeNode> {
    // Go keys nodes by pid, so a duplicated pid's later row wins and both rows share it.
    let mut index: HashMap<i64, usize> = HashMap::new();
    for (i, p) in procs.iter().enumerate() {
        index.insert(p.pid, i);
    }
    let mut children: HashMap<i64, Vec<i64>> = HashMap::new();
    let mut roots = Vec::new();
    for p in procs {
        if index.contains_key(&p.ppid) && p.ppid != p.pid {
            children.entry(p.ppid).or_default().push(p.pid);
        } else {
            roots.push(p.pid);
        }
    }
    let mut building = BTreeSet::new();
    roots.iter().map(|&pid| node(pid, procs, &index, &children, &mut building)).collect()
}

fn node(
    pid: i64,
    procs: &[Info],
    index: &HashMap<i64, usize>,
    children: &HashMap<i64, Vec<i64>>,
    building: &mut BTreeSet<i64>,
) -> TreeNode {
    let info = procs[index[&pid]].clone();
    // Go's pointers make a cycle's encoding fail; here a pid already on the path stops it.
    if !building.insert(pid) {
        return TreeNode { info, children: Vec::new() };
    }
    let kids = children.get(&pid).map_or_else(Vec::new, |c| c.iter().map(|&c| node(c, procs, index, children, building)).collect());
    building.remove(&pid);
    TreeNode { info, children: kids }
}

/// Collects process inventory (Go's `Collector`).
pub trait Collector {
    fn collect(&self, opts: &CollectOptions) -> Vec<Info>;
}

impl<F: Fn(&CollectOptions) -> Vec<Info>> Collector for F {
    fn collect(&self, opts: &CollectOptions) -> Vec<Info> {
        self(opts)
    }
}

/// The local system through `seams` (Go's `SystemCollector`).
pub struct SystemCollector<'a>(pub Seams<'a>);

impl Collector for SystemCollector<'_> {
    fn collect(&self, opts: &CollectOptions) -> Vec<Info> {
        collect_all(&self.0, opts)
    }
}

/// Tree collection options (Go's `TreeOptions`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TreeOptions {
    pub bundles: Vec<String>,
    pub deep: bool,
}

/// Builds process trees from a collector (Go's `TreeService`).
pub struct TreeService<'a> {
    collector: &'a dyn Collector,
}

impl<'a> TreeService<'a> {
    pub fn new(collector: &'a dyn Collector) -> Self {
        TreeService { collector }
    }

    /// Collects and returns the tree; with bundles, only their processes and ancestors.
    pub fn build(&self, opts: &TreeOptions) -> Vec<TreeNode> {
        let mut procs = self.collector.collect(&CollectOptions { bundle_paths: opts.bundles.clone(), deep: opts.deep });
        if !opts.bundles.is_empty() {
            procs = scope_tree_processes(&procs);
        }
        build_tree(&procs)
    }
}

/// Go's `ScopeTreeProcesses`: app-attributed processes and their ancestors.
pub fn scope_tree_processes(procs: &[Info]) -> Vec<Info> {
    let mut by_pid: HashMap<i64, &Info> = HashMap::new();
    let mut keep = BTreeSet::new();
    for p in procs {
        by_pid.insert(p.pid, p);
        if !p.app_path.is_empty() {
            keep.insert(p.pid);
        }
    }
    for p in procs.iter().filter(|p| !p.app_path.is_empty()) {
        let mut cur = p;
        // Go's loop has no cycle guard beyond self-parenthood; bound it by the listing size.
        for _ in 0..procs.len() {
            match by_pid.get(&cur.ppid) {
                Some(parent) if parent.pid != cur.pid => {
                    keep.insert(parent.pid);
                    cur = parent;
                }
                _ => break,
            }
        }
    }
    procs.iter().filter(|p| keep.contains(&p.pid)).cloned().collect()
}

/// Go's `process.ClassifyWithArgv`: fills role, runtime, app, signing and evidence in place.
/// `argv(pid, full)` reads a pid's argv (empty when it no longer matches `full`); it is called
/// only for interpreters, and what it returned is kept on the process.
pub fn classify(procs: &mut [Info], c: &Classifier, argv: &dyn Fn(i64, &str) -> Vec<String>, cancel: &CancelToken) -> Option<Cancelled> {
    let inputs: Vec<classify::Input> = procs
        .iter()
        .map(|p| classify::Input {
            pid: p.pid,
            ppid: p.ppid,
            uid: p.uid,
            command: p.command.clone(),
            executable_path: p.executable_path.clone(),
            executable_deleted: p.executable_deleted,
            command_line: p.full_command_line.clone(),
            argv: p.argv.clone(),
            load_argv: p.argv.is_empty() && p.pid > 0,
        })
        .collect();
    let load = |i: usize| argv(inputs[i].pid, &inputs[i].command_line);
    let (results, err) = c.classify(&inputs, &load, cancel);
    for (p, r) in procs.iter_mut().zip(results) {
        p.role = r.role;
        p.runtime = r.runtime;
        p.app = r.app;
        p.signing = r.signing;
        p.evidence = r.evidence;
        if let Some(a) = r.loaded_argv {
            p.argv = a;
        }
    }
    err
}

/// A fresh, empty directory under the system temp dir for a test.
#[cfg(test)]
pub(crate) fn test_dir(name: &str) -> std::path::PathBuf {
    static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let n = SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let dir = std::env::temp_dir().join(format!("spectra-host-{name}-{}-{n}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("test dir");
    dir
}

#[cfg(test)]
mod tests;
