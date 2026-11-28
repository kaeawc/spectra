//! Linux pressure-stall information (PSI) and cgroup v2 accounting (Go's `internal/pressure`).
//! PSI answers "is the host (or this cgroup) stalled waiting on CPU, memory, or I/O right now";
//! the cgroup counters answer "did this workload hit its memory limit, get CPU-throttled, or get
//! OOM-killed".
//!
//! Everything is a plain file read through [`Fs`] under the roots the caller passes (normally
//! `/proc` and `/sys/fs/cgroup`), so the parsers run against fixture trees on any host. Paths
//! are Go `path` strings, joined and cleaned as Go's `filepath.Join` does.

mod assess;
pub mod gopath;

use std::collections::HashMap;
use std::fmt;
use std::io;
use std::path::Path;

use serde::Serialize;
use spectra_base::fs::Fs;
use spectra_base::gojson;

use crate::goerr::{self, parse_float, parse_int, parse_uint};
pub use assess::{Finding, Severity, assess_cgroup, assess_psi};
use gopath::join;

/// Go's `ErrUnavailable` text: the kernel does not expose the data (PSI disabled, or the cgroup
/// is not on a v2 hierarchy).
pub const UNAVAILABLE: &str = "pressure data unavailable";

/// A failed read. [`Error::is_unavailable`] is Go's `errors.Is(err, ErrUnavailable)`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Error {
    message: String,
    unavailable: bool,
}

impl Error {
    fn new(message: impl Into<String>) -> Error {
        Error { message: message.into(), unavailable: false }
    }

    fn unavailable(context: impl fmt::Display) -> Error {
        Error { message: format!("{context}: {UNAVAILABLE}"), unavailable: true }
    }

    /// Whether the kernel simply does not expose this data.
    pub fn is_unavailable(&self) -> bool {
        self.unavailable
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for Error {}

/// One PSI line. Avg values are the percentage of wall time, over the trailing window, in which
/// tasks were stalled.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct Line {
    pub avg10: f64,
    pub avg60: f64,
    pub avg300: f64,
    pub total_usec: u64,
}

/// The PSI for one resource. "some" means at least one task was stalled; "full" means every
/// non-idle task was stalled at once. CPU has no meaningful full line at host level.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct Resource {
    pub some: Line,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub full: Option<Line>,
}

/// The host-wide pressure for each resource.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct Psi {
    pub cpu: Resource,
    pub memory: Resource,
    pub io: Resource,
}

fn read_error(context: &str, path: &str, e: &io::Error) -> Error {
    Error::new(format!("{context}: {}", goerr::path_error("open", path, e)))
}

/// Go's `ReadPSI`: `<proc_root>/pressure/{cpu,memory,io}`. A missing file is
/// [`Error::is_unavailable`].
pub fn read_psi(fs: &dyn Fs, proc_root: &str) -> Result<Psi, Error> {
    let mut p = Psi::default();
    for (name, dst) in [("cpu", &mut p.cpu), ("memory", &mut p.memory), ("io", &mut p.io)] {
        let path = join(&[proc_root, "pressure", name]);
        let data = match fs.read(Path::new(&path)) {
            Ok(d) => d,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Err(Error::unavailable(format_args!("psi {name}"))),
            Err(e) => return Err(read_error(&format!("psi {name}"), &path, &e)),
        };
        *dst = parse_resource(&data).map_err(|e| Error::new(format!("psi {name}: {e}")))?;
    }
    Ok(p)
}

/// Go's `ParseResource`: one PSI file (host or cgroup), such as `cpu.pressure`.
pub fn parse_resource(data: &[u8]) -> Result<Resource, String> {
    let text = String::from_utf8_lossy(data);
    let mut r = Resource::default();
    let mut seen = false;
    for raw in text.lines() {
        let Some((kind, rest)) = raw.trim().split_once(' ') else { continue };
        let line = parse_line(rest)?;
        match kind {
            "some" => {
                r.some = line;
                seen = true;
            }
            "full" => r.full = Some(line),
            _ => {}
        }
    }
    if !seen {
        return Err("no 'some' line".to_string());
    }
    Ok(r)
}

fn parse_line(s: &str) -> Result<Line, String> {
    let mut l = Line::default();
    for kv in s.split_whitespace() {
        let Some((k, v)) = kv.split_once('=') else { continue };
        let err = match k {
            "avg10" => parse_float(v).map(|f| l.avg10 = f).err().map(|e| e.to_string()),
            "avg60" => parse_float(v).map(|f| l.avg60 = f).err().map(|e| e.to_string()),
            "avg300" => parse_float(v).map(|f| l.avg300 = f).err().map(|e| e.to_string()),
            "total" => parse_uint(v).map(|n| l.total_usec = n).err().map(|e| e.to_string()),
            _ => None,
        };
        if let Some(e) = err {
            return Err(format!("parse {kv}: {e}"));
        }
    }
    Ok(l)
}

