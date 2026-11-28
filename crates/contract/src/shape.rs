//! Parity layer 2: the shape of a command's JSON, compared between the Go and Rust builds on a
//! live host (`spectra-shape-diff`). Layer 1 is the recorded cases ([`crate::Case`]), which pin
//! bytes for fixed inputs; this layer needs no recording and so covers commands whose output
//! depends on the machine. It compares structure, never values:
//!
//! - every key path, so an added or removed key is a difference;
//! - the JSON type at each path (`null`, `bool`, `number`, `string`, `array`, `object`), so
//!   `null` versus an absent key, and `1` versus `"1"`, are differences;
//! - not values, and not integer versus float (both are `number`).
//!
//! **Arrays** are compared by the union of their elements' shapes: every element contributes to
//! one path `p[]`, whose type set is the types seen and whose children are the union of the
//! elements' keys. Element order and count are ignored, which suits lists whose contents differ
//! between runs (processes, interfaces). The consequence is that a key present in only some
//! elements still counts as present, and that an empty array has no element shape: when either
//! build prints `[]` where the other prints elements, the element paths are reported as
//! unchecked rather than as differences, since the two builds simply saw different data.

use std::collections::{BTreeMap, BTreeSet};
use std::fmt::Write as _;

use serde_json::Value;
use spectra_base::proc::{Cmd, Runner};

/// The JSON types, named as in the diff output.
fn type_name(v: &Value) -> &'static str {
    match v {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
    }
}

/// A document's shape: the set of types seen at each path, plus the arrays that were empty.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Shape {
    types: BTreeMap<String, BTreeSet<&'static str>>,
    empty_arrays: BTreeSet<String>,
}

impl Shape {
    pub fn of(v: &Value) -> Shape {
        let mut shape = Shape::default();
        shape.visit("$", v);
        shape
    }

    fn visit(&mut self, path: &str, v: &Value) {
        self.types.entry(path.to_string()).or_default().insert(type_name(v));
        match v {
            Value::Object(map) => {
                for (k, child) in map {
                    self.visit(&format!("{path}.{k}"), child);
                }
            }
            Value::Array(items) => {
                if items.is_empty() {
                    self.empty_arrays.insert(path.to_string());
                }
                for item in items {
                    self.visit(&format!("{path}[]"), item);
                }
            }
            _ => {}
        }
    }
}

/// One difference between two shapes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Difference {
    /// Present in the Rust output only.
    Added { path: String, types: String },
    /// Present in the Go output only.
    Removed { path: String, types: String },
    /// Present in both with different types.
    TypeChanged { path: String, go: String, rust: String },
}

impl std::fmt::Display for Difference {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Difference::Added { path, types } => write!(f, "+ {path}: only in rust ({types})"),
            Difference::Removed { path, types } => write!(f, "- {path}: only in go ({types})"),
            Difference::TypeChanged { path, go, rust } => write!(f, "~ {path}: go {go}, rust {rust}"),
        }
    }
}

/// The outcome of comparing two shapes.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Report {
    pub differences: Vec<Difference>,
    /// Element paths under an array that one build printed empty and the other did not: not
    /// compared, because the two builds saw different data.
    pub unchecked: Vec<String>,
}

fn join(types: &BTreeSet<&'static str>) -> String {
    types.iter().copied().collect::<Vec<_>>().join("|")
}

/// Whether `path` lies under (or at an element of) an array in `empty`.
fn under_any(path: &str, empty: &BTreeSet<String>) -> Option<String> {
    empty.iter().find(|a| path.strip_prefix(a.as_str()).is_some_and(|rest| rest.starts_with("[]"))).cloned()
}

/// Compares `go` (the reference) with `rust`.
pub fn compare(go: &Shape, rust: &Shape) -> Report {
    let mut report = Report::default();
    let mut unchecked = BTreeSet::new();
    let paths: BTreeSet<&String> = go.types.keys().chain(rust.types.keys()).collect();
    for path in paths {
        let (g, r) = (go.types.get(path), rust.types.get(path));
        // One side's empty array hides the other's element shape: not a difference.
        let hidden = under_any(path, &go.empty_arrays).is_some() || under_any(path, &rust.empty_arrays).is_some();
        if hidden && (g.is_none() || r.is_none()) {
            unchecked.insert(path.clone());
            continue;
        }
        match (g, r) {
            (Some(g), Some(r)) if g != r => {
                report.differences.push(Difference::TypeChanged { path: path.clone(), go: join(g), rust: join(r) });
            }
            (Some(g), None) => report.differences.push(Difference::Removed { path: path.clone(), types: join(g) }),
            (None, Some(r)) => report.differences.push(Difference::Added { path: path.clone(), types: join(r) }),
            _ => {}
        }
    }
    report.unchecked = unchecked.into_iter().collect();
    report
}

