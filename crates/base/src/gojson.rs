//! JSON written exactly as Go's `encoding/json` writes it.
//!
//! Spectra's JSON contract (docs/reference/result-schema.md) is read by name by outside tools,
//! and the Rust port must print the same bytes as the Go build. `serde_json` differs from Go in
//! a few places; this module closes each one:
//!
//! - **Escaping.** Go escapes `<`, `>` and `&` as `\u003c`, `\u003e` and `\u0026`, and U+2028
//!   and U+2029 as `\u2028` and `\u2029`, in keys and values. [`GoFormatter`] does the same.
//! - **Floats.** Go prints the shortest round-trip digits, with no `.0` on whole numbers, and
//!   switches to exponent form only below 1e-6 or from 1e21 (`1e-7`, `1e+21`). [`GoFormatter`]
//!   lays out `ryu`'s digits by Go's rules. NaN and infinities differ: `serde_json` writes
//!   `null` before the formatter sees them, where Go fails the whole `Marshal`. Collectors must
//!   not produce them (the Go build would print an error instead of a report).
//! - **`omitempty`.** `#[serde(skip_serializing_if = "gojson::is_empty")]` omits what Go omits:
//!   0, false, "", empty `Vec` and map, and `None` (a nil pointer). Go never omits a struct, so
//!   structs don't implement [`GoEmpty`]. For a slice or map field that Go can leave nil, use
//!   `Option<Vec<T>>` (`None` writes `null`) and [`nil_or_empty`].
//! - **Maps.** Go sorts map keys as strings. Use `BTreeMap<String, _>` for every map that reaches
//!   output; integer keys must be stringified first, because Go sorts `"10"` before `"9"`.
//! - **Time.** [`GoTime`] writes RFC 3339 with nanoseconds and trailing zeros trimmed, and `Z`
//!   for a zero offset; [`GoDuration`] writes integer nanoseconds.
//! - **Layout.** [`to_vec`] is `json.Marshal`, [`to_vec_indent`] is `json.MarshalIndent(v, "",
//!   "  ")`, and [`to_writer_encoder`] is `json.NewEncoder(w).Encode(v)` (trailing newline).
//!
//! `crates/base/testdata/gojson-vectors.json` holds thousands of values written by Go
//! (`internal/gojsonvectors`); `tests/gojson_vectors.rs` checks this module against every one.

use std::collections::{BTreeMap, HashMap};
use std::fmt;
use std::io;

use serde::{Serialize, Serializer};
use serde_json::ser::{CompactFormatter, Formatter, PrettyFormatter};

pub use serde_json::Error;

/// `json.Marshal(v)`.
pub fn to_vec<T: Serialize + ?Sized>(value: &T) -> Result<Vec<u8>, Error> {
    let mut out = Vec::with_capacity(128);
    to_writer(&mut out, value, Layout::Compact)?;
    Ok(out)
}

/// `json.MarshalIndent(v, "", "  ")`.
pub fn to_vec_indent<T: Serialize + ?Sized>(value: &T) -> Result<Vec<u8>, Error> {
    let mut out = Vec::with_capacity(256);
    to_writer(&mut out, value, Layout::Indent)?;
    Ok(out)
}

/// `json.NewEncoder(w).Encode(v)`, with `SetIndent("", "  ")` when `layout` is
/// [`Layout::Indent`]: the value followed by a newline.
pub fn to_writer_encoder<W: io::Write, T: Serialize + ?Sized>(mut writer: W, value: &T, layout: Layout) -> Result<(), Error> {
    to_writer(&mut writer, value, layout)?;
    writer.write_all(b"\n").map_err(Error::io)
}

/// Go's `json.Encoder` with its settings: `Encoder::default()` is `json.NewEncoder(w)`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Encoder {
    /// `SetIndent("", "  ")`.
    pub indent: bool,
    /// `SetEscapeHTML`; true by default, as in Go.
    pub escape_html: bool,
}

impl Default for Encoder {
    fn default() -> Self {
        Encoder { indent: false, escape_html: true }
    }
}

