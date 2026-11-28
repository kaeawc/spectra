//! "Why is this Linux process slow or stuck?" from `/proc` alone (Go's `internal/procdiag`):
//! every thread is sampled twice, an interval apart, and the deltas are reasoned about. A single
//! snapshot cannot tell a leak from a steady state or a stall from a blink, so the two-sample
//! delta is the point.
//!
//! Every read goes through [`Fs`] under [`Options::proc_root`] and [`Options::cgroup_root`], the
//! wait through [`Sleeper`] and the timestamps through [`Clock`], so the analysis runs against
//! fixture trees on any host. Unreadable files (another user's process) are reported in
//! [`Report::notes`] and the checks that need them are skipped.

mod diagnose;
mod syscalls;

use std::collections::{BTreeMap, BTreeSet};
use std::fmt;
use std::io;
use std::path::Path;
use std::time::Duration;

use serde::{Serialize, Serializer};
use spectra_base::clock::{Clock, Sleeper};
use spectra_base::fs::Fs;
use spectra_base::gojson::{self, GoDuration};

use crate::goerr;
pub use crate::pressure::Severity;
use crate::pressure::gopath::join;
use crate::pressure::{self, Cgroup};

/// USER_HZ; 100 on effectively every Linux target (Go reads it as a constant too).
const CLK_TCK: f64 = 100.0;

/// Bounds [`Report::threads`]: threads are ranked by CPU, then by a non-sleeping state, so the
/// interesting ones win.
pub const MAX_REPORTED_THREADS: usize = 32;

/// Finding kinds.
pub const KIND_ZOMBIE: &str = "zombie";
pub const KIND_STOPPED: &str = "stopped";
pub const KIND_UNINTERRUPT: &str = "uninterruptible-io";
pub const KIND_LOCK_WAIT: &str = "lock-wait";
pub const KIND_BLOCKED_SYS: &str = "blocked-syscall";
pub const KIND_CPU_SPIN: &str = "cpu-spin";
pub const KIND_FD_EXHAUSTION: &str = "fd-exhaustion";
pub const KIND_FD_GROWTH: &str = "fd-growth";
pub const KIND_MEMORY_GROWTH: &str = "memory-growth";
pub const KIND_SWAPPED_OUT: &str = "swapped-out";
pub const KIND_ACTIVE: &str = "active";
pub const KIND_IDLE: &str = "idle";

/// [`Finding::scope`] of a finding about the process's whole cgroup. Such findings are context:
/// they set neither the verdict nor the exit status.
pub const SCOPE_CGROUP: &str = "cgroup";

/// What [`inspect`] reads and how long it waits.
pub struct Options<'a> {
    pub fs: &'a dyn Fs,
    pub clock: &'a dyn Clock,
    pub sleeper: &'a dyn Sleeper,
    /// The procfs mount; empty means `/proc`.
    pub proc_root: String,
    /// The cgroup v2 mount; empty means `/sys/fs/cgroup`.
    pub cgroup_root: String,
    /// Time between the two samples; 1s when not positive.
    pub interval: GoDuration,
    /// Selects the syscall-name table (`amd64` or `arm64`, as Go's `GOARCH`); empty means the
    /// host's.
    pub arch: String,
}

/// Why there is no report.
#[derive(Debug)]
pub enum Error {
    /// The pid does not exist or exited mid-sample (Go's `ErrNoProcess`); `context` is what the
    /// Go error wraps it with (`pid 5`, `list threads of pid 5`).
    NoProcess { context: String },
    /// `<proc>/<pid>/stat` could not be read for another reason.
    ReadStat { pid: i64, path: String, source: io::Error },
}

impl Error {
    /// Go's `errors.Is(err, ErrNoProcess)`.
    pub fn is_no_process(&self) -> bool {
        matches!(self, Error::NoProcess { .. })
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::NoProcess { context } => write!(f, "{context}: no such process"),
            Error::ReadStat { pid, path, source } => write!(f, "read stat of pid {pid}: {}", goerr::path_error("open", path, source)),
        }
    }
}

impl std::error::Error for Error {}

/// The second-sample state of one thread plus its activity over the interval.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Thread {
    pub tid: i64,
    pub comm: String,
    pub state: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub wchan: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub syscall: String,
    /// This thread's share of one core over the interval.
    pub cpu_pct: f64,
    /// The part of [`Thread::cpu_pct`] spent in the kernel.
    pub system_pct: f64,
    /// Voluntary plus involuntary context switches over the interval.
    pub ctx_switches: i64,
}

