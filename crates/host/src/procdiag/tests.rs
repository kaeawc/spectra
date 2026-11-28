//! Ports of `internal/procdiag/procdiag_test.go`, against an in-memory `/proc` that the sleeper
//! swaps for the second sample.

use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use spectra_base::clock::{Clock, FakeClock, Sleeper};
use spectra_base::fs::{DirEntry, FileInfo, FileType, Fs};
use spectra_base::gojson::GoDuration;

use super::diagnose::verdict;
use super::*;

/// An in-memory tree: files at absolute paths, directories implied by them; paths in `denied`
/// fail with EACCES.
#[derive(Default)]
struct PhaseFs {
    files: Mutex<BTreeMap<String, Vec<u8>>>,
    denied: Mutex<Vec<String>>,
}

impl PhaseFs {
    fn set(&self, files: BTreeMap<String, Vec<u8>>) {
        *self.files.lock().expect("lock") = files;
    }

    fn check(&self, path: &Path) -> io::Result<String> {
        let p = path.to_string_lossy().into_owned();
        if self.denied.lock().expect("lock").contains(&p) {
            return Err(io::Error::from_raw_os_error(13));
        }
        Ok(p)
    }
}

fn info(file_type: FileType) -> FileInfo {
    FileInfo { file_type, len: 0, mode: 0o644, modified: None, dev: 0, ino: 0, ctime: None }
}

impl Fs for PhaseFs {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        let p = self.check(path)?;
        self.files.lock().expect("lock").get(&p).cloned().ok_or_else(|| io::Error::from_raw_os_error(2))
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<DirEntry>> {
        let p = self.check(path)?;
        let prefix = format!("{p}/");
        let files = self.files.lock().expect("lock");
        let mut out: BTreeMap<String, FileType> = BTreeMap::new();
        for k in files.keys() {
            if let Some(rest) = k.strip_prefix(&prefix) {
                match rest.split_once('/') {
                    Some((dir, _)) => out.insert(dir.to_string(), FileType::Dir),
                    None => out.insert(rest.to_string(), FileType::File),
                };
            }
        }
        if out.is_empty() {
            return Err(io::Error::from_raw_os_error(2));
        }
        Ok(out.into_iter().map(|(name, file_type)| DirEntry { name, file_type }).collect())
    }

    fn metadata(&self, path: &Path) -> io::Result<FileInfo> {
        let p = self.check(path)?;
        let files = self.files.lock().expect("lock");
        if files.contains_key(&p) {
            return Ok(info(FileType::File));
        }
        let prefix = format!("{p}/");
        if files.keys().any(|k| k.starts_with(&prefix)) {
            return Ok(info(FileType::Dir));
        }
        Err(io::Error::from_raw_os_error(2))
    }

    fn symlink_metadata(&self, path: &Path) -> io::Result<FileInfo> {
        self.metadata(path)
    }

    fn read_link(&self, _path: &Path) -> io::Result<PathBuf> {
        Err(io::Error::from_raw_os_error(22))
    }
}

/// Swaps in the second phase and moves the clock by the slept duration.
struct PhaseSleeper {
    fs: Arc<PhaseFs>,
    clock: Arc<FakeClock>,
    next: Mutex<Option<BTreeMap<String, Vec<u8>>>>,
}

impl Sleeper for PhaseSleeper {
    fn sleep(&self, d: Duration) {
        if let Some(next) = self.next.lock().expect("lock").take() {
            self.fs.set(next);
        }
        self.clock.advance(d);
    }
}

#[derive(Clone, Default)]
struct Thr {
    tid: i64,
    comm: &'static str,
    state: &'static str,
    utime: i64,
    stime: i64,
    wchan: &'static str,
    /// Raw `/proc/.../syscall` text; empty means the file is absent.
    syscall: &'static str,
    vol: i64,
    nonvol: i64,
}

#[derive(Clone, Default)]
struct Proc {
    pid: i64,
    state: &'static str,
    threads: Vec<Thr>,
    fds: usize,
    nofile: &'static str,
    rss_kib: i64,
    swap_kib: i64,
    read: i64,
    write: i64,
}

