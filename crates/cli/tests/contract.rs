//! The Go build's recorded cases, replayed through `spectra_cli::run` (the real dispatch):
//!
//! - `cli/`: `spectra help`, `spectra version` with the version injected, and a bare `spectra`;
//! - `unsupported/`: `spectra <macOS-only command> --json` on Linux;
//! - `unsupported-text/`: the same commands without `--json`, which report on stderr;
//! - `capabilities/`: the manifest, JSON and text, per OS;
//! - `memory/`: `spectra memory` from a recorded macOS reading or a Linux `/proc` tree, and its
//!   flag errors;
//! - `pressure/`: `spectra pressure` on Linux PSI and cgroup v2 trees, its errors, and the
//!   refusal on macOS.
//! - `procdiag/`: `spectra procdiag` on Linux `/proc` trees sampled twice (`before/`, then
//!   `root/`), its errors, and the refusal on macOS.
//!
//! Each compares stdout and the exit code with the Go build's, and stderr with
//! `expected.stderr` where the case recorded one (`cmd/spectra/contract_cases_test.go`).

use std::sync::Arc;

use serde::Deserialize;
use spectra_base::clock::FakeClock;
use spectra_base::fs::{Fs, OsFs};
use spectra_base::hostos::Kind;
use spectra_cli::Ctx;
use spectra_contract::{Case, first_difference};
use spectra_host::memstate::{DarwinSample, FixedMemory};

/// The seams a case replays: its env, clock, runner and filesystem, on `os`.
fn ctx(case: &Case, os: Kind, version: &str) -> Ctx {
    Ctx {
        os,
        env: Arc::new(case.env()),
        clock: Arc::new(case.clock()),
        runner: Arc::new(case.runner()),
        fs: Arc::new(case.fs()),
        ..Ctx::system(version)
    }
}

fn run(ctx: &Ctx, args: &[String]) -> (i32, Vec<u8>, Vec<u8>) {
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = spectra_cli::run(args, ctx, &mut out, &mut err);
    (code, out, err)
}

/// `expected.stderr`, when the case recorded one.
fn expected_stderr(case: &Case) -> Option<Vec<u8>> {
    OsFs.read(&case.dir.join("expected.stderr")).ok()
}

fn assert_case(case: &Case, code: i32, stdout: &[u8], stderr: &[u8]) {
    case.assert_stdout(stdout);
    assert_eq!(code, case.expected_exit(), "{}: exit code", case.dir.display());
    if let Some(want) = expected_stderr(case) {
        assert!(want == stderr, "{}: stderr differs from the Go build\n{}", case.dir.display(), first_difference(&want, stderr));
    }
}

#[derive(Deserialize)]
struct CliInput {
    args: Vec<String>,
    #[serde(default)]
    version: Option<String>,
}

#[test]
fn help_version_and_no_args_match_go() {
    let cases = spectra_contract::cases("cli");
    let names: Vec<&str> = cases.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(names, ["help", "no-args", "version"]);
    for case in &cases {
        let input: CliInput = case.input();
        // Help and version print the same on every OS.
        for os in [Kind::Linux, Kind::Darwin] {
            let ctx = ctx(case, os, input.version.as_deref().unwrap_or("not-the-injected-version"));
            let (code, out, err) = run(&ctx, &input.args);
            assert_case(case, code, &out, &err);
        }
    }
}

#[test]
fn version_prints_the_injected_version() {
    let case = spectra_contract::cases("cli").into_iter().find(|c| c.name == "version").expect("cli/version");
    let input: CliInput = case.input();
    let version = input.version.expect("the case injects a version");
    let (code, out, _) = run(&ctx(&case, Kind::Linux, &version), &input.args);
    assert_eq!((code, String::from_utf8(out).expect("utf8")), (0, format!("{version}\n")));
    let (_, out, _) = run(&ctx(&case, Kind::Linux, "v9.9.9"), &input.args);
    assert_eq!(out, b"v9.9.9\n", "version comes from the Ctx, not the case");
}

#[derive(Deserialize)]
struct JsonInput {
    command: String,
    os: String,
}

