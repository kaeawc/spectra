use std::path::Path;
use std::sync::Arc;
use std::sync::Mutex;

use jiff::tz::Offset;
use spectra_base::clock::FakeClock;
use spectra_base::env::MapEnv;
use spectra_base::fs::OsFs;
use spectra_base::fsutil::write_file_atomic;
use spectra_base::proc::{FakeRunner, Output, RunError, match_exact};

use super::*;

/// The ps format: pid ppid pcpu rss vsz uid user lstart(5) command.
const PS_FIXTURE: &str = "1      0   0.0   4096    8192 0 root Sat May  2 22:37:01 2026 /sbin/launchd
412   1    1.2  184320  409600 501 alice Sat May  2 22:40:00 2026 /Applications/Slack.app/Contents/MacOS/Slack --some-flag
415   412  0.5  92160   204800 501 alice Sat May  2 22:40:05 2026 /Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper --type=renderer
999   1    0.0  2048    4096 501 alice Sat May  2 23:00:00 2026 /bin/bash -l
";

fn utc() -> TimeZone {
    TimeZone::UTC
}

/// The seams a test collects through.
struct Env {
    runner: FakeRunner,
    env: MapEnv,
    clock: FakeClock,
    source: FixedProcesses,
    tz: TimeZone,
    proc_fs: String,
    os: Kind,
}

impl Env {
    fn darwin(runner: FakeRunner) -> Env {
        Env {
            runner,
            env: MapEnv::default(),
            clock: FakeClock::new(jiff::Timestamp::UNIX_EPOCH),
            source: FixedProcesses::default(),
            tz: utc(),
            proc_fs: String::new(),
            os: Kind::Darwin,
        }
    }

    fn seams(&self) -> Seams<'_> {
        Seams {
            os: self.os,
            runner: &self.runner,
            env: &self.env,
            fs: &OsFs,
            clock: &self.clock,
            source: &self.source,
            tz: &self.tz,
            proc_fs: &self.proc_fs,
        }
    }

    fn collect(&self, opts: &CollectOptions) -> Vec<Info> {
        collect_all(&self.seams(), opts)
    }
}

fn ps_runner(listing: &str, comm: Option<&str>) -> FakeRunner {
    let r = FakeRunner::new();
    r.on_stdout(match_exact("ps", &["-axwwo", PS_COLUMNS]), listing.as_bytes().to_vec());
    match comm {
        Some(c) => r.on_stdout(match_exact("ps", &["-axwwo", "pid=,comm="]), c.as_bytes().to_vec()),
        None => r.on_error(match_exact("ps", &["-axwwo", "pid=,comm="]), "ps failed"),
    };
    r
}

// Go: TestParsePS.
#[test]
fn parse_ps_rows() {
    assert_eq!(parse_ps(PS_FIXTURE, &utc()).len(), 4);
}

// Go: TestParsePSFields.
#[test]
fn parse_ps_fields() {
    let slack = &parse_ps(PS_FIXTURE, &utc())[1];
    assert_eq!((slack.pid, slack.ppid, slack.thread_count), (412, 1, 0));
    assert_eq!(slack.cpu_pct, 1.2);
    assert_eq!((slack.rss_kib, slack.vsize_kib), (184_320, 409_600));
    assert_eq!((slack.user.as_str(), slack.command.as_str()), ("alice", "Slack"));
    assert_eq!(slack.full_command_line, "/Applications/Slack.app/Contents/MacOS/Slack --some-flag");
}

// Go: TestParsePSEmptyLines, TestParsePSShortRow.
#[test]
fn parse_ps_skips_blank_and_short_rows() {
    assert!(parse_ps("\n\n", &utc()).is_empty());
    assert!(parse_ps("412 1 1 0.0 100", &utc()).is_empty());
}

// Go: TestParsePSStartTime.
#[test]
fn parse_ps_start_time_is_local() {
    let tz = TimeZone::fixed(Offset::constant(-5));
    let slack = &parse_ps(PS_FIXTURE, &tz)[1];
    assert!(!slack.start_time.is_zero());
    assert_eq!(slack.start_time.to_string(), "2026-05-02T17:40:00-05:00", "UTC lstart shown in the local zone");
}

// Go: TestCollectAll.
#[test]
fn collect_all_derives_fields() {
    let mut e = Env::darwin(ps_runner(PS_FIXTURE, Some("")));
    e.clock = FakeClock::new("2026-05-03T22:40:00Z".parse().expect("ts"));
    e.source.memory_bytes = 1024 * 1024 * 1024;
    let procs = e.collect(&CollectOptions::default());
    assert_eq!(procs.len(), 4);
    let slack = &procs[1];
    assert_eq!(slack.vsz_bytes, 409_600 * 1024);
    assert_eq!(slack.cpu_percent, slack.cpu_pct);
    assert!(slack.mem_percent > 17.5 && slack.mem_percent < 17.6, "{}", slack.mem_percent);
    assert_eq!(slack.elapsed, GoDuration(24 * 3600 * 1_000_000_000));
    assert_eq!(slack.started_at, slack.start_time);
}