/// The descriptor table usage.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct Fds {
    pub count: i64,
    pub limit: i64,
    pub delta: i64,
    pub rate_per_sec: f64,
}

/// The process's memory in KiB.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct Memory {
    pub rss_kib: i64,
    pub anon_kib: i64,
    pub swap_kib: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub pss_kib: i64,
    pub rss_delta_kib: i64,
    #[serde(rename = "rss_rate_kib_per_sec")]
    pub rss_rate_kib_ps: f64,
}

/// Storage I/O over the interval: bytes that reached or came from the block layer.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Io {
    pub read_bytes: i64,
    pub write_bytes: i64,
}

/// One conclusion, with the raw observations that support it.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Finding {
    pub kind: String,
    pub severity: Severity,
    pub summary: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub evidence: Vec<String>,
    /// Empty for findings about this process, [`SCOPE_CGROUP`] for its whole cgroup.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub scope: String,
}

impl Finding {
    /// Whether the finding is about the process itself.
    pub fn process_scoped(&self) -> bool {
        self.scope != SCOPE_CGROUP
    }
}

/// The result of inspecting one process (Go's `procdiag.Report`); field order and `omitempty`
/// match its JSON. An empty `threads` or `findings` is Go's nil slice, `null`.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Report {
    pub pid: i64,
    pub comm: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub cmdline: String,
    pub state: String,
    pub interval_sec: f64,
    pub cpu_pct: f64,
    pub thread_count: i64,
    pub state_counts: BTreeMap<String, i64>,
    #[serde(serialize_with = "null_if_empty")]
    pub threads: Vec<Thread>,
    pub fds: Fds,
    pub memory: Memory,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub io: Option<Io>,
    pub oom_score: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub cgroup: Option<Cgroup>,
    #[serde(serialize_with = "null_if_empty")]
    pub findings: Vec<Finding>,
    pub verdict: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub notes: Vec<String>,
}

impl Report {
    /// Go's `exitForFindings`: 3 when a process-scoped finding is worse than info, else 0.
    pub fn exit_code(&self) -> i32 {
        if self.findings.iter().any(|f| f.process_scoped() && f.severity != Severity::Info) { 3 } else { 0 }
    }
}

fn null_if_empty<S: Serializer, T: Serialize>(v: &[T], s: S) -> Result<S::Ok, S::Error> {
    if v.is_empty() { s.serialize_none() } else { v.serialize(s) }
}

#[derive(Clone, Debug, Default)]
struct TaskSample {
    comm: String,
    state: String,
    wchan: String,
    /// -2 unreadable, -1 running or not in a syscall.
    syscall_nr: i64,
    cpu_ticks: i64,
    sys_ticks: i64,
    ctx: i64,
}

#[derive(Debug, Default)]
struct ProcSample {
    /// Nanoseconds since the Unix epoch.
    at: i128,
    tasks: BTreeMap<i64, TaskSample>,
    fds: i64,
    rss_kib: i64,
    io_read: i64,
    io_write: i64,
    have_io: bool,
    notes: Vec<String>,
    proc_name: String,
    state: String,
}

/// Go's `GOARCH` for the host.
fn host_arch() -> &'static str {
    match std::env::consts::ARCH {
        "aarch64" => "arm64",
        "x86_64" => "amd64",
        other => other,
    }
}

struct Resolved<'a> {
    fs: &'a dyn Fs,
    proc_root: String,
    cgroup_root: String,
    interval: i64,
    arch: String,
}

/// Go's `Inspect`: samples `pid` twice and returns the diagnosis, or [`Error::NoProcess`] when
/// the process is gone.
pub fn inspect(pid: i64, opts: &Options<'_>) -> Result<Report, Error> {
    let o = Resolved {
        fs: opts.fs,
        proc_root: if opts.proc_root.is_empty() { "/proc".to_string() } else { opts.proc_root.clone() },
        cgroup_root: if opts.cgroup_root.is_empty() { "/sys/fs/cgroup".to_string() } else { opts.cgroup_root.clone() },
        interval: if opts.interval.0 <= 0 { 1_000_000_000 } else { opts.interval.0 },
        arch: if opts.arch.is_empty() { host_arch().to_string() } else { opts.arch.clone() },
    };
    let first = take_sample(&o, opts.clock, pid)?;
    opts.sleeper.sleep(Duration::from_nanos(o.interval.unsigned_abs()));
    let second = take_sample(&o, opts.clock, pid)?;
    Ok(analyze(&o, pid, &first, &second))
}

