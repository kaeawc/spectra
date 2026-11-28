//! The pieces of Go's `strconv` and `time` that the `flag` package leans on: parsing flag values
//! (`ParseBool`, `ParseInt`/`ParseUint` with base 0, `ParseFloat`, `time.ParseDuration`) and
//! printing defaults (`FormatFloat(f, 'g', -1, 64)`, `Duration.String`, `strconv.Quote`).

use spectra_base::gojson::GoDuration;

/// Go's `strconv.ErrSyntax`, as the `flag` package words it.
pub const PARSE_ERROR: &str = "parse error";
/// Go's `strconv.ErrRange`, as the `flag` package words it.
pub const RANGE_ERROR: &str = "value out of range";

/// `strconv.ParseBool`.
pub fn parse_bool(s: &str) -> Option<bool> {
    match s {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}

/// Why a number did not parse (`strconv.NumError.Err`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NumError {
    Syntax,
    Range,
}

impl NumError {
    /// The `flag` package's wording.
    pub fn as_str(self) -> &'static str {
        match self {
            NumError::Syntax => PARSE_ERROR,
            NumError::Range => RANGE_ERROR,
        }
    }
}

/// `strconv.ParseUint(s, 0, 64)`: `0b`, `0o`, `0x` and leading-`0` octal prefixes, and `_`
/// separators where Go allows them.
pub fn parse_uint(s: &str) -> Result<u64, NumError> {
    if s.is_empty() {
        return Err(NumError::Syntax);
    }
    let b = s.as_bytes();
    let (base, digits): (u64, &[u8]) = if b[0] == b'0' {
        match b.get(1).map(u8::to_ascii_lowercase) {
            Some(b'b') if b.len() >= 3 => (2, &b[2..]),
            Some(b'o') if b.len() >= 3 => (8, &b[2..]),
            Some(b'x') if b.len() >= 3 => (16, &b[2..]),
            _ => (8, &b[1..]),
        }
    } else {
        (10, b)
    };
    let cutoff = u64::MAX / base + 1;
    let mut n: u64 = 0;
    let mut underscores = false;
    for &c in digits {
        let d = match c {
            b'_' => {
                underscores = true;
                continue;
            }
            b'0'..=b'9' => u64::from(c - b'0'),
            _ if c.is_ascii_alphabetic() => u64::from(c.to_ascii_lowercase() - b'a') + 10,
            _ => return Err(NumError::Syntax),
        };
        if d >= base {
            return Err(NumError::Syntax);
        }
        if n >= cutoff {
            return Err(NumError::Range);
        }
        n = (n * base).checked_add(d).ok_or(NumError::Range)?;
    }
    if underscores && !underscore_ok(s) {
        return Err(NumError::Syntax);
    }
    Ok(n)
}

/// `strconv.ParseInt(s, 0, 64)`.
pub fn parse_int(s: &str) -> Result<i64, NumError> {
    if s.is_empty() {
        return Err(NumError::Syntax);
    }
    let (neg, rest) = match s.as_bytes()[0] {
        b'+' => (false, &s[1..]),
        b'-' => (true, &s[1..]),
        _ => (false, s),
    };
    let un = parse_uint(rest)?;
    let cutoff = 1u64 << 63;
    if (!neg && un >= cutoff) || (neg && un > cutoff) {
        return Err(NumError::Range);
    }
    // un <= 2^63 here, and -(2^63) is i64::MIN.
    Ok(if neg { 0i64.wrapping_sub_unsigned(un) } else { un as i64 })
}

/// Go's `underscoreOK`: `_` only between digits, or right after a base prefix.
fn underscore_ok(s: &str) -> bool {
    let mut b = s.as_bytes();
    if let Some(&(b'-' | b'+')) = b.first() {
        b = &b[1..];
    }
    let mut saw = b'^';
    let mut i = 0;
    let mut hex = false;
    if b.len() >= 2 && b[0] == b'0' && matches!(b[1].to_ascii_lowercase(), b'b' | b'o' | b'x') {
        i = 2;
        saw = b'0';
        hex = b[1].eq_ignore_ascii_case(&b'x');
    }
    while i < b.len() {
        let c = b[i];
        i += 1;
        if c.is_ascii_digit() || (hex && c.is_ascii_hexdigit()) {
            saw = b'0';
            continue;
        }
        if c == b'_' {
            if saw != b'0' {
                return false;
            }
            saw = b'_';
            continue;
        }
        if saw == b'_' {
            return false;
        }
        saw = b'!';
    }
    saw != b'_'
}

