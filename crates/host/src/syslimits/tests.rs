use std::fmt::Write as _;

use spectra_base::clock::FakeClock;
use spectra_base::proc::{FakeRunner, match_exact};

use super::*;

fn fixed_now() -> jiff::Timestamp {
    "2026-05-26T12:00:00Z".parse().expect("timestamp")
}

fn stub(responses: &[(&[&str], &str)]) -> FakeRunner {
    let fake = FakeRunner::new();
    for (argv, out) in responses {
        fake.on_stdout(match_exact(argv[0], &argv[1..]), *out);
    }
    fake
}

fn lsof_pty_fixture(n: usize) -> String {
    let mut b = String::from("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\n");
    for i in 0..n {
        let _ = writeln!(b, "App 777 alice {i}u CHR 3,2 0t0 339 /dev/ttys{i:03}");
    }
    b
}

fn numbered_lines(n: usize) -> String {
    (1..=n).map(|i| format!("{i}\n")).collect()
}

fn collect_with(runner: &FakeRunner) -> SystemLimits {
    let clock = FakeClock::new(fixed_now());
    collect(&Options { runner, clock: &clock, uid: 501 })
}

// Go: TestCollectHealthyMachine.
#[test]
fn collect_healthy_machine() {
    let lsof = lsof_pty_fixture(10);
    let runner = stub(&[
        (&["sysctl", "-n", "kern.tty.ptmx_max"], "128\n"),
        (&["sysctl", "-n", "kern.num_files"], "100\n"),
        (&["sysctl", "-n", "kern.maxfiles"], "1000\n"),
        (&["sysctl", "-n", "kern.maxfilesperproc"], "256\n"),
        (&["sysctl", "-n", "kern.maxproc"], "2048\n"),
        (&["sysctl", "-n", "kern.maxprocperuid"], "1024\n"),
        (&["lsof", "-n", "-P", "-d", "^txt"], &lsof),
        (&["ps", "-A", "-o", "pid="], "1\n2\n3\n4\n"),
        (&["ps", "-u", "501", "-o", "pid="], "2\n3\n"),
    ]);
    let got = collect_with(&runner);
    assert_eq!((got.pty.current, got.pty.limit, got.pty.warn, got.pty.critical), (10, 128, false, false));
    assert_eq!((got.files.current, got.files.limit, got.files_per_proc), (100, 1000, 256));
    assert_eq!((got.procs.current, got.procs_per_uid.current), (4, 2));
    assert_eq!(got.collected_at, GoTime::utc(fixed_now()));
    assert!(got.partial_failures.is_empty());
}

// Go: TestCollectWarnCriticalAndSaturated.
#[test]
fn collect_warn_critical_and_saturated() {
    let (lsof, all, uid) = (lsof_pty_fixture(85), numbered_lines(120), numbered_lines(50));
    let runner = stub(&[
        (&["sysctl", "-n", "kern.tty.ptmx_max"], "100\n"),
        (&["sysctl", "-n", "kern.num_files"], "98\n"),
        (&["sysctl", "-n", "kern.maxfiles"], "100\n"),
        (&["sysctl", "-n", "kern.maxfilesperproc"], "256\n"),
        (&["sysctl", "-n", "kern.maxproc"], "100\n"),
        (&["sysctl", "-n", "kern.maxprocperuid"], "100\n"),
        (&["lsof", "-n", "-P", "-d", "^txt"], &lsof),
        (&["ps", "-A", "-o", "pid="], &all),
        (&["ps", "-u", "501", "-o", "pid="], &uid),
    ]);
    let got = collect_with(&runner);
    assert!(got.pty.warn && !got.pty.critical, "{:?}", got.pty);
    assert!(got.files.critical, "{:?}", got.files);
    assert!(got.procs.pct == 120.0 && got.procs.critical, "{:?}", got.procs);
    assert!(!got.procs_per_uid.warn && !got.procs_per_uid.critical, "{:?}", got.procs_per_uid);
    assert!(got.any_critical());
}

// Go: TestCollectPartialFailures.
#[test]
fn collect_partial_failures() {
    let runner = stub(&[
        (&["sysctl", "-n", "kern.tty.ptmx_max"], "100\n"),
        (&["sysctl", "-n", "kern.maxfiles"], "100\n"),
        (&["sysctl", "-n", "kern.maxfilesperproc"], "256\n"),
        (&["sysctl", "-n", "kern.maxproc"], "100\n"),
        (&["sysctl", "-n", "kern.maxprocperuid"], "100\n"),
        (&["lsof", "-n", "-P", "-d", "^txt"], ""),
        (&["ps", "-A", "-o", "pid="], "1\n"),
        (&["ps", "-u", "501", "-o", "pid="], "1\n"),
    ]);
    assert_eq!(collect_with(&runner).partial_failures, ["kern.num_files"]);
}

