use std::collections::BTreeMap;

use super::*;

const UDID_A: &str = "AAAAAAAA-1111-2222-3333-444444444444";
const UDID_B: &str = "BBBBBBBB-1111-2222-3333-444444444444";
const UDID_C: &str = "CCCCCCCC-1111-2222-3333-444444444444";
const HOUR: i64 = 3_600_000_000_000;
const MINUTE: i64 = 60_000_000_000;

fn now() -> Timestamp {
    "2026-10-23T12:00:00Z".parse().expect("ts")
}

fn ago(d: i64) -> GoTime {
    GoTime::utc(Timestamp::from_nanosecond(now().as_nanosecond() - i128::from(d)).expect("ts"))
}

fn p(pid: i64, ppid: i64, cmd: &str) -> Info {
    Info { pid, ppid, command: cmd.into(), ..Info::default() }
}

fn with_cmdline(mut i: Info, s: &str) -> Info {
    i.full_command_line = s.into();
    i
}

fn with_exe(mut i: Info, s: &str) -> Info {
    i.executable_path = s.into();
    i
}

fn with_cpu(mut i: Info, cpu: f64) -> Info {
    i.cpu_pct = cpu;
    i
}

fn sim_procs(base: i64, udid: &str, children: i64, cpu_each: f64, age: i64) -> Vec<Info> {
    let mut root = with_cmdline(
        p(base, 1, "launchd_sim"),
        &format!("launchd_sim /Users/x/Library/Developer/CoreSimulator/Devices/{udid}/data/var/run/launchd_bootstrap.plist"),
    );
    root.started_at = ago(age);
    root.cpu_pct = 1.0;
    let mut out = vec![root];
    for i in 1..=children {
        // System daemons in the runtime volume carry no UDID: resolved by ancestry.
        let mut c = with_exe(p(base + i, base, &format!("svc{i}")), "/Library/Developer/CoreSimulator/Volumes/iOS/usr/libexec/svc");
        c.started_at = ago(age);
        c.cpu_pct = cpu_each;
        out.push(c);
    }
    out
}

// Go: TestGroupFamiliesThreeSimulators.
#[test]
fn three_simulators() {
    let mut procs = sim_procs(1000, UDID_A, 100, 2.5, 3 * HOUR);
    procs.extend(sim_procs(2000, UDID_B, 100, 2.5, 2 * HOUR));
    procs.extend(sim_procs(3000, UDID_C, 100, 2.5, MINUTE));
    let mut git = with_cpu(p(9, 1, "git"), 91.0);
    git.started_at = ago(5_000_000_000);
    procs.push(git);
    let fams = group_families(&procs, now(), 2);
    let sims: BTreeMap<&str, &Family> = fams.iter().filter(|f| f.kind == FAMILY_SIMULATOR).map(|f| (f.udid.as_str(), f)).collect();
    assert_eq!(sims.len(), 3, "{fams:?}");
    for (udid, age) in [(UDID_A, 3 * HOUR), (UDID_B, 2 * HOUR), (UDID_C, MINUTE)] {
        let f = sims[udid];
        assert_eq!(f.members, 101, "{udid}");
        assert!((f.cpu_pct - 251.0).abs() < 0.001, "{udid} cpu {}", f.cpu_pct);
        assert_eq!(f.age, GoDuration(age), "{udid}");
        assert_eq!(f.heaviest.len(), 2, "{udid}");
        let want_share = 251.0 / (3.0 * 251.0 + 91.0) * 100.0;
        assert!((f.share_pct - want_share).abs() < 0.01, "{udid} share {}", f.share_pct);
    }
    assert_eq!(fams[0].kind, FAMILY_SIMULATOR, "simulators rank first");
    let git = fams.iter().find(|f| f.key == "other:git").expect("git");
    assert_eq!((git.kind.as_str(), git.members, git.cpu_pct), (FAMILY_OTHER, 1, 91.0));
}

