//! `spectra-shape-diff <go-bin> <rust-bin> -- <args>...`: runs both builds with the same
//! arguments and compares the shape of their JSON output (see `spectra_contract::shape`).
//! Exit 0 when the shapes match, 1 with a path-by-path diff when they differ, 2 when the
//! comparison could not run.

use std::io::Write;
use std::process::ExitCode;

use spectra_base::proc::OsRunner;
use spectra_contract::shape::{self, Outcome};

const USAGE: &str = "usage: spectra-shape-diff <go-bin> <rust-bin> -- <args>...";

fn parse(args: &[String]) -> Option<(&str, &str, &[String])> {
    match args {
        [go, rust, sep, rest @ ..] if sep == "--" => Some((go, rust, rest)),
        _ => None,
    }
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args_os().skip(1).map(|a| a.to_string_lossy().into_owned()).collect();
    let Some((go, rust, rest)) = parse(&args) else {
        let _ = writeln!(std::io::stderr(), "{USAGE}");
        return ExitCode::from(2);
    };
    // The one real-process entry point: tests drive `shape::run` with a FakeRunner.
    let outcome = shape::run(&OsRunner, go, rust, rest);
    let code = outcome.exit_code();
    match &outcome {
        Outcome::Match(m) => print!("{m}"),
        Outcome::Differ(m) | Outcome::Error(m) => {
            let _ = write!(std::io::stderr(), "{m}");
            if matches!(outcome, Outcome::Error(_)) {
                let _ = writeln!(std::io::stderr());
            }
        }
    }
    ExitCode::from(code as u8)
}
