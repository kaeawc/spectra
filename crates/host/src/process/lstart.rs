//! `ps -o lstart` as Go's `time.ParseInLocation("Mon Jan 2 15:04:05 2006", s, time.UTC)` reads
//! it: `ps` runs with `TZ=UTC0 LC_ALL=C`, so the text is an English UTC wall clock.

use jiff::Timestamp;
use jiff::civil::DateTime;

const DAYS: [&str; 7] = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS: [&str; 12] = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/// The instant `s` names, or `None` where Go's parse fails. The weekday is checked for syntax
/// only, as in Go.
pub fn parse_lstart(s: &str) -> Option<Timestamp> {
    let mut v = s.as_bytes();
    lookup(&mut v, &DAYS)?;
    space(&mut v)?;
    let month = lookup(&mut v, &MONTHS)? + 1;
    space(&mut v)?;
    let day = getnum(&mut v, false)?;
    space(&mut v)?;
    let hour = getnum(&mut v, false)?;
    literal(&mut v, b':')?;
    let minute = getnum(&mut v, true)?;
    literal(&mut v, b':')?;
    let second = getnum(&mut v, true)?;
    space(&mut v)?;
    if v.len() < 4 || !v[0].is_ascii_digit() || !v[1..4].iter().all(u8::is_ascii_digit) {
        return None;
    }
    let year = v[..4].iter().fold(0i16, |n, d| n * 10 + i16::from(d - b'0'));
    v = &v[4..];
    if !v.is_empty() || hour >= 24 || minute >= 60 || second >= 60 {
        return None;
    }
    // Go checks the day against the month's length ("day out of range").
    let dt = DateTime::new(year, month as i8, day as i8, hour as i8, minute as i8, second as i8, 0).ok()?;
    if day < 1 {
        return None;
    }
    jiff::tz::Offset::UTC.to_timestamp(dt).ok()
}

/// Go's `lookup`: an ASCII case-insensitive three-letter name.
fn lookup(v: &mut &[u8], names: &[&str]) -> Option<usize> {
    for (i, n) in names.iter().enumerate() {
        if v.len() >= n.len() && v[..n.len()].eq_ignore_ascii_case(n.as_bytes()) {
            *v = &v[n.len()..];
            return Some(i);
        }
    }
    None
}

/// Go's `skip` for a layout space: at least one space, then any more.
fn space(v: &mut &[u8]) -> Option<()> {
    if v.first() != Some(&b' ') {
        return None;
    }
    while v.first() == Some(&b' ') {
        *v = &v[1..];
    }
    Some(())
}

fn literal(v: &mut &[u8], c: u8) -> Option<()> {
    if v.first() != Some(&c) {
        return None;
    }
    *v = &v[1..];
    Some(())
}

/// Go's `getnum`: one or two digits, exactly two when `fixed`.
fn getnum(v: &mut &[u8], fixed: bool) -> Option<u32> {
    let d0 = *v.first().filter(|c| c.is_ascii_digit())?;
    match v.get(1).filter(|c| c.is_ascii_digit()) {
        Some(&d1) => {
            *v = &v[2..];
            Some(u32::from(d0 - b'0') * 10 + u32::from(d1 - b'0'))
        }
        None if fixed => None,
        None => {
            *v = &v[1..];
            Some(u32::from(d0 - b'0'))
        }
    }
}