fn read(fs: &dyn Fs, path: &str) -> io::Result<Vec<u8>> {
    fs.read(Path::new(path))
}

fn take_sample(o: &Resolved<'_>, clock: &dyn Clock, pid: i64) -> Result<ProcSample, Error> {
    let base = join(&[&o.proc_root, &pid.to_string()]);
    let stat_path = join(&[&base, "stat"]);
    let stat = read(o.fs, &stat_path).map_err(|e| {
        if e.kind() == io::ErrorKind::NotFound {
            Error::NoProcess { context: format!("pid {pid}") }
        } else {
            Error::ReadStat { pid, path: stat_path.clone(), source: e }
        }
    })?;
    let mut s = ProcSample { at: clock.now().as_nanosecond(), ..ProcSample::default() };
    if let Some(st) = parse_stat(&stat) {
        (s.proc_name, s.state) = (st.comm, st.state);
    }
    let entries =
        o.fs.read_dir(Path::new(&join(&[&base, "task"])))
            .map_err(|_| Error::NoProcess { context: format!("list threads of pid {pid}") })?;
    let mut denied = BTreeSet::new();
    for e in entries {
        let Ok(tid) = goerr::parse_int(&e.name) else { continue };
        if let Some(t) = read_task(o.fs, &join(&[&base, "task", &e.name]), &mut denied) {
            s.tasks.insert(tid, t);
        }
    }
    s.notes = denied.iter().map(|f| format!("{f} not readable (permission); checks that need it were skipped")).collect();
    match o.fs.read_dir(Path::new(&join(&[&base, "fd"]))) {
        Ok(ents) => s.fds = ents.len() as i64,
        Err(_) => {
            s.fds = -1;
            s.notes.push("fd table not readable (permission)".to_string());
        }
    }
    if let Ok(st) = read(o.fs, &join(&[&base, "status"])) {
        s.rss_kib = status_int(&st, "VmRSS");
    }
    if let Ok(data) = read(o.fs, &join(&[&base, "io"])) {
        (s.io_read, s.io_write, s.have_io) = parse_io(&data);
    }
    Ok(s)
}

fn is_permission(e: &io::Error) -> bool {
    e.kind() == io::ErrorKind::PermissionDenied
}

fn read_task(fs: &dyn Fs, dir: &str, denied: &mut BTreeSet<&'static str>) -> Option<TaskSample> {
    // A thread that exited between listing and read is skipped.
    let st = parse_stat(&read(fs, &join(&[dir, "stat"])).ok()?)?;
    let mut t = TaskSample {
        comm: st.comm,
        state: st.state,
        cpu_ticks: st.utime.wrapping_add(st.stime),
        sys_ticks: st.stime,
        syscall_nr: -2,
        ..TaskSample::default()
    };
    match read(fs, &join(&[dir, "wchan"])) {
        Ok(w) => {
            let w = String::from_utf8_lossy(&w).trim().to_string();
            if w != "0" {
                t.wchan = w;
            }
        }
        Err(e) if is_permission(&e) => {
            denied.insert("wchan");
        }
        Err(_) => {}
    }
    match read(fs, &join(&[dir, "syscall"])) {
        Ok(sc) => t.syscall_nr = parse_syscall_nr(&sc),
        Err(e) if is_permission(&e) => {
            denied.insert("syscall");
        }
        Err(_) => {}
    }
    if let Ok(status) = read(fs, &join(&[dir, "status"])) {
        t.ctx = status_int(&status, "voluntary_ctxt_switches").wrapping_add(status_int(&status, "nonvoluntary_ctxt_switches"));
    }
    Some(t)
}

/// The `stat` fields procdiag uses.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct StatFields {
    pub comm: String,
    pub state: String,
    pub utime: i64,
    pub stime: i64,
}

/// Go's `parseStat`: `/proc/<pid>/stat` or `/proc/<pid>/task/<tid>/stat`. comm is
/// parenthesised and may contain spaces and parentheses, so fields are taken from after the
/// final `)`.
pub fn parse_stat(data: &[u8]) -> Option<StatFields> {
    let s = String::from_utf8_lossy(data);
    let open = s.find('(')?;
    let end = s.rfind(')')?;
    if end < open {
        return None;
    }
    let f: Vec<&str> = s[end + 1..].split_whitespace().collect();
    // f[0] is state (field 3); utime and stime are fields 14 and 15.
    if f.len() < 13 {
        return None;
    }
    let utime = goerr::parse_int(f[11]).ok()?;
    let stime = goerr::parse_int(f[12]).ok()?;
    Some(StatFields { comm: s[open + 1..end].to_string(), state: f[0].to_string(), utime, stime })
}