#[test]
fn unsupported_json_matches_go_through_dispatch() {
    let cases = spectra_contract::cases("unsupported");
    assert_eq!(cases.len(), 19);
    for case in &cases {
        let input: JsonInput = case.input();
        assert_eq!(input.os, "linux");
        let ctx = ctx(case, Kind::Linux, "test");
        for args in [vec![input.command.clone(), "--json".to_string()], vec![input.command.clone(), "-json".into(), "1234".into()]] {
            let (code, out, err) = run(&ctx, &args);
            assert_case(case, code, &out, &err);
            assert!(err.is_empty(), "{}: JSON mode writes nothing on stderr", case.name);
        }
    }
}

#[derive(Deserialize)]
struct TextInput {
    args: Vec<String>,
    os: String,
}

#[test]
fn unsupported_text_matches_go_through_dispatch() {
    let cases = spectra_contract::cases("unsupported-text");
    assert_eq!(cases.len(), 19);
    for case in &cases {
        let input: TextInput = case.input();
        assert_eq!(input.os, "linux");
        assert!(expected_stderr(case).is_some_and(|e| !e.is_empty()), "{}: no expected.stderr", case.name);
        let (code, out, err) = run(&ctx(case, Kind::Linux, "test"), &input.args);
        assert_case(case, code, &out, &err);
    }
}

#[derive(Deserialize)]
struct CapabilitiesInput {
    args: Vec<String>,
    os: String,
    arch: String,
    version: String,
}

fn kind_of(os: &str) -> Kind {
    match os {
        "darwin" => Kind::Darwin,
        "linux" => Kind::Linux,
        other => panic!("unknown os {other}"),
    }
}

/// `capabilities` and `capabilities --json` on each OS, byte for byte (the interface list
/// differs by OS). The host running the test does not matter: the OS comes from the `Ctx`.
#[test]
fn capabilities_match_go_through_dispatch() {
    let cases = spectra_contract::cases("capabilities");
    let names: Vec<&str> = cases.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(names, ["json-darwin", "json-linux", "text-darwin", "text-linux"]);
    for case in &cases {
        let input: CapabilitiesInput = case.input();
        let ctx = Ctx { arch: input.arch.clone(), ..ctx(case, kind_of(&input.os), &input.version) };
        let (code, out, err) = run(&ctx, &input.args);
        assert_case(case, code, &out, &err);
        assert!(err.is_empty(), "{}: nothing on stderr", case.name);
    }
}

/// The protocol crate accepts what the Rust build prints, and the OS-specific interfaces are
/// where the cases say.
#[test]
fn capabilities_json_decodes_with_the_protocol_crate() {
    for case in spectra_contract::cases("capabilities").iter().filter(|c| c.name.starts_with("json-")) {
        let input: CapabilitiesInput = case.input();
        let ctx = Ctx { arch: input.arch.clone(), ..ctx(case, kind_of(&input.os), &input.version) };
        let (_, out, _) = run(&ctx, &input.args);
        let m = spectra_protocol::decode_spectra_capabilities(&out).unwrap_or_else(|e| panic!("{}: {e}", case.name));
        assert_eq!((m.os.as_str(), m.arch.as_str(), m.spectra_version.as_str()), (input.os.as_str(), "arm64", input.version.as_str()));
        assert_eq!(m.interface("inspect").is_some(), input.os == "darwin", "{}", case.name);
        assert_eq!(m.interface("startup").is_some(), input.os == "darwin", "{}", case.name);
    }
}

#[derive(Deserialize)]
struct MemoryInput {
    args: Vec<String>,
    os: String,
    #[serde(default)]
    now: Option<String>,
    #[serde(default)]
    page_size: u64,
    #[serde(default)]
    darwin_sample: Option<DarwinSample>,
}

/// The case's OS, its collection time, and its recorded macOS reading or Linux page size.
fn memory_ctx(case: &Case, input: &MemoryInput) -> Ctx {
    let now = input.now.as_deref().map_or(jiff::Timestamp::UNIX_EPOCH, |n| n.parse().expect("now"));
    Ctx {
        clock: Arc::new(FakeClock::new(now)),
        memory: Arc::new(FixedMemory { darwin: input.darwin_sample, page_size: input.page_size }),
        ..ctx(case, kind_of(&input.os), "test")
    }
}

