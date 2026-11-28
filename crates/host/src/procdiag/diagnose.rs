//! The findings drawn from two samples (Go's `internal/procdiag/diagnose.go`).

use super::{
    CLK_TCK, Finding, KIND_ACTIVE, KIND_BLOCKED_SYS, KIND_CPU_SPIN, KIND_FD_EXHAUSTION, KIND_FD_GROWTH, KIND_IDLE, KIND_LOCK_WAIT,
    KIND_MEMORY_GROWTH, KIND_STOPPED, KIND_SWAPPED_OUT, KIND_UNINTERRUPT, KIND_ZOMBIE, ProcSample, Report, SCOPE_CGROUP, Severity,
    TaskSample, syscall_name,
};
use crate::pressure;

// Thresholds. They are deliberately conservative: a false "your process is deadlocked" costs the
// reader more than a missed borderline case.
const CPU_SPIN_PCT: f64 = 90.0; // of one core, over the interval
const FD_WARN_PCT: f64 = 80.0; // of RLIMIT_NOFILE
const FD_CRIT_PCT: f64 = 95.0;
const FD_GROWTH_PER_SEC: f64 = 5.0; // sustained, on top of a minimum absolute delta
const FD_GROWTH_MIN_DELTA: i64 = 5;
const MEM_GROWTH_KIB_PER_SEC: f64 = 8192.0; // 8 MiB/s
const SWAP_MIN_KIB: i64 = 262_144;
const SWAP_SHARE_OF_FOOTPRINT: f64 = 0.25;

/// Where a healthy, waiting thread parks. A main thread in one of these is not "blocked".
fn idle_syscall(name: &str) -> bool {
    matches!(
        name,
        "epoll_wait"
            | "epoll_pwait"
            | "epoll_pwait2"
            | "poll"
            | "ppoll"
            | "select"
            | "pselect6"
            | "nanosleep"
            | "clock_nanosleep"
            | "pause"
            | "accept"
            | "accept4"
            | "wait4"
            | "waitid"
            | "rt_sigtimedwait"
            | "rt_sigsuspend"
            | "io_uring_enter"
            | "io_getevents"
            | "futex_waitv"
    )
}

/// Waits on a network peer.
fn network_syscall(name: &str) -> bool {
    matches!(name, "connect" | "recvfrom" | "recvmsg" | "recvmmsg" | "sendto" | "sendmsg" | "sendmmsg")
}

/// Waits that normally mean a slow disk, NFS server, or FUSE daemon.
fn storage_syscall(name: &str) -> bool {
    matches!(name, "fsync" | "fdatasync" | "sync" | "syncfs" | "msync" | "flock" | "fallocate" | "ftruncate")
}

fn finding(kind: &str, severity: Severity, summary: String, evidence: Vec<String>) -> Finding {
    Finding { kind: kind.to_string(), severity, summary, evidence, scope: String::new() }
}

pub(super) fn diagnose(r: &Report, a: &ProcSample, b: &ProcSample, total_sys_ticks: i64, arch: &str) -> Vec<Finding> {
    let mut f = Vec::new();
    match r.state.as_str() {
        "Z" => {
            f.push(finding(
                KIND_ZOMBIE,
                Severity::Warning,
                "process has exited but its parent has not reaped it (wait()); it holds a pid slot but no memory".to_string(),
                vec!["state=Z".to_string()],
            ));
            return f; // nothing else about a zombie is meaningful
        }
        "T" | "t" => f.push(finding(
            KIND_STOPPED,
            Severity::Warning,
            "process is stopped (SIGSTOP/SIGTSTP or ptrace-attached); it will make no progress until continued".to_string(),
            vec![format!("state={}", r.state), "send SIGCONT or detach the debugger".to_string()],
        )),
        _ => {}
    }
    f.extend(diagnose_threads(r, a, b, total_sys_ticks, arch));
    f.extend(diagnose_fds(r));
    f.extend(diagnose_memory(r));
    if let Some(cg) = &r.cgroup {
        for pf in pressure::assess_cgroup(cg) {
            f.push(Finding {
                kind: pf.kind,
                severity: pf.severity,
                summary: pf.summary,
                evidence: Vec::new(),
                scope: SCOPE_CGROUP.to_string(),
            });
        }
    }
    // Process evidence leads; cgroup-wide findings follow as context.
    f.sort_by(|x, y| y.process_scoped().cmp(&x.process_scoped()).then_with(|| rank(y.severity).cmp(&rank(x.severity))));
    f
}

fn rank(s: Severity) -> i32 {
    match s {
        Severity::Critical => 3,
        Severity::Warning => 2,
        Severity::Info => 1,
    }
}