// Go: TestCollectAllWithProcRunner.
#[test]
fn collect_all_runs_two_listings() {
    let comm = "415 /Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper\n";
    let e = Env::darwin(ps_runner(PS_FIXTURE, Some(comm)));
    let procs = e.collect(&CollectOptions::default());
    assert_eq!(procs.len(), 4);
    assert_eq!(e.runner.calls().len(), 2, "command listing + comm listing");
    assert_eq!(procs[2].command, "Slack Helper");
}

// Go: TestCollectAllRunsPSWithUTCAndCLocale.
#[test]
fn ps_runs_with_utc_and_c_locale() {
    let mut e = Env::darwin(ps_runner(PS_FIXTURE, Some("")));
    e.env = MapEnv::new([("TZ", "America/Chicago"), ("LC_ALL", "de_DE"), ("PATH", "/bin")]);
    e.collect(&CollectOptions::default());
    let calls = e.runner.calls();
    assert!(!calls.is_empty());
    for c in calls {
        let env = c.env.clone().unwrap_or_default();
        assert!(env.contains(&("TZ".into(), "UTC0".into())) && env.contains(&("LC_ALL".into(), "C".into())), "{env:?}");
        assert!(!env.contains(&("TZ".into(), "America/Chicago".into())));
    }
}

// Go: TestParseRowDistinctAcrossFallBackHour.
#[test]
fn lstart_is_read_as_utc_across_a_fall_back_hour() {
    let Ok(data) = OsFs.read(Path::new("/usr/share/zoneinfo/America/New_York")) else { return };
    let tz = TimeZone::tzif("America/New_York", &data).expect("tzif");
    // 2026-11-01 05:30Z is 01:30 EDT; 06:30Z is 01:30 EST.
    let a = parse_row("10 1 0.0 1 1 501 u Sun Nov 1 05:30:00 2026 /bin/a", &tz);
    let b = parse_row("11 1 0.0 1 1 501 u Sun Nov 1 06:30:00 2026 /bin/b", &tz);
    assert_eq!(b.started_at.instant.as_second() - a.started_at.instant.as_second(), 3600);
    let wall = |t: &GoTime| t.to_string()[11..16].to_string();
    assert_eq!((wall(&a.started_at), wall(&b.started_at)), ("01:30".into(), "01:30".into()));
}

#[test]
fn lstart_follows_go_parse_rules() {
    let ok = |s: &str| parse_lstart(s).map(|t| t.to_string());
    assert_eq!(ok("Fri Nov 20 10:00:00 2026").as_deref(), Some("2026-11-20T10:00:00Z"));
    assert_eq!(ok("fri nov 2 9:05:07 2026").as_deref(), Some("2026-11-02T09:05:07Z"), "names fold case; 1-digit day and hour");
    assert_eq!(ok("Mon Nov 20 10:00:00 2026").as_deref(), Some("2026-11-20T10:00:00Z"), "the weekday is not checked");
    for bad in [
        "Xyz Nov 23 07:00:00 2026",
        "Fri Foo 20 10:00:00 2026",
        "Fri Nov 31 10:00:00 2026",
        "Fri Nov 20 24:00:00 2026",
        "Fri Nov 20 10:0:00 2026",
        "Fri Nov 20 10:00:60 2026",
        "Fri Nov 20 10:00:00 26",
        "Fri Nov 20 10:00:00 2026x",
        "Fri Feb 29 10:00:00 2026",
    ] {
        assert_eq!(parse_lstart(bad), None, "{bad}");
    }
    assert!(parse_lstart("Sat Feb 29 10:00:00 2028").is_some(), "leap day");
}