/// `spectra memory`, JSON and text, on macOS readings and Linux trees, and its flag errors: stdout,
/// stderr and exit code, byte for byte.
#[test]
fn memory_matches_go_through_dispatch() {
    let cases = spectra_contract::cases("memory");
    let names: Vec<&str> = cases.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(
        names,
        [
            "flag-help",
            "flag-unknown",
            "flag-watch-invalid",
            "flag-watch-zero",
            "json-darwin",
            "json-darwin-fallback",
            "json-linux",
            "json-linux-nopsi",
            "linux-no-memtotal",
            "text-darwin",
            "text-darwin-fallback",
            "text-linux",
            "text-linux-nopsi",
        ]
    );
    for case in &cases {
        let input: MemoryInput = case.input();
        assert!(expected_stderr(case).is_some(), "{}: no expected.stderr", case.name);
        let (code, out, err) = run(&memory_ctx(case, &input), &input.args);
        assert_case(case, code, &out, &err);
    }
}

/// PR #755: on macOS `pressure_free_percent` is `kern.memorystatus_level` when it reads as at
/// most 100, else the page fraction, which `free_pages_percent` always holds; Linux has no
/// `free_pages_percent`.
#[test]
fn memory_pressure_free_percent_follows_memorystatus_level() {
    for case in spectra_contract::cases("memory").iter().filter(|c| c.name.starts_with("json-")) {
        let input: MemoryInput = case.input();
        let (_, out, _) = run(&memory_ctx(case, &input), &input.args);
        let v: serde_json::Value = serde_json::from_slice(&out).unwrap_or_else(|e| panic!("{}: {e}", case.name));
        let free = v["pressure_free_percent"].as_f64().expect("pressure_free_percent");
        match input.darwin_sample {
            Some(s) => {
                let pages = v["free_pages_percent"].as_f64().expect("free_pages_percent");
                let want = s.memorystatus_level.filter(|&l| l <= 100).map_or(pages, f64::from);
                assert_eq!(free, want, "{}", case.name);
            }
            None => assert!(v.get("free_pages_percent").is_none(), "{}: Linux omits free_pages_percent", case.name),
        }
    }
}

#[derive(Deserialize)]
struct PressureInput {
    args: Vec<String>,
    os: String,
}

/// `spectra pressure`, JSON and text, on Linux fixture trees (served from the case's `root/` at
/// `/proc` and `/sys/fs/cgroup`), its errors, and the macOS refusal.
#[test]
fn pressure_matches_go_through_dispatch() {
    let cases = spectra_contract::cases("pressure");
    let names: Vec<&str> = cases.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(
        names,
        [
            "flag-invalid-pid",
            "json-darwin",
            "json-linux-healthy",
            "json-linux-nopsi",
            "json-linux-pid",
            "linux-bad-psi",
            "text-darwin",
            "text-linux-healthy",
            "text-linux-nopsi",
            "text-linux-pid",
        ]
    );
    for case in &cases {
        let input: PressureInput = case.input();
        assert!(expected_stderr(case).is_some(), "{}: no expected.stderr", case.name);
        let (code, out, err) = run(&ctx(case, kind_of(&input.os), "test"), &input.args);
        assert_case(case, code, &out, &err);
    }
}

#[derive(Deserialize)]
struct ProcdiagInput {
    args: Vec<String>,
    os: String,
    arch: String,
    now: String,
}

/// Serves the case's `before/` tree (the first sample) until [`ProcdiagSleeper`] runs, then its
/// `root/` tree (the second sample, which the analysis also reads).
struct ProcdiagFs {
    before: spectra_base::fs::RootedFs,
    after: spectra_base::fs::RootedFs,
    slept: Arc<std::sync::atomic::AtomicBool>,
}

impl ProcdiagFs {
    fn current(&self) -> &spectra_base::fs::RootedFs {
        if self.slept.load(std::sync::atomic::Ordering::SeqCst) { &self.after } else { &self.before }
    }
}

impl Fs for ProcdiagFs {
    fn read(&self, path: &std::path::Path) -> std::io::Result<Vec<u8>> {
        self.current().read(path)
    }
    fn read_dir(&self, path: &std::path::Path) -> std::io::Result<Vec<spectra_base::fs::DirEntry>> {
        self.current().read_dir(path)
    }
    fn metadata(&self, path: &std::path::Path) -> std::io::Result<spectra_base::fs::FileInfo> {
        self.current().metadata(path)
    }
    fn symlink_metadata(&self, path: &std::path::Path) -> std::io::Result<spectra_base::fs::FileInfo> {
        self.current().symlink_metadata(path)
    }
    fn read_link(&self, path: &std::path::Path) -> std::io::Result<std::path::PathBuf> {
        self.current().read_link(path)
    }
}