/// Go's TestGroupFamiliesClassification table: processes and the families (key, members) they
/// make.
type Case = (&'static str, Vec<Info>, Vec<(String, i64)>);

fn cases() -> Vec<Case> {
    let s = |k: &str| k.to_string();
    let dev = |u: &str| format!("/Users/x/Library/Developer/CoreSimulator/Devices/{u}/data/Containers/Bundle/MyApp");
    let launchd = |pid, u: &str| {
        with_cmdline(
            p(pid, 1, "launchd_sim"),
            &format!("launchd_sim /Users/x/Library/Developer/CoreSimulator/Devices/{u}/data/var/run/launchd_bootstrap.plist"),
        )
    };
    vec![
        (
            "xcodebuild children inherit xcode",
            vec![with_cpu(p(10, 1, "xcodebuild"), 10.0), with_cpu(p(11, 10, "sh"), 1.0), with_cpu(p(12, 11, "swift-frontend"), 80.0)],
            vec![(s(FAMILY_XCODE), 3)],
        ),
        (
            "build tools without xcode ancestor",
            vec![
                p(10, 1, "make"),
                p(11, 10, "clang"),
                with_cmdline(p(12, 1, "java"), "java -cp x org.gradle.launcher.daemon.bootstrap.GradleDaemon"),
            ],
            vec![(s(FAMILY_BUILD), 3)],
        ),
        (
            "generic compiler tools without a driver stay ungrouped",
            vec![p(10, 1, "clang"), p(11, 1, "ld"), p(12, 1, "link")],
            vec![(s("other:clang"), 1), (s("other:ld"), 1), (s("other:link"), 1)],
        ),
        (
            "browser names in arguments or directories do not match",
            vec![
                with_cmdline(p(10, 1, "vim"), "vim /tmp/chrome/notes"),
                with_cmdline(p(11, 1, "rg"), "rg foo /home/u/src/chrome"),
                with_exe(p(12, 1, "safaridriver"), "/usr/bin/safaridriver"),
                with_exe(
                    p(13, 1, "com.apple.WebKit.WebContent"),
                    "/System/Library/Frameworks/WebKit.framework/Versions/A/XPCServices/com.apple.WebKit.WebContent.xpc/Contents/MacOS/com.apple.WebKit.WebContent",
                ),
                with_cmdline(p(14, 1, "vim"), "vim /Applications/Safari.app/notes"),
            ],
            vec![(s("other:vim"), 2), (s("other:rg"), 1), (s("other:safaridriver"), 1), (s("other:com.apple.WebKit.WebContent"), 1)],
        ),
        (
            "positional path arguments do not classify when ExecutablePath is empty",
            vec![
                with_cmdline(p(10, 1, "cat"), "/bin/cat /Applications/Safari.app/Contents/Info.plist"),
                with_cmdline(p(11, 1, "less"), "/usr/bin/less /Applications/Xcode.app/Contents/Info.plist"),
                with_cmdline(p(12, 1, "vim"), "/usr/bin/vim /Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
            ],
            vec![(s("other:cat"), 1), (s("other:less"), 1), (s("other:vim"), 1)],
        ),
        (
            "bundle executable with spaces still matches from the command line",
            vec![
                with_cmdline(p(10, 1, "Google Chrome"), "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --type=renderer"),
                with_cmdline(
                    p(11, 1, "Google Chrome Helper"),
                    "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper --type=gpu",
                ),
            ],
            vec![(s("browser:Google Chrome"), 2)],
        ),
        (
            "Xcode-hosted runtime and MobileSafari stay in the device family",
            vec![
                launchd(50, UDID_A),
                with_exe(
                    p(51, 50, "backboardd"),
                    "/Applications/Xcode.app/Contents/Developer/Platforms/iPhoneOS.platform/Library/Developer/CoreSimulator/Profiles/Runtimes/iOS.simruntime/Contents/Resources/RuntimeRoot/usr/libexec/backboardd",
                ),
                with_exe(p(52, 51, "MobileSafari"), "/Applications/Safari.app/MobileSafari"),
                p(53, 52, "com.apple.WebKit.WebContent"),
            ],
            vec![(format!("simulator:{UDID_A}"), 4)],
        ),
        (
            "device path in an argument does not classify as the simulator",
            vec![
                with_cmdline(
                    p(90, 1, "tail"),
                    &format!("/usr/bin/tail -f /Users/x/Library/Developer/CoreSimulator/Devices/{UDID_A}/data/log"),
                ),
                with_cmdline(p(91, 1, "cat"), &format!("/bin/cat /Users/x/Library/Developer/CoreSimulator/Devices/{UDID_A}/data/log")),
            ],
            vec![(s("other:tail"), 1), (s("other:cat"), 1)],
        ),
        (
            "launchd_sim without UDID unifies with a descendant that exposes one",
            vec![
                p(70, 1, "launchd_sim"),
                with_cpu(p(71, 70, "runtimed"), 60.0),
                with_cpu(with_exe(p(72, 70, "MyApp"), &dev(UDID_A)), 60.0),
                p(73, 72, "helper"),
            ],
            vec![(format!("simulator:{UDID_A}"), 4)],
        ),
        (
            "two launchd_sim trees with different UDIDs stay separate",
            vec![
                p(100, 1, "launchd_sim"),
                with_exe(p(101, 100, "a"), &format!("/x/CoreSimulator/Devices/{UDID_A}/data/a")),
                p(102, 100, "rt"),
                p(200, 1, "launchd_sim"),
                with_exe(p(201, 200, "b"), &format!("/x/CoreSimulator/Devices/{UDID_B}/data/b")),
                p(202, 200, "rt"),
            ],
            vec![(format!("simulator:{UDID_A}"), 3), (format!("simulator:{UDID_B}"), 3)],
        ),
        (
            "several UDIDs under one launchd_sim keep the pid family for the rest",
            vec![
                p(300, 1, "launchd_sim"),
                with_exe(p(301, 300, "a"), &format!("/x/CoreSimulator/Devices/{UDID_A}/data/a")),
                with_exe(p(302, 300, "b"), &format!("/x/CoreSimulator/Devices/{UDID_B}/data/b")),
                p(303, 300, "rt"),
            ],
            vec![(format!("simulator:{UDID_A}"), 1), (format!("simulator:{UDID_B}"), 1), (s("simulator:launchd_sim-300"), 2)],
        ),
        (
            "launchd_sim without UDID is keyed by pid",
            vec![p(70, 1, "launchd_sim"), p(71, 70, "a"), p(80, 1, "launchd_sim"), p(81, 80, "b")],
            vec![(s("simulator:launchd_sim-70"), 2), (s("simulator:launchd_sim-80"), 2)],
        ),
        (
            "parent started after child (pid reuse) is not an ancestor",
            vec![Info { started_at: ago(MINUTE), ..p(60, 1, "launchd_sim") }, Info { started_at: ago(HOUR), ..p(61, 60, "old") }],
            vec![(s("simulator:launchd_sim-60"), 1), (s("other:old"), 1)],
        ),
        (
            "browsers split by app",
            vec![
                with_exe(p(10, 1, "Google Chrome"), "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
                with_exe(p(11, 10, "Google Chrome Helper"), "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Helper"),
                with_exe(p(12, 1, "firefox"), "/usr/lib/firefox/firefox"),
                with_cmdline(p(13, 1, "grep"), "grep -r chrome ."),
            ],
            vec![(s("browser:Google Chrome"), 2), (s("browser:Firefox"), 1), (s("other:grep"), 1)],
        ),
        ("emulator and qemu", vec![p(10, 1, "qemu-system-aarch64"), p(11, 1, "emulator")], vec![(s(FAMILY_EMULATOR), 2)]),
        (
            "simulator child with UDID in its own path and missing parent",
            vec![with_exe(p(20, 99999, "MyApp"), &dev(UDID_A)), p(21, 99999, "orphan")],
            vec![(format!("simulator:{UDID_A}"), 1), (s("other:orphan"), 1)],
        ),
        ("ancestry cycle terminates", vec![p(30, 31, "a"), p(31, 30, "b")], vec![(s("other:a"), 1), (s("other:b"), 1)]),
        (
            "lowercase UDID is normalised",
            vec![with_cmdline(p(40, 1, "launchd_sim"), "launchd_sim /x/CoreSimulator/Devices/aaaaaaaa-1111-2222-3333-444444444444/data")],
            vec![(format!("simulator:{UDID_A}"), 1)],
        ),
    ]
}

// Go: TestGroupFamiliesClassification.
#[test]
fn classification() {
    for (name, procs, want) in cases() {
        let got: BTreeMap<String, i64> = group_families(&procs, now(), 0).into_iter().map(|f| (f.key, f.members)).collect();
        let want: BTreeMap<String, i64> = want.into_iter().collect();
        assert_eq!(got, want, "{name}");
    }
}

// Go: TestGroupFamiliesEmptyAndAge.
#[test]
fn empty_and_age() {
    assert!(group_families(&[], now(), 0).is_empty());
    let procs = [
        Info { elapsed: GoDuration(10 * MINUTE), cpu_pct: 5.0, ..p(1, 0, "make") },
        Info { start_time: ago(HOUR), cpu_pct: 50.0, ..p(2, 1, "clang") },
    ];
    let f = &group_families(&procs, now(), 0)[0];
    assert_eq!((f.age, f.age_seconds), (GoDuration(HOUR), 3600));
    assert_eq!(f.started_at.map(|s| s.instant), Some(ago(HOUR).instant));
    assert_eq!((f.heaviest[0].pid, f.share_pct), (2, 100.0));
}

#[test]
fn go_base_matches_filepath_base() {
    for (p, want) in [("", "."), ("/", "/"), ("///", "/"), ("a/b/", "b"), ("/usr/bin/x", "x"), ("x", "x")] {
        assert_eq!(go_base(p), want, "{p:?}");
    }
}
