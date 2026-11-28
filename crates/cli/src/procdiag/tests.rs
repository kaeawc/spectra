//! Ports of the procdiag tests in `cmd/spectra/procdiag_test.go`.

use std::sync::Arc;

use super::*;
use spectra_base::env::MapEnv;
use spectra_base::fs::RootedFs;

fn finding(kind: &str, severity: Severity, summary: &str, evidence: &[&str], scope: &str) -> Finding {
    Finding {
        kind: kind.into(),
        severity,
        summary: summary.into(),
        evidence: evidence.iter().map(|s| s.to_string()).collect(),
        scope: scope.into(),
    }
}

fn text(r: &Report) -> String {
    let mut out = Vec::new();
    write_text(&mut out, r);
    String::from_utf8(out).expect("utf8")
}

fn run_on(os: Kind, args: &[&str]) -> (i32, String, String) {
    let ctx = Ctx { os, env: Arc::new(MapEnv::default()), fs: Arc::new(RootedFs::new("/nonexistent")), ..Ctx::system("v") };
    let args: Vec<String> = args.iter().map(|s| s.to_string()).collect();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = crate::run(&args, &ctx, &mut out, &mut err);
    (code, String::from_utf8(out).expect("utf8"), String::from_utf8(err).expect("utf8"))
}

// Go: TestProcdiagReportsFindingsAndExit3.
#[test]
fn reports_findings_and_exit_3() {
    let mut rep = Report { pid: 9, comm: "leaky".into(), verdict: diag::KIND_FD_GROWTH.into(), interval_sec: 1.0, ..Report::default() };
    rep.state_counts.insert("S".into(), 1);
    rep.findings = vec![finding(diag::KIND_FD_GROWTH, Severity::Warning, "fds climbing", &["+98 fds"], "")];
    assert_eq!(rep.exit_code(), 3);
    let out = text(&rep);
    for want in ["pid 9 (leaky) — fd-growth", "[warning] fd-growth: fds climbing", "+98 fds"] {
        assert!(out.contains(want), "missing {want:?}:\n{out}");
    }
}

// Go: TestProcdiagInfoOnlyExitsZeroJSON (the exit code; the JSON shape is in the contract cases).
#[test]
fn info_only_exits_zero() {
    let rep = Report { findings: vec![finding("x", Severity::Info, "", &[], "")], ..Report::default() };
    assert_eq!(rep.exit_code(), 0);
}

// Go: TestProcdiagStateCountsAreReadable.
#[test]
fn state_counts_are_readable() {
    let mut rep =
        Report { pid: 9, comm: "srv".into(), verdict: diag::KIND_IDLE.into(), interval_sec: 1.0, thread_count: 5, ..Report::default() };
    rep.state_counts = [("S", 3), ("R", 1), ("D", 1)].into_iter().map(|(k, v)| (k.to_string(), v)).collect();
    let out = text(&rep);
    assert!(out.contains("5 thread(s) (D=1 R=1 S=3)\n") && !out.contains("map["), "{out}");
    assert_eq!(format_state_counts(&BTreeMap::new()), "");
}

// Go: TestProcdiagCgroupContextExitsZero.
#[test]
fn cgroup_context_exits_zero() {
    let rep = Report {
        pid: 9,
        comm: "srv".into(),
        verdict: diag::KIND_IDLE.into(),
        findings: vec![finding("cpu-pressure", Severity::Critical, "cgroup /app.slice cpu: stalled", &[], diag::SCOPE_CGROUP)],
        ..Report::default()
    };
    assert_eq!(rep.exit_code(), 0, "cgroup-only findings exit 0");
    assert!(text(&rep).contains("[critical] (cgroup) cpu-pressure:"), "{}", text(&rep));
}

// Go: TestProcdiagErrors.
#[test]
fn errors() {
    let cases: &[(&[&str], i32, &str)] = &[
        (&["procdiag"], 2, "usage: spectra procdiag [--interval <d>] [--json] <pid>\n"),
        (&["procdiag", "1", "2"], 2, "usage: spectra procdiag [--interval <d>] [--json] <pid>\n"),
        (&["procdiag", "abc"], 2, "invalid PID \"abc\"\n"),
        (&["procdiag", "--", "-3"], 2, "invalid PID \"-3\"\n"),
        (&["procdiag", "--interval", "0s", "5"], 2, "--interval must be positive\n"),
        (&["procdiag", "5"], 1, "spectra procdiag: no such process 5\n"),
    ];
    for &(args, code, msg) in cases {
        let (got, out, err) = run_on(Kind::Linux, args);
        assert_eq!((got, out.as_str(), err.as_str()), (code, "", msg), "{args:?}");
    }
}

// Go: TestProcdiagUnsupportedOnDarwin.
#[test]
fn unsupported_on_darwin_after_parsing_flags() {
    let (code, out, err) = run_on(Kind::Darwin, &["procdiag", "5"]);
    assert_eq!((code, out.as_str()), (1, ""));
    assert_eq!(
        err,
        "spectra procdiag: /proc process diagnosis: unsupported on host OS (darwin) (use `spectra sample` or `spectra hang` on macOS)\n"
    );
    assert_eq!(run_on(Kind::Darwin, &["procdiag", "--nope"]).0, 2, "flags are parsed first");
    assert_eq!(run_on(Kind::Darwin, &["procdiag"]).0, 1, "the OS check comes before the usage check");
}

#[test]
fn api_refuses_off_linux() {
    let ctx = Ctx { os: Kind::Darwin, ..Ctx::system("v") };
    assert!(matches!(crate::api::procdiag(&ctx, Options { pid: 1, ..Options::default() }), Err(Error::Unsupported(_))));
}

#[test]
fn truncate_counts_runes() {
    assert_eq!(truncate("abc", 3), "abc");
    assert_eq!(truncate("abcd", 3), "ab…");
    assert_eq!(truncate("ééééé", 4), "ééé…");
}