/// The wait between samples: switches [`ProcdiagFs`] to the second tree and moves the clock by
/// the slept duration, as the Go recording's `Sleep` re-points its symlink and advances `Now`.
struct ProcdiagSleeper {
    slept: Arc<std::sync::atomic::AtomicBool>,
    clock: Arc<FakeClock>,
}

impl spectra_base::clock::Sleeper for ProcdiagSleeper {
    fn sleep(&self, d: std::time::Duration) {
        self.slept.store(true, std::sync::atomic::Ordering::SeqCst);
        self.clock.advance(d);
    }
}

/// `spectra procdiag`, JSON and text, on Linux trees sampled twice, its errors, and the macOS
/// refusal: stdout, stderr and exit code, byte for byte.
#[test]
fn procdiag_matches_go_through_dispatch() {
    let cases = spectra_contract::cases("procdiag");
    let names: Vec<&str> = cases.iter().map(|c| c.name.as_str()).collect();
    let mut want = vec![
        "darwin-json",
        "darwin-text",
        "flag-help",
        "flag-unknown",
        "interval-invalid",
        "interval-zero",
        "invalid-pid",
        "invalid-pid-zero",
    ];
    let scenarios = [
        "blocked-connect",
        "blocked-read",
        "cgroup-context",
        "cpu-spin",
        "fd-exhaustion",
        "fd-growth",
        "idle",
        "lock-wait",
        "many-threads",
        "memory-growth",
        "missing-files",
        "stopped",
        "uninterruptible",
        "zombie",
    ];
    let json: Vec<String> = scenarios.iter().map(|s| format!("json-{s}")).collect();
    let text: Vec<String> = scenarios.iter().map(|s| format!("text-{s}")).collect();
    want.extend(json.iter().map(String::as_str));
    want.push("no-such-process");
    want.extend(text.iter().map(String::as_str));
    want.extend(["usage-no-pid", "usage-two-pids"]);
    assert_eq!(names, want);
    for case in &cases {
        let input: ProcdiagInput = case.input();
        assert!(expected_stderr(case).is_some(), "{}: no expected.stderr", case.name);
        let slept = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let clock = Arc::new(FakeClock::new(input.now.parse().expect("now")));
        let fs = ProcdiagFs {
            before: spectra_base::fs::RootedFs::new(case.dir.join("before")),
            after: spectra_base::fs::RootedFs::new(case.dir.join("root")),
            slept: Arc::clone(&slept),
        };
        let ctx = Ctx {
            arch: input.arch.clone(),
            fs: Arc::new(fs),
            clock: Arc::clone(&clock) as Arc<dyn spectra_base::clock::Clock>,
            sleeper: Arc::new(ProcdiagSleeper { slept, clock }),
            ..ctx(case, kind_of(&input.os), "test")
        };
        let (code, out, err) = run(&ctx, &input.args);
        assert_case(case, code, &out, &err);
    }
}

/// A `process` or `lineage` case: the collection inputs Go injected (`cmd/spectra/contract_process_test.go`).
#[derive(Deserialize)]
struct ProcessInput {
    args: Vec<String>,
    os: String,
    #[serde(default)]
    now: Option<String>,
    /// `time.Local` while recording: a fixed zone this many seconds east of UTC.
    #[serde(default)]
    tz_offset: i32,
    #[serde(default)]
    page_size: u64,
    #[serde(default)]
    memory_bytes: u64,
    /// libproc details per decimal pid.
    #[serde(default)]
    details: std::collections::BTreeMap<String, spectra_host::process::Details>,
    /// Raw `kern.procargs2` argv per decimal pid.
    #[serde(default)]
    argv: std::collections::BTreeMap<String, Vec<String>>,
}