fn thr(tid: i64, comm: &'static str, state: &'static str) -> Thr {
    Thr { tid, comm, state, ..Thr::default() }
}

/// A stat line; utime and stime are fields 14 and 15.
fn stat_line(id: i64, comm: &str, state: &str, ut: i64, st: i64) -> String {
    format!("{id} ({comm}) {state} 1 1 1 0 -1 0 0 0 0 0 {ut} {st} 0 0 20 0 1 0 100 0 0 0\n")
}

const ROOT: &str = "/fixture";

fn put(m: &mut BTreeMap<String, Vec<u8>>, path: String, content: impl Into<Vec<u8>>) {
    m.insert(path, content.into());
}

fn proc_files(p: &Proc) -> BTreeMap<String, Vec<u8>> {
    let mut m = BTreeMap::new();
    let base = format!("{ROOT}/{}", p.pid);
    let comm = p.threads.first().map_or("app", |t| t.comm);
    let state = if p.state.is_empty() { "S" } else { p.state };
    put(&mut m, format!("{base}/stat"), stat_line(p.pid, comm, state, 0, 0));
    put(&mut m, format!("{base}/cmdline"), "/usr/bin/app\0--serve\0");
    for t in &p.threads {
        let d = format!("{base}/task/{}", t.tid);
        put(&mut m, format!("{d}/stat"), stat_line(t.tid, t.comm, t.state, t.utime, t.stime));
        put(&mut m, format!("{d}/wchan"), if t.wchan.is_empty() { "0\n".to_string() } else { format!("{}\n", t.wchan) });
        if !t.syscall.is_empty() {
            put(&mut m, format!("{d}/syscall"), format!("{}\n", t.syscall));
        }
        put(&mut m, format!("{d}/status"), format!("voluntary_ctxt_switches:\t{}\nnonvoluntary_ctxt_switches:\t{}\n", t.vol, t.nonvol));
    }
    for i in 0..p.fds {
        put(&mut m, format!("{base}/fd/{i}"), "");
    }
    let nofile = if p.nofile.is_empty() { "1024" } else { p.nofile };
    put(
        &mut m,
        format!("{base}/limits"),
        format!(
            "Limit                     Soft Limit           Hard Limit           Units\nMax open files            {nofile}                  4096                 files\n"
        ),
    );
    put(&mut m, format!("{base}/status"), format!("VmRSS:\t{} kB\nRssAnon:\t{} kB\nVmSwap:\t{} kB\n", p.rss_kib, p.rss_kib, p.swap_kib));
    put(&mut m, format!("{base}/io"), format!("rchar: 0\nread_bytes: {}\nwrite_bytes: {}\n", p.read, p.write));
    put(&mut m, format!("{base}/oom_score"), "7\n");
    m
}

struct Env {
    fs: Arc<PhaseFs>,
    clock: Arc<FakeClock>,
    sleeper: PhaseSleeper,
}

fn env(first: BTreeMap<String, Vec<u8>>, second: Option<BTreeMap<String, Vec<u8>>>) -> Env {
    let fs = Arc::new(PhaseFs::default());
    fs.set(first);
    let clock = Arc::new(FakeClock::new(jiff::Timestamp::from_second(1000).expect("ts")));
    let sleeper = PhaseSleeper { fs: Arc::clone(&fs), clock: Arc::clone(&clock), next: Mutex::new(second) };
    Env { fs, clock, sleeper }
}

fn options<'a>(e: &'a Env, cgroup_root: &str) -> Options<'a> {
    Options {
        fs: e.fs.as_ref(),
        clock: e.clock.as_ref(),
        sleeper: &e.sleeper,
        proc_root: ROOT.to_string(),
        cgroup_root: cgroup_root.to_string(),
        interval: GoDuration(1_000_000_000),
        arch: "amd64".to_string(),
    }
}