/// Compares two JSON documents' shapes.
pub fn compare_json(go: &[u8], rust: &[u8]) -> Result<Report, String> {
    let parse = |who: &str, raw: &[u8]| -> Result<Shape, String> {
        serde_json::from_slice::<Value>(raw).map(|v| Shape::of(&v)).map_err(|e| format!("{who} stdout is not JSON: {e}"))
    };
    Ok(compare(&parse("go", go)?, &parse("rust", rust)?))
}

/// What a `spectra-shape-diff` run decided.
#[derive(Debug, PartialEq, Eq)]
pub enum Outcome {
    /// Shapes match (exit 0).
    Match(String),
    /// Shapes differ (exit 1), with the readable diff.
    Differ(String),
    /// Could not compare (exit 2).
    Error(String),
}

impl Outcome {
    pub fn exit_code(&self) -> i32 {
        match self {
            Outcome::Match(_) => 0,
            Outcome::Differ(_) => 1,
            Outcome::Error(_) => 2,
        }
    }

    pub fn message(&self) -> &str {
        match self {
            Outcome::Match(m) | Outcome::Differ(m) | Outcome::Error(m) => m,
        }
    }
}

/// Runs `go_bin` and `rust_bin` with `args` through `runner` and compares the shape of their
/// stdout JSON. The binaries' exit codes must also agree: a command that fails in one build
/// only is a difference even when both print JSON.
pub fn run(runner: &dyn Runner, go_bin: &str, rust_bin: &str, args: &[String]) -> Outcome {
    let exec = |bin: &str| runner.run(&Cmd::new(bin, args.to_vec())).map_err(|e| format!("run {bin}: {e}"));
    let (go, rust) = match (exec(go_bin), exec(rust_bin)) {
        (Ok(g), Ok(r)) => (g, r),
        (Err(e), _) | (_, Err(e)) => return Outcome::Error(e),
    };
    let report = match compare_json(&go.stdout, &rust.stdout) {
        Ok(r) => r,
        Err(e) => return Outcome::Error(e),
    };
    let mut out = String::new();
    if go.exit_code != rust.exit_code {
        let _ = writeln!(out, "! exit code: go {}, rust {}", go.exit_code, rust.exit_code);
    }
    for d in &report.differences {
        let _ = writeln!(out, "{d}");
    }
    for p in &report.unchecked {
        let _ = writeln!(out, "? {p}: not compared (an empty array on one side)");
    }
    if report.differences.is_empty() && go.exit_code == rust.exit_code {
        let _ = writeln!(out, "shapes match");
        return Outcome::Match(out);
    }
    Outcome::Differ(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use spectra_base::proc::{FakeRunner, Output, match_exact};

    fn diff(go: &str, rust: &str) -> Report {
        compare_json(go.as_bytes(), rust.as_bytes()).expect("both parse")
    }

    fn lines(r: &Report) -> Vec<String> {
        r.differences.iter().map(ToString::to_string).collect()
    }

    #[test]
    fn values_are_ignored() {
        assert_eq!(diff(r#"{"a":1,"b":"x","c":[1,2],"d":{"e":true}}"#, r#"{"a":2.5,"b":"y","c":[9],"d":{"e":false}}"#), Report::default());
    }

    #[test]
    fn added_key() {
        let r = diff(r#"{"a":1}"#, r#"{"a":1,"b":"x"}"#);
        assert_eq!(lines(&r), ["+ $.b: only in rust (string)"]);
    }

    #[test]
    fn removed_key() {
        let r = diff(r#"{"a":1,"b":{"c":null}}"#, r#"{"a":1}"#);
        assert_eq!(lines(&r), ["- $.b: only in go (object)", "- $.b.c: only in go (null)"]);
    }

    #[test]
    fn type_change() {
        let r = diff(r#"{"a":1,"b":[1]}"#, r#"{"a":"1","b":{"x":1}}"#);
        assert_eq!(
            lines(&r),
            [
                "~ $.a: go number, rust string",
                "~ $.b: go array, rust object",
                "+ $.b.x: only in rust (number)",
                "- $.b[]: only in go (number)"
            ]
        );
    }

    #[test]
    fn null_versus_absent() {
        // Go printing null where Rust omits the key (or the reverse) is a difference, and so is
        // null against a real value.
        assert_eq!(lines(&diff(r#"{"a":null}"#, "{}")), ["- $.a: only in go (null)"]);
        assert_eq!(lines(&diff("{}", r#"{"a":null}"#)), ["+ $.a: only in rust (null)"]);
        assert_eq!(lines(&diff(r#"{"a":null}"#, r#"{"a":[]}"#)), ["~ $.a: go null, rust array"]);
    }

    #[test]
    fn nested_arrays_compare_by_union_of_element_shapes() {
        let go = r#"{"rows":[{"id":1},{"id":2,"extra":true}],"grid":[[1,2],[3]]}"#;
        // Order, count and which element carries a key do not matter.
        let rust = r#"{"rows":[{"id":1,"extra":false}],"grid":[[7]]}"#;
        assert_eq!(diff(go, rust), Report::default());
        // A key no element has is missing; a type no element has changes the set.
        let rust = r#"{"rows":[{"id":"1"}],"grid":[["a"]]}"#;
        assert_eq!(
            lines(&diff(go, rust)),
            ["~ $.grid[][]: go number, rust string", "- $.rows[].extra: only in go (bool)", "~ $.rows[].id: go number, rust string"]
        );
        // Mixed element types are one set.
        assert_eq!(lines(&diff(r#"{"a":[1,"x"]}"#, r#"{"a":[1]}"#)), ["~ $.a[]: go number|string, rust number"]);
    }

    #[test]
    fn empty_array_hides_element_shape() {
        let r = diff(r#"{"a":[{"x":1}]}"#, r#"{"a":[]}"#);
        assert!(r.differences.is_empty(), "{r:?}");
        assert_eq!(r.unchecked, ["$.a[]", "$.a[].x"]);
        // The array itself still has to be an array.
        assert_eq!(
            lines(&diff(r#"{"a":[{"x":1}]}"#, r#"{"a":null}"#)),
            ["~ $.a: go array, rust null", "- $.a[]: only in go (object)", "- $.a[].x: only in go (number)"]
        );
    }

    #[test]
    fn non_json_is_an_error() {
        assert_eq!(compare_json(b"{}", b"nope").expect_err("bad"), "rust stdout is not JSON: expected ident at line 1 column 2");
    }

    fn fake(go: &str, rust: &str, rust_exit: i32) -> FakeRunner {
        let f = FakeRunner::new();
        f.on_stdout(match_exact("go-bin", &["capabilities", "--json"]), go);
        f.on(
            match_exact("rust-bin", &["capabilities", "--json"]),
            Output { stdout: rust.as_bytes().to_vec(), exit_code: rust_exit, ..Output::default() },
        );
        f
    }

    #[test]
    fn run_goes_through_the_runner() {
        let args = vec!["capabilities".to_string(), "--json".to_string()];
        let ok = run(&fake(r#"{"a":1}"#, r#"{"a":2}"#, 0), "go-bin", "rust-bin", &args);
        assert_eq!((ok.exit_code(), ok.message()), (0, "shapes match\n"));
        let bad = run(&fake(r#"{"a":1}"#, r#"{"b":2}"#, 0), "go-bin", "rust-bin", &args);
        assert_eq!((bad.exit_code(), bad.message()), (1, "- $.a: only in go (number)\n+ $.b: only in rust (number)\n"));
        let exit = run(&fake("{}", "{}", 3), "go-bin", "rust-bin", &args);
        assert_eq!((exit.exit_code(), exit.message()), (1, "! exit code: go 0, rust 3\n"));
        let missing = run(&FakeRunner::new(), "go-bin", "rust-bin", &args);
        assert_eq!(missing.exit_code(), 2);
    }
}