impl Encoder {
    /// `enc.Encode(v)`: the value followed by a newline.
    pub fn encode<W: io::Write, T: Serialize + ?Sized>(&self, mut writer: W, value: &T) -> Result<(), Error> {
        let layout = if self.indent { Layout::Indent } else { Layout::Compact };
        let mut ser = serde_json::Serializer::with_formatter(&mut writer, GoFormatter::with_escape_html(layout, self.escape_html));
        value.serialize(&mut ser)?;
        writer.write_all(b"\n").map_err(Error::io)
    }

    /// [`Encoder::encode`] into a new buffer.
    pub fn encode_to_vec<T: Serialize + ?Sized>(&self, value: &T) -> Result<Vec<u8>, Error> {
        let mut out = Vec::with_capacity(256);
        self.encode(&mut out, value)?;
        Ok(out)
    }
}

/// Writes `value` with Go's escaping and number formatting.
pub fn to_writer<W: io::Write, T: Serialize + ?Sized>(writer: W, value: &T, layout: Layout) -> Result<(), Error> {
    let mut ser = serde_json::Serializer::with_formatter(writer, GoFormatter::new(layout));
    value.serialize(&mut ser)
}

/// Compact (`Marshal`) or two-space indented (`MarshalIndent(v, "", "  ")`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Layout {
    Compact,
    Indent,
}

/// A `serde_json` formatter that writes strings and floats as Go's `encoding/json` does.
pub struct GoFormatter {
    inner: Inner,
    escape_html: bool,
}

enum Inner {
    Compact(CompactFormatter),
    Pretty(PrettyFormatter<'static>),
}

impl GoFormatter {
    pub fn new(layout: Layout) -> Self {
        GoFormatter::with_escape_html(layout, true)
    }

    /// `escape_html: false` is Go's `Encoder.SetEscapeHTML(false)`: `<`, `>` and `&` are written
    /// as is. U+2028 and U+2029 are escaped either way, as in Go.
    pub fn with_escape_html(layout: Layout, escape_html: bool) -> Self {
        let inner = match layout {
            Layout::Compact => Inner::Compact(CompactFormatter),
            Layout::Indent => Inner::Pretty(PrettyFormatter::with_indent(b"  ")),
        };
        GoFormatter { inner, escape_html }
    }
}

macro_rules! delegate {
    ($($name:ident($($arg:ident: $ty:ty),*);)*) => {
        $(
            fn $name<W: ?Sized + io::Write>(&mut self, writer: &mut W $(, $arg: $ty)*) -> io::Result<()> {
                match &mut self.inner {
                    Inner::Compact(f) => f.$name(writer $(, $arg)*),
                    Inner::Pretty(f) => f.$name(writer $(, $arg)*),
                }
            }
        )*
    };
}

impl Formatter for GoFormatter {
    delegate! {
        begin_array();
        end_array();
        begin_array_value(first: bool);
        end_array_value();
        begin_object();
        end_object();
        begin_object_key(first: bool);
        end_object_key();
        begin_object_value();
        end_object_value();
    }

    fn write_f64<W: ?Sized + io::Write>(&mut self, writer: &mut W, value: f64) -> io::Result<()> {
        let mut buf = ryu::Buffer::new();
        let abs = value.abs();
        let exp = abs != 0.0 && !(1e-6..1e21).contains(&abs);
        writer.write_all(go_float(buf.format_finite(value), exp).as_bytes())
    }

    fn write_f32<W: ?Sized + io::Write>(&mut self, writer: &mut W, value: f32) -> io::Result<()> {
        let mut buf = ryu::Buffer::new();
        let abs = value.abs();
        let exp = abs != 0.0 && !(1e-6f32..1e21f32).contains(&abs);
        writer.write_all(go_float(buf.format_finite(value), exp).as_bytes())
    }

    fn write_string_fragment<W: ?Sized + io::Write>(&mut self, writer: &mut W, fragment: &str) -> io::Result<()> {
        let mut start = 0;
        for (i, c) in fragment.char_indices() {
            let escape: &[u8] = match c {
                '<' if self.escape_html => b"\\u003c",
                '>' if self.escape_html => b"\\u003e",
                '&' if self.escape_html => b"\\u0026",
                '\u{2028}' => b"\\u2028",
                '\u{2029}' => b"\\u2029",
                _ => continue,
            };
            writer.write_all(&fragment.as_bytes()[start..i])?;
            writer.write_all(escape)?;
            start = i + c.len_utf8();
        }
        writer.write_all(&fragment.as_bytes()[start..])
    }

