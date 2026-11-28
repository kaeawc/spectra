//! Parity cases recorded from the Go build (docs/design/rust-port.md, "Parity proof").
//!
//! A case is a directory `contract/cases/<kind>/<name>/` written by a Go test with
//! `-contract-update` (see `internal/contracttest`):
//!
//! - `case.json`: the inputs that aren't host state (command, flags, OS...).
//! - `calls.json` (optional): recorded subprocess calls, replayed by [`Case::runner`].
//! - `env.json` (optional): the environment, served by [`Case::env`].
//! - `now` (optional): an RFC 3339 instant for [`Case::clock`].
//! - `root/` (optional): a filesystem tree, served by [`Case::fs`] at absolute paths.
//! - `expected.stdout`, `expected.exit`: what the Go build printed and its exit code.
//!
//! A Rust test runs the port with these seams and calls [`Case::assert_stdout`].

pub mod shape;

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use base64::Engine;
use serde::Deserialize;
use spectra_base::clock::FakeClock;
use spectra_base::env::MapEnv;
use spectra_base::fs::{FileType, Fs, OsFs, RootedFs};
use spectra_base::proc::{FakeRunner, Output, match_exact};

/// `contract/cases` at the repository root.
pub fn cases_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../contract/cases")
}

/// Every case of `kind`, sorted by name. Panics when there are none, so a misnamed kind can't
/// pass silently.
pub fn cases(kind: &str) -> Vec<Case> {
    let dir = cases_dir().join(kind);
    let entries = OsFs.read_dir(&dir).unwrap_or_else(|e| panic!("{}: {e}", dir.display()));
    let cases: Vec<Case> = entries
        .into_iter()
        .filter(|e| e.file_type == FileType::Dir)
        .map(|e| Case { name: e.name.clone(), dir: dir.join(&e.name) })
        .collect();
    assert!(!cases.is_empty(), "no cases under {}", dir.display());
    cases
}

/// One recorded case.
#[derive(Clone, Debug)]
pub struct Case {
    pub name: String,
    pub dir: PathBuf,
}

/// One recorded subprocess call (Go's `contracttest.Call`).
#[derive(Deserialize)]
struct Call {
    name: String,
    #[serde(default)]
    args: Option<Vec<String>>,
    #[serde(default)]
    stdout: String,
    #[serde(default)]
    stdout_b64: String,
    #[serde(default)]
    stderr: String,
    #[serde(default)]
    stderr_b64: String,
    #[serde(default)]
    exit_code: i32,
    #[serde(default)]
    error: String,
}

fn text_or_b64(text: String, b64: &str) -> Vec<u8> {
    if b64.is_empty() {
        return text.into_bytes();
    }
    base64::engine::general_purpose::STANDARD.decode(b64).unwrap_or_else(|e| panic!("calls.json base64: {e}"))
}

impl Case {
    fn read(&self, file: &str) -> Option<Vec<u8>> {
        OsFs.read(&self.dir.join(file)).ok()
    }

    /// `case.json`, decoded into `T`.
    pub fn input<T: for<'de> Deserialize<'de>>(&self) -> T {
        let raw = self.read("case.json").unwrap_or_else(|| panic!("{}: no case.json", self.dir.display()));
        serde_json::from_slice(&raw).unwrap_or_else(|e| panic!("{}/case.json: {e}", self.dir.display()))
    }

    /// The bytes the Go build printed.
    pub fn expected_stdout(&self) -> Vec<u8> {
        self.read("expected.stdout").unwrap_or_else(|| panic!("{}: no expected.stdout", self.dir.display()))
    }

    /// The Go build's exit code.
    pub fn expected_exit(&self) -> i32 {
        let raw = self.read("expected.exit").unwrap_or_else(|| panic!("{}: no expected.exit", self.dir.display()));
        String::from_utf8_lossy(&raw).trim().parse().unwrap_or_else(|e| panic!("{}/expected.exit: {e}", self.dir.display()))
    }

