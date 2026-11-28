//! Kernel resource limits against current usage: pty slots, open files, processes and processes
//! per user (Go's `internal/syslimits`). Go asks the macOS tools (`sysctl kern.*`, `lsof`, `ps`)
//! on every OS and reports what fails as partial failures, so this does too; there is no Linux
//! branch to port.

use std::collections::BTreeMap;

use serde::Serialize;
use spectra_base::clock::Clock;
use spectra_base::gojson::{self, GoTime};
use spectra_base::proc::Runner;

use crate::cmdrun::bounded;
use crate::goerr::{NumErrorKind, parse_int};
use crate::process::Info;

/// Where the live host's identity comes from, so recorded cases replay without it. The real one
/// is [`HostSystem`]; [`FixedSystem`] answers from a recording.
pub trait SystemSource: Send + Sync {
    /// The invoking user's uid (Go's `os.Getuid()`).
    fn uid(&self) -> i64;
}

/// The live host.
#[derive(Clone, Copy, Debug, Default)]
pub struct HostSystem;

impl SystemSource for HostSystem {
    fn uid(&self) -> i64 {
        #[cfg(unix)]
        {
            spectra_sys::uid()
        }
        #[cfg(not(unix))]
        {
            0
        }
    }
}

/// A recorded identity.
#[derive(Clone, Copy, Debug, Default)]
pub struct FixedSystem {
    pub uid: i64,
}

impl SystemSource for FixedSystem {
    fn uid(&self) -> i64 {
        self.uid
    }
}

/// What collection runs against.
#[derive(Clone, Copy)]
pub struct Options<'a> {
    /// Runs `sysctl`, `lsof` and `ps`.
    pub runner: &'a dyn Runner,
    pub clock: &'a dyn Clock,
    /// The user `ps -u` counts processes for.
    pub uid: i64,
}

/// The kernel resource-limit snapshot (Go's `SystemLimits`).
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct SystemLimits {
    pub pty: ResourceUsage,
    pub files: ResourceUsage,
    #[serde(rename = "max_files_per_proc")]
    pub files_per_proc: i64,
    pub procs: ResourceUsage,
    pub procs_per_uid: ResourceUsage,
    pub collected_at: GoTime,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub partial_failures: Vec<String>,
}

/// One current-vs-limit measurement (Go's `ResourceUsage`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct ResourceUsage {
    pub limit: i64,
    pub current: i64,
    pub pct: f64,
    pub warn: bool,
    pub critical: bool,
}

/// One process contributing to a saturated resource (Go's `TopHolder`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct TopHolder {
    pub pid: i64,
    pub count: i64,
    pub command: String,
}

/// Per-resource process attribution (Go's `TopHolders`); a map, so its keys print sorted.
pub type TopHolders = BTreeMap<String, Vec<TopHolder>>;

/// Go's `Collect`: best-effort, with each failed reading named in `partial_failures`, in the
/// order Go reads them: `lsof`, the two `ps` counts, then the sysctls.
pub fn collect(opts: &Options<'_>) -> SystemLimits {
    let run = opts.runner;
    let mut failures: Vec<String> = Vec::new();
    let (pty_current, ok) = current_ptys(run);
    if !ok {
        failures.push("lsof:pty".into());
    }
    let (proc_current, ok) = process_count(run, &["-A"]);
    if !ok {
        failures.push("ps:procs".into());
    }
    let uid = opts.uid.to_string();
    let (proc_uid_current, ok) = process_count(run, &["-u", &uid]);
    if !ok {
        failures.push("ps:procs_per_uid".into());
    }
    let mut int_val = |key: &str| {
        let (v, ok) = sysctl_int(run, key);
        if !ok {
            failures.push(key.to_string());
        }
        v
    };
    let pty = usage(pty_current, int_val("kern.tty.ptmx_max"));
    let files = usage(int_val("kern.num_files"), int_val("kern.maxfiles"));
    let files_per_proc = int_val("kern.maxfilesperproc");
    let procs = usage(proc_current, int_val("kern.maxproc"));
    let procs_per_uid = usage(proc_uid_current, int_val("kern.maxprocperuid"));
    SystemLimits {
        pty,
        files,
        files_per_proc,
        procs,
        procs_per_uid,
        collected_at: GoTime::utc(opts.clock.now()),
        partial_failures: failures,
    }
}

/// Go's `sysctlInt`: the value is whatever `strconv.Atoi` returned even when it reported an
/// error (a range error carries the clamped limit), and `false` says it did.
fn sysctl_int(run: &dyn Runner, key: &str) -> (i64, bool) {
    let (out, err) = bounded(run, "sysctl", &["-n", key]);
    if err.is_some() {
        return (0, false);
    }
    match parse_int(String::from_utf8_lossy(&out).trim()) {
        Ok(v) => (v, true),
        Err(e) => (if e.kind == NumErrorKind::Range { e.value } else { 0 }, false),
    }
}