    /// Go writes a `json.RawMessage` through `appendCompact` with HTML escaping: whitespace
    /// between tokens dropped, and `<`, `>`, `&`, U+2028 and U+2029 (which valid JSON holds only
    /// inside strings) escaped as in [`write_string_fragment`](Self::write_string_fragment).
    /// Under [`Layout::Indent`] Go would also re-indent the value; it stays compact here.
    fn write_raw_fragment<W: ?Sized + io::Write>(&mut self, writer: &mut W, fragment: &str) -> io::Result<()> {
        let mut compact = String::with_capacity(fragment.len());
        let (mut in_string, mut escaped) = (false, false);
        for c in fragment.chars() {
            if in_string {
                in_string = escaped || c != '"';
                escaped = !escaped && c == '\\';
            } else if matches!(c, ' ' | '\t' | '\n' | '\r') {
                continue;
            } else {
                in_string = c == '"';
            }
            compact.push(c);
        }
        self.write_string_fragment(writer, &compact)
    }
}

/// Lays out the shortest round-trip digits of a finite float the way Go's
/// `strconv.AppendFloat(f, 'f' or 'e', -1, bits)` does, then applies `encoding/json`'s cleanup
/// of a two-digit negative exponent (`e-07` becomes `e-7`).
fn go_float(ryu_text: &str, exponent_form: bool) -> String {
    let (negative, digits, point) = decimal_digits(ryu_text);
    let mut out = String::with_capacity(digits.len() + 8);
    if negative {
        out.push('-');
    }
    if digits.is_empty() {
        out.push('0');
        return out;
    }
    if exponent_form {
        push_exponent_form(&mut out, &digits, point);
    } else {
        push_fixed_form(&mut out, &digits, point);
    }
    out
}

/// Splits ryu's output (`123.45`, `1.0`, `1e21`, `1.5e-7`, `-0.0`) into the sign, the
/// significant digits with no leading or trailing zeros, and the position of the decimal point
/// relative to the first digit (value = 0.DIGITS × 10^point).
fn decimal_digits(text: &str) -> (bool, Vec<u8>, i32) {
    let (negative, text) = match text.strip_prefix('-') {
        Some(rest) => (true, rest),
        None => (false, text),
    };
    let (mantissa, exp) = match text.split_once(['e', 'E']) {
        Some((m, e)) => (m, e.parse::<i32>().unwrap_or(0)),
        None => (text, 0),
    };
    let (int_part, frac_part) = mantissa.split_once('.').unwrap_or((mantissa, ""));
    let mut digits: Vec<u8> = int_part.bytes().chain(frac_part.bytes()).collect();
    let mut point = int_part.len() as i32 + exp;
    let leading = digits.iter().take_while(|&&d| d == b'0').count();
    digits.drain(..leading);
    point -= leading as i32;
    while digits.last() == Some(&b'0') {
        digits.pop();
    }
    (negative, digits, point)
}

fn push_fixed_form(out: &mut String, digits: &[u8], point: i32) {
    let digits = std::str::from_utf8(digits).unwrap_or_default();
    if point <= 0 {
        out.push_str("0.");
        out.extend(std::iter::repeat_n('0', point.unsigned_abs() as usize));
        out.push_str(digits);
    } else if point as usize >= digits.len() {
        out.push_str(digits);
        out.extend(std::iter::repeat_n('0', point as usize - digits.len()));
    } else {
        let (int, frac) = digits.split_at(point as usize);
        out.push_str(int);
        out.push('.');
        out.push_str(frac);
    }
}

fn push_exponent_form(out: &mut String, digits: &[u8], point: i32) {
    let digits = std::str::from_utf8(digits).unwrap_or_default();
    let (first, rest) = digits.split_at(1);
    out.push_str(first);
    if !rest.is_empty() {
        out.push('.');
        out.push_str(rest);
    }
    let exp = point - 1;
    out.push('e');
    out.push(if exp < 0 { '-' } else { '+' });
    // strconv writes at least two exponent digits; encoding/json then trims `e-0X` to `e-X`
    // (but leaves positive exponents, which are always ≥ 21 here, alone).
    let magnitude = exp.unsigned_abs();
    if magnitude < 10 && exp >= 0 {
        out.push('0');
    }
    out.push_str(&magnitude.to_string());
}

/// What Go's `omitempty` treats as empty. Not implemented for structs: Go never omits them.
pub trait GoEmpty {
    fn go_empty(&self) -> bool;
}

/// `#[serde(skip_serializing_if = "spectra_base::gojson::is_empty")]` for an `omitempty` field.
pub fn is_empty<T: GoEmpty + ?Sized>(value: &T) -> bool {
    value.go_empty()
}

/// `omitempty` on a Go slice or map that can be nil, modelled as `Option<Vec<T>>` or
/// `Option<BTreeMap<..>>`: omitted when nil or empty. Without `omitempty`, `None` writes `null`.
pub fn nil_or_empty<T: GoEmpty>(value: &Option<T>) -> bool {
    value.as_ref().is_none_or(GoEmpty::go_empty)
}

macro_rules! go_empty_zero {
    ($($t:ty),*) => {
        $(impl GoEmpty for $t {
            fn go_empty(&self) -> bool {
                *self == (0 as $t)
            }
        })*
    };
}

go_empty_zero!(i8, i16, i32, i64, isize, u8, u16, u32, u64, usize, f32, f64);

impl GoEmpty for bool {
    fn go_empty(&self) -> bool {
        !*self
    }
}

impl GoEmpty for str {
    fn go_empty(&self) -> bool {
        self.is_empty()
    }
}

impl GoEmpty for String {
    fn go_empty(&self) -> bool {
        self.is_empty()
    }
}

impl<T> GoEmpty for [T] {
    fn go_empty(&self) -> bool {
        self.is_empty()
    }
}

impl<T> GoEmpty for Vec<T> {
    fn go_empty(&self) -> bool {
        self.is_empty()
    }
}

impl<K, V> GoEmpty for BTreeMap<K, V> {
    fn go_empty(&self) -> bool {
        self.is_empty()
    }
}

impl<K, V, S> GoEmpty for HashMap<K, V, S> {
    fn go_empty(&self) -> bool {
        self.is_empty()
    }
}

/// A Go pointer: `omitempty` omits only nil, even when the pointee is a zero value.
impl<T> GoEmpty for Option<T> {
    fn go_empty(&self) -> bool {
        self.is_none()
    }
}

impl<T: GoEmpty + ?Sized> GoEmpty for &T {
    fn go_empty(&self) -> bool {
        (**self).go_empty()
    }
}

/// A Go `time.Time`: an instant plus the UTC offset it is shown in. Serializes as Go's
/// `Time.MarshalJSON` does: RFC 3339 with nanoseconds, trailing zeros trimmed, `Z` for a zero
/// offset. The default value is Go's zero time, `0001-01-01T00:00:00Z`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct GoTime {
    pub instant: jiff::Timestamp,
    pub offset: jiff::tz::Offset,
}