/// A cgroup limit written as `max`.
pub const UNLIMITED: i64 = -1;

/// The `memory.events` counters, cumulative since the cgroup was created: any non-zero
/// `oom_kill` means the kernel killed a process in the cgroup (or a descendant) for exceeding a
/// limit.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
pub struct MemoryEvents {
    pub low: u64,
    pub high: u64,
    pub max: u64,
    pub oom: u64,
    pub oom_kill: u64,
}

/// The cgroup v2 memory controller view.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Memory {
    #[serde(rename = "current_bytes")]
    pub current: u64,
    #[serde(rename = "max_bytes")]
    pub max: i64,
    #[serde(rename = "high_bytes")]
    pub high: i64,
    #[serde(rename = "swap_current_bytes")]
    pub swap_current: u64,
    #[serde(rename = "anon_bytes")]
    pub anon: u64,
    #[serde(rename = "file_bytes")]
    pub file: u64,
    pub events: MemoryEvents,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub pressure: Option<Resource>,
    /// The tightest `memory.max` on this cgroup or any ancestor; [`UNLIMITED`] when none is set.
    #[serde(rename = "effective_max_bytes")]
    pub effective_max: i64,
    /// The cgroup [`Memory::effective_max`] came from.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub limited_by: String,
}

/// The cgroup v2 cpu controller view.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Cpu {
    pub usage_usec: u64,
    pub user_usec: u64,
    pub system_usec: u64,
    pub nr_periods: u64,
    pub nr_throttled: u64,
    pub throttled_usec: u64,
    pub quota_usec: i64,
    pub period_usec: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub pressure: Option<Resource>,
}

/// The cgroup v2 pids controller view.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Pids {
    pub current: i64,
    pub max: i64,
}

/// The accounting for one cgroup v2 directory. A `None` section means that controller is not
/// enabled for the cgroup.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Cgroup {
    pub path: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub memory: Option<Memory>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub cpu: Option<Cpu>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub pids: Option<Pids>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub io_pressure: Option<Resource>,
}

/// Go's `ReadCgroup`: finds `pid`'s cgroup v2 path in `<proc_root>/<pid>/cgroup` and reads it
/// from `cgroup_root`. A host still on cgroup v1 (or a hybrid layout whose unified directory
/// has no controllers) is [`Error::is_unavailable`].
pub fn read_cgroup(fs: &dyn Fs, proc_root: &str, cgroup_root: &str, pid: i64) -> Result<Cgroup, Error> {
    let path = join(&[proc_root, &pid.to_string(), "cgroup"]);
    let data = fs.read(Path::new(&path)).map_err(|e| read_error(&format!("read cgroup of pid {pid}"), &path, &e))?;
    let Some(p) = v2_path(&data) else {
        return Err(Error::unavailable(format_args!("pid {pid} has no cgroup v2 path")));
    };
    read_cgroup_path(fs, cgroup_root, &p)
}

/// Go's `V2Path`: the path from the unified-hierarchy `0::<path>` line.
pub fn v2_path(data: &[u8]) -> Option<String> {
    let text = String::from_utf8_lossy(data);
    text.split('\n').find_map(|line| line.strip_prefix("0::")).map(|p| {
        // The kernel appends " (deleted)" for a removed cgroup.
        let p = p.trim();
        let p = p.strip_suffix(" (deleted)").unwrap_or(p);
        gopath::clean(&format!("/{p}"))
    })
}

/// Go's `ReadCgroupPath`: the cgroup at `cgroup_root` + `cg_path`.
pub fn read_cgroup_path(fs: &dyn Fs, cgroup_root: &str, cg_path: &str) -> Result<Cgroup, Error> {
    let dir = join(&[cgroup_root, cg_path]);
    let controllers = join(&[&dir, "cgroup.controllers"]);
    if let Err(e) = fs.metadata(Path::new(&controllers)) {
        if e.kind() == io::ErrorKind::NotFound {
            return Err(Error::unavailable(format_args!("{dir} is not a cgroup v2 directory")));
        }
        return Err(Error::new(format!("stat cgroup {dir}: {}", goerr::path_error("stat", &controllers, &e))));
    }
    Ok(Cgroup {
        path: cg_path.to_string(),
        memory: read_memory(fs, cgroup_root, cg_path),
        cpu: read_cpu(fs, &dir),
        pids: read_pids(fs, &dir),
        io_pressure: read_pressure_file(fs, &join(&[&dir, "io.pressure"])),
    })
}

