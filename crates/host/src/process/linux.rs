//! The Linux backend: processes from `/proc` (Go's `proclinux.go`), user names from
//! `/etc/passwd` the way Go's pure-Go `os/user` reads it, all through [`Fs`].

use std::collections::HashMap;
use std::path::Path;

use jiff::Timestamp;
use spectra_base::fs::Fs;
use spectra_base::gojson::GoTime;

use super::{Info, Seams, atoi, duration_seconds, in_zone, sub_nanos};

/// The kernel's `USER_HZ`: 100 on effectively every Linux target (reading it needs cgo, which the
/// static Go build disables).
pub const LINUX_CLK_TCK: i64 = 100;

const DELETED_SUFFIX: &str = " (deleted)";

/// Go's `ParseLinuxCPUTimes`: utime+stime and the start time of a `/proc/<pid>/stat` line, in
/// clock ticks.
pub fn parse_linux_cpu_times(data: &[u8]) -> Option<(i64, i64)> {
    let st = parse_linux_stat(data)?;
    Some((st.utime.wrapping_add(st.stime), st.starttime))
}

/// Go's `collectLinuxProcs`: every readable `<root>/<pid>`, in directory order. A pid that
/// exits mid-scan or cannot be read is skipped.
pub(super) fn collect(s: &Seams<'_>, root: &str, now: Timestamp) -> Vec<Info> {
    let Ok(entries) = s.fs.read_dir(Path::new(root)) else { return Vec::new() };
    let boot = read_btime(s.fs, root);
    let page_kib = match i64::try_from(s.source.page_size()).unwrap_or(0) / 1024 {
        k if k <= 0 => 4,
        k => k,
    };
    let mut users = Users { fs: s.fs, names: HashMap::new() };
    let mut out = Vec::new();
    for e in entries {
        let Ok(pid) = crate::goerr::parse_int(&e.name) else { continue };
        if pid <= 0 {
            continue;
        }
        if let Some(info) = read_proc(s, root, pid, boot, now, page_kib, &mut users) {
            out.push(info);
        }
    }
    out
}

fn read_btime(fs: &dyn Fs, root: &str) -> Option<i64> {
    let data = fs.read(Path::new(&format!("{root}/stat"))).ok()?;
    parse_btime(&data)
}

/// Go's `parseBtime`: the `btime <epoch seconds>` line of `/proc/stat`.
pub fn parse_btime(data: &[u8]) -> Option<i64> {
    let text = String::from_utf8_lossy(data);
    let line = text.split('\n').find_map(|l| l.strip_prefix("btime "))?;
    crate::goerr::parse_int(line.trim()).ok()
}

fn read_proc(s: &Seams<'_>, root: &str, pid: i64, boot: Option<i64>, now: Timestamp, page_kib: i64, users: &mut Users<'_>) -> Option<Info> {
    let dir = format!("{root}/{pid}");
    let stat = s.fs.read(Path::new(&format!("{dir}/stat"))).ok()?;
    let st = parse_linux_stat(&stat)?;
    let mut info = Info {
        pid,
        ppid: st.ppid,
        command: st.comm.clone(),
        rss_kib: st.rss_pages.wrapping_mul(page_kib),
        vsize_kib: st.vsize / 1024,
        thread_count: st.threads,
        ..Info::default()
    };
    let cmdline = read_cmdline(s.fs, &dir);
    // Kernel threads have an empty cmdline; ps shows the comm in brackets.
    info.full_command_line = if cmdline.is_empty() { format!("[{}]", st.comm) } else { cmdline };
    (info.executable_path, info.executable_deleted) = read_exe_link(s.fs, &dir);
    if let Some(uid) = read_proc_uid(s.fs, &dir) {
        info.uid = uid;
        info.user = users.name(uid);
    }
    if let Some(btime) = boot
        && st.starttime > 0
    {
        // Go: bootTime.Add(time.Duration(ticks) * time.Second / 100), in int64 nanoseconds.
        let offset = st.starttime.wrapping_mul(1_000_000_000) / LINUX_CLK_TCK;
        let start_ns = i128::from(btime) * 1_000_000_000 + i128::from(offset);
        if let Ok(start) = Timestamp::from_nanosecond(start_ns) {
            info.start_time = in_zone(s.tz, start);
            let elapsed = duration_seconds(sub_nanos(now.as_nanosecond(), start_ns));
            if elapsed > 0.0 {
                let cpu_secs = st.utime.wrapping_add(st.stime) as f64 / LINUX_CLK_TCK as f64;
                info.cpu_pct = 100.0 * cpu_secs / elapsed;
            }
        } else {
            info.start_time = GoTime::zero();
        }
    }
    Some(info)
}