/// Go's `currentPTYs`: descriptors on a pty in `lsof`'s listing. A failed `lsof` that printed
/// something still counts.
fn current_ptys(run: &dyn Runner) -> (i64, bool) {
    let (out, err) = bounded(run, "lsof", &["-n", "-P", "-d", "^txt"]);
    if err.is_some() && out.is_empty() {
        return (0, false);
    }
    let mut count = 0;
    for line in String::from_utf8_lossy(&out).split('\n') {
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() < 9 {
            continue;
        }
        if !fields[3].starts_with(|c: char| c.is_ascii_digit()) {
            continue;
        }
        if is_pty_path(&fields[8..].join(" ")) {
            count += 1;
        }
    }
    (count, true)
}

fn process_count(run: &dyn Runner, selector: &[&str]) -> (i64, bool) {
    let mut args: Vec<&str> = selector.to_vec();
    args.extend(["-o", "pid="]);
    let (out, err) = bounded(run, "ps", &args);
    if err.is_some() {
        return (0, false);
    }
    let count = String::from_utf8_lossy(&out).split('\n').filter(|l| !l.trim().is_empty()).count();
    (i64::try_from(count).unwrap_or(i64::MAX), true)
}

/// Go's `usage`: more than 80% warns, more than 95% is critical.
pub fn usage(current: i64, limit: i64) -> ResourceUsage {
    let mut u = ResourceUsage { limit, current, ..ResourceUsage::default() };
    if limit > 0 {
        u.pct = (current as f64 / limit as f64) * 100.0;
    }
    u.warn = u.pct > 80.0;
    u.critical = u.pct > 95.0;
    u
}

impl SystemLimits {
    /// Go's `AnyCritical`.
    pub fn any_critical(&self) -> bool {
        self.pty.critical || self.files.critical || self.procs.critical || self.procs_per_uid.critical
    }
}

/// Go's `TopHoldersOf`: `procs`, a deep process table, ranked by what each holds of the
/// saturated resources; `uid` is the user whose processes count against `procs_per_uid`.
pub fn top_holders_of(procs: &[Info], limit: usize, uid: i64) -> TopHolders {
    let limit = if limit == 0 { 10 } else { limit };
    let mut out = TopHolders::new();
    out.insert("pty".into(), top_by(procs, limit, |p| p.fd_breakdown.map_or(0, |b| b.pty)));
    out.insert("files".into(), top_by(procs, limit, |p| p.open_fds));
    out.insert("procs".into(), top_by_children(procs, limit, -1));
    out.insert("procs_per_uid".into(), top_by_children(procs, limit, uid));
    out
}

fn top_by(procs: &[Info], limit: usize, count: impl Fn(&Info) -> i64) -> Vec<TopHolder> {
    let mut rows: Vec<TopHolder> = procs
        .iter()
        .filter_map(|p| {
            let c = count(p);
            (c > 0).then(|| TopHolder { pid: p.pid, count: c, command: p.command.clone() })
        })
        .collect();
    finish(&mut rows, limit);
    rows
}

fn top_by_children(procs: &[Info], limit: usize, uid: i64) -> Vec<TopHolder> {
    let names: std::collections::HashMap<i64, &str> = procs.iter().map(|p| (p.pid, p.command.as_str())).collect();
    let mut parents: BTreeMap<i64, TopHolder> = BTreeMap::new();
    for p in procs {
        if uid >= 0 && p.uid != uid {
            continue;
        }
        if p.ppid <= 0 {
            continue;
        }
        let h = parents.entry(p.ppid).or_insert_with(|| TopHolder { pid: p.ppid, count: 0, command: String::new() });
        h.count += 1;
        h.command = names.get(&p.ppid).copied().unwrap_or("").to_string();
    }
    let mut rows: Vec<TopHolder> = parents
        .into_values()
        .map(|mut h| {
            if h.command.is_empty() {
                h.command = format!("pid {}", h.pid);
            }
            h
        })
        .collect();
    finish(&mut rows, limit);
    rows
}

/// Count descending, then pid ascending (a total order), cut to `limit`.
fn finish(rows: &mut Vec<TopHolder>, limit: usize) {
    rows.sort_by(|a, b| b.count.cmp(&a.count).then(a.pid.cmp(&b.pid)));
    rows.truncate(limit);
}

fn is_pty_path(name: &str) -> bool {
    name.starts_with("/dev/ttys") || name.starts_with("/dev/pty") || name == "/dev/ptmx"
}

#[cfg(test)]
mod tests;
