use std::sync::Arc;

use spectra_base::env::MapEnv;
use spectra_base::hostos::Kind;
use spectra_base::proc::{FakeRunner, match_exact};
use spectra_host::process::{FixedProcesses, PS_COLUMNS};

use super::*;

/// launchd, zsh (pid 10, a reparented user process) and make (pid 20), as `ps` lists them
/// (Go's `lineageFakeProcs`).
fn ctx() -> Ctx {
    let runner = FakeRunner::new();
    runner.on_stdout(
        match_exact("ps", &["-axwwo", PS_COLUMNS]),
        "1 0 0.0 0 100 0 root Sat May 2 22:37:01 2026 launchd\n10 1 0.0 100 100 501 me Sat May 2 22:37:01 2026 zsh\n20 10 5.0 200 100 501 me Sat May 2 22:37:01 2026 make\n",
    );
    runner.on_stdout(match_exact("ps", &["-axwwo", "pid=,comm="]), "");
    Ctx {
        os: Kind::Darwin,
        env: Arc::new(MapEnv::default()),
        runner: Arc::new(runner),
        processes: Arc::new(FixedProcesses::default()),
        ..Ctx::system("test")
    }
}

fn run_with(args: &[&str], stdout: &mut dyn Write) -> (i32, String) {
    let ctx = ctx();
    let mut err = Vec::new();
    let mut cli = Cli::new(&ctx, false, stdout, &mut err);
    let code = run(&mut cli, &args.iter().map(|s| s.to_string()).collect::<Vec<_>>());
    (code, String::from_utf8(err).expect("utf8"))
}

// Go: TestRunLineage.
#[test]
fn text() {
    let mut out = Vec::new();
    let (code, err) = run_with(&["10"], &mut out);
    assert_eq!(code, 0, "{err}");
    let s = String::from_utf8(out).expect("utf8");
    for want in ["Ancestors", "launchd", "zsh", "make", "reparented", "2 processes"] {
        assert!(s.contains(want), "missing {want:?} in:\n{s}");
    }
}

// Go: TestRunLineageJSON.
#[test]
fn json() {
    let mut out = Vec::new();
    let (code, err) = run_with(&["--json", "20"], &mut out);
    assert_eq!(code, 0, "{err}");
    let v: serde_json::Value = serde_json::from_slice(&out).expect("json");
    assert_eq!(v["pid"], 20);
}

// Go: TestRunLineageErrors.
#[test]
fn errors() {
    for (name, args, want) in [
        ("no args", &[][..], 2),
        ("bad pid", &["abc"], 2),
        ("zero pid", &["0"], 2),
        ("bad flag", &["--nope", "1"], 2),
        ("unknown pid", &["999"], 1),
    ] {
        let (code, err) = run_with(args, &mut Vec::new());
        assert_eq!(code, want, "{name}: {err}");
    }
}

struct FailWriter;

impl Write for FailWriter {
    fn write(&mut self, _: &[u8]) -> io::Result<usize> {
        Err(io::Error::other("closed"))
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

// Go: TestRunLineageWriteError.
#[test]
fn write_error() {
    let (code, err) = run_with(&["10"], &mut FailWriter);
    assert_eq!((code, err.as_str()), (1, "lineage: write output: closed\n"));
}
