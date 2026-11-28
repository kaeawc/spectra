//! The Go texts the collectors' errors carry: `strconv` number errors, `os.PathError` and
//! `syscall.Errno`, so a Rust error message reads as the Go build's does.

use std::fmt;
use std::io;

/// `syscall.Errno.Error()` for an OS error: the C library's message with a lowercase first
/// letter (Go's tables are the same text), without Rust's `(os error N)` suffix. Errors that
/// don't come from the OS print as they are.
pub fn errno_text(e: &io::Error) -> String {
    let Some(code) = e.raw_os_error() else { return e.to_string() };
    let text = io::Error::from_raw_os_error(code).to_string();
    let text = text.strip_suffix(&format!(" (os error {code})")).unwrap_or(&text);
    let mut chars = text.chars();
    match chars.next() {
        Some(first) => first.to_lowercase().chain(chars).collect(),
        None => String::new(),
    }
}

/// `os.PathError.Error()`: `<op> <path>: <errno>`, as `os.ReadFile` (`open`) or `os.Stat`
/// (`stat`) report a failure.
pub fn path_error(op: &str, path: &str, e: &io::Error) -> String {
    format!("{op} {path}: {}", errno_text(e))
}

/// Go's `strconv.ErrSyntax` and `strconv.ErrRange`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NumErrorKind {
    Syntax,
    Range,
}

/// `strconv.NumError`. `value` is what Go returns alongside the error: 0 for a syntax error and
/// the clamped limit for a range error, which callers that ignore the error still store.
#[derive(Clone, Debug, PartialEq)]
pub struct NumError<T> {
    pub func: &'static str,
    pub num: String,
    pub kind: NumErrorKind,
    pub value: T,
}

impl<T> fmt::Display for NumError<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let err = match self.kind {
            NumErrorKind::Syntax => "invalid syntax",
            NumErrorKind::Range => "value out of range",
        };
        write!(f, "strconv.{}: parsing {}: {err}", self.func, quote(&self.num))
    }
}

impl<T: fmt::Debug> std::error::Error for NumError<T> {}

fn num_err<T>(func: &'static str, s: &str, kind: NumErrorKind, value: T) -> NumError<T> {
    NumError { func, num: s.to_string(), kind, value }
}

/// `strconv.ParseUint(s, 10, 64)`: ASCII digits only, no sign, no `_`.
pub fn parse_uint(s: &str) -> Result<u64, NumError<u64>> {
    if s.is_empty() || !s.bytes().all(|b| b.is_ascii_digit()) {
        return Err(num_err("ParseUint", s, NumErrorKind::Syntax, 0));
    }
    let mut n: u64 = 0;
    for b in s.bytes() {
        n = match n.checked_mul(10).and_then(|n| n.checked_add(u64::from(b - b'0'))) {
            Some(n) => n,
            None => return Err(num_err("ParseUint", s, NumErrorKind::Range, u64::MAX)),
        };
    }
    Ok(n)
}

/// `strconv.ParseInt(s, 10, 64)` (and `strconv.Atoi` on 64-bit hosts): an optional sign, then
/// ASCII digits.
pub fn parse_int(s: &str) -> Result<i64, NumError<i64>> {
    let (neg, digits) = match s.as_bytes().first() {
        Some(b'+') => (false, &s[1..]),
        Some(b'-') => (true, &s[1..]),
        _ => (false, s),
    };
    let un = match parse_uint(digits) {
        Ok(v) => v,
        Err(e) if e.kind == NumErrorKind::Syntax => return Err(num_err("ParseInt", s, NumErrorKind::Syntax, 0)),
        Err(_) => u64::MAX,
    };
    let cutoff = 1u64 << 63;
    if !neg && un >= cutoff {
        return Err(num_err("ParseInt", s, NumErrorKind::Range, i64::MAX));
    }
    if neg && un > cutoff {
        return Err(num_err("ParseInt", s, NumErrorKind::Range, i64::MIN));
    }
    // un <= 2^63 here, and -(2^63) is i64::MIN.
    Ok(if neg { 0i64.wrapping_sub_unsigned(un) } else { un as i64 })
}

/// `strconv.ParseFloat(s, 64)` for decimal input, `inf`/`infinity` and `nan`. Go also takes
/// hexadecimal floats (`0x1p-2`); those are a syntax error here, and the kernel never writes them.
pub fn parse_float(s: &str) -> Result<f64, NumError<f64>> {
    let unsigned = s.strip_prefix(['+', '-']).unwrap_or(s);
    if unsigned.len() != s.len() && unsigned.eq_ignore_ascii_case("nan") {
        return Err(num_err("ParseFloat", s, NumErrorKind::Syntax, 0.0));
    }
    // Rust and Go agree on the decimal grammar; both take "inf", "infinity" and "nan" in any case.
    let v: f64 = s.parse().map_err(|_| num_err("ParseFloat", s, NumErrorKind::Syntax, 0.0))?;
    let literal_inf = unsigned.eq_ignore_ascii_case("inf") || unsigned.eq_ignore_ascii_case("infinity");
    if v.is_infinite() && !literal_inf {
        return Err(num_err("ParseFloat", s, NumErrorKind::Range, v));
    }
    Ok(v)
}

/// `strconv.Quote` for valid UTF-8.
pub fn quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => out.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn number_errors_read_as_go() {
        assert_eq!(parse_float("abc").expect_err("abc").to_string(), r#"strconv.ParseFloat: parsing "abc": invalid syntax"#);
        assert_eq!(parse_uint("+5").expect_err("+5").to_string(), r#"strconv.ParseUint: parsing "+5": invalid syntax"#);
        assert_eq!(parse_int("x").expect_err("x").to_string(), r#"strconv.ParseInt: parsing "x": invalid syntax"#);
        let big = parse_uint("18446744073709551616").expect_err("overflow");
        assert_eq!((big.kind, big.value), (NumErrorKind::Range, u64::MAX));
        assert_eq!(big.to_string(), r#"strconv.ParseUint: parsing "18446744073709551616": value out of range"#);
        assert_eq!(parse_int("9223372036854775808").expect_err("overflow").value, i64::MAX);
        assert_eq!(parse_int("-9223372036854775809").expect_err("overflow").value, i64::MIN);
        assert_eq!(parse_float("1e400").expect_err("range").kind, NumErrorKind::Range);
        assert_eq!(quote("a\"b\\\n\u{1}"), r#""a\"b\\\n\x01""#);
    }

    #[test]
    fn numbers_parse_as_go() {
        assert_eq!(parse_uint("0042"), Ok(42));
        assert_eq!(parse_uint("18446744073709551615"), Ok(u64::MAX));
        assert_eq!(parse_int("-9223372036854775808"), Ok(i64::MIN));
        assert_eq!(parse_int("+7"), Ok(7));
        assert!(parse_int("0x10").is_err(), "base 10 takes no prefix");
        assert!(parse_uint("1_000").is_err());
        assert_eq!(parse_float("26.31"), Ok(26.31));
        assert_eq!(parse_float("Inf"), Ok(f64::INFINITY));
        assert!(parse_float("-nan").is_err());
        assert!(parse_float("").is_err());
    }

    #[test]
    fn os_errors_read_as_go() {
        let e = io::Error::from_raw_os_error(libc_enoent());
        assert_eq!(errno_text(&e), "no such file or directory");
        assert_eq!(path_error("open", "/proc/meminfo", &e), "open /proc/meminfo: no such file or directory");
        assert_eq!(errno_text(&io::Error::other("custom")), "custom");
    }

    fn libc_enoent() -> i32 {
        2
    }
}