/// The case's OS, collection time, local zone and recorded macOS process facts, with `ps`,
/// `lsof` and `codesign` from `calls.json` and the Linux `/proc` tree (and classified bundles)
/// from `root/`.
fn process_ctx(case: &Case, input: &ProcessInput) -> Ctx {
    let now = input.now.as_deref().map_or(jiff::Timestamp::UNIX_EPOCH, |n| n.parse().expect("now"));
    let offset = jiff::tz::Offset::from_seconds(input.tz_offset).expect("tz_offset");
    Ctx {
        clock: Arc::new(FakeClock::new(now)),
        tz: jiff::tz::TimeZone::fixed(offset),
        processes: Arc::new(spectra_host::process::FixedProcesses {
            details: input.details.iter().map(|(k, v)| (k.parse().expect("pid"), v.clone())).collect(),
            memory_bytes: input.memory_bytes,
            page_size: input.page_size,
            argv: input.argv.iter().map(|(k, v)| (k.parse().expect("pid"), v.clone())).collect(),
        }),
        ..ctx(case, kind_of(&input.os), "test")
    }
}

fn replay_process_kind(kind: &str, want: &[&str]) {
    let cases = spectra_contract::cases(kind);
    let names: Vec<&str> = cases.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(names, want);
    // Every case runs, so one report names every difference.
    let failed: Vec<String> = cases
        .iter()
        .filter_map(|case| {
            let input: ProcessInput = case.input();
            assert!(expected_stderr(case).is_some(), "{}: no expected.stderr", case.name);
            let (code, out, err) = run(&process_ctx(case, &input), &input.args);
            std::panic::catch_unwind(|| assert_case(case, code, &out, &err))
                .err()
                .map(|e| e.downcast_ref::<String>().cloned().unwrap_or_else(|| case.name.clone()))
        })
        .collect();
    assert!(failed.is_empty(), "{} of {} {kind} cases differ from Go:\n{}", failed.len(), cases.len(), failed.join("\n"));
}

/// `spectra process`: rows, trees, families, `--deep`, `--fd-breakdown`, every sort key, filters
/// and `--classify`, JSON and text, on macOS `ps` listings and Linux `/proc` trees, and its flag
/// errors: stdout, stderr and exit code, byte for byte.
#[test]
fn process_matches_go_through_dispatch() {
    replay_process_kind(
        "process",
        &[
            "flag-group-by-bogus",
            "flag-group-by-conflict",
            "flag-help",
            "flag-min-elapsed",
            "flag-min-rss",
            "flag-min-vsz-negative",
            "flag-top",
            "flag-unknown",
            "json-darwin",
            "json-darwin-classify",
            "json-darwin-deep",
            "json-darwin-family",
            "json-darwin-filters",
            "json-darwin-top-cpu",
            "json-darwin-tree",
            "json-linux",
            "json-linux-classify",
            "json-linux-tree",
            "text-darwin",
            "text-darwin-bad-sort",
            "text-darwin-classify",
            "text-darwin-classify-tree",
            "text-darwin-deep",
            "text-darwin-family",
            "text-darwin-family-top",
            "text-darwin-fd-breakdown",
            "text-darwin-min-elapsed",
            "text-darwin-min-rss",
            "text-darwin-min-vsz",
            "text-darwin-no-match",
            "text-darwin-ppid",
            "text-darwin-ppid-tree",
            "text-darwin-ps-error",
            "text-darwin-sort-cmd",
            "text-darwin-sort-cpu",
            "text-darwin-sort-elapsed",
            "text-darwin-sort-mem",
            "text-darwin-sort-pid",
            "text-darwin-sort-rss",
            "text-darwin-sort-startedat",
            "text-darwin-sort-vsz",
            "text-darwin-top-cpu",
            "text-darwin-tree",
            "text-linux",
            "text-linux-classify",
            "text-linux-tree",
        ],
    );
}

/// `spectra lineage <pid>`: ancestors, the descendant subtree, zombie and reparented flags, JSON
/// and text, on macOS and Linux, and its usage and lookup errors.
#[test]
fn lineage_matches_go_through_dispatch() {
    replay_process_kind(
        "lineage",
        &[
            "flag-bad-pid",
            "flag-help",
            "flag-no-pid",
            "flag-two-pids",
            "flag-unknown",
            "flag-zero-pid",
            "json-darwin",
            "json-darwin-reparented",
            "json-linux",
            "no-such-pid",
            "text-darwin",
            "text-darwin-reparented",
            "text-linux",
        ],
    );
}