/// Seconds from the Unix epoch to Go's zero time, January 1, year 1, 00:00:00 UTC.
const GO_ZERO_UNIX_SECONDS: i64 = -62_135_596_800;

impl GoTime {
    /// Go's zero `time.Time`.
    pub fn zero() -> GoTime {
        let instant = jiff::Timestamp::new(GO_ZERO_UNIX_SECONDS, 0).unwrap_or(jiff::Timestamp::UNIX_EPOCH);
        GoTime { instant, offset: jiff::tz::Offset::UTC }
    }

    /// `instant` shown in UTC, like Go's `t.UTC()`.
    pub fn utc(instant: jiff::Timestamp) -> GoTime {
        GoTime { instant, offset: jiff::tz::Offset::UTC }
    }

    /// Go's `t.IsZero()`; `#[serde(skip_serializing_if = "GoTime::is_zero")]` for `omitzero`.
    pub fn is_zero(&self) -> bool {
        self.instant == GoTime::zero().instant
    }
}

impl Default for GoTime {
    fn default() -> Self {
        GoTime::zero()
    }
}

impl fmt::Display for GoTime {
    /// Go's `time.RFC3339Nano` layout.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let dt = self.offset.to_datetime(self.instant);
        write!(f, "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}", dt.year(), dt.month(), dt.day(), dt.hour(), dt.minute(), dt.second())?;
        let nanos = dt.subsec_nanosecond();
        if nanos != 0 {
            let frac = format!("{nanos:09}");
            write!(f, ".{}", frac.trim_end_matches('0'))?;
        }
        let secs = self.offset.seconds();
        if secs == 0 {
            return f.write_str("Z");
        }
        let sign = if secs < 0 { '-' } else { '+' };
        let mins = secs.unsigned_abs() / 60;
        write!(f, "{sign}{:02}:{:02}", mins / 60, mins % 60)
    }
}