    /// Replays `calls.json`; without it, every command fails as unmatched.
    pub fn runner(&self) -> FakeRunner {
        let fake = FakeRunner::new();
        let Some(raw) = self.read("calls.json") else { return fake };
        let calls: Vec<Call> = serde_json::from_slice(&raw).unwrap_or_else(|e| panic!("{}/calls.json: {e}", self.dir.display()));
        for call in calls {
            let args: Vec<&str> = call.args.iter().flatten().map(String::as_str).collect();
            let matcher = match_exact(&call.name, &args);
            if call.error.is_empty() {
                let out = Output {
                    stdout: text_or_b64(call.stdout, &call.stdout_b64),
                    stderr: text_or_b64(call.stderr, &call.stderr_b64),
                    exit_code: call.exit_code,
                };
                fake.on(matcher, out);
            } else {
                fake.on_error(matcher, call.error);
            }
        }
        fake
    }

    /// `env.json`, or an empty environment.
    pub fn env(&self) -> MapEnv {
        let vars: BTreeMap<String, String> =
            self.read("env.json").map(|raw| serde_json::from_slice(&raw).unwrap_or_default()).unwrap_or_default();
        MapEnv(vars)
    }

    /// The instant in `now`, or the Unix epoch.
    pub fn clock(&self) -> FakeClock {
        let now = self
            .read("now")
            .map(|raw| {
                String::from_utf8_lossy(&raw)
                    .trim()
                    .parse::<jiff::Timestamp>()
                    .unwrap_or_else(|e| panic!("{}/now: {e}", self.dir.display()))
            })
            .unwrap_or(jiff::Timestamp::UNIX_EPOCH);
        FakeClock::new(now)
    }

    /// The case's `root/` served at absolute paths.
    pub fn fs(&self) -> RootedFs {
        RootedFs::new(self.dir.join("root"))
    }

    /// Fails with the first differing line when `got` isn't byte-identical to the Go build's
    /// output.
    pub fn assert_stdout(&self, got: &[u8]) {
        let want = self.expected_stdout();
        if got == want.as_slice() {
            return;
        }
        panic!("{}: stdout differs from the Go build\n{}", self.dir.display(), first_difference(&want, got));
    }
}

/// A short description of where two outputs first differ, by line.
pub fn first_difference(want: &[u8], got: &[u8]) -> String {
    let want = String::from_utf8_lossy(want);
    let got = String::from_utf8_lossy(got);
    let (mut w, mut g) = (want.split_inclusive('\n'), got.split_inclusive('\n'));
    for line in 1.. {
        match (w.next(), g.next()) {
            (None, None) => return "outputs differ only in bytes that aren't valid UTF-8".to_string(),
            (a, b) if a == b => continue,
            (a, b) => return format!("line {line}:\n  go:   {:?}\n  rust: {:?}", a.unwrap_or("<end>"), b.unwrap_or("<end>")),
        }
    }
    unreachable!()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn runner_replays_recorded_calls() {
        use spectra_base::fsutil::write_file_atomic;
        use spectra_base::proc::{Cmd, RunError, Runner};
        let dir = std::env::temp_dir().join(format!("spectra-contract-replay-{}", std::process::id()));
        std::fs::create_dir_all(&dir).expect("mkdir");
        let calls = br#"[
          {"name": "sysctl", "args": ["-n", "hw.ncpu"], "stdout": "8\n", "exit_code": 0},
          {"name": "bin", "args": null, "stdout_b64": "/wA=", "exit_code": 3},
          {"name": "gone", "args": [], "exit_code": -1, "error": "exec: not found"}
        ]"#;
        write_file_atomic(&dir.join("calls.json"), calls, 0o644).expect("write");
        let case = Case { name: "replay".into(), dir: dir.clone() };
        let runner = case.runner();
        assert_eq!(runner.run(&Cmd::new("sysctl", ["-n", "hw.ncpu"])).expect("sysctl").stdout, b"8\n");
        let bin = runner.run(&Cmd::new("bin", Vec::<String>::new())).expect("bin");
        assert_eq!((bin.stdout, bin.exit_code), (vec![0xff, 0x00], 3));
        assert!(matches!(runner.run(&Cmd::new("gone", Vec::<String>::new())), Err(RunError::Spawn { .. })));
        assert!(matches!(runner.run(&Cmd::new("sysctl", ["-a"])), Err(RunError::Unmatched { .. })));
        std::fs::remove_dir_all(dir).expect("cleanup");
    }

    #[test]
    fn first_difference_names_the_line() {
        let d = first_difference(b"a\nb\nc\n", b"a\nB\nc\n");
        assert!(d.starts_with("line 2:"), "{d}");
        assert!(first_difference(b"a\n", b"a\nb\n").contains("<end>"));
    }
}