/// Go's `parseSyscallNr`: the first token of `/proc/.../syscall`; -1 for `running` (and for a
/// thread blocked outside a syscall), -2 when unparsable.
pub fn parse_syscall_nr(data: &[u8]) -> i64 {
    let s = String::from_utf8_lossy(data);
    match s.split_whitespace().next() {
        None => -2,
        Some("running") => -1,
        Some(tok) => goerr::parse_int(tok).unwrap_or(-2),
    }
}

/// Go's `statusInt`: the first number after `<key>:`, 0 when absent.
fn status_int(data: &[u8], key: &str) -> i64 {
    let text = String::from_utf8_lossy(data);
    let prefix = format!("{key}:");
    for line in text.split('\n') {
        if let Some(rest) = line.strip_prefix(&prefix)
            && let Some(first) = rest.split_whitespace().next()
        {
            // Go keeps the clamped value strconv returns with a range error.
            return goerr::parse_int(first).unwrap_or_else(|e| e.value);
        }
    }
    0
}

fn parse_io(data: &[u8]) -> (i64, i64, bool) {
    let (mut read, mut write, mut ok) = (0, 0, false);
    for line in String::from_utf8_lossy(data).split('\n') {
        let Some((k, v)) = line.split_once(':') else { continue };
        let Ok(n) = goerr::parse_int(v.trim()) else { continue };
        match k {
            "read_bytes" => (read, ok) = (n, true),
            "write_bytes" => write = n,
            _ => {}
        }
    }
    (read, write, ok)
}

/// Go's `SyscallName`: a raw syscall number for `arch` (`amd64` or `arm64`); unknown numbers
/// and architectures are `syscall_<n>`.
pub fn syscall_name(arch: &str, nr: i64) -> String {
    let name = match arch {
        "amd64" => syscalls::amd64(nr),
        "arm64" => syscalls::arm64(nr),
        _ => None,
    };
    name.map_or_else(|| format!("syscall_{nr}"), str::to_string)
}

/// Go's `Duration.Seconds()` of the nanoseconds between two instants, saturating at Go's range.
fn seconds_between(b: i128, a: i128) -> f64 {
    let d = b - a;
    duration_seconds(i64::try_from(d).unwrap_or(if d < 0 { i64::MIN } else { i64::MAX }))
}

fn duration_seconds(d: i64) -> f64 {
    let (sec, nsec) = (d / 1_000_000_000, d % 1_000_000_000);
    sec as f64 + nsec as f64 / 1e9
}

fn analyze(o: &Resolved<'_>, pid: i64, a: &ProcSample, b: &ProcSample) -> Report {
    let mut secs = seconds_between(b.at, a.at);
    if secs <= 0.0 {
        secs = duration_seconds(o.interval);
    }
    let mut r = Report {
        pid,
        comm: b.proc_name.clone(),
        state: b.state.clone(),
        interval_sec: secs,
        thread_count: b.tasks.len() as i64,
        notes: b.notes.clone(),
        ..Report::default()
    };
    r.cmdline = read_cmdline(o.fs, &o.proc_root, pid);

    let (mut total_ticks, mut total_sys) = (0i64, 0i64);
    let mut threads = Vec::with_capacity(b.tasks.len());
    for (&tid, t) in &b.tasks {
        *r.state_counts.entry(t.state.clone()).or_insert(0) += 1;
        let mut th = Thread { tid, comm: t.comm.clone(), state: t.state.clone(), wchan: t.wchan.clone(), ..Thread::default() };
        if t.syscall_nr >= 0 {
            th.syscall = syscall_name(&o.arch, t.syscall_nr);
        } else if t.syscall_nr == -1 && t.state == "R" {
            th.syscall = "running".to_string();
        }
        if let Some(prev) = a.tasks.get(&tid) {
            let dt = t.cpu_ticks.wrapping_sub(prev.cpu_ticks).max(0);
            let ds = t.sys_ticks.wrapping_sub(prev.sys_ticks).max(0);
            total_ticks = total_ticks.wrapping_add(dt);
            total_sys = total_sys.wrapping_add(ds);
            th.cpu_pct = pct(dt, secs);
            th.system_pct = pct(ds, secs);
            th.ctx_switches = t.ctx.wrapping_sub(prev.ctx).max(0);
        }
        threads.push(th);
    }
    r.cpu_pct = pct(total_ticks, secs);
    r.threads = rank_threads(threads);

    r.fds = fd_usage(o.fs, &o.proc_root, pid, a.fds, b.fds, secs);
    r.memory = memory_usage(o.fs, &o.proc_root, pid, a.rss_kib, b.rss_kib, secs);
    if a.have_io && b.have_io {
        r.io = Some(Io { read_bytes: b.io_read.wrapping_sub(a.io_read).max(0), write_bytes: b.io_write.wrapping_sub(a.io_write).max(0) });
    }
    r.oom_score = read_int(o.fs, &join(&[&o.proc_root, &pid.to_string(), "oom_score"]));

    match pressure::read_cgroup(o.fs, &o.proc_root, &o.cgroup_root, pid) {
        Ok(cg) => r.cgroup = Some(cg),
        Err(e) if e.is_unavailable() => {}
        Err(e) => r.notes.push(format!("cgroup not readable: {e}")),
    }

    r.findings = diagnose::diagnose(&r, a, b, total_sys, &o.arch);
    r.verdict = diagnose::verdict(&r.findings, r.cpu_pct).to_string();
    r
}

