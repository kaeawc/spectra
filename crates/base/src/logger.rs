//! Structured logging (Go's `internal/logger`, a small interface over `log/slog`). Production
//! code logs to stderr as text or JSON through [`new`]; tests substitute [`Capture`] to assert on
//! the records that were emitted.
//!
//! Attributes are `(key, value)` pairs where the value is a [`serde_json::Value`]
//! (`("k", 42.into())`), the Rust counterpart of slog's alternating `args ...any`.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::io::Write;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};

pub use serde_json::Value;

use crate::clock::{Clock, SystemClock};

/// One `key, value` pair of a record.
pub type Attr<'a> = (&'a str, Value);

/// A record's severity, ordered like slog's levels (Debug < Info < Warn < Error).
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Level {
    Debug,
    Info,
    Warn,
    Error,
}

impl Level {
    /// slog's name for the level, as written in the `level` field.
    pub fn as_str(self) -> &'static str {
        match self {
            Level::Debug => "DEBUG",
            Level::Info => "INFO",
            Level::Warn => "WARN",
            Level::Error => "ERROR",
        }
    }
}

/// The structured-logging interface. `with` returns a child that prepends `attrs` to every
/// record.
pub trait Logger: Send + Sync {
    fn log(&self, level: Level, msg: &str, attrs: &[Attr]);
    /// Whether the logger accepts records at `level`.
    fn enabled(&self, level: Level) -> bool;
    fn with(&self, attrs: &[Attr]) -> Arc<dyn Logger>;

    fn debug(&self, msg: &str, attrs: &[Attr]) {
        self.log(Level::Debug, msg, attrs);
    }
    fn info(&self, msg: &str, attrs: &[Attr]) {
        self.log(Level::Info, msg, attrs);
    }
    fn warn(&self, msg: &str, attrs: &[Attr]) {
        self.log(Level::Warn, msg, attrs);
    }
    fn error(&self, msg: &str, attrs: &[Attr]) {
        self.log(Level::Error, msg, attrs);
    }
}

/// The on-wire representation.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Format {
    /// Human-readable `key=value` lines (slog's `TextHandler`). Go's zero value, so ours.
    #[default]
    Text,
    /// One JSON object per record (slog's `JSONHandler`).
    Json,
}

/// Controls [`new`]. The default writes text at info level to stderr, like Go's zero `Config`.
#[derive(Default)]
pub struct Config {
    /// Receives the encoded records; stderr when `None`.
    pub writer: Option<Box<dyn Write + Send>>,
    pub format: Format,
    pub level: Option<Level>,
    /// Stamps each record; the real clock when `None`.
    pub clock: Option<Arc<dyn Clock>>,
}

/// A logger writing to `cfg.writer` (stderr by default) in `cfg.format`.
pub fn new(cfg: Config) -> Arc<dyn Logger> {
    let writer: Box<dyn Write + Send> = cfg.writer.unwrap_or_else(|| Box::new(std::io::stderr()));
    let clock = cfg.clock.unwrap_or_else(|| Arc::new(SystemClock));
    let sink = Sink { writer: Mutex::new(writer), format: cfg.format, level: cfg.level.unwrap_or(Level::Info), clock };
    Arc::new(WriterLogger { sink: Arc::new(sink), with: Vec::new() })
}

/// A logger that drops everything.
pub fn discard() -> Arc<dyn Logger> {
    new(Config { writer: Some(Box::new(std::io::sink())), ..Config::default() })
}

fn lock<T: ?Sized>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

struct Sink {
    writer: Mutex<Box<dyn Write + Send>>,
    format: Format,
    level: Level,
    clock: Arc<dyn Clock>,
}

struct WriterLogger {
    sink: Arc<Sink>,
    with: Vec<(String, Value)>,
}