/// `ps -axwwo pid=,...,command=` captured on macOS 26 (argument tails trimmed); 62337 caught
/// mid-exec, 41214 a zombie, 49282 missing from the comm listing, 99999 only there.
const PS_SPACES: &str = "  370     1   0.0   2464 435371600     0 root  Thu Nov 12 16:16:29 2026     /usr/sbin/syslogd
 1345     1   0.0  45344 469038976   501 jason Thu Nov 12 16:18:20 2026     /Users/jason/Library/Application Support/JetBrains/Daemon/bundles/current/jetbrainsd.app/Contents/MacOS/jetbrainsd run
 2515 69551   0.0 160736 1951527712   501 jason Wed Nov 18 19:31:51 2026     /Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/157.0.8103.79/Helpers/Google Chrome Helper (Aperitif Renderer).app/Contents/MacOS/Google Chrome Helper (Aperitif Renderer) --type=renderer --origin-trial-disabled-features=CanvasTextNg
 5589 73802   0.0     32 435299504   501 jason Wed Nov 18 19:54:41 2026     /Applications/Claude.app/Contents/Helpers/disclaimer --pgroup -- /Users/jason/Library/Application Support/Claude/claude-code/2.1.321/ddbdf2b334e1/claude.app/Contents/MacOS/claude --output-format stream-json
 5591  5589   0.0 252528 441158720   501 jason Wed Nov 18 19:54:41 2026     /Users/jason/Library/Application Support/Claude/claude-code/2.1.321/ddbdf2b334e1/claude.app/Contents/MacOS/claude --output-format stream-json --verbose
62337 62105   0.3   2704 435433888   501 jason            Thu Nov 19 12:55:51 2026     (bash)
49282 49279   2.6 556816 410565376   501 jason            Thu Nov 19 11:52:05 2026     /Library/Developer/CoreSimulator/Volumes/iOS_23F77/Library/Developer/CoreSimulator/Profiles/Runtimes/iOS 26.5.simruntime/Contents/Resources/RuntimeRoot/System/Library/CoreServices/SpringBoard.app/SpringBoard
41214 41209   0.0      0        0   501 jason Thu Nov 19 12:52:42 2026     <defunct>
49281 49279   0.6  52016 410134592   501 jason Thu Nov 19 11:52:05 2026     /Library/Developer/CoreSimulator/Volumes/iOS_23F77/Library/Developer/CoreSimulator/Profiles/Runtimes/iOS 26.5.simruntime/Contents/Resources/RuntimeRoot/usr/libexec/UserEventAgent (System)
76290 76289   0.0     32 435308240     0 root  Fri Nov 13 12:21:52 2026     /usr/bin/login -fpl jason /Applications/iTerm.app/Contents/MacOS/ShellLauncher --launch_shell
";

/// `ps -axwwo pid=,comm=` on the same host.
const PS_COMM: &str = "  370 /usr/sbin/syslogd
 1345 /Users/jason/Library/Application Support/JetBrains/Daemon/bundles/current/jetbrainsd.app/Contents/MacOS/jetbrainsd
 2515 /Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/157.0.8103.79/Helpers/Google Chrome Helper (Aperitif Renderer).app/Contents/MacOS/Google Chrome Helper (Aperitif Renderer)
 5589 /Applications/Claude.app/Contents/Helpers/disclaimer
 5591 /Users/jason/Library/Application Support/Claude/claude-code/2.1.321/ddbdf2b334e1/claude.app/Contents/MacOS/claude
62337 (bash)
41214 <defunct>
49281 /Library/Developer/CoreSimulator/Volumes/iOS_23F77/Library/Developer/CoreSimulator/Profiles/Runtimes/iOS 26.5.simruntime/Contents/Resources/RuntimeRoot/usr/libexec/UserEventAgent
76290 /usr/bin/login
99999 /usr/bin/true
";

// Go: TestCollectAllNamesExecutablesWithSpaces.
#[test]
fn comm_listing_names_executables_with_spaces() {
    let procs = Env::darwin(ps_runner(PS_SPACES, Some(PS_COMM))).collect(&CollectOptions::default());
    let want: &[(i64, &str, &str)] = &[
        (370, "syslogd", "/usr/sbin/syslogd"),
        (
            1345,
            "jetbrainsd",
            "/Users/jason/Library/Application Support/JetBrains/Daemon/bundles/current/jetbrainsd.app/Contents/MacOS/jetbrainsd run",
        ),
        (2515, "Google Chrome Helper (Aperitif Renderer)", ""),
        (5589, "disclaimer", ""),
        (
            5591,
            "claude",
            "/Users/jason/Library/Application Support/Claude/claude-code/2.1.321/ddbdf2b334e1/claude.app/Contents/MacOS/claude --output-format stream-json --verbose",
        ),
        (62337, "(bash)", "(bash)"),
        (49282, "iOS", ""),
        (41214, "<defunct>", "<defunct>"),
        (49281, "UserEventAgent", ""),
        (76290, "login", "/usr/bin/login -fpl jason /Applications/iTerm.app/Contents/MacOS/ShellLauncher --launch_shell"),
    ];
    assert_eq!(procs.len(), want.len());
    for &(pid, cmd, full) in want {
        let p = procs.iter().find(|p| p.pid == pid).unwrap_or_else(|| panic!("pid {pid}"));
        assert_eq!(p.command, cmd, "pid {pid}");
        if !full.is_empty() {
            assert_eq!(p.full_command_line, full, "pid {pid}");
        }
    }
}