/// `strconv.ParseFloat(s, 64)` for decimal input, `inf`/`infinity` and `nan`. Go also accepts
/// hexadecimal floats (`0x1p-2`) and `_` after a base prefix; those report a syntax error here.
pub fn parse_float(s: &str) -> Result<f64, NumError> {
    let unsigned = s.strip_prefix(['+', '-']).unwrap_or(s);
    if unsigned.len() != s.len() && unsigned.eq_ignore_ascii_case("nan") {
        return Err(NumError::Syntax);
    }
    // Rust and Go agree on the decimal grammar; Rust also takes "infinity" and "inf" any case.
    let v: f64 = s.parse().map_err(|_| NumError::Syntax)?;
    let literal_inf = unsigned.eq_ignore_ascii_case("inf") || unsigned.eq_ignore_ascii_case("infinity");
    if v.is_infinite() && !literal_inf {
        return Err(NumError::Range);
    }
    Ok(v)
}

/// `strconv.FormatFloat(f, 'g', -1, 64)`: the shortest text that round-trips, in `%e` form when
/// the decimal exponent is below -4 or at least 6 (Go's rule for shortest `%g`).
pub fn format_float(f: f64) -> String {
    if f.is_nan() {
        return "NaN".into();
    }
    if f.is_infinite() {
        return if f > 0.0 { "+Inf".into() } else { "-Inf".into() };
    }
    let neg = f.is_sign_negative();
    let sign = if neg { "-" } else { "" };
    if f == 0.0 {
        return format!("{sign}0");
    }
    // Rust's `{:e}` prints the shortest round-tripping digits, like Go's shortest mode.
    let e = format!("{:e}", f.abs());
    let (mantissa, exp) = e.split_once('e').unwrap_or((&e, "0"));
    let digits: Vec<u8> = mantissa.bytes().filter(u8::is_ascii_digit).collect();
    let exp: i64 = exp.parse().unwrap_or(0);
    let nd = digits.len() as i64;
    let dp = exp + 1;
    if !(-4..6).contains(&exp) {
        let mut out = String::from(sign);
        out.push(char::from(digits[0]));
        if digits.len() > 1 {
            out.push('.');
            out.extend(digits[1..].iter().map(|&d| char::from(d)));
        }
        let (esign, eabs) = if exp < 0 { ('-', -exp) } else { ('+', exp) };
        out.push_str(&format!("e{esign}{eabs:02}"));
        return out;
    }
    let digit = |i: i64| -> char { if (0..nd).contains(&i) { char::from(digits[i as usize]) } else { '0' } };
    let mut out = String::from(sign);
    if dp > 0 {
        (0..dp).for_each(|i| out.push(digit(i)));
    } else {
        out.push('0');
    }
    let frac = (nd - dp).max(0);
    if frac > 0 {
        out.push('.');
        (0..frac).for_each(|i| out.push(digit(dp + i)));
    }
    out
}

/// `strconv.Quote`. Go's `unicode.IsPrint` is approximated: non-ASCII control and space
/// characters and the common format characters (zero-width and bidi controls, BOM, soft hyphen)
/// are escaped; every other non-ASCII character is printed.
pub fn quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            c if is_print(c) => out.push(c),
            '\x07' => out.push_str("\\a"),
            '\x08' => out.push_str("\\b"),
            '\x0c' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\x0b' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\x7f' => out.push_str(&format!("\\x{:02x}", c as u32)),
            c if (c as u32) < 0x10000 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push_str(&format!("\\U{:08x}", c as u32)),
        }
    }
    out.push('"');
    out
}

fn is_print(c: char) -> bool {
    if c.is_ascii() {
        return (' '..='~').contains(&c);
    }
    let format_char = matches!(c as u32, 0xad | 0x200b..=0x200f | 0x202a..=0x202e | 0x2060..=0x2064 | 0xfeff | 0xfff9..=0xfffb);
    !c.is_control() && !c.is_whitespace() && !format_char
}

const NANOSECOND: u64 = 1;
const MICROSECOND: u64 = 1000 * NANOSECOND;
const MILLISECOND: u64 = 1000 * MICROSECOND;
const SECOND: u64 = 1000 * MILLISECOND;
const MINUTE: u64 = 60 * SECOND;
const HOUR: u64 = 60 * MINUTE;

fn unit(u: &str) -> Option<u64> {
    Some(match u {
        "ns" => NANOSECOND,
        "us" | "\u{b5}s" | "\u{3bc}s" => MICROSECOND,
        "ms" => MILLISECOND,
        "s" => SECOND,
        "m" => MINUTE,
        "h" => HOUR,
        _ => return None,
    })
}