impl Logger for WriterLogger {
    fn log(&self, level: Level, msg: &str, attrs: &[Attr]) {
        if level < self.sink.level {
            return;
        }
        let time = self.sink.clock.now().to_string();
        let all = self.with.iter().map(|(k, v)| (k.as_str(), v)).chain(attrs.iter().map(|(k, v)| (*k, v)));
        let mut line = match self.sink.format {
            Format::Json => encode_json(&time, level, msg, all),
            Format::Text => encode_text(&time, level, msg, all),
        };
        line.push('\n');
        // A log write that fails has nowhere to be reported.
        let _ = lock(&self.sink.writer).write_all(line.as_bytes());
    }

    fn enabled(&self, level: Level) -> bool {
        level >= self.sink.level
    }

    fn with(&self, attrs: &[Attr]) -> Arc<dyn Logger> {
        Arc::new(WriterLogger { sink: Arc::clone(&self.sink), with: concat(&self.with, attrs) })
    }
}

fn concat(base: &[(String, Value)], attrs: &[Attr]) -> Vec<(String, Value)> {
    base.iter().cloned().chain(attrs.iter().map(|(k, v)| ((*k).to_string(), v.clone()))).collect()
}

fn encode_json<'a>(time: &str, level: Level, msg: &str, attrs: impl Iterator<Item = (&'a str, &'a Value)>) -> String {
    let mut out = String::from("{");
    out.push_str("\"time\":");
    json_string(&mut out, time);
    out.push_str(",\"level\":");
    json_string(&mut out, level.as_str());
    out.push_str(",\"msg\":");
    json_string(&mut out, msg);
    for (k, v) in attrs {
        out.push(',');
        json_string(&mut out, k);
        out.push(':');
        match v {
            Value::String(s) => json_string(&mut out, s),
            other => out.push_str(&other.to_string()),
        }
    }
    out.push('}');
    out
}

/// slog's JSON string escaping: `"`, `\`, `\n`, `\r`, `\t`, other control characters and U+2028/9
/// as `\u00XX`-style escapes, nothing HTML-specific.
fn json_string(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c < ' ' || c == '\u{2028}' || c == '\u{2029}' => {
                let _ = write!(out, "\\u{:04x}", c as u32);
            }
            c => out.push(c),
        }
    }
    out.push('"');
}

fn encode_text<'a>(time: &str, level: Level, msg: &str, attrs: impl Iterator<Item = (&'a str, &'a Value)>) -> String {
    let mut out = format!("time={time} level={} msg=", level.as_str());
    text_string(&mut out, msg);
    for (k, v) in attrs {
        out.push(' ');
        text_string(&mut out, k);
        out.push('=');
        match v {
            Value::String(s) => text_string(&mut out, s),
            Value::Null => out.push_str("<nil>"),
            Value::Array(_) | Value::Object(_) => text_string(&mut out, &v.to_string()),
            other => out.push_str(&other.to_string()),
        }
    }
    out
}

fn text_string(out: &mut String, s: &str) {
    if needs_quoting(s) {
        // serde_json's string form is Go's strconv.Quote for the characters that matter here.
        out.push_str(&Value::String(s.to_string()).to_string());
    } else {
        out.push_str(s);
    }
}

/// slog's `needsQuoting`: empty, or containing a space, `=`, `"`, a control character or any
/// non-printable or whitespace rune.
fn needs_quoting(s: &str) -> bool {
    s.is_empty()
        || s.chars().any(|c| {
            if c.is_ascii() {
                c != '\\' && (c == ' ' || c == '=' || c == '"' || c.is_ascii_control())
            } else {
                c.is_whitespace() || c.is_control() || c == '\u{fffd}'
            }
        })
}

/// A captured log entry.
#[derive(Clone, Debug, PartialEq)]
pub struct Record {
    pub level: Level,
    pub msg: String,
    pub attrs: BTreeMap<String, Value>,
}

/// A [`Logger`] that buffers records in memory for assertions (Go's `logger.Capture`). Safe for
/// concurrent use.
#[derive(Debug)]
pub struct Capture {
    level: Level,
    with: Vec<(String, Value)>,
    records: Mutex<Vec<Record>>,
}

