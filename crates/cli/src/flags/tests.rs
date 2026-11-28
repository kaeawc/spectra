//! Ported from Go's `flag/flag_test.go` (the `FlagSet` cases) and from the `cmd/spectra` tables
//! that exercise `parseInterspersed` (`owner_test.go`).

use super::*;

const S: i64 = 1_000_000_000;

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

fn parse(fs: &mut FlagSet, a: &[&str]) -> (Result<(), FlagError>, String) {
    let mut out = Vec::new();
    let r = fs.parse(&args(a), &mut out);
    (r, String::from_utf8(out).expect("utf8"))
}

// Go: testParse / TestFlagSetParse.
#[test]
fn parses_every_builtin_type() {
    let mut f = FlagSet::new("test");
    assert!(!f.parsed());
    let bool_flag = f.bool("bool", false, "bool value");
    let bool2 = f.bool("bool2", false, "bool2 value");
    let int_flag = f.int("int", 0, "int value");
    let int64_flag = f.int64("int64", 0, "int64 value");
    let uint_flag = f.uint("uint", 0, "uint value");
    let uint64_flag = f.uint64("uint64", 0, "uint64 value");
    let string_flag = f.string("string", "0", "string value");
    let float_flag = f.float64("float64", 0.0, "float64 value");
    let duration_flag = f.duration("duration", GoDuration(5 * S), "time.Duration value");
    let extra = "one-extra-argument";
    let (r, _) = parse(
        &mut f,
        &[
            "-bool",
            "-bool2=true",
            "--int",
            "22",
            "--int64",
            "0x23",
            "-uint",
            "24",
            "--uint64",
            "25",
            "-string",
            "hello",
            "-float64",
            "2718e28",
            "-duration",
            "2m",
            extra,
        ],
    );
    r.expect("parse");
    assert!(f.parsed());
    assert!(bool_flag.get());
    assert!(bool2.get());
    assert_eq!(int_flag.get(), 22);
    assert_eq!(int64_flag.get(), 0x23);
    assert_eq!(uint_flag.get(), 24);
    assert_eq!(uint64_flag.get(), 25);
    assert_eq!(string_flag.get(), "hello");
    assert_eq!(float_flag.get(), 2718e28);
    assert_eq!(duration_flag.get(), GoDuration(120 * S));
    assert_eq!(f.args(), [extra]);
}

/// `flagVar` from Go's tests: a list that appends on every Set.
struct FlagVar(Rc<RefCell<Vec<String>>>);

impl Value for FlagVar {
    fn set(&mut self, s: &str) -> Result<(), String> {
        self.0.borrow_mut().push(s.to_string());
        Ok(())
    }
    fn string(&self) -> String {
        format!("[{}]", self.0.borrow().join(" "))
    }
    fn zero_string(&self) -> String {
        "[]".into()
    }
}

// Go: TestUserDefined.
#[test]
fn user_defined_value() {
    let mut f = FlagSet::new("test");
    let v = Rc::new(RefCell::new(Vec::new()));
    f.var("v", "usage", Box::new(FlagVar(Rc::clone(&v))));
    parse(&mut f, &["-v", "1", "-v", "2", "-v=3"]).0.expect("parse");
    assert_eq!(*v.borrow(), ["1", "2", "3"]);
    assert_eq!(f.lookup("v").expect("v").value().string(), "[1 2 3]");
}

/// `boolFlagVar` from Go's tests: a switch until it has been set four times.
struct BoolFlagVar(Rc<RefCell<i32>>);

impl Value for BoolFlagVar {
    fn set(&mut self, s: &str) -> Result<(), String> {
        if s == "true" {
            *self.0.borrow_mut() += 1;
        }
        Ok(())
    }
    fn string(&self) -> String {
        self.0.borrow().to_string()
    }
    fn is_bool_flag(&self) -> bool {
        *self.0.borrow() < 4
    }
    fn zero_string(&self) -> String {
        "0".into()
    }
}