fn diagnose_threads(r: &Report, a: &ProcSample, b: &ProcSample, total_sys_ticks: i64, arch: &str) -> Vec<Finding> {
    let mut f = uninterruptible(r, a, b);
    f.extend(lock_wait(r, a, b, arch));
    f.extend(blocked_main(r, a, b, arch));
    if r.cpu_pct >= CPU_SPIN_PCT {
        f.push(cpu_spin(r, total_sys_ticks));
    }
    f
}

/// Threads in D state in both samples: a single D reading is ordinary disk wait; D across the
/// whole interval almost always means a hung block device, NFS/FUSE mount, or a kernel bug.
fn uninterruptible(r: &Report, a: &ProcSample, b: &ProcSample) -> Vec<Finding> {
    let mut stuck = Vec::new();
    for (tid, t) in &b.tasks {
        if let Some(p) = a.tasks.get(tid)
            && t.state == "D"
            && p.state == "D"
        {
            let w = if t.wchan.is_empty() { "?" } else { t.wchan.as_str() };
            stuck.push(format!("tid {tid} ({}) wchan={w}", t.comm));
        }
    }
    if stuck.is_empty() {
        return Vec::new();
    }
    stuck.sort();
    vec![finding(
        KIND_UNINTERRUPT,
        Severity::Critical,
        format!(
            "{} thread(s) stayed in uninterruptible sleep (D) for the whole {:.0}s interval — SIGKILL will not work; suspect a hung disk, NFS/FUSE mount, or driver",
            stuck.len(),
            r.interval_sec
        ),
        stuck,
    )]
}

fn is_futex(t: &TaskSample, arch: &str) -> bool {
    if t.syscall_nr >= 0 && syscall_name(arch, t.syscall_nr) == "futex" {
        return true;
    }
    t.wchan.starts_with("futex")
}

/// A multi-threaded process in which every thread is parked on a futex and none was scheduled
/// during the interval: a strong (not conclusive) deadlock or lost-wakeup signal.
fn lock_wait(r: &Report, a: &ProcSample, b: &ProcSample, arch: &str) -> Option<Finding> {
    if b.tasks.len() < 2 {
        return None;
    }
    let mut evidence = Vec::new();
    for (tid, t) in &b.tasks {
        let p = a.tasks.get(tid)?;
        if !is_futex(t, arch) || !is_futex(p, arch) || t.state != "S" || t.ctx != p.ctx {
            return None;
        }
        evidence.push(format!("tid {tid} ({}) in futex", t.comm));
    }
    evidence.sort();
    Some(finding(
        KIND_LOCK_WAIT,
        Severity::Warning,
        format!(
            "all {} threads were parked on a futex and none ran for {:.0}s — consistent with a deadlock or lost wake-up (also what an idle thread pool looks like; confirm with a stack dump)",
            b.tasks.len(),
            r.interval_sec
        ),
        evidence,
    ))
}

fn blocked_main(r: &Report, a: &ProcSample, b: &ProcSample, arch: &str) -> Option<Finding> {
    let m = b.tasks.get(&r.pid)?;
    let p = a.tasks.get(&r.pid)?;
    if m.state != "S" || p.state != "S" || m.syscall_nr < 0 || m.syscall_nr != p.syscall_nr || m.ctx != p.ctx {
        return None;
    }
    let name = syscall_name(arch, m.syscall_nr);
    if idle_syscall(&name) {
        return None;
    }
    let (sev, why) = if network_syscall(&name) {
        (Severity::Warning, "it is waiting on a network peer")
    } else if storage_syscall(&name) {
        (Severity::Warning, "it is waiting on storage or a lock; check disk, NFS and FUSE health")
    } else {
        (Severity::Info, "may be normal for a program waiting on input")
    };
    if name == "futex" {
        return None; // lock waits are judged across all threads by lock_wait
    }
    let mut ev = vec![format!("syscall={name}")];
    if !m.wchan.is_empty() {
        ev.push(format!("wchan={}", m.wchan));
    }
    Some(finding(
        KIND_BLOCKED_SYS,
        sev,
        format!("main thread sat in {name}() for the whole {:.0}s interval without being scheduled; {why}", r.interval_sec),
        ev,
    ))
}