impl Capture {
    /// Records every entry at `level` or above.
    pub fn new(level: Level) -> Self {
        Capture { level, with: Vec::new(), records: Mutex::new(Vec::new()) }
    }

    /// A snapshot of every captured record.
    pub fn records(&self) -> Vec<Record> {
        lock(&self.records).clone()
    }

    pub fn reset(&self) {
        lock(&self.records).clear();
    }

    /// Whether any captured record's message contains `substr`.
    pub fn has_message(&self, substr: &str) -> bool {
        lock(&self.records).iter().any(|r| r.msg.contains(substr))
    }

    /// The records at exactly `level`.
    pub fn filter_level(&self, level: Level) -> Vec<Record> {
        lock(&self.records).iter().filter(|r| r.level == level).cloned().collect()
    }

    /// The child [`Logger::with`] returns, as a concrete `Capture` so a test can read it. As in
    /// Go, the child keeps its own record buffer, not the parent's.
    pub fn child(&self, attrs: &[Attr]) -> Capture {
        Capture { level: self.level, with: concat(&self.with, attrs), records: Mutex::new(Vec::new()) }
    }
}

impl Logger for Capture {
    fn log(&self, level: Level, msg: &str, attrs: &[Attr]) {
        if level < self.level {
            return;
        }
        let mut map = BTreeMap::new();
        for (k, v) in self.with.iter().map(|(k, v)| (k.as_str(), v)).chain(attrs.iter().map(|(k, v)| (*k, v))) {
            map.insert(k.to_string(), v.clone());
        }
        lock(&self.records).push(Record { level, msg: msg.to_string(), attrs: map });
    }

    fn enabled(&self, level: Level) -> bool {
        level >= self.level
    }