// Go: TestUserDefinedBool.
#[test]
fn user_defined_bool_flag() {
    let mut f = FlagSet::new("test");
    let count = Rc::new(RefCell::new(0));
    f.var("b", "usage", Box::new(BoolFlagVar(Rc::clone(&count))));
    let (r, _) = parse(&mut f, &["-b", "-b", "-b", "-b=true", "-b=false", "-b", "barg", "-b"]);
    assert_eq!(*count.borrow(), 4);
    assert!(r.is_err(), "the last -b needs an argument once it stops being a switch");
}

// Go: TestUserDefinedBoolUsage.
#[test]
fn user_defined_bool_usage() {
    let mut f = FlagSet::new("test");
    let count = Rc::new(RefCell::new(0));
    f.var("b", "X", Box::new(BoolFlagVar(Rc::clone(&count))));
    let mut out = Vec::new();
    f.print_defaults(&mut out);
    assert_eq!(String::from_utf8_lossy(&out), "  -b\tX\n");
    *count.borrow_mut() = 4;
    f.print_defaults(&mut out);
    // The default is the text at definition time ("0"), which is the zero value: not printed.
    assert_eq!(String::from_utf8_lossy(&out), "  -b\tX\n  -b value\n    \tX\n");
}

// Go: TestSetOutput and TestUsageOutput.
#[test]
fn unknown_flag_prints_error_and_usage() {
    let mut f = FlagSet::new("app");
    let (r, out) = parse(&mut f, &["-i=1", "-unknown"]);
    assert_eq!(r, Err(FlagError::Invalid("flag provided but not defined: -i".into())));
    assert_eq!(out, "flag provided but not defined: -i\nUsage of app:\n");
    let mut unnamed = FlagSet::new("");
    assert_eq!(parse(&mut unnamed, &["-x"]).1, "flag provided but not defined: -x\nUsage:\n");
}

// Go: TestUsage (a custom usage runs for an unknown flag).
#[test]
fn custom_usage_runs_on_error() {
    let mut f = FlagSet::new("t");
    f.set_usage(Box::new(|fs, out| {
        let _ = writeln!(out, "custom usage for {}", fs.name());
    }));
    let (r, out) = parse(&mut f, &["-x"]);
    assert!(r.is_err());
    assert_eq!(out, "flag provided but not defined: -x\ncustom usage for t\n");
}

// Go: TestHelp.
#[test]
fn help_flag_prints_usage_unless_defined() {
    let mut f = FlagSet::new("help test");
    f.set_usage(Box::new(|_, out| {
        let _ = write!(out, "USAGE");
    }));
    let flag = f.bool("flag", false, "regular flag");
    let (r, out) = parse(&mut f, &["-flag=true"]);
    r.expect("regular flag");
    assert!(flag.get());
    assert_eq!(out, "");
    for h in ["-help", "-h", "--help", "--h"] {
        assert_eq!(parse(&mut f, &[h]), (Err(FlagError::Help), "USAGE".to_string()), "{h}");
    }
    assert_eq!(FlagError::Help.to_string(), "flag: help requested");
    let help = f.bool("help", false, "help flag");
    assert_eq!(parse(&mut f, &["-help"]), (Ok(()), String::new()));
    assert!(help.get());
}