impl Serialize for GoTime {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let year = self.offset.to_datetime(self.instant).year();
        if !(0..=9999).contains(&year) {
            // Go: "Time.MarshalJSON: year outside of range [0,9999]".
            return Err(serde::ser::Error::custom("Time.MarshalJSON: year outside of range [0,9999]"));
        }
        serializer.collect_str(self)
    }
}

/// A Go `time.Duration`: serializes as integer nanoseconds.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct GoDuration(pub i64);

impl GoDuration {
    /// Saturates at `i64::MAX` nanoseconds (about 292 years), as Go's `Duration` does.
    pub fn from_std(d: std::time::Duration) -> GoDuration {
        GoDuration(i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
    }
}

impl Serialize for GoDuration {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_i64(self.0)
    }
}

impl GoEmpty for GoDuration {
    fn go_empty(&self) -> bool {
        self.0 == 0
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn f64_text(v: f64) -> String {
        String::from_utf8(to_vec(&v).expect("finite")).expect("utf8")
    }

    #[test]
    fn floats_follow_go_layout() {
        assert_eq!(f64_text(3.0), "3");
        assert_eq!(f64_text(-0.0), "-0");
        assert_eq!(f64_text(0.1), "0.1");
        assert_eq!(f64_text(1e-6), "0.000001");
        assert_eq!(f64_text(1e-7), "1e-7");
        assert_eq!(f64_text(1.5e-10), "1.5e-10");
        assert_eq!(f64_text(1e20), "100000000000000000000");
        assert_eq!(f64_text(1e21), "1e+21");
        assert_eq!(f64_text(1.7976931348623157e308), "1.7976931348623157e+308");
    }

    #[test]
    fn non_finite_floats_write_null() {
        // serde_json writes these before the formatter sees them; Go fails the whole Marshal.
        assert_eq!(to_vec(&f64::NAN).expect("nan"), b"null");
        assert_eq!(to_vec(&f32::NEG_INFINITY).expect("inf"), b"null");
    }

    #[test]
    fn html_and_line_separators_are_escaped() {
        let out = to_vec("<a href=\"x\">&\u{2028}").expect("string");
        let want = r#""\u003ca href=\"x\"\u003e\u0026\u2028""#;
        assert_eq!(String::from_utf8(out).expect("utf8"), want);
    }

    #[test]
    fn raw_values_are_compacted_and_escaped() {
        let raw = serde_json::value::RawValue::from_string("{ \"t\" : \"<a> & \\\" \u{2029}\",\n\"n\": 1.50 }".into()).expect("raw");
        let out = to_vec(&raw).expect("raw");
        assert_eq!(String::from_utf8(out).expect("utf8"), r#"{"t":"\u003ca\u003e \u0026 \" \u2029","n":1.50}"#);
    }

    #[test]
    fn go_zero_time() {
        assert_eq!(GoTime::zero().to_string(), "0001-01-01T00:00:00Z");
        assert!(GoTime::default().is_zero());
    }

    #[test]
    fn omitempty_pointer_keeps_zero_pointee() {
        assert!(is_empty(&None::<f64>));
        assert!(!is_empty(&Some(0.0)));
        assert!(nil_or_empty(&Some(Vec::<u8>::new())));
        assert!(!nil_or_empty(&Some(vec![1])));
    }

    #[test]
    fn encoder_appends_newline() {
        let mut out = Vec::new();
        to_writer_encoder(&mut out, &[1, 2], Layout::Compact).expect("encode");
        assert_eq!(out, b"[1,2]\n");
    }
}