fn pct(ticks: i64, secs: f64) -> f64 {
    100.0 * ticks as f64 / CLK_TCK / secs
}

fn rank_threads(mut ts: Vec<Thread>) -> Vec<Thread> {
    ts.sort_by(|x, y| {
        if x.cpu_pct != y.cpu_pct {
            return y.cpu_pct.partial_cmp(&x.cpu_pct).unwrap_or(std::cmp::Ordering::Equal);
        }
        let (bx, by) = (x.state != "S", y.state != "S");
        if bx != by {
            return by.cmp(&bx);
        }
        x.tid.cmp(&y.tid)
    });
    ts.truncate(MAX_REPORTED_THREADS);
    ts
}

fn fd_usage(fs: &dyn Fs, proc_root: &str, pid: i64, first: i64, second: i64, secs: f64) -> Fds {
    let mut f = Fds { count: second.max(0), ..Fds::default() };
    if second < 0 || first < 0 {
        return f;
    }
    f.delta = second.wrapping_sub(first);
    f.rate_per_sec = f.delta as f64 / secs;
    if let Ok(data) = read(fs, &join(&[proc_root, &pid.to_string(), "limits"])) {
        f.limit = parse_nofile_limit(&data);
    }
    f
}

/// Go's `parseNoFileLimit`: the soft "Max open files" limit, 0 for `unlimited` or unparsable.
fn parse_nofile_limit(data: &[u8]) -> i64 {
    for line in String::from_utf8_lossy(data).split('\n') {
        if let Some(rest) = line.strip_prefix("Max open files")
            && let Some(first) = rest.split_whitespace().next()
            && let Ok(v) = goerr::parse_int(first)
        {
            return v;
        }
    }
    0
}

fn memory_usage(fs: &dyn Fs, proc_root: &str, pid: i64, rss1: i64, rss2: i64, secs: f64) -> Memory {
    let mut m = Memory { rss_kib: rss2, rss_delta_kib: rss2.wrapping_sub(rss1), ..Memory::default() };
    m.rss_rate_kib_ps = m.rss_delta_kib as f64 / secs;
    let base = join(&[proc_root, &pid.to_string()]);
    if let Ok(st) = read(fs, &join(&[&base, "status"])) {
        m.anon_kib = status_int(&st, "RssAnon");
        m.swap_kib = status_int(&st, "VmSwap");
    }
    if let Ok(sm) = read(fs, &join(&[&base, "smaps_rollup"])) {
        m.pss_kib = status_int(&sm, "Pss");
    }
    m
}

fn read_int(fs: &dyn Fs, file: &str) -> i64 {
    let Ok(data) = read(fs, file) else { return 0 };
    goerr::parse_int(String::from_utf8_lossy(&data).trim()).unwrap_or_else(|e| e.value)
}

fn read_cmdline(fs: &dyn Fs, proc_root: &str, pid: i64) -> String {
    let Ok(data) = read(fs, &join(&[proc_root, &pid.to_string(), "cmdline"])) else { return String::new() };
    String::from_utf8_lossy(&data).replace('\0', " ").trim().to_string()
}

#[cfg(test)]
mod tests;