/// Inspects with `before` in place, switching to `after` during the sleep; the fake clock makes
/// the 1s interval exactly 1s.
fn run(before: &Proc, after: &Proc) -> Report {
    let e = env(proc_files(before), Some(proc_files(after)));
    inspect(before.pid, &options(&e, &format!("{ROOT}/nocg"))).expect("inspect")
}

fn find<'a>(r: &'a Report, kind: &str) -> Option<&'a Finding> {
    r.findings.iter().find(|f| f.kind == kind)
}

#[test]
fn cpu_spin() {
    let b = Proc { pid: 10, fds: 3, threads: vec![Thr { utime: 100, syscall: "running", ..thr(10, "spin", "R") }], ..Proc::default() };
    let a = Proc { pid: 10, fds: 3, threads: vec![Thr { utime: 199, syscall: "running", ..thr(10, "spin", "R") }], ..Proc::default() };
    let r = run(&b, &a);
    let f = find(&r, KIND_CPU_SPIN).unwrap_or_else(|| panic!("findings {:?}", r.findings));
    assert_eq!(r.verdict, KIND_CPU_SPIN);
    assert!((98.0..=100.0).contains(&r.cpu_pct), "cpu = {}", r.cpu_pct);
    assert!(f.summary.contains("user-space loop"), "{}", f.summary);
}

#[test]
fn syscall_storm() {
    let b = Proc { pid: 10, threads: vec![thr(10, "x", "R")], ..Proc::default() };
    let a = Proc { pid: 10, threads: vec![Thr { utime: 10, stime: 90, ..thr(10, "x", "R") }], ..Proc::default() };
    let r = run(&b, &a);
    assert!(find(&r, KIND_CPU_SPIN).is_some_and(|f| f.summary.contains("syscall storm")), "{:?}", r.findings);
}

#[test]
fn fd_leak_with_eta() {
    let th = vec![Thr { wchan: "hrtimer_nanosleep", syscall: "230 0x0", ..thr(20, "leaky", "S") }];
    let b = Proc { pid: 20, fds: 100, nofile: "1000", threads: th.clone(), ..Proc::default() };
    let a = Proc { pid: 20, fds: 200, nofile: "1000", threads: th, ..Proc::default() };
    let r = run(&b, &a);
    let f = find(&r, KIND_FD_GROWTH).unwrap_or_else(|| panic!("findings {:?}", r.findings));
    assert_eq!(r.fds.rate_per_sec, 100.0);
    assert!(f.summary.contains("limit is reached in 8s"), "{}", f.summary);
}

#[test]
fn fd_exhaustion_is_critical() {
    let p = Proc { pid: 20, fds: 970, nofile: "1000", threads: vec![thr(20, "x", "S")], ..Proc::default() };
    let r = run(&p, &p);
    let f = find(&r, KIND_FD_EXHAUSTION).expect("fd-exhaustion");
    assert_eq!((f.severity, r.verdict.as_str()), (Severity::Critical, KIND_FD_EXHAUSTION));
    assert!(find(&r, KIND_FD_GROWTH).is_none(), "steady fd count must not report growth");
}

#[test]
fn uninterruptible_only_when_persistent() {
    let d = Proc { pid: 30, threads: vec![Thr { wchan: "nfs_wait", ..thr(30, "io", "D") }], ..Proc::default() };
    let r = run(&d, &d);
    let f = find(&r, KIND_UNINTERRUPT).unwrap_or_else(|| panic!("findings {:?}", r.findings));
    assert_eq!(f.severity, Severity::Critical);
    assert!(f.evidence[0].contains("nfs_wait"));
    let s = Proc { pid: 30, threads: vec![thr(30, "io", "S")], ..Proc::default() };
    assert!(find(&run(&d, &s), KIND_UNINTERRUPT).is_none(), "a single D reading is ordinary disk wait");
}