fn read_memory(fs: &dyn Fs, cgroup_root: &str, cg_path: &str) -> Option<Memory> {
    let dir = join(&[cgroup_root, cg_path]);
    let file = |name: &str| join(&[&dir, name]);
    let (current, ok) = read_uint(fs, &file("memory.current"));
    if !ok {
        return None;
    }
    let mut m = Memory { current, ..Memory::default() };
    m.max = read_limit(fs, &file("memory.max"));
    m.high = read_limit(fs, &file("memory.high"));
    m.swap_current = read_uint(fs, &file("memory.swap.current")).0;
    if let Some(kv) = read_kv(fs, &file("memory.stat")) {
        (m.anon, m.file) = (get(&kv, "anon"), get(&kv, "file"));
    }
    if let Some(kv) = read_kv(fs, &file("memory.events")) {
        m.events = MemoryEvents {
            low: get(&kv, "low"),
            high: get(&kv, "high"),
            max: get(&kv, "max"),
            oom: get(&kv, "oom"),
            oom_kill: get(&kv, "oom_kill"),
        };
    }
    m.pressure = read_pressure_file(fs, &file("memory.pressure"));
    (m.effective_max, m.limited_by) = effective_memory_max(fs, cgroup_root, cg_path);
    Some(m)
}

/// Go's `effectiveMemoryMax`: walks from `cg_path` up to the root and returns the smallest
/// `memory.max` it finds, and where.
fn effective_memory_max(fs: &dyn Fs, cgroup_root: &str, cg_path: &str) -> (i64, String) {
    let (mut best, mut from) = (UNLIMITED, String::new());
    let mut p = cg_path.to_string();
    loop {
        let v = read_limit(fs, &join(&[cgroup_root, &p, "memory.max"]));
        if v != UNLIMITED && (best == UNLIMITED || v < best) {
            (best, from) = (v, p.clone());
        }
        if p == "/" || p == "." {
            return (best, from);
        }
        p = gopath::dir(&p);
    }
}

fn read_cpu(fs: &dyn Fs, dir: &str) -> Option<Cpu> {
    let kv = read_kv(fs, &join(&[dir, "cpu.stat"]))?;
    let mut c = Cpu {
        usage_usec: get(&kv, "usage_usec"),
        user_usec: get(&kv, "user_usec"),
        system_usec: get(&kv, "system_usec"),
        nr_periods: get(&kv, "nr_periods"),
        nr_throttled: get(&kv, "nr_throttled"),
        throttled_usec: get(&kv, "throttled_usec"),
        quota_usec: UNLIMITED,
        ..Cpu::default()
    };
    if let Ok(data) = fs.read(Path::new(&join(&[dir, "cpu.max"]))) {
        let text = String::from_utf8_lossy(&data);
        let f: Vec<&str> = text.split_whitespace().collect();
        if let [quota, period] = f[..] {
            if quota != "max"
                && let Ok(q) = parse_int(quota)
            {
                c.quota_usec = q;
            }
            // Go keeps ParseInt's value even on error: 0, or the clamped limit when out of range.
            c.period_usec = parse_int(period).unwrap_or_else(|e| e.value);
        }
    }
    c.pressure = read_pressure_file(fs, &join(&[dir, "cpu.pressure"]));
    Some(c)
}

fn read_pids(fs: &dyn Fs, dir: &str) -> Option<Pids> {
    let data = fs.read(Path::new(&join(&[dir, "pids.current"]))).ok()?;
    let current = parse_int(String::from_utf8_lossy(&data).trim()).ok()?;
    Some(Pids { current, max: read_limit(fs, &join(&[dir, "pids.max"])) })
}

/// A limit file whose value is a number or `max`.
fn read_limit(fs: &dyn Fs, file: &str) -> i64 {
    let Ok(data) = fs.read(Path::new(file)) else { return UNLIMITED };
    parse_int(String::from_utf8_lossy(&data).trim()).unwrap_or(UNLIMITED)
}

/// Go's `readUint`: the value (as `strconv.ParseUint` returns it, also on a range error) and
/// whether it parsed.
fn read_uint(fs: &dyn Fs, file: &str) -> (u64, bool) {
    let Ok(data) = fs.read(Path::new(file)) else { return (0, false) };
    match parse_uint(String::from_utf8_lossy(&data).trim()) {
        Ok(v) => (v, true),
        Err(e) => (e.value, false),
    }
}

/// A flat `key value` file, or `None` when it cannot be read.
fn read_kv(fs: &dyn Fs, file: &str) -> Option<HashMap<String, u64>> {
    let data = fs.read(Path::new(file)).ok()?;
    let text = String::from_utf8_lossy(&data);
    let mut out = HashMap::new();
    for line in text.split('\n') {
        if let [k, v] = line.split_whitespace().collect::<Vec<_>>()[..]
            && let Ok(v) = parse_uint(v)
        {
            out.insert(k.to_string(), v);
        }
    }
    Some(out)
}

fn get(kv: &HashMap<String, u64>, key: &str) -> u64 {
    kv.get(key).copied().unwrap_or(0)
}

fn read_pressure_file(fs: &dyn Fs, file: &str) -> Option<Resource> {
    let data = fs.read(Path::new(file)).ok()?;
    parse_resource(&data).ok()
}

#[cfg(test)]
mod tests;