fn cpu_spin(r: &Report, total_sys_ticks: i64) -> Finding {
    let mut ev = vec![format!("process cpu={:.0}% of one core over {:.1}s", r.cpu_pct, r.interval_sec)];
    if let Some(t) = r.threads.first() {
        ev.push(format!("hottest thread tid {} ({}) at {:.0}%", t.tid, t.comm, t.cpu_pct));
    }
    let mut hot = "user-space loop";
    if r.cpu_pct > 0.0 {
        let sys_share = 100.0 * total_sys_ticks as f64 / CLK_TCK / r.interval_sec / r.cpu_pct * 100.0;
        ev.push(format!("{sys_share:.0}% of that time in the kernel"));
        if sys_share >= 50.0 {
            hot = "syscall storm (most time is in the kernel; try strace -c -p)";
        }
    }
    finding(KIND_CPU_SPIN, Severity::Warning, format!("process is pegging CPU — {hot}"), ev)
}

fn diagnose_fds(r: &Report) -> Vec<Finding> {
    let fd = &r.fds;
    let mut f = Vec::new();
    if fd.limit > 0 {
        let used = 100.0 * fd.count as f64 / fd.limit as f64;
        if let Some(sev) = fd_severity(used) {
            f.push(finding(
                KIND_FD_EXHAUSTION,
                sev,
                format!(
                    "{} of {} file descriptors in use ({used:.0}%); the next open()/accept()/socket() will fail with EMFILE once the limit is hit",
                    fd.count, fd.limit
                ),
                vec![format!("RLIMIT_NOFILE soft={}", fd.limit)],
            ));
        }
    }
    if fd.delta >= FD_GROWTH_MIN_DELTA && fd.rate_per_sec >= FD_GROWTH_PER_SEC {
        let mut ev = vec![format!("+{} fds in {:.1}s ({:.1}/s)", fd.delta, r.interval_sec, fd.rate_per_sec)];
        let mut eta = String::new();
        if fd.limit > 0 {
            let secs = fd.limit.wrapping_sub(fd.count) as f64 / fd.rate_per_sec;
            eta = format!("; at this rate the limit is reached in {}", human_duration(secs));
            ev.push(format!("limit={}", fd.limit));
        }
        f.push(finding(
            KIND_FD_GROWTH,
            Severity::Warning,
            format!(
                "file descriptor count is climbing {:.1}/s — likely a leak{eta} (ls -l /proc/{}/fd shows what)",
                fd.rate_per_sec, r.pid
            ),
            ev,
        ));
    }
    f
}

fn fd_severity(used_pct: f64) -> Option<Severity> {
    if used_pct >= FD_CRIT_PCT {
        Some(Severity::Critical)
    } else if used_pct >= FD_WARN_PCT {
        Some(Severity::Warning)
    } else {
        None
    }
}

fn diagnose_memory(r: &Report) -> Vec<Finding> {
    let m = &r.memory;
    let mut f = Vec::new();
    if m.rss_rate_kib_ps >= MEM_GROWTH_KIB_PER_SEC {
        f.push(finding(
            KIND_MEMORY_GROWTH,
            Severity::Warning,
            format!(
                "resident memory grew {:.1} MiB/s (+{} MiB in {:.1}s); sample again over a longer interval to separate a leak from a warm-up",
                m.rss_rate_kib_ps / 1024.0,
                m.rss_delta_kib / 1024,
                r.interval_sec
            ),
            vec![format!("rss={} MiB", m.rss_kib / 1024)],
        ));
    }
    let footprint = m.rss_kib.wrapping_add(m.swap_kib);
    if m.swap_kib >= SWAP_MIN_KIB && footprint > 0 && m.swap_kib as f64 / footprint as f64 >= SWAP_SHARE_OF_FOOTPRINT {
        f.push(finding(
            KIND_SWAPPED_OUT,
            Severity::Info,
            format!(
                "{} MiB of this process lives in swap ({:.0}% of its footprint); page faults back in will look like latency",
                m.swap_kib / 1024,
                100.0 * m.swap_kib as f64 / footprint as f64
            ),
            vec![format!("rss={} MiB swap={} MiB", m.rss_kib / 1024, m.swap_kib / 1024)],
        ));
    }
    f
}

fn human_duration(secs: f64) -> String {
    if secs < 90.0 {
        format!("{secs:.0}s")
    } else if secs < 5400.0 {
        format!("{:.0}m", secs / 60.0)
    } else {
        format!("{:.1}h", secs / 3600.0)
    }
}

/// Go's `verdict`: the kind of the most severe process-scoped finding at warning or above, else
/// `active` or `idle` by CPU use. Cgroup-wide findings never decide it.
pub(super) fn verdict(f: &[Finding], cpu_pct: f64) -> &str {
    for x in f {
        if x.process_scoped() && rank(x.severity) >= rank(Severity::Warning) {
            return &x.kind;
        }
    }
    if cpu_pct >= 5.0 { KIND_ACTIVE } else { KIND_IDLE }
}