/// Go's `time.ParseDuration`: a signed sequence of decimal numbers, each with an optional
/// fraction and a unit (`ns`, `us`/`µs`, `ms`, `s`, `m`, `h`), such as `300ms` or `-1.5h`.
/// The error text matches Go's.
pub fn parse_duration(orig: &str) -> Result<GoDuration, String> {
    let invalid = || format!("time: invalid duration {}", quote(orig));
    let mut s = orig.as_bytes();
    let mut neg = false;
    if let Some(&c @ (b'-' | b'+')) = s.first() {
        neg = c == b'-';
        s = &s[1..];
    }
    if s == b"0" {
        return Ok(GoDuration(0));
    }
    if s.is_empty() {
        return Err(invalid());
    }
    let mut d: u64 = 0;
    while !s.is_empty() {
        if !(s[0] == b'.' || s[0].is_ascii_digit()) {
            return Err(invalid());
        }
        let before = s.len();
        let (v, rest) = leading_int(s).ok_or_else(invalid)?;
        s = rest;
        let pre = before != s.len();
        let (mut f, mut scale, mut post) = (0u64, 1.0f64, false);
        if s.first() == Some(&b'.') {
            s = &s[1..];
            let before = s.len();
            (f, scale, s) = leading_fraction(s);
            post = before != s.len();
        }
        if !pre && !post {
            return Err(invalid());
        }
        let i = s.iter().position(|&c| c == b'.' || c.is_ascii_digit()).unwrap_or(s.len());
        if i == 0 {
            return Err(format!("time: missing unit in duration {}", quote(orig)));
        }
        let u = String::from_utf8_lossy(&s[..i]).into_owned();
        s = &s[i..];
        let Some(unit) = unit(&u) else {
            return Err(format!("time: unknown unit {} in duration {}", quote(&u), quote(orig)));
        };
        let mut v = v;
        if v > (1u64 << 63) / unit {
            return Err(invalid());
        }
        v *= unit;
        if f > 0 {
            // Go adds the fraction in float64 and truncates, so fractional nanoseconds drop.
            v += (f as f64 * (unit as f64 / scale)) as u64;
            if v > 1 << 63 {
                return Err(invalid());
            }
        }
        d += v;
        if d > 1 << 63 {
            return Err(invalid());
        }
    }
    if neg {
        return Ok(GoDuration(0i64.wrapping_sub_unsigned(d)));
    }
    i64::try_from(d).map(GoDuration).map_err(|_| invalid())
}

fn leading_int(s: &[u8]) -> Option<(u64, &[u8])> {
    let mut x: u64 = 0;
    let mut i = 0;
    while i < s.len() && s[i].is_ascii_digit() {
        if x > (1 << 63) / 10 {
            return None;
        }
        x = x * 10 + u64::from(s[i] - b'0');
        if x > 1 << 63 {
            return None;
        }
        i += 1;
    }
    Some((x, &s[i..]))
}

fn leading_fraction(s: &[u8]) -> (u64, f64, &[u8]) {
    let (mut x, mut scale, mut overflow) = (0u64, 1.0f64, false);
    let mut i = 0;
    while i < s.len() && s[i].is_ascii_digit() {
        let c = s[i];
        i += 1;
        if overflow {
            continue;
        }
        if x > ((1u64 << 63) - 1) / 10 {
            overflow = true;
            continue;
        }
        let y = x * 10 + u64::from(c - b'0');
        if y > 1 << 63 {
            overflow = true;
            continue;
        }
        x = y;
        scale *= 10.0;
    }
    (x, scale, &s[i..])
}

/// Go's `time.Duration.String`, such as `1h2m0.5s`, `1.5ms` or `0s`.
pub fn format_duration(d: GoDuration) -> String {
    let neg = d.0 < 0;
    let mut u = d.0.unsigned_abs();
    let mut out = String::new();
    if u < SECOND {
        let (prec, unit) = match u {
            0 => return "0s".into(),
            u if u < MICROSECOND => (0, "ns"),
            u if u < MILLISECOND => (3, "\u{b5}s"),
            _ => (6, "ms"),
        };
        let frac;
        (frac, u) = fraction(u, prec);
        out.push_str(&format!("{u}{frac}{unit}"));
    } else {
        let frac;
        (frac, u) = fraction(u, 9);
        let secs = format!("{}{frac}s", u % 60);
        u /= 60;
        if u > 0 {
            let mins = u % 60;
            u /= 60;
            if u > 0 {
                out.push_str(&format!("{u}h"));
            }
            out.push_str(&format!("{mins}m"));
        }
        out.push_str(&secs);
    }
    if neg {
        out.insert(0, '-');
    }
    out
}