// Go: TestPrintDefaults, without the two zeroPanicker flags (Rust has no reflection to build a
// zero value, so a custom Value states its zero text instead).
#[test]
fn print_defaults_matches_go() {
    let mut f = FlagSet::new("print defaults test");
    f.bool("A", false, "for bootstrapping, allow 'any' type");
    f.bool("Alongflagname", false, "disable bounds checking");
    f.bool("C", true, "a boolean defaulting to true");
    f.string("D", "", "set relative `path` for local imports");
    f.string("E", "0", "issue 23543");
    f.float64("F", 2.7, "a non-zero `number`");
    f.float64("G", 0.0, "a float that defaults to zero");
    f.string("M", "", "a multiline\nhelp\nstring");
    f.int("N", 27, "a non-zero int");
    f.bool("O", true, "a flag\nmultiline help string");
    f.var("V", "a `list` of strings", Box::new(FlagVar(Rc::new(RefCell::new(vec!["a".into(), "b".into()])))));
    f.int("Z", 0, "an int that defaults to zero");
    f.duration("maxT", GoDuration(0), "set `timeout` for dial");
    let mut out = Vec::new();
    f.print_defaults(&mut out);
    let want = "  -A\tfor bootstrapping, allow 'any' type
  -Alongflagname
    \tdisable bounds checking
  -C\ta boolean defaulting to true (default true)
  -D path
    \tset relative path for local imports
  -E string
    \tissue 23543 (default \"0\")
  -F number
    \ta non-zero number (default 2.7)
  -G float
    \ta float that defaults to zero
  -M string
    \ta multiline
    \thelp
    \tstring
  -N int
    \ta non-zero int (default 27)
  -O\ta flag
    \tmultiline help string (default true)
  -V list
    \ta list of strings (default [a b])
  -Z int
    \tan int that defaults to zero
  -maxT timeout
    \tset timeout for dial
";
    assert_eq!(String::from_utf8(out).expect("utf8"), want);
}

#[test]
fn print_defaults_shows_non_zero_durations_and_uints() {
    let mut f = FlagSet::new("spectra owner");
    f.duration("timeout", GoDuration(10 * S), "Give up (exit 1) after this long");
    f.uint("n", 3, "count");
    f.string("only-unit", "", "one back quote ` is not a name");
    let mut out = Vec::new();
    f.usage(&mut out);
    assert_eq!(
        String::from_utf8(out).expect("utf8"),
        "Usage of spectra owner:\n  -n uint\n    \tcount (default 3)\n  -only-unit string\n    \tone back quote ` is not a name\n  -timeout duration\n    \tGive up (exit 1) after this long (default 10s)\n"
    );
}

// Go: TestParseError.
#[test]
fn parse_errors_say_parse_error() {
    for typ in ["bool", "int", "int64", "uint", "uint64", "float64", "duration"] {
        let mut f = FlagSet::new("parse error test");
        f.bool("bool", false, "");
        f.int("int", 0, "");
        f.int64("int64", 0, "");
        f.uint("uint", 0, "");
        f.uint64("uint64", 0, "");
        f.float64("float64", 0.0, "");
        f.duration("duration", GoDuration(0), "");
        let (r, _) = parse(&mut f, &[&format!("-{typ}=x")]);
        let msg = r.expect_err(typ).to_string();
        assert!(msg.contains("invalid") && msg.contains("parse error"), "{typ}: {msg}");
    }
}

// Go: TestRangeError.
#[test]
fn range_errors_say_out_of_range() {
    for arg in [
        "-int=123456789012345678901",
        "-int64=123456789012345678901",
        "-uint=123456789012345678901",
        "-uint64=123456789012345678901",
        "-float64=1e1000",
    ] {
        let mut f = FlagSet::new("parse error test");
        f.int("int", 0, "");
        f.int64("int64", 0, "");
        f.uint("uint", 0, "");
        f.uint64("uint64", 0, "");
        f.float64("float64", 0.0, "");
        let msg = parse(&mut f, &[arg]).0.expect_err(arg).to_string();
        assert!(msg.contains("invalid") && msg.contains("value out of range"), "{arg}: {msg}");
    }
}

// Go: TestInvalidFlags.
#[test]
#[should_panic(expected = "flag \"-foo\" begins with -")]
fn flag_names_cannot_begin_with_a_dash() {
    FlagSet::new("").bool("-foo", false, "");
}

#[test]
#[should_panic(expected = "flag \"foo=bar\" contains =")]
fn flag_names_cannot_contain_equals() {
    FlagSet::new("").bool("foo=bar", false, "");
}

// Go: TestRedefinedFlags.
#[test]
#[should_panic(expected = "fs flag redefined: foo")]
fn redefining_a_flag_panics() {
    let mut f = FlagSet::new("fs");
    f.bool("foo", false, "");
    f.bool("foo", false, "");
}

#[test]
#[should_panic(expected = "flag redefined: foo")]
fn redefining_a_flag_in_an_unnamed_set_panics() {
    let mut f = FlagSet::new("");
    f.string("foo", "", "");
    f.string("foo", "", "");
}

// The four spellings the CLI promises, for a switch and for a valued flag.
#[test]
fn one_and_two_dashes_with_and_without_equals() {
    for (spelling, want_json, want_limit) in [
        (&["-json", "-limit", "5"][..], true, 5),
        (&["--json", "--limit", "5"][..], true, 5),
        (&["-json=true", "-limit=5"][..], true, 5),
        (&["--json=false", "--limit=5"][..], false, 5),
        (&["-json=0", "--limit=-0x10"][..], false, -16),
    ] {
        let mut f = FlagSet::new("t");
        let json = f.bool("json", false, "");
        let limit = f.int("limit", 0, "");
        parse(&mut f, spelling).0.unwrap_or_else(|e| panic!("{spelling:?}: {e}"));
        assert_eq!((json.get(), limit.get()), (want_json, want_limit), "{spelling:?}");
        assert!(f.args().is_empty());
    }
}

// Go's parseOne edge cases, each with the message Go prints.
#[test]
fn parse_one_edge_cases_match_go() {
    struct Case {
        args: &'static [&'static str],
        err: Option<&'static str>,
        rest: &'static [&'static str],
    }
    let cases = [
        Case { args: &["-v", "a", "-n", "1"], err: None, rest: &["a", "-n", "1"] },
        Case { args: &["-", "-v"], err: None, rest: &["-", "-v"] },
        Case { args: &["--", "-v"], err: None, rest: &["-v"] },
        Case { args: &["-v", "false"], err: None, rest: &["false"] },
        Case { args: &["-s", "-v"], err: None, rest: &[] },
        Case { args: &["-s="], err: None, rest: &[] },
        // Go reports bad syntax before consuming the argument.
        Case { args: &["---v"], err: Some("bad flag syntax: ---v"), rest: &["---v"] },
        Case { args: &["-=v"], err: Some("bad flag syntax: -=v"), rest: &["-=v"] },
        Case { args: &["--=v"], err: Some("bad flag syntax: --=v"), rest: &["--=v"] },
        Case { args: &["-n"], err: Some("flag needs an argument: -n"), rest: &[] },
        Case { args: &["-n", "x"], err: Some("invalid value \"x\" for flag -n: parse error"), rest: &[] },
        Case { args: &["-v=maybe"], err: Some("invalid boolean value \"maybe\" for -v: parse error"), rest: &[] },
        Case { args: &["-v=", "a"], err: Some("invalid boolean value \"\" for -v: parse error"), rest: &["a"] },
        Case { args: &["-nope", "a"], err: Some("flag provided but not defined: -nope"), rest: &["a"] },
        Case { args: &["-é=1"], err: Some("flag provided but not defined: -é"), rest: &[] },
    ];
    for c in cases {
        let mut f = FlagSet::new("t");
        f.bool("v", false, "verbose");
        f.int("n", 0, "count");
        f.string("s", "", "text");
        let (r, out) = parse(&mut f, c.args);
        match c.err {
            None => {
                r.unwrap_or_else(|e| panic!("{:?}: {e}", c.args));
                assert_eq!(out, "", "{:?}", c.args);
            }
            Some(msg) => {
                assert_eq!(r, Err(FlagError::Invalid(msg.into())), "{:?}", c.args);
                assert!(out.starts_with(&format!("{msg}\nUsage of t:\n")), "{:?}: {out:?}", c.args);
            }
        }
        assert_eq!(f.args(), c.rest, "{:?}", c.args);
    }
}

