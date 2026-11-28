//! The per-process file-descriptor limit macOS launches processes under (Go's `fdlimit.go`).

use serde::Serialize;
use spectra_base::gojson;
use spectra_base::proc::Runner;

use crate::cmdrun::succeeded;
use crate::goerr::parse_int;

/// Soft is the current limit, hard the ceiling. It complements the system-wide `kern.maxfiles`
/// and per-process open FD counts.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
pub struct FdLimit {
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub soft: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub hard: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub hard_unlimited: bool,
}

/// Go's `CollectFDLimit`: runs `launchctl limit maxfiles` and parses the soft and hard limits.
/// Defensive: failed, empty or unparseable output yields the zero value.
pub fn collect_fd_limit(runner: &dyn Runner) -> FdLimit {
    match succeeded(runner, "launchctl", &["limit", "maxfiles"]) {
        Some(out) => parse_fd_limit(&String::from_utf8_lossy(&out)),
        None => FdLimit::default(),
    }
}

/// Go's `parseFDLimit`. A `unlimited` column leaves its number 0; the hard column also sets
/// `hard_unlimited`.
pub fn parse_fd_limit(out: &str) -> FdLimit {
    for line in out.split('\n') {
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() < 3 || fields[0] != "maxfiles" {
            continue;
        }
        let mut fd = FdLimit::default();
        if let Some(v) = parse_limit_value(fields[1]) {
            fd.soft = v;
        }
        if fields[2] == "unlimited" {
            fd.hard_unlimited = true;
        } else if let Some(v) = parse_limit_value(fields[2]) {
            fd.hard = v;
        }
        return fd;
    }
    FdLimit::default()
}

/// The number in a limit column; `None` for `unlimited` or anything that is not a non-negative
/// integer.
fn parse_limit_value(s: &str) -> Option<i64> {
    if s == "unlimited" {
        return None;
    }
    parse_int(s).ok().filter(|n| *n >= 0)
}

#[cfg(test)]
mod tests {
    use spectra_base::proc::{FakeRunner, match_exact};

    use super::*;

    fn run(out: Option<&str>) -> FdLimit {
        let fake = FakeRunner::new();
        match out {
            Some(o) => fake.on_stdout(match_exact("launchctl", &["limit", "maxfiles"]), o),
            None => fake.on_error(match_exact("launchctl", &["limit", "maxfiles"]), "permission denied"),
        };
        collect_fd_limit(&fake)
    }

    // Go: TestCollectFDLimit.
    #[test]
    fn collect_fd_limit_table() {
        let fd = |soft, hard, hard_unlimited| FdLimit { soft, hard, hard_unlimited };
        let cases: &[(&str, Option<&str>, FdLimit)] = &[
            ("soft numeric, hard unlimited", Some("\tmaxfiles    256            unlimited\n"), fd(256, 0, true)),
            ("both numeric", Some("\tmaxfiles    10240          12288\n"), fd(10240, 12288, false)),
            ("both unlimited", Some("\tmaxfiles    unlimited      unlimited\n"), fd(0, 0, true)),
            ("empty output", Some(""), FdLimit::default()),
            ("runner error", None, FdLimit::default()),
            ("irregular whitespace", Some("   maxfiles\t 200 \t 400 \n"), fd(200, 400, false)),
            ("line does not start with maxfiles", Some("\tmaxproc     2000           4000\n"), FdLimit::default()),
            ("too few columns", Some("\tmaxfiles    256\n"), FdLimit::default()),
            ("non-numeric column", Some("\tmaxfiles    abc            def\n"), FdLimit::default()),
            ("multi-line, header ignored", Some("Some header line\n\tmaxfiles    512            1024\n"), fd(512, 1024, false)),
        ];
        for (name, out, want) in cases {
            assert_eq!(run(*out), *want, "{name}");
        }
    }

    #[test]
    fn negative_columns_are_not_numbers() {
        assert_eq!(parse_fd_limit("maxfiles -1 -2\n"), FdLimit::default());
    }

    #[test]
    fn zero_values_are_omitted_from_json() {
        let json = String::from_utf8(spectra_base::gojson::to_vec(&FdLimit { soft: 256, hard: 0, hard_unlimited: true }).expect("json"))
            .expect("utf8");
        assert_eq!(json, r#"{"soft":256,"hard_unlimited":true}"#);
    }
}