// Go: TestExecNameRejectsMismatchedArgv0, TestExecNameIgnoresCollapsedWhitespace.
#[test]
fn exec_name_trusts_only_a_matching_argv0() {
    for (full, argv0, want) in [
        ("/usr/bin/foo a b", "/usr/bin/foo", "foo"),
        ("/usr/bin/foo", "/usr/bin/foo", "foo"),
        ("/usr/bin/foobar x", "/usr/bin/foo", ""),
        ("/bin/sh -c x", "/usr/bin/foo", ""),
        ("/bin/sh", "", ""),
    ] {
        assert_eq!(exec_name(full, argv0), want, "{full:?} {argv0:?}");
    }
    assert_eq!(
        exec_name("/Applications/My App.app/Contents/MacOS/My App --flag", "/Applications/My  App.app/Contents/MacOS/My  App"),
        "My  App"
    );
}

// Go: TestCollectAllCommListingFailureKeepsFallback.
#[test]
fn comm_listing_failure_keeps_the_fallback() {
    let procs = Env::darwin(ps_runner(PS_SPACES, None)).collect(&CollectOptions::default());
    let p = procs.iter().find(|p| p.pid == 1345).expect("pid 1345");
    assert_eq!(p.command, "Application", "whitespace-split fallback");
}

// Go: TestCollectAllAppliesThreadCounts and TestCollectAllAppliesProcessDetails (both through
// the one details source here).
#[test]
fn details_apply_thread_counts_names_and_paths() {
    let mut e = Env::darwin(ps_runner(PS_FIXTURE, Some("")));
    e.source.details.insert(
        412,
        Details { thread_count: 13, bsd_name: "Slack".into(), executable_path: "/Applications/Slack.app/Contents/MacOS/Slack".into() },
    );
    e.source.details.insert(415, Details { thread_count: 7, ..Details::default() });
    let procs = e.collect(&CollectOptions::default());
    assert_eq!((procs[0].thread_count, procs[1].thread_count, procs[2].thread_count), (0, 13, 7));
    assert_eq!((procs[1].bsd_name.as_str(), procs[1].executable_path.as_str()), ("Slack", "/Applications/Slack.app/Contents/MacOS/Slack"));
    assert_eq!(procs[2].bsd_name, "", "a zero-valued detail leaves the field alone");
}

/// A source that records the pids it was asked about.
struct CountingSource(Mutex<Vec<Vec<i64>>>);

impl ProcessSource for CountingSource {
    fn details(&self, pids: &[i64]) -> BTreeMap<i64, Details> {
        self.0.lock().expect("lock").push(pids.to_vec());
        BTreeMap::new()
    }
    fn memory_bytes(&self) -> u64 {
        0
    }
    fn page_size(&self) -> u64 {
        4096
    }
    fn argv(&self, _: i64) -> Result<Vec<String>, String> {
        Err("none".into())
    }
}

#[test]
fn details_are_asked_once_for_every_listed_pid() {
    let e = Env::darwin(ps_runner(PS_FIXTURE, Some("")));
    let source = CountingSource(Mutex::new(vec![]));
    let s = Seams { source: &source, ..e.seams() };
    collect_all(&s, &CollectOptions::default());
    assert_eq!(*source.0.lock().expect("lock"), vec![vec![1, 412, 415, 999]]);
}

// Go: TestCollectAllPSError.
#[test]
fn ps_error_is_no_processes() {
    let r = FakeRunner::new();
    r.on_error(match_exact("ps", &["-axwwo", PS_COLUMNS]), "ps failed");
    assert!(Env::darwin(r).collect(&CollectOptions::default()).is_empty());
    let r = FakeRunner::new();
    r.on(match_exact("ps", &["-axwwo", PS_COLUMNS]), Output { stdout: PS_FIXTURE.into(), exit_code: 1, ..Output::default() });
    assert!(Env::darwin(r).collect(&CollectOptions::default()).is_empty(), "a non-zero exit is a failure");
}

// Go: TestBundleAttributionUsesExecutablePath.
#[test]
fn bundle_attribution_uses_the_executable_path() {
    let mut e = Env::darwin(ps_runner("700 1 0.0 1024 2048 501 alice Sat May  2 23:00:00 2026 Helper --flag\n", Some("")));
    e.source
        .details
        .insert(700, Details { executable_path: "/Applications/Foo Bar.app/Contents/MacOS/Helper".into(), ..Details::default() });
    let procs = e.collect(&CollectOptions { bundle_paths: vec!["/Applications/Foo Bar.app".into()], deep: false });
    assert_eq!(procs.len(), 1);
    assert_eq!(procs[0].app_path, "/Applications/Foo Bar.app");
}