#[test]
fn string_value_may_look_like_a_flag() {
    let mut f = FlagSet::new("t");
    let s = f.string("s", "", "");
    let v = f.bool("v", false, "");
    parse(&mut f, &["-s", "-v"]).0.expect("parse");
    assert_eq!((s.get().as_str(), v.get()), ("-v", false));
    let mut visited = Vec::new();
    f.visit(|fl| visited.push(fl.name.clone()));
    assert_eq!(visited, ["s"]);
    assert_eq!(f.nflag(), 1);
}

#[test]
fn set_and_arg_accessors_match_go() {
    let mut f = FlagSet::new("t");
    let n = f.int("n", 0, "");
    f.set("n", "7").expect("set");
    assert_eq!(n.get(), 7);
    assert_eq!(f.set("x", "1"), Err("no such flag -x".into()));
    assert_eq!(f.set("n", "x"), Err("parse error".into()));
    parse(&mut f, &["a", "b"]).0.expect("parse");
    assert_eq!((f.narg(), f.arg(0), f.arg(1), f.arg(2)), (2, "a", "b", ""));
    let mut all = Vec::new();
    f.visit_all(|fl| all.push((fl.name.clone(), fl.def_value.clone())));
    assert_eq!(all, [("n".to_string(), "0".to_string())]);
}