/// The `/proc/<pid>/stat` fields Spectra uses.
struct LinuxStat {
    comm: String,
    ppid: i64,
    utime: i64,
    stime: i64,
    threads: i64,
    starttime: i64,
    vsize: i64,
    rss_pages: i64,
}

/// Go's `parseLinuxStat`. comm (field 2) is parenthesised and may hold spaces and parentheses,
/// so the numeric fields come from after the last `)`; field N is `rest[N-3]`.
fn parse_linux_stat(data: &[u8]) -> Option<LinuxStat> {
    let s = String::from_utf8_lossy(data);
    let open = s.find('(')?;
    let end = s.rfind(')')?;
    if end < open {
        return None;
    }
    let rest: Vec<&str> = s[end + 1..].split_whitespace().collect();
    if rest.len() <= 24 - 3 {
        return None;
    }
    let n = |field: usize| atoi(rest[field - 3]);
    Some(LinuxStat {
        comm: s[open + 1..end].to_string(),
        ppid: n(4),
        utime: n(14),
        stime: n(15),
        threads: n(20),
        starttime: n(22),
        vsize: n(23),
        rss_pages: n(24),
    })
}

/// Go's `readExeLink`: the kernel-reported executable, and whether it was deleted. Kernel threads
/// and (without privilege) other users' processes have no readable link.
fn read_exe_link(fs: &dyn Fs, dir: &str) -> (String, bool) {
    let Ok(target) = fs.read_link(Path::new(&format!("{dir}/exe"))) else { return (String::new(), false) };
    let target = target.to_string_lossy().into_owned();
    match target.strip_suffix(DELETED_SUFFIX) {
        Some(trimmed) => (trimmed.to_string(), true),
        None => (target, false),
    }
}

/// Go's `readCmdline`: argv joined by spaces, or empty.
fn read_cmdline(fs: &dyn Fs, dir: &str) -> String {
    let Ok(data) = fs.read(Path::new(&format!("{dir}/cmdline"))) else { return String::new() };
    if data.is_empty() {
        return String::new();
    }
    String::from_utf8_lossy(&data).trim_end_matches('\0').split('\0').collect::<Vec<_>>().join(" ")
}

/// Go's `readProcUID`: the real uid, the first column of the `Uid:` line of `status`.
fn read_proc_uid(fs: &dyn Fs, dir: &str) -> Option<i64> {
    let data = fs.read(Path::new(&format!("{dir}/status"))).ok()?;
    let text = String::from_utf8_lossy(&data);
    let rest = text.split('\n').find_map(|l| l.strip_prefix("Uid:"))?;
    crate::goerr::parse_int(rest.split_whitespace().next()?).ok()
}

/// uid to user name, memoized per collection; a failed lookup or an empty name is the decimal
/// uid (Go's `userCache`).
struct Users<'a> {
    fs: &'a dyn Fs,
    names: HashMap<i64, String>,
}

impl Users<'_> {
    fn name(&mut self, uid: i64) -> String {
        if let Some(n) = self.names.get(&uid) {
            return n.clone();
        }
        let name = lookup_passwd_uid(self.fs, uid).filter(|n| !n.is_empty()).unwrap_or_else(|| uid.to_string());
        self.names.insert(uid, name.clone());
        name
    }
}

/// Go's pure-Go `user.LookupId` (`os/user/lookup_unix.go`): the first `/etc/passwd` line whose
/// third field is `uid`, skipping blank and `#` lines, `+`/`-` NIS entries and rows whose uid or
/// gid is not a number.
pub fn lookup_passwd_uid(fs: &dyn Fs, uid: i64) -> Option<String> {
    let data = fs.read(Path::new("/etc/passwd")).ok()?;
    let text = String::from_utf8_lossy(&data);
    let value = uid.to_string();
    let needle = format!(":{value}:");
    for line in text.split('\n') {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if !line.contains(&needle) || line.matches(':').count() < 6 {
            continue;
        }
        let parts: Vec<&str> = line.splitn(7, ':').collect();
        if parts.len() < 6 || parts[2] != value || parts[0].is_empty() || parts[0].starts_with(['+', '-']) {
            continue;
        }
        if crate::goerr::parse_int(parts[2]).is_err() || crate::goerr::parse_int(parts[3]).is_err() {
            continue;
        }
        return Some(parts[0].to_string());
    }
    None
}