// Go: TestBundleAttribution, TestGroupByApp.
#[test]
fn bundle_attribution_and_grouping() {
    let e = Env::darwin(ps_runner(PS_FIXTURE, Some("")));
    let procs = e.collect(&CollectOptions { bundle_paths: vec!["/Applications/Slack.app".into()], deep: false });
    assert_eq!(procs.iter().filter(|p| p.app_path == "/Applications/Slack.app").count(), 2, "main + helper");
    for p in procs.iter().filter(|p| ["launchd", "bash", "-l"].contains(&p.command.as_str())) {
        assert_eq!(p.app_path, "", "{}", p.command);
    }
    let groups = group_by_app(&procs);
    assert_eq!(groups["/Applications/Slack.app"].len(), 2);
    assert_eq!(groups[""].len(), 2, "launchd + bash");
}

// Go: TestTotalRSS, TestTotalRSSEmpty.
#[test]
fn total_rss_sums() {
    assert_eq!(total_rss(&parse_ps(PS_FIXTURE, &utc())), 282_624);
    assert_eq!(total_rss(&[]), 0);
}

fn info(pid: i64, ppid: i64, cmd: &str) -> Info {
    Info { pid, ppid, command: cmd.into(), ..Info::default() }
}

// Go: TestBuildTreeEmpty, SingleRoot, ParentChild, SelfReferentialBecomesRoot, OrphanBecomesRoot.
#[test]
fn build_tree_shapes() {
    assert!(build_tree(&[]).is_empty());
    let roots = build_tree(&[info(1, 0, "launchd")]);
    assert_eq!((roots.len(), roots[0].info.pid, roots[0].children.len()), (1, 1, 0));
    let roots = build_tree(&[info(1, 0, "launchd"), info(10, 1, "bash"), info(20, 10, "go")]);
    assert_eq!(roots.len(), 1);
    let bash = &roots[0].children[0];
    assert_eq!((roots[0].children.len(), bash.info.pid, bash.children[0].info.pid), (1, 10, 20));
    let roots = build_tree(&[info(0, 0, "kernel"), info(1, 0, "launchd")]);
    let kernel = roots.iter().find(|r| r.info.pid == 0).expect("kernel root");
    assert_eq!(kernel.children.iter().map(|c| c.info.pid).collect::<Vec<_>>(), [1]);
    let roots = build_tree(&[info(100, 999, "orphan")]);
    assert_eq!((roots.len(), roots[0].info.pid), (1, 100));
}

// Go: TestTreeServiceBuildPassesCollectionOptions.
#[test]
fn tree_service_passes_collection_options() {
    let seen: Mutex<Vec<CollectOptions>> = Mutex::new(vec![]);
    let collector = |opts: &CollectOptions| {
        seen.lock().expect("lock").push(opts.clone());
        vec![info(1, 0, "launchd")]
    };
    let roots = TreeService::new(&collector).build(&TreeOptions { bundles: vec!["/Applications/App.app".into()], deep: true });
    assert!(roots.is_empty(), "no process matched the bundle scope");
    let seen = seen.lock().expect("lock");
    assert_eq!(seen.len(), 1);
    assert!(seen[0].deep);
    assert_eq!(seen[0].bundle_paths, ["/Applications/App.app"]);
}

// Go: TestScopeTreeProcessesKeepsMatchedProcessesAndAncestors.
#[test]
fn scope_tree_keeps_matches_and_ancestors() {
    let app = |pid, ppid, cmd: &str| Info { app_path: "/Applications/App.app".into(), ..info(pid, ppid, cmd) };
    let procs = [info(1, 0, "launchd"), app(10, 1, "App"), app(11, 10, "Helper"), info(99, 1, "Other")];
    let scoped = scope_tree_processes(&procs);
    assert_eq!(scoped.iter().map(|p| p.pid).collect::<Vec<_>>(), [1, 10, 11]);
    let roots = build_tree(&scoped);
    assert_eq!((roots.len(), roots[0].info.pid, roots[0].children[0].info.pid), (1, 1, 10));
}

#[test]
fn round_second_and_seconds_follow_go() {
    assert_eq!(round_second(1_499_999_999), 1_000_000_000);
    assert_eq!(round_second(1_500_000_000), 2_000_000_000);
    assert_eq!(round_second(-1_500_000_000), -2_000_000_000);
    assert_eq!(round_second(i64::MAX), i64::MAX);
    assert_eq!(duration_seconds(1_500_000_000), 1.5);
}