/// Go's `fmtFrac`: the low `prec` digits of `v` as `.ddd` without trailing zeros, and `v` with
/// them removed.
fn fraction(mut v: u64, prec: u32) -> (String, u64) {
    let mut digits = Vec::new();
    let mut print = false;
    for _ in 0..prec {
        let digit = v % 10;
        print = print || digit != 0;
        if print {
            digits.push(char::from(b'0' + digit as u8));
        }
        v /= 10;
    }
    if !print {
        return (String::new(), v);
    }
    digits.push('.');
    (digits.into_iter().rev().collect(), v)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_bool_accepts_gos_spellings() {
        for s in ["1", "t", "T", "TRUE", "true", "True"] {
            assert_eq!(parse_bool(s), Some(true), "{s}");
        }
        for s in ["0", "f", "F", "FALSE", "false", "False"] {
            assert_eq!(parse_bool(s), Some(false), "{s}");
        }
        for s in ["", "yes", "tRUE", "2", "x"] {
            assert_eq!(parse_bool(s), None, "{s}");
        }
    }

    // Cases from Go's strconv atoi_test.go (parseInt64BaseTests, base 0).
    #[test]
    fn parse_int_base_zero_matches_go() {
        let ok: &[(&str, i64)] = &[
            ("0", 0),
            ("-0", 0),
            ("1", 1),
            ("-1", -1),
            ("12345", 12345),
            ("012345", 0o12345),
            ("0x1f", 0x1f),
            ("-0X1F", -0x1f),
            ("0b101", 5),
            ("0o17", 15),
            ("0x_1_2_3", 0x123),
            ("1_000", 1000),
            ("9223372036854775807", i64::MAX),
            ("-9223372036854775808", i64::MIN),
        ];
        for &(s, want) in ok {
            assert_eq!(parse_int(s), Ok(want), "{s}");
        }
        let bad: &[(&str, NumError)] = &[
            ("", NumError::Syntax),
            ("x", NumError::Syntax),
            ("0x", NumError::Syntax),
            ("08", NumError::Syntax),
            ("1__2", NumError::Syntax),
            ("_12", NumError::Syntax),
            ("12_", NumError::Syntax),
            ("9223372036854775808", NumError::Range),
            ("-9223372036854775809", NumError::Range),
            ("123456789012345678901", NumError::Range),
        ];
        for &(s, want) in bad {
            assert_eq!(parse_int(s), Err(want), "{s}");
        }
    }

    #[test]
    fn parse_float_matches_go() {
        assert_eq!(parse_float("2718e28"), Ok(2718e28));
        assert_eq!(parse_float("-1.5"), Ok(-1.5));
        assert_eq!(parse_float("+Inf"), Ok(f64::INFINITY));
        assert_eq!(parse_float("-infinity"), Ok(f64::NEG_INFINITY));
        assert!(parse_float("NaN").is_ok_and(f64::is_nan));
        assert_eq!(parse_float("+nan"), Err(NumError::Syntax));
        assert_eq!(parse_float("1e1000"), Err(NumError::Range));
        assert_eq!(parse_float("x"), Err(NumError::Syntax));
        assert_eq!(parse_float(""), Err(NumError::Syntax));
    }

    // Expected strings are Go's strconv.FormatFloat(f, 'g', -1, 64).
    #[test]
    fn format_float_matches_go() {
        let cases: &[(f64, &str)] = &[
            (0.0, "0"),
            (-0.0, "-0"),
            (1.0, "1"),
            (2.7, "2.7"),
            (0.5, "0.5"),
            (100000.0, "100000"),
            (1e6, "1e+06"),
            (123456789.0, "1.23456789e+08"),
            (0.0001, "0.0001"),
            (0.00001, "1e-05"),
            (2718e28, "2.718e+31"),
            (-3.25, "-3.25"),
            (f64::INFINITY, "+Inf"),
            (f64::NEG_INFINITY, "-Inf"),
            (f64::NAN, "NaN"),
        ];
        for &(f, want) in cases {
            assert_eq!(format_float(f), want, "{f}");
        }
    }

    #[test]
    fn quote_matches_go() {
        assert_eq!(quote("0"), r#""0""#);
        assert_eq!(quote(""), r#""""#);
        assert_eq!(quote("a\"b\\c"), r#""a\"b\\c""#);
        assert_eq!(quote("\n\t\x07\x01\x7f"), r#""\n\t\a\x01\x7f""#);
        assert_eq!(quote("héllo ☺"), "\"héllo ☺\"");
        assert_eq!(quote("\u{a0}\u{200b}\u{85}"), r#""\u00a0\u200b\u0085""#);
    }

    // Cases from Go's time_test.go parseDurationTests.
    #[test]
    fn parse_duration_matches_go() {
        const NS: i64 = 1;
        const US: i64 = 1000;
        const MS: i64 = 1_000_000;
        const S: i64 = 1_000_000_000;
        const M: i64 = 60 * S;
        const H: i64 = 60 * M;
        let ok: &[(&str, i64)] = &[
            ("0", 0),
            ("5s", 5 * S),
            ("30s", 30 * S),
            ("1478s", 1478 * S),
            ("-5s", -5 * S),
            ("+5s", 5 * S),
            ("-0", 0),
            ("+0", 0),
            ("5.0s", 5 * S),
            ("5.6s", 5 * S + 600 * MS),
            ("5.s", 5 * S),
            (".5s", 500 * MS),
            ("1.0s", S),
            ("1.00s", S),
            ("1.004s", S + 4 * MS),
            ("1.0040s", S + 4 * MS),
            ("100.00100s", 100 * S + MS),
            ("10ns", 10 * NS),
            ("11us", 11 * US),
            ("12µs", 12 * US),
            ("12μs", 12 * US),
            ("13ms", 13 * MS),
            ("14s", 14 * S),
            ("15m", 15 * M),
            ("16h", 16 * H),
            ("3h30m", 3 * H + 30 * M),
            ("10.5s4m", 4 * M + 10 * S + 500 * MS),
            ("-2m3.4s", -(2 * M + 3 * S + 400 * MS)),
            ("1h2m3s4ms5us6ns", H + 2 * M + 3 * S + 4 * MS + 5 * US + 6 * NS),
            ("39h9m14.425s", 39 * H + 9 * M + 14 * S + 425 * MS),
            ("52763797000ns", 52763797000 * NS),
            ("0.3333333333333333333h", 20 * M),
            ("9007199254740993ns", (1 << 53) + 1),
            ("9223372036854775807ns", i64::MAX),
            ("9223372036854775.807us", i64::MAX),
            ("9223372036s854ms775us807ns", i64::MAX),
            ("-9223372036854775808ns", i64::MIN),
            ("-9223372036854775.808us", i64::MIN),
            ("-9223372036s854ms775us808ns", i64::MIN),
            ("-9223372036854775808ns", -1 << 63),
            ("-2562047h47m16.854775808s", -1 << 63),
            ("0.100000000000000000000h", 6 * M),
            ("0.830103483285477580700h", 49 * M + 48 * S + 372539827 * NS),
        ];
        for &(s, want) in ok {
            assert_eq!(parse_duration(s), Ok(GoDuration(want)), "{s}");
        }
        for s in
            ["", "3", "-", "s", ".", "-.", ".s", "+.s", "1d", "\u{85}\u{85}", "\u{fffd}", "9223372036854775808ns", "-9223372036854775809ns"]
        {
            assert!(parse_duration(s).is_err(), "{s:?} parsed");
        }
        assert_eq!(parse_duration("3").expect_err("3"), "time: missing unit in duration \"3\"");
        assert_eq!(parse_duration("1d").expect_err("1d"), "time: unknown unit \"d\" in duration \"1d\"");
        assert_eq!(parse_duration("x").expect_err("x"), "time: invalid duration \"x\"");
    }

    // Cases from Go's time_test.go durationTests.
    #[test]
    fn format_duration_matches_go() {
        const US: i64 = 1000;
        const MS: i64 = 1_000_000;
        const S: i64 = 1_000_000_000;
        const M: i64 = 60 * S;
        const H: i64 = 60 * M;
        let cases: &[(&str, i64)] = &[
            ("0s", 0),
            ("1ns", 1),
            ("1.1µs", 1100),
            ("2.2ms", 2200 * US),
            ("3.3s", 3300 * MS),
            ("4m5s", 4 * M + 5 * S),
            ("4m5.001s", 4 * M + 5001 * MS),
            ("5h6m7.001s", 5 * H + 6 * M + 7001 * MS),
            ("8m0.000000001s", 8 * M + 1),
            ("2562047h47m16.854775807s", i64::MAX),
            ("-2562047h47m16.854775808s", i64::MIN),
            ("5s", 5 * S),
            ("1h0m0s", H),
        ];
        for &(want, d) in cases {
            assert_eq!(format_duration(GoDuration(d)), want, "{d}");
        }
    }
}
