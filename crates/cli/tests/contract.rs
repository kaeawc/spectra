//! The Go build's recorded cases, replayed through `spectra_cli::run` (the real dispatch):
//!
//! - `cli/`: `spectra help`, `spectra version` with the version injected, and a bare `spectra`;
//! - `unsupported/`: `spectra <macOS-only command> --json` on Linux;
//! - `unsupported-text/`: the same commands without `--json`, which report on stderr.
//!
//! Each compares stdout and the exit code with the Go build's, and stderr with
//! `expected.stderr` where the case recorded one (`cmd/spectra/contract_cases_test.go`).

use std::sync::Arc;

use serde::Deserialize;
use spectra_base::fs::{Fs, OsFs};
use spectra_base::hostos::Kind;
use spectra_cli::Ctx;
use spectra_contract::{Case, first_difference};

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