// parseInterspersed, with the argument shapes cmd/spectra/owner_test.go drives through it.
#[test]
fn parse_interspersed_matches_flagparse_go() {
    struct Case {
        args: &'static [&'static str],
        pos: &'static [&'static str],
        under: bool,
        timeout: i64,
        err: Option<&'static str>,
    }
    let cases = [
        Case { args: &[":8080"], pos: &[":8080"], under: false, timeout: 10, err: None },
        Case { args: &[":8080", "--json"], pos: &[":8080"], under: false, timeout: 10, err: None },
        Case { args: &[":8080", "--nope"], pos: &[], under: false, timeout: 10, err: Some("flag provided but not defined: -nope") },
        Case { args: &["--under", "/x", "--timeout", "0s"], pos: &["/x"], under: true, timeout: 0, err: None },
        Case { args: &["/work/repo", "--under"], pos: &["/work/repo"], under: true, timeout: 10, err: None },
        Case { args: &["--under", "--", "-odd", "--json"], pos: &["-odd", "--json"], under: true, timeout: 10, err: None },
        Case { args: &["a", "--", "-b"], pos: &["a", "-b"], under: false, timeout: 10, err: None },
        Case { args: &["a", "b", "--timeout=2s", "c"], pos: &["a", "b", "c"], under: false, timeout: 2, err: None },
        Case { args: &[], pos: &[], under: false, timeout: 10, err: None },
    ];
    for c in cases {
        let mut f = FlagSet::new("spectra owner");
        let json = f.bool("json", false, "Emit JSON instead of a human summary");
        let under = f.bool("under", false, "Treat the arguments as directories");
        let timeout = f.duration("timeout", GoDuration(10 * S), "Give up (exit 1) after this long");
        let mut out = Vec::new();
        match (f.parse_interspersed(&args(c.args), &mut out), c.err) {
            (Ok(pos), None) => {
                assert_eq!(pos, c.pos, "{:?}", c.args);
                assert_eq!((under.get(), timeout.get()), (c.under, GoDuration(c.timeout * S)), "{:?}", c.args);
                assert_eq!(json.get(), c.args.contains(&"--json") && !c.args.contains(&"--"), "{:?}", c.args);
            }
            (Err(e), Some(msg)) => assert_eq!(e, FlagError::Invalid(msg.into()), "{:?}", c.args),
            (got, want) => panic!("{:?}: got {got:?}, want error {want:?}", c.args),
        }
    }
}

#[test]
fn parse_interspersed_help_returns_err_help() {
    let mut f = FlagSet::new("spectra owner");
    f.bool("json", false, "Emit JSON");
    let mut out = Vec::new();
    assert_eq!(f.parse_interspersed(&args(&["x", "-h"]), &mut out), Err(FlagError::Help));
    assert_eq!(String::from_utf8(out).expect("utf8"), "Usage of spectra owner:\n  -json\n    \tEmit JSON\n");
}