/// A Linux procfs tree under a temp dir (Go's `writeProcFixture`).
fn proc_fixture() -> std::path::PathBuf {
    let root = test_dir("procfs");
    let w = |rel: &str, body: &str| {
        let p = root.join(rel);
        std::fs::create_dir_all(p.parent().expect("parent")).expect("mkdir");
        write_file_atomic(&p, body.as_bytes(), 0o644).expect("write");
    };
    w("stat", "cpu  1 2 3\nbtime 1600000000\n");
    // comm contains a space and parentheses.
    w("1234/stat", "1234 (my (proc)) S 1 1234 1234 0 -1 4194560 100 0 0 0 50 30 0 0 20 0 8 0 1000 123456789 2048 0 0 0 0 0 0 0 0\n");
    w("1234/status", "Name:\tmy (proc)\nUid:\t1000\t1000\t1000\t1000\n");
    w("1234/cmdline", "/usr/bin/myproc\0--flag\0value\0");
    w("2/stat", "2 (kthreadd) S 0 0 0 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0 0 0 0 0 0 0\n");
    w("2/status", "Uid:\t0\t0\t0\t0\n");
    w("2/cmdline", "");
    root
}

fn linux_env(root: &Path) -> Env {
    Env {
        os: Kind::Linux,
        proc_fs: root.to_string_lossy().into_owned(),
        source: FixedProcesses { page_size: 4096, ..FixedProcesses::default() },
        clock: FakeClock::new(jiff::Timestamp::from_second(1_600_000_110).expect("ts")),
        ..Env::darwin(FakeRunner::new())
    }
}

// Go: TestCollectLinuxProcs.
#[test]
fn linux_procs_from_procfs() {
    let root = proc_fixture();
    let procs = linux_env(&root).collect(&CollectOptions::default());
    let p = procs.iter().find(|p| p.pid == 1234).expect("pid 1234");
    assert_eq!((p.ppid, p.command.as_str(), p.thread_count, p.uid), (1, "my (proc)", 8, 1000));
    assert_eq!(p.full_command_line, "/usr/bin/myproc --flag value");
    assert_eq!((p.rss_kib, p.vsize_kib), (2048 * 4, 123_456_789 / 1024));
    // (50+30)/100 ticks = 0.8s over 100s elapsed.
    assert!(p.cpu_pct > 0.79 && p.cpu_pct < 0.81, "{}", p.cpu_pct);
    assert_eq!(p.start_time.instant.as_second(), 1_600_000_010);
    let k = procs.iter().find(|p| p.pid == 2).expect("pid 2");
    assert_eq!(k.full_command_line, "[kthreadd]");
    std::fs::remove_dir_all(root).expect("cleanup");
}

// Go: TestCollectAllLinuxBranch.
#[test]
fn collect_all_linux_branch() {
    let root = proc_fixture();
    assert_eq!(linux_env(&root).collect(&CollectOptions::default()).len(), 2);
    std::fs::remove_dir_all(root).expect("cleanup");
}

// Go: TestParseLinuxStatRejectsGarbage, TestParseBtime.
#[test]
fn linux_stat_and_btime_parsing() {
    assert_eq!(parse_linux_cpu_times(b"not a stat line"), None);
    assert_eq!(parse_btime(b"cpu 1 2 3\nbtime 1700000000\nprocs_running 1\n"), Some(1_700_000_000));
    assert_eq!(parse_btime(b"no btime here\n"), None);
}

#[test]
fn passwd_lookup_follows_go_os_user() {
    let root = test_dir("passwd");
    std::fs::create_dir_all(root.join("etc")).expect("mkdir");
    let passwd = "# comment\n\n  root:x:0:0:root:/root:/bin/bash\n+nis:x:5:5::/:/bin/sh\nbad:x:7:x::/:/bin/sh\nuser:x:1000:1000:U:/home/user:/bin/bash\n";
    write_file_atomic(&root.join("etc/passwd"), passwd.as_bytes(), 0o644).expect("write");
    let fs = spectra_base::fs::RootedFs::new(&root);
    assert_eq!(lookup_passwd_uid(&fs, 0).as_deref(), Some("root"));
    assert_eq!(lookup_passwd_uid(&fs, 1000).as_deref(), Some("user"));
    assert_eq!(lookup_passwd_uid(&fs, 5), None, "NIS entries are skipped");
    assert_eq!(lookup_passwd_uid(&fs, 7), None, "a non-numeric gid is skipped");
    assert_eq!(lookup_passwd_uid(&fs, 1001), None);
    std::fs::remove_dir_all(root).expect("cleanup");
}

fn proc_args2(argc: u32, exe: &str, args: &[&str]) -> Vec<u8> {
    let mut b = argc.to_ne_bytes().to_vec();
    b.extend_from_slice(exe.as_bytes());
    b.extend_from_slice(&[0, 0, 0, 0]);
    for a in args {
        b.extend_from_slice(a.as_bytes());
        b.push(0);
    }
    b
}

