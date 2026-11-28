use spectra_base::clock::FakeClock;
use spectra_base::hostos::Kind;
use spectra_base::proc::{FakeRunner, Output, match_exact};
use spectra_host::syslimits::FixedSystem;

use super::*;

fn healthy_runner() -> FakeRunner {
    let fake = FakeRunner::new();
    for (key, v) in [
        ("kern.tty.ptmx_max", "511"),
        ("kern.num_files", "4096"),
        ("kern.maxfiles", "245760"),
        ("kern.maxfilesperproc", "122880"),
        ("kern.maxproc", "6000"),
        ("kern.maxprocperuid", "4000"),
    ] {
        fake.on_stdout(match_exact("sysctl", &["-n", key]), format!("{v}\n"));
    }
    fake.on_stdout(
        match_exact("lsof", &["-n", "-P", "-d", "^txt"]),
        "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\nzsh 7 u 0u CHR 16,3 0t0 1 /dev/ttys003\n",
    );
    fake.on_stdout(match_exact("ps", &["-A", "-o", "pid="]), "1\n2\n3\n");
    fake.on_stdout(match_exact("ps", &["-u", "501", "-o", "pid="]), "2\n3\n");
    fake
}

fn ctx(runner: FakeRunner) -> Ctx {
    Ctx {
        os: Kind::Darwin,
        runner: std::sync::Arc::new(runner),
        clock: std::sync::Arc::new(FakeClock::new("2026-11-23T10:00:00Z".parse().expect("now"))),
        system: std::sync::Arc::new(FixedSystem { uid: 501 }),
        ..Ctx::system("test")
    }
}

fn run_args(ctx: &Ctx, args: &[&str]) -> (i32, String, String) {
    let args: Vec<String> = args.iter().map(ToString::to_string).collect();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = crate::run(&args, ctx, &mut out, &mut err);
    (code, String::from_utf8(out).expect("utf8"), String::from_utf8(err).expect("utf8"))
}

#[test]
fn healthy_text_exits_zero_and_pads_columns_like_go() {
    let (code, out, err) = run_args(&ctx(healthy_runner()), &["system", "limits"]);
    assert_eq!((code, err.as_str()), (0, ""));
    assert_eq!(
        out,
        "RESOURCE        USED      LIMIT     PCT      STATE\n\
         ----------------------------------------------------\n\
         pty slots       1         511       0%       \n\
         open files      4096      245760    2%       \n\
         processes       3         6000      0%       \n\
         processes/uid   2         4000      0%       \n\
         files/proc      -         122880    -        \n"
    );
}

#[test]
fn saturated_exits_one_and_json_is_the_limits_alone() {
    let fake = healthy_runner();
    // Later rules lose to earlier ones, so a saturated host needs its own runner.
    let saturated = FakeRunner::new();
    saturated.on_stdout(match_exact("sysctl", &["-n", "kern.maxproc"]), "3\n");
    saturated.on_stdout(match_exact("ps", &["-A", "-o", "pid="]), "1\n2\n3\n");
    drop(fake);
    let (code, out, _) = run_args(&ctx(saturated), &["system", "limits", "--json"]);
    assert_eq!(code, 1);
    assert!(out.starts_with("{\n  \"pty\": {"), "{out}");
    assert!(!out.contains("top_holders"), "{out}");
    assert!(out.contains("\"partial_failures\": ["), "{out}");
}

#[test]
fn partial_failures_warn_on_stderr_in_text_mode() {
    let (code, out, err) = run_args(&ctx(FakeRunner::new()), &["system", "limits"]);
    assert_eq!(code, 0);
    assert!(out.contains("pty slots       0         0         -"), "{out}");
    assert!(err.starts_with("warning: partial collection failures: lsof:pty, ps:procs, ps:procs_per_uid, kern.tty.ptmx_max"), "{err}");
}

#[test]
fn a_failing_sysctl_with_output_still_fails() {
    let fake = FakeRunner::new();
    fake.on(match_exact("sysctl", &["-n", "kern.maxproc"]), Output { stdout: b"9\n".to_vec(), stderr: Vec::new(), exit_code: 1 });
    let report = collect(&ctx(fake), &Options::default());
    assert_eq!(report.limits.procs.limit, 0);
    assert!(report.limits.partial_failures.contains(&"kern.maxproc".to_string()));
    assert!(report.top_holders.is_empty());
}

#[test]
fn usage_errors_exit_two() {
    let ctx = ctx(FakeRunner::new());
    for args in [&["system"][..], &["system", "nope"], &["system", "--json"]] {
        let (code, out, err) = run_args(&ctx, args);
        assert_eq!((code, out.as_str(), err.as_str()), (2, "", "usage: spectra system limits [--json] [--top]\n"), "{args:?}");
    }
    let (code, _, err) = run_args(&ctx, &["system", "limits", "--nope"]);
    assert_eq!(code, 2);
    assert!(err.starts_with("flag provided but not defined: -nope\nUsage of spectra system limits:\n"), "{err}");
}

#[test]
fn top_holders_print_only_for_saturated_resources() {
    use spectra_host::syslimits::{TopHolder, usage};
    let limits = SystemLimits {
        pty: usage(10, 10),
        files: usage(50, 100),
        files_per_proc: 1,
        procs: usage(9, 10),
        procs_per_uid: usage(10, 10),
        collected_at: spectra_base::gojson::GoTime::zero(),
        partial_failures: Vec::new(),
    };
    let row = |pid, count, command: &str| TopHolder { pid, count, command: command.into() };
    let mut top = TopHolders::new();
    top.insert("pty".into(), vec![row(7, 3, &"x".repeat(60))]);
    top.insert("files".into(), vec![row(8, 9, "not shown")]);
    top.insert("procs".into(), vec![row(1, 5, "launchd")]);
    top.insert("procs_per_uid".into(), Vec::new());
    let mut out = Vec::new();
    write_top(&mut out, &limits, &top);
    let want = format!(
        "\nRESOURCE: pty slots (10/10, CRITICAL)\n  PID      COUNT    COMMAND\n  7        3        {}…\n\nRESOURCE: processes (9/10, WARN)\n  PID      COUNT    COMMAND\n  1        5        launchd\n",
        "x".repeat(47)
    );
    assert_eq!(String::from_utf8(out).expect("utf8"), want);
}