#[test]
fn lock_wait() {
    let mk = |vol| Proc {
        pid: 40,
        threads: vec![
            Thr { syscall: "202 0x1", wchan: "futex_do_wait", vol, ..thr(40, "app", "S") },
            Thr { syscall: "202 0x1", wchan: "futex_do_wait", vol, ..thr(41, "worker", "S") },
        ],
        ..Proc::default()
    };
    let r = run(&mk(5), &mk(5));
    assert!(find(&r, KIND_LOCK_WAIT).is_some_and(|f| f.summary.contains("all 2 threads")), "{:?}", r.findings);
    assert!(find(&run(&mk(5), &mk(6)), KIND_LOCK_WAIT).is_none(), "a thread scheduled during the interval is not stuck");
}

#[test]
fn lock_wait_falls_back_to_wchan() {
    let mk = Proc {
        pid: 40,
        threads: vec![Thr { wchan: "futex_wait_queue", ..thr(40, "app", "S") }, Thr { wchan: "futex_wait_queue", ..thr(41, "w", "S") }],
        ..Proc::default()
    };
    assert!(find(&run(&mk, &mk), KIND_LOCK_WAIT).is_some(), "wchan alone is enough when syscall is unreadable");
}

#[test]
fn idle_server_is_not_blocked() {
    let mk = Proc {
        pid: 50,
        threads: vec![Thr { syscall: "232 0x3", ..thr(50, "srv", "S") }, Thr { syscall: "202 0x1", ..thr(51, "w", "S") }],
        ..Proc::default()
    };
    let r = run(&mk, &mk);
    assert!(r.findings.is_empty() && r.verdict == KIND_IDLE, "verdict={} findings={:?}", r.verdict, r.findings);
    assert!(!r.threads[0].syscall.is_empty(), "{:?}", r.threads);
}

#[test]
fn blocked_connect() {
    let mk =
        Proc { pid: 60, threads: vec![Thr { syscall: "42 0x3", wchan: "inet_stream_connect", ..thr(60, "cli", "S") }], ..Proc::default() };
    let r = run(&mk, &mk);
    let f = find(&r, KIND_BLOCKED_SYS).unwrap_or_else(|| panic!("findings {:?}", r.findings));
    assert_eq!(f.severity, Severity::Warning);
    assert!(f.summary.contains("connect()"));
}

#[test]
fn blocked_read_is_only_info() {
    let mk = Proc { pid: 61, threads: vec![Thr { syscall: "0 0x0", ..thr(61, "cat", "S") }], ..Proc::default() };
    assert_eq!(find(&run(&mk, &mk), KIND_BLOCKED_SYS).map(|f| f.severity), Some(Severity::Info));
}

#[test]
fn zombie_and_stopped() {
    let z = Proc { pid: 70, state: "Z", threads: vec![thr(70, "defunct", "Z")], ..Proc::default() };
    let r = run(&z, &z);
    assert_eq!((r.verdict.as_str(), r.findings.len()), (KIND_ZOMBIE, 1), "{:?}", r.findings);
    let s = Proc { pid: 71, state: "T", threads: vec![thr(71, "stopped", "T")], ..Proc::default() };
    assert_eq!(run(&s, &s).verdict, KIND_STOPPED);
}

#[test]
fn memory_growth_and_swap() {
    let th = vec![thr(80, "grow", "S")];
    let r = run(
        &Proc { pid: 80, threads: th.clone(), rss_kib: 100 * 1024, ..Proc::default() },
        &Proc { pid: 80, threads: th, rss_kib: 120 * 1024, swap_kib: 400 * 1024, ..Proc::default() },
    );
    assert!(find(&r, KIND_MEMORY_GROWTH).is_some_and(|f| f.summary.contains("20.0 MiB/s")), "{:?}", r.findings);
    assert!(find(&r, KIND_SWAPPED_OUT).is_some(), "{:?}", r.findings);
}

#[test]
fn threads_are_ranked_and_capped() {
    let before: Vec<Thr> = (0..40).map(|i| thr(100 + i, "w", "S")).collect();
    let after: Vec<Thr> = (0..40).map(|i| Thr { utime: i, ..thr(100 + i, "w", "S") }).collect();
    let r = run(&Proc { pid: 100, threads: before, ..Proc::default() }, &Proc { pid: 100, threads: after, ..Proc::default() });
    assert_eq!((r.thread_count, r.threads.len()), (40, MAX_REPORTED_THREADS));
    assert_eq!(r.threads[0].tid, 139, "hottest thread leads");
}