// Go: TestParseProcArgs2.
#[test]
fn proc_args2_parsing() {
    let script = "/Users/Jane Doe/node_modules/@google/gemini-cli/dist/index.js";
    let raw = proc_args2(2, "/opt/homebrew/bin/node", &["node", script, "HOME=/Users/Jane Doe", "PATH=/bin"]);
    assert_eq!(parse_proc_args2(&raw), Ok(vec!["node".to_string(), script.to_string()]));
    let mut truncated = proc_args2(2, "/bin/x", &["x"]);
    truncated.push(b'y');
    for (name, bad) in [
        ("short", vec![1, 0]),
        ("no exe terminator", 1u32.to_ne_bytes().to_vec()),
        ("truncated argument", truncated),
        ("argc beyond the bound", proc_args2(MAX_ARGV_COUNT as u32 + 1, "/bin/x", &["x"])),
    ] {
        assert!(parse_proc_args2(&bad).is_err(), "{name}");
    }
    assert_eq!(parse_proc_args2(&proc_args2(0, "/bin/x", &[])), Ok(vec![]));
}

// Go: TestSplitCmdline, TestArgvMatchesListing.
#[test]
fn cmdline_split_and_argv_match() {
    assert_eq!(split_cmdline(b"node codex\0server.js\0"), ["node codex", "server.js"]);
    assert!(split_cmdline(b"").is_empty());
    let argv = vec!["node".to_string(), "/a  b/x.js".to_string()];
    assert!(argv_matches(&argv, "node /a b/x.js"));
    assert!(!argv_matches(&argv, "python3 other.py"));
    assert!(!argv_matches(&[], "node"));
}

// Go: TestHostArgvLinuxProcfs.
#[test]
fn host_argv_reads_linux_procfs() {
    let root = test_dir("argv");
    std::fs::create_dir_all(root.join("42")).expect("mkdir");
    write_file_atomic(&root.join("42/cmdline"), b"node\0/home/Jane Doe/app.js\0", 0o600).expect("write");
    let e = linux_env(&root);
    let procfs = root.to_string_lossy();
    assert_eq!(host_argv(&e.seams(), Kind::Linux, &procfs, 42), Ok(vec!["node".to_string(), "/home/Jane Doe/app.js".to_string()]));
    assert!(host_argv(&e.seams(), Kind::Linux, &procfs, 43).is_err());
    std::fs::remove_dir_all(root).expect("cleanup");
}

// Go: TestHostArgvOwnProcess.
#[test]
fn host_argv_own_process() {
    let pid = i64::from(std::process::id());
    let own: Vec<String> = std::env::args().collect();
    let e = Env::darwin(FakeRunner::new());
    let host = HostProcesses;
    let s = Seams { source: &host, ..e.seams() };
    let got = if cfg!(target_os = "macos") {
        host_argv(&s, Kind::Darwin, "/proc", pid)
    } else if cfg!(target_os = "linux") {
        host_argv(&s, Kind::Linux, "/proc", pid)
    } else {
        return;
    };
    assert_eq!(got, Ok(own));
}

/// A classifier with no runner rules and the real filesystem, on `os`.
fn classifier(os: Kind, proc_fs: &str) -> Classifier {
    Classifier::new(classify::Options {
        os,
        runner: Arc::new(FakeRunner::new()),
        fs: Arc::new(OsFs),
        store: None,
        workers: 0,
        max_scan_bytes: 0,
        proc_fs: proc_fs.to_string(),
        arch: String::new(),
    })
}

// Go: TestClassifyReadsArgvOnlyForInterpreters.
#[test]
fn classify_reads_argv_only_for_interpreters() {
    let script = "/home/Jane Doe/node_modules/@google/gemini-cli/dist/index.js";
    let asked = Mutex::new(Vec::new());
    let argv = |pid: i64, _full: &str| {
        asked.lock().expect("lock").push(pid);
        match pid {
            10 => vec!["node".to_string(), script.to_string()],
            11 => vec!["tool".to_string(), "--flag".to_string()],
            _ => vec![],
        }
    };
    let mut procs = vec![
        Info { executable_path: "/opt/x/bin/node".into(), full_command_line: format!("node {script}"), ..info(10, 0, "node") },
        Info { executable_path: "/opt/x/bin/tool".into(), full_command_line: "tool --flag".into(), ..info(11, 0, "tool") },
    ];
    let dir = test_dir("classify-argv");
    let c = classifier(Kind::Linux, &dir.to_string_lossy());
    assert_eq!(classify(&mut procs, &c, &argv, &CancelToken::new()), None);
    assert_eq!(*asked.lock().expect("lock"), [10], "argv read only for the interpreter");
    assert_eq!(procs[0].role, Role::AiAgent, "script path with a space lost its package: {:?}", procs[0].evidence);
    assert_eq!(procs[0].argv, ["node", script]);
    assert!(procs[1].argv.is_empty());
    let json = String::from_utf8(gojson::to_vec(&procs[1]).expect("json")).expect("utf8");
    assert!(!json.contains("\"argv\""), "{json}");
    std::fs::remove_dir_all(dir).expect("cleanup");
}

