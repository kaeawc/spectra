use spectra_base::gojson::GoTime;
use spectra_host::classify::{App, Role, Runtime, Signing, SigningStatus};
use spectra_host::process::FdBreakdown;

use super::*;

const HOUR: i64 = 3_600_000_000_000;

#[allow(clippy::too_many_arguments)]
fn row(pid: i64, ppid: i64, user: &str, rss: i64, vsz: i64, cpu: f64, mem: f64, elapsed: i64, cmd: &str) -> Info {
    Info {
        pid,
        ppid,
        user: user.into(),
        rss_kib: rss,
        vsize_kib: vsz,
        vsz_bytes: (vsz as u64) * 1024,
        cpu_percent: cpu,
        mem_percent: mem,
        elapsed: GoDuration(elapsed),
        command: cmd.into(),
        ..Info::default()
    }
}

// Go: TestProcessFiltersAndSort.
#[test]
fn filters_and_sort() {
    let mut procs = vec![
        row(10, 1, "root", 1024, 4096, 1.0, 0.1, HOUR, "short"),
        row(20, 1, "root", 4096, 8192, 4.0, 0.4, 3 * HOUR, "long"),
        row(30, 20, "alice", 8192, 16384, 2.0, 0.8, 2 * HOUR, "child"),
    ];
    let f = Filters { min_rss: 2 * 1024 * 1024, min_vsz: 0, min_elapsed: GoDuration(2 * HOUR), ppid: 1, user: "root".into() };
    let filtered: Vec<i64> = procs.iter().filter(|p| matches(p, &f)).map(|p| p.pid).collect();
    assert_eq!(filtered, [20]);
    assert!(sort_processes(&mut procs, "elapsed"));
    assert_eq!(procs.iter().map(|p| p.pid).collect::<Vec<_>>(), [20, 30, 10]);
    assert!(sort_processes(&mut procs, "mem"));
    assert_eq!(procs[0].pid, 30);
    assert!(!sort_processes(&mut procs, "bad"));
}

// Go: TestParseProcessSize.
#[test]
fn process_size() {
    for (raw, want) in [("", 0), ("512", 512), ("2K", 2 * 1024), ("1.5M", 1536 * 1024), ("3G", 3 * 1024 * 1024 * 1024), (" 1t ", 1 << 40)] {
        assert_eq!(parse_process_size(raw), Ok(want), "{raw:?}");
    }
    assert!(parse_process_size("-1").is_err());
    assert!(parse_process_size("x").is_err());
}

/// Every field `spectra process --json` emitted before `--classify` existed (Go's
/// `pinnedProcessFixture`).
fn pinned() -> Vec<Info> {
    let start = GoTime::parse_rfc3339("2026-11-19T08:30:00Z").expect("time");
    vec![
        Info {
            pid: 4242,
            ppid: 1,
            uid: 501,
            user: "alice".into(),
            command: "Slack Helper".into(),
            bsd_name: "Slack Helper".into(),
            executable_path: "/Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper".into(),
            full_command_line: "/Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper --type=renderer"
                .into(),
            rss_kib: 2048,
            vsize_kib: 4096,
            vsz_bytes: 4096 * 1024,
            thread_count: 12,
            cpu_pct: 1.5,
            cpu_percent: 1.5,
            mem_percent: 0.25,
            start_time: start,
            started_at: start,
            elapsed: GoDuration(90_000_000_000),
            open_fds: 7,
            fd_breakdown: Some(FdBreakdown { total: 7, pty: 1, socket: 2, regular: 3, pipe: 1, ..FdBreakdown::default() }),
            listening_ports: vec![8080],
            outbound_connections: vec!["1.2.3.4:443".into()],
            log_files: vec!["/tmp/x.log".into()],
            cwd: "/".into(),
            app_path: "/Applications/Slack.app".into(),
            ..Info::default()
        },
        Info { pid: 7, command: "launchd".into(), full_command_line: "/sbin/launchd".into(), ..Info::default() },
    ]
}

// Go: TestProcessJSONWithoutClassifyIsPinned (against Go's golden file).
#[test]
fn json_without_classify_is_pinned() {
    let want = include_str!("../../../../cmd/spectra/testdata/process_unclassified.golden.json");
    let got = Encoder { indent: true, escape_html: true }.encode_to_vec(&pinned()).expect("json");
    assert_eq!(String::from_utf8(got).expect("utf8"), want);
}