#[test]
fn no_process() {
    let e = env(BTreeMap::new(), None);
    let err = inspect(424242, &options(&e, "")).expect_err("no process");
    assert!(err.is_no_process(), "{err}");
    assert_eq!(err.to_string(), "pid 424242: no such process");
}

#[test]
fn process_exits_between_samples() {
    let p = Proc { pid: 5, threads: vec![thr(5, "x", "S")], ..Proc::default() };
    let e = env(proc_files(&p), Some(BTreeMap::new()));
    assert!(inspect(5, &options(&e, "")).expect_err("gone").is_no_process());
}

#[test]
fn missing_optional_files_degrade_quietly() {
    let mut m = BTreeMap::new();
    put(&mut m, format!("{ROOT}/9/stat"), stat_line(9, "bare", "S", 0, 0));
    put(&mut m, format!("{ROOT}/9/task/9/stat"), stat_line(9, "bare", "S", 0, 0));
    let e = env(m, None);
    let r = inspect(9, &options(&e, "")).expect("inspect");
    assert_eq!((r.verdict.as_str(), r.fds.count, r.io), (KIND_IDLE, 0, None));
    assert!(!r.notes.is_empty(), "unreadable fd table should be noted");
}

#[test]
fn permission_denied_files_are_noted_once() {
    let p = Proc { pid: 12, threads: vec![thr(12, "a", "S"), thr(13, "b", "S")], ..Proc::default() };
    let e = env(proc_files(&p), None);
    e.fs.denied.lock().expect("lock").extend([format!("{ROOT}/12/task/12/wchan"), format!("{ROOT}/12/task/13/wchan")]);
    let r = inspect(12, &options(&e, "")).expect("inspect");
    assert_eq!(r.notes.iter().filter(|n| n.starts_with("wchan not readable (permission)")).count(), 1, "{:?}", r.notes);
}

#[test]
fn read_stat_errors_other_than_missing_are_not_no_process() {
    let p = Proc { pid: 3, threads: vec![thr(3, "a", "S")], ..Proc::default() };
    let e = env(proc_files(&p), None);
    e.fs.denied.lock().expect("lock").push(format!("{ROOT}/3/stat"));
    let err = inspect(3, &options(&e, "")).expect_err("denied");
    assert!(!err.is_no_process());
    assert_eq!(err.to_string(), "read stat of pid 3: open /fixture/3/stat: permission denied");
}

#[test]
fn parse_stat_comm_with_parens() {
    let st = parse_stat(stat_line(1, "a) (b c", "R", 7, 9).as_bytes()).expect("parse");
    assert_eq!(st, StatFields { comm: "a) (b c".into(), state: "R".into(), utime: 7, stime: 9 });
    assert!(parse_stat(b"garbage").is_none());
}

#[test]
fn parse_syscall_nr_table() {
    for (input, want) in [("running\n", -1), ("-1 0x7ffc 0x55\n", -1), ("202 0x1 0x2\n", 202), ("", -2), ("x", -2)] {
        assert_eq!(parse_syscall_nr(input.as_bytes()), want, "{input:?}");
    }
}

#[test]
fn syscall_name_tables() {
    assert_eq!(syscall_name("amd64", 202), "futex");
    assert_eq!(syscall_name("arm64", 98), "futex");
    assert_eq!(syscall_name("amd64", 45), "recvfrom");
    assert_eq!(syscall_name("riscv64", 1), "syscall_1");
    assert_eq!(syscall_name("amd64", 99999), "syscall_99999");
}