/// Go's `addProc`: a minimal `/proc/<pid>` entry whose exe link points at `target`.
fn add_proc(root: &Path, pid: &str, comm: &str, cmdline: &str, target: Option<&Path>, uid: &str) {
    let dir = root.join(pid);
    std::fs::create_dir_all(&dir).expect("mkdir");
    let w = |name: &str, body: String| write_file_atomic(&dir.join(name), body.as_bytes(), 0o644).expect("write");
    w("stat", format!("{pid} ({comm}) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0 0 0 0 0 0 0\n"));
    w("status", format!("Uid:\t{uid}\t{uid}\t{uid}\t{uid}\n"));
    w("cmdline", format!("{}\0", cmdline.replace(' ', "\0")));
    if let Some(t) = target {
        std::os::unix::fs::symlink(t, dir.join("exe")).expect("symlink");
    }
}

// Go: TestLinuxExecutablePathFeedsClassify.
#[test]
fn linux_executable_path_feeds_classify() {
    let root = test_dir("exe-classify");
    write_file_atomic(&root.join("stat"), b"btime 1600000000\n", 0o644).expect("write");
    let own = std::env::current_exe().expect("current exe");
    let bin = test_dir("exe-classify-bin");
    let tool = bin.join("tool");
    std::os::unix::fs::symlink(&own, &tool).expect("symlink");
    // The kernel's view of an unlinked executable: the link text gains " (deleted)" while the
    // link itself still opens the running image.
    let gone = bin.join("gone");
    let gone_deleted = std::path::PathBuf::from(format!("{} (deleted)", gone.display()));
    std::os::unix::fs::symlink(&own, &gone_deleted).expect("symlink");
    add_proc(&root, "100", "tool", &format!("{} serve", tool.display()), Some(&tool), "1000");
    add_proc(&root, "101", "gone", &gone.to_string_lossy(), Some(&gone_deleted), "1000");
    // exec -a /usr/sbin/sshd ./evil as root, with /proc/<pid>/exe hidden.
    add_proc(&root, "102", "sshd", "/usr/sbin/sshd -D", None, "0");

    let e = linux_env(&root);
    let mut procs = e.collect(&CollectOptions::default());
    let by = |procs: &[Info], pid: i64| procs.iter().find(|p| p.pid == pid).cloned().expect("pid");
    let p = by(&procs, 100);
    assert_eq!((p.executable_path.as_str(), p.executable_deleted), (&*tool.to_string_lossy(), false));
    let p = by(&procs, 101);
    assert_eq!((p.executable_path.as_str(), p.executable_deleted), (&*gone.to_string_lossy(), true));
    assert_eq!(by(&procs, 102).executable_path, "");

    let c = classifier(Kind::Linux, &root.to_string_lossy());
    assert_eq!(classify(&mut procs, &c, &|_, _| Vec::new(), &CancelToken::new()), None);
    let p = by(&procs, 100);
    assert_ne!(p.runtime, Runtime::Unknown, "pid 100 runtime unknown with a readable exe: {:?}", p.evidence);
    let p = by(&procs, 101);
    assert!(p.runtime != Runtime::Unknown && p.evidence.join(" ").contains("was deleted"), "deleted exe: {:?} {:?}", p.runtime, p.evidence);
    let p = by(&procs, 102);
    assert!(
        p.role != Role::System && p.runtime == Runtime::Unknown,
        "argv[0]-only root process: {:?} {:?} {:?}",
        p.role,
        p.runtime,
        p.evidence
    );
    std::fs::remove_dir_all(root).expect("cleanup");
    std::fs::remove_dir_all(bin).expect("cleanup");
}

#[test]
fn json_matches_go_field_order_and_omitempty() {
    let p = Info { pid: 7, command: "launchd".into(), full_command_line: "/sbin/launchd".into(), ..Info::default() };
    let got = String::from_utf8(gojson::to_vec(&p).expect("json")).expect("utf8");
    assert_eq!(
        got,
        r#"{"pid":7,"ppid":0,"uid":0,"command":"launchd","full_command_line":"/sbin/launchd","rss_kib":0,"vsize_kib":0,"thread_count":0,"cpu_pct":0,"cpu_percent":0,"mem_percent":0,"start_time":"0001-01-01T00:00:00Z","started_at":"0001-01-01T00:00:00Z"}"#
    );
    let _ = RunError::Unmatched { cmd: String::new() };
}