#[test]
fn failures_are_named_in_go_order() {
    let got = collect_with(&FakeRunner::new());
    assert_eq!(
        got.partial_failures,
        [
            "lsof:pty",
            "ps:procs",
            "ps:procs_per_uid",
            "kern.tty.ptmx_max",
            "kern.num_files",
            "kern.maxfiles",
            "kern.maxfilesperproc",
            "kern.maxproc",
            "kern.maxprocperuid"
        ]
    );
}

#[test]
fn failing_lsof_with_output_still_counts_and_sysctl_range_keeps_the_clamped_value() {
    let fake = FakeRunner::new();
    fake.on(
        match_exact("lsof", &["-n", "-P", "-d", "^txt"]),
        spectra_base::proc::Output { stdout: lsof_pty_fixture(3).into_bytes(), stderr: Vec::new(), exit_code: 1 },
    );
    fake.on_stdout(match_exact("sysctl", &["-n", "kern.maxproc"]), "99999999999999999999\n");
    fake.on_stdout(match_exact("sysctl", &["-n", "kern.maxfiles"]), "12x\n");
    let got = collect_with(&fake);
    assert_eq!(got.pty.current, 3);
    assert!(!got.partial_failures.contains(&"lsof:pty".to_string()));
    assert_eq!(got.procs.limit, i64::MAX);
    assert!(got.partial_failures.contains(&"kern.maxproc".to_string()));
    assert_eq!(got.files.limit, 0);
}

#[test]
fn usage_thresholds_are_strict() {
    assert!(!usage(80, 100).warn && usage(81, 100).warn);
    assert!(!usage(95, 100).critical && usage(96, 100).critical);
    assert_eq!(usage(5, 0), ResourceUsage { limit: 0, current: 5, pct: 0.0, warn: false, critical: false });
}

fn proc(pid: i64, ppid: i64, uid: i64, command: &str, open_fds: i64, pty: i64) -> Info {
    Info {
        pid,
        ppid,
        uid,
        command: command.into(),
        open_fds,
        fd_breakdown: (pty > 0).then(|| crate::process::FdBreakdown { pty, ..Default::default() }),
        ..Info::default()
    }
}

#[test]
fn top_holders_rank_by_count_then_pid_and_name_unlisted_parents() {
    let procs = vec![
        proc(1, 0, 0, "launchd", 3, 0),
        proc(10, 1, 501, "app", 7, 2),
        proc(11, 10, 501, "helper", 7, 1),
        proc(12, 10, 501, "helper", 1, 0),
        proc(13, 99, 502, "orphan", 0, 0),
    ];
    let top = top_holders_of(&procs, 2, 501);
    assert_eq!(top["pty"].iter().map(|h| (h.pid, h.count)).collect::<Vec<_>>(), [(10, 2), (11, 1)]);
    assert_eq!(top["files"].iter().map(|h| (h.pid, h.count)).collect::<Vec<_>>(), [(10, 7), (11, 7)]);
    assert_eq!(top["procs"].iter().map(|h| (h.pid, h.count, h.command.as_str())).collect::<Vec<_>>(), [(10, 2, "app"), (1, 1, "launchd")]);
    let all = top_holders_of(&procs, 0, 501);
    assert_eq!(all["procs"].last().map(|h| h.command.as_str()), Some("pid 99"));
    // procs_per_uid counts only the given user's processes.
    assert_eq!(all["procs_per_uid"].iter().map(|h| h.pid).collect::<Vec<_>>(), [10, 1]);
}

#[test]
fn limits_serialize_like_go() {
    let clock = FakeClock::new(fixed_now());
    let got = collect(&Options { runner: &FakeRunner::new(), clock: &clock, uid: 501 });
    let json = String::from_utf8(spectra_base::gojson::to_vec(&got).expect("json")).expect("utf8");
    assert!(json.starts_with(r#"{"pty":{"limit":0,"current":0,"pct":0,"warn":false,"critical":false}"#), "{json}");
    assert!(
        json.contains(r#""max_files_per_proc":0,"#) && json.contains(r#""collected_at":"2026-05-26T12:00:00Z","partial_failures":["#),
        "{json}"
    );
}