    fn with(&self, attrs: &[Attr]) -> Arc<dyn Logger> {
        Arc::new(self.child(attrs))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::clock::FakeClock;
    use serde_json::json;

    #[derive(Clone, Default)]
    struct Buf(Arc<Mutex<Vec<u8>>>);

    impl Write for Buf {
        fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
            lock(&self.0).extend_from_slice(data);
            Ok(data.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }

    impl Buf {
        fn text(&self) -> String {
            String::from_utf8(lock(&self.0).clone()).expect("utf8")
        }
    }

    fn logger(buf: &Buf, format: Format, level: Option<Level>) -> Arc<dyn Logger> {
        let clock = Arc::new(FakeClock::new("2026-05-28T13:38:24Z".parse().expect("ts")));
        new(Config { writer: Some(Box::new(buf.clone())), format, level, clock: Some(clock) })
    }

    #[test]
    fn new_writes_json() {
        let buf = Buf::default();
        logger(&buf, Format::Json, Some(Level::Info)).info("hello", &[("k", 42.into())]);
        assert_eq!(buf.text(), "{\"time\":\"2026-05-28T13:38:24Z\",\"level\":\"INFO\",\"msg\":\"hello\",\"k\":42}\n");
        let rec: Value = serde_json::from_str(buf.text().trim()).expect("json");
        assert_eq!(rec["msg"], "hello");
        assert_eq!(rec["k"], json!(42));
    }

    #[test]
    fn new_text_format() {
        let buf = Buf::default();
        logger(&buf, Format::Text, None).info("started", &[("addr", ":8080".into())]);
        let out = buf.text();
        assert!(out.contains("msg=started"), "{out:?}");
        assert!(out.contains("addr=:8080"), "{out:?}");
        assert_eq!(out, "time=2026-05-28T13:38:24Z level=INFO msg=started addr=:8080\n");
    }

    #[test]
    fn text_quotes_values_that_need_it() {
        let buf = Buf::default();
        logger(&buf, Format::Text, None).info("two words", &[("empty", "".into()), ("eq", "a=b".into()), ("n", Value::Null)]);
        assert_eq!(buf.text(), "time=2026-05-28T13:38:24Z level=INFO msg=\"two words\" empty=\"\" eq=\"a=b\" n=<nil>\n");
    }

    #[test]
    fn json_escapes_like_slog() {
        let buf = Buf::default();
        logger(&buf, Format::Json, None).info("a\"b\u{8}<", &[]);
        assert!(buf.text().contains("\"msg\":\"a\\\"b\\u0008<\""), "{}", buf.text());
    }

    #[test]
    fn level_gating() {
        let buf = Buf::default();
        let l = logger(&buf, Format::Json, Some(Level::Warn));
        l.debug("dbg", &[]);
        l.info("inf", &[]);
        l.warn("wrn", &[]);
        assert_eq!(buf.text().matches('\n').count(), 1, "{}", buf.text());
    }

    #[test]
    fn with_prepends_args() {
        let buf = Buf::default();
        let child = logger(&buf, Format::Json, None).with(&[("svc", "auth".into())]);
        child.info("ok", &[("k", 1.into())]);
        assert!(buf.text().contains("\"msg\":\"ok\",\"svc\":\"auth\",\"k\":1"), "{}", buf.text());
    }

    #[test]
    fn logger_enabled_on_writer_impl() {
        let l = logger(&Buf::default(), Format::Json, Some(Level::Warn));
        assert!(!l.enabled(Level::Info));
        assert!(l.enabled(Level::Error));
    }

    #[test]
    fn capture_basics() {
        let c = Capture::new(Level::Debug);
        c.info("hello", &[("k", 1.into())]);
        c.warn("careful", &[("n", 2.into())]);
        let recs = c.records();
        assert_eq!(recs.len(), 2);
        assert_eq!((recs[0].level, recs[0].msg.as_str(), recs[0].attrs["k"].clone()), (Level::Info, "hello", json!(1)));
        assert!(c.has_message("careful"));
        assert!(c.has_message("car"));
        assert!(!c.has_message("absent"));
    }

    #[test]
    fn capture_level_gate() {
        let c = Capture::new(Level::Warn);
        c.debug("dbg", &[]);
        c.info("inf", &[]);
        c.warn("wrn", &[]);
        c.error("err", &[]);
        assert_eq!(c.records().len(), 2);
    }

    #[test]
    fn capture_filter_level() {
        let c = Capture::new(Level::Debug);
        c.info("a", &[]);
        c.warn("b", &[]);
        c.info("c", &[]);
        assert_eq!(c.filter_level(Level::Info).len(), 2);
    }

    #[test]
    fn capture_reset() {
        let c = Capture::new(Level::Debug);
        c.info("x", &[]);
        c.reset();
        assert!(c.records().is_empty());
    }

    #[test]
    fn capture_concurrent() {
        let c = Arc::new(Capture::new(Level::Debug));
        let handles: Vec<_> = (0..100)
            .map(|i| {
                let c = Arc::clone(&c);
                std::thread::spawn(move || c.info("msg", &[("i", i.into())]))
            })
            .collect();
        for h in handles {
            h.join().expect("thread");
        }
        assert_eq!(c.records().len(), 100);
    }

    #[test]
    fn capture_enabled() {
        let c = Capture::new(Level::Info);
        assert!(!c.enabled(Level::Debug));
        assert!(c.enabled(Level::Info));
    }

    #[test]
    fn capture_with_prepends_and_keeps_its_own_buffer() {
        let c = Capture::new(Level::Debug);
        let child = c.child(&[("svc", "auth".into()), ("k", 1.into())]);
        child.info("ok", &[("k", 2.into())]);
        assert_eq!(child.records()[0].attrs["svc"], json!("auth"));
        assert_eq!(child.records()[0].attrs["k"], json!(2));
        assert!(c.records().is_empty());
    }

    #[test]
    fn discard_swallows_everything() {
        let l = discard();
        l.info("noop", &[]);
        l.error("also noop", &[]);
    }
}