/// The real procfs, end to end (Linux hosts only).
#[test]
fn inspect_self() {
    if std::env::consts::OS != "linux" {
        return;
    }
    let pid = i64::from(std::process::id());
    let sleeper = spectra_base::clock::SystemSleeper;
    let opts = Options {
        fs: &spectra_base::fs::OsFs,
        clock: &spectra_base::clock::SystemClock,
        sleeper: &sleeper,
        proc_root: String::new(),
        cgroup_root: String::new(),
        interval: GoDuration(50_000_000),
        arch: String::new(),
    };
    let r = inspect(pid, &opts).expect("inspect self");
    assert!(r.pid == pid && r.thread_count >= 1 && r.fds.count >= 3 && r.memory.rss_kib > 0, "{r:?}");
}

/// An idle process in a throttled, pressured cgroup is still idle; the cgroup findings ride
/// along as context.
#[test]
fn cgroup_findings_do_not_drive_idle_verdict() {
    let p = Proc { pid: 90, threads: vec![Thr { syscall: "232 0x3", ..thr(90, "srv", "S") }], ..Proc::default() };
    let mut m = proc_files(&p);
    let cg = "/cg/app.slice";
    put(&mut m, format!("{cg}/cgroup.controllers"), "cpu memory pids\n");
    put(&mut m, format!("{cg}/cpu.stat"), "usage_usec 100\nnr_periods 100\nnr_throttled 80\nthrottled_usec 5000000\n");
    put(
        &mut m,
        format!("{cg}/cpu.pressure"),
        "some avg10=55.00 avg60=40.00 avg300=20.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
    );
    put(&mut m, format!("{ROOT}/90/cgroup"), "0::/app.slice\n");
    let e = env(m, None);
    let r = inspect(90, &options(&e, "/cg")).expect("inspect");
    assert_eq!(r.verdict, KIND_IDLE, "{:?}", r.findings);
    let throttled = find(&r, "cpu-throttled").expect("cpu-throttled");
    let psi = find(&r, "cpu-pressure").expect("cpu-pressure");
    assert_eq!((throttled.severity, psi.severity), (Severity::Warning, Severity::Critical));
    assert!(r.findings.iter().all(|f| !f.process_scoped()), "{:?}", r.findings);
    assert_eq!(r.exit_code(), 0);
}

/// Process evidence decides the verdict even when a cgroup finding is more severe.
#[test]
fn process_findings_lead_cgroup_context() {
    let f = |kind: &str, severity, scope: &str| Finding {
        kind: kind.into(),
        severity,
        summary: String::new(),
        evidence: Vec::new(),
        scope: scope.into(),
    };
    let fs = vec![f("cgroup-oom-kill", Severity::Critical, SCOPE_CGROUP), f(KIND_FD_GROWTH, Severity::Warning, "")];
    assert_eq!(verdict(&fs, 0.0), KIND_FD_GROWTH);
    assert_eq!(verdict(&fs[..1], 50.0), KIND_ACTIVE);
}

#[test]
fn clock_drives_the_interval() {
    let p = Proc { pid: 7, threads: vec![thr(7, "a", "S")], ..Proc::default() };
    let e = env(proc_files(&p), Some(proc_files(&p)));
    let opts = Options { interval: GoDuration(2_500_000_000), ..options(&e, "") };
    let r = inspect(7, &opts).expect("inspect");
    assert_eq!(r.interval_sec, 2.5);
    assert_eq!(e.clock.now(), jiff::Timestamp::from_second(1000).expect("ts") + jiff::SignedDuration::from_millis(2500));
}

#[test]
fn report_json_matches_go_shapes() {
    let r = Report { pid: 1, verdict: KIND_IDLE.into(), ..Report::default() };
    let json = String::from_utf8(spectra_base::gojson::to_vec(&r).expect("json")).expect("utf8");
    assert_eq!(
        json,
        r#"{"pid":1,"comm":"","state":"","interval_sec":0,"cpu_pct":0,"thread_count":0,"state_counts":{},"threads":null,"fds":{"count":0,"limit":0,"delta":0,"rate_per_sec":0},"memory":{"rss_kib":0,"anon_kib":0,"swap_kib":0,"rss_delta_kib":0,"rss_rate_kib_per_sec":0},"oom_score":0,"findings":null,"verdict":"idle"}"#
    );
}