// Go: TestProcessJSONWithClassifyAddsFields.
#[test]
fn json_with_classify_adds_fields() {
    let mut p = pinned().remove(0);
    p.role = Role::AppHelper;
    p.runtime = Runtime::Electron;
    p.app =
        Some(App { name: "Slack".into(), bundle_path: "/Applications/Slack.app".into(), bundle_id: "com.tinyspeck.slackmacgap".into() });
    p.signing = Some(Signing { status: SigningStatus::DeveloperId, team_id: "BQR82RBBHL".into(), hardened_runtime: true, validated: true });
    p.evidence = vec!["role: x".into(), "runtime: y".into()];
    let data = String::from_utf8(spectra_base::gojson::to_vec(&p).expect("json")).expect("utf8");
    for want in [
        r#""role":"app_helper""#,
        r#""runtime":"electron""#,
        r#""app":{"name":"Slack","bundle_path":"/Applications/Slack.app","bundle_id":"com.tinyspeck.slackmacgap"}"#,
        r#""signing":{"status":"developer_id","team_id":"BQR82RBBHL","hardened_runtime":true,"validated":true}"#,
        r#""evidence":["role: x","runtime: y"]"#,
    ] {
        assert!(data.contains(want), "JSON missing {want}:\n{data}");
    }
}

// Go: TestProcessClassColumnsOffAddNothing.
#[test]
fn class_columns_off_add_nothing() {
    let off = ClassColumns::new(false, Kind::Darwin);
    assert_eq!((off.header(), off.row(&Info { role: Role::App, ..Info::default() }), off.width()), (String::new(), String::new(), 0));
    let on = ClassColumns { on: true, sign: false };
    let got = on.row(&Info { role: Role::Cli, runtime: Runtime::Go, ..Info::default() });
    assert_eq!(got, format!("cli         go        -{}", " ".repeat(CLASS_APP_WIDTH + 1)));
    assert!(ClassColumns { on: true, sign: true }.row(&Info::default()).starts_with("-           -         -"));
    assert!(!ClassColumns::new(true, Kind::Linux).sign, "SIGN is macOS only");
}

// Go: TestProcessClassColumnsShowBoundedApp.
#[test]
fn class_columns_bound_the_app() {
    let cols = ClassColumns { on: true, sign: true };
    assert!(cols.header().contains("APP"));
    let long = Info { app: Some(App { name: "Visual Studio Code - Insiders Edition".into(), ..App::default() }), ..Info::default() };
    let row = cols.row(&long);
    assert!(row.contains("Visual Studio C…") && !row.contains("Insiders"), "{row:?}");
    assert_eq!((row.chars().count(), cols.header().chars().count()), (cols.width(), cols.width()));
    let slack = Info { app: Some(App { name: "Slack".into(), ..App::default() }), ..Info::default() };
    assert!(cols.row(&slack).contains("Slack "));
}

#[test]
fn text_helpers_match_go() {
    assert_eq!(
        (human_size_kib(0), human_size_kib(512), human_size_kib(2048), human_size_kib(3 << 20)),
        ("-".into(), "512K".into(), "2.0M".into(), "3.0G".into())
    );
    assert_eq!(
        (human_size_bytes(0), human_size_bytes(1000), human_size_bytes(4096), human_size_bytes(5 << 20)),
        ("-".into(), "1000B".into(), "4K".into(), "5.0M".into())
    );
    assert_eq!(truncate("héllo wörld", 5), "héll…");
    assert_eq!(truncate("short", 40), "short");
    for (d, want) in [(5_000_000_000, "5s"), (125 * 1_000_000_000, "2m5s"), (2 * HOUR + 120_000_000_000, "2h2m"), (49 * HOUR, "2d1h")] {
        assert_eq!(format_duration(GoDuration(d)), want);
    }
    let m = process::Member { pid: 7, command: "clang".into(), cpu_pct: 97.9, ..process::Member::default() };
    assert_eq!(format_members(&[m.clone(), m]), "clang (pid 7, 98%), clang (pid 7, 98%)");
    assert_eq!(Error::Sort("size".into()).to_string(), r#"invalid --sort "size""#);
}
