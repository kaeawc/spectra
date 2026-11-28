//! `powermetrics --samplers tasks` rows (Go's `powermetrics_tasks.go`, the "L3" energy
//! attribution).
//!
//! `powermetrics -f plist` emits an XML plist with one dict per pid. Apple renamed several keys
//! in macOS Sequoia (snake_case to camelCase), so each field reads from both variants. Go reads
//! it with `encoding/xml` in non-strict mode; [`Tokens`] is the small subset of that decoder a
//! plist needs (tags, text, entities, and skipping declarations, doctypes and comments).

use std::collections::BTreeMap;
use std::fmt;

use serde::{Serialize, Serializer};

use crate::goerr::parse_int;
use crate::gosort;

/// One pid's row from the tasks sampler (Go's `TaskPowerSample`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct TaskPowerSample {
    pub pid: i64,
    pub command: String,
    pub energy_impact: f64,
    pub cpu_ns: u64,
    pub gpu_ns: u64,
    pub ane_ns: u64,
    pub short_timer_wakeups: u64,
    #[serde(rename = "qos_default_pct")]
    pub qos_default_pct: f64,
    #[serde(rename = "qos_background_pct")]
    pub qos_background_pct: f64,
}

/// The parsed result of one tasks sample (Go's `PowermetricsTasks`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct PowermetricsTasks {
    pub elapsed_ns: u64,
    /// Go's nil slice prints `null`.
    #[serde(serialize_with = "null_if_empty")]
    pub tasks: Vec<TaskPowerSample>,
}

fn null_if_empty<S: Serializer>(v: &[TaskPowerSample], s: S) -> Result<S::Ok, S::Error> {
    if v.is_empty() { s.serialize_none() } else { s.collect_seq(v) }
}

/// Why a plist did not parse.
#[derive(Debug, PartialEq, Eq)]
pub struct ParseError(String);

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "plist: {}", self.0)
    }
}

impl std::error::Error for ParseError {}

/// Go's `ParseTasksPlist`: the per-pid task rows of `powermetrics --samplers tasks -f plist`,
/// tolerating the Sonoma to Sequoia key rename. The first `<dict>` is the root.
///
/// Error text differs from Go's, which quotes `encoding/xml`'s syntax errors.
pub fn parse_tasks_plist(data: &[u8]) -> Result<PowermetricsTasks, ParseError> {
    let text = String::from_utf8_lossy(data);
    let mut toks = Tokens::new(&text);
    let mut root = None;
    while let Some(tok) = toks.next_token()? {
        if matches!(&tok, Token::Start(name) if name == "dict") {
            root = Some(decode_dict(&mut toks)?);
            break;
        }
    }
    let Some(root) = root else { return Err(ParseError("no root dict".into())) };
    let get = |k: &str| root.get(k);
    let mut out = PowermetricsTasks { elapsed_ns: uint_from_any(&[get("elapsed_ns"), get("elapsedNs")]), tasks: Vec::new() };
    if let Some(Value::Array(items)) = get("tasks") {
        out.tasks = items
            .iter()
            .filter_map(|t| match t {
                Value::Dict(d) => Some(task_from_dict(d)),
                _ => None,
            })
            .collect();
    }
    Ok(out)
}

impl PowermetricsTasks {
    /// Go's `TopTasks`: the `n` samples with the highest energy impact, descending, in Go's
    /// order for ties. The sample is not changed.
    pub fn top_tasks(&self, n: i64) -> Vec<TaskPowerSample> {
        let Ok(n) = usize::try_from(n) else { return Vec::new() };
        if n == 0 || self.tasks.is_empty() {
            return Vec::new();
        }
        let mut dup = self.tasks.clone();
        gosort::slice(&mut dup, |a, b| a.energy_impact > b.energy_impact);
        dup.truncate(n);
        dup
    }
}

fn task_from_dict(m: &BTreeMap<String, Value>) -> TaskPowerSample {
    let g = |k: &str| m.get(k);
    TaskPowerSample {
        pid: clamp_pid(int_from_any(&[g("pid")])),
        command: str_from_any(&[g("name"), g("command")]),
        energy_impact: float_from_any(&[g("energy_impact"), g("energyImpact")]),
        cpu_ns: uint_from_any(&[g("cputime_ns"), g("cputimeNs")]),
        gpu_ns: uint_from_any(&[g("gputime_ns"), g("gputimeNs")]),
        ane_ns: uint_from_any(&[g("ane_energy"), g("aneEnergy"), g("ane_ns"), g("aneNs")]),
        short_timer_wakeups: uint_from_any(&[g("short_timer_wakeups"), g("shortTimerWakeups")]),
        qos_default_pct: ms_per_s_to_pct(float_from_any(&[g("qos_default_ms_per_s"), g("qosDefaultMsPerS")])),
        qos_background_pct: ms_per_s_to_pct(float_from_any(&[g("qos_background_ms_per_s"), g("qosBackgroundMsPerS")])),
    }
}

/// A ms-per-second rate as a wall-clock percentage.
fn ms_per_s_to_pct(ms_per_s: f64) -> f64 {
    ms_per_s / 10.0
}

/// Go's `clampPID`: macOS pids never exceed 99999, so anything outside the int32 range is a
/// malformed plist and becomes 0 rather than wrapping.
fn clamp_pid(n: i64) -> i64 {
    if (0..=i64::from(i32::MAX)).contains(&n) { n } else { 0 }
}

/// A decoded plist value; Go's `any`, with `nil` for the types it skips.
#[derive(Clone, Debug, PartialEq)]
enum Value {
    Dict(BTreeMap<String, Value>),
    Array(Vec<Value>),
    Str(String),
    Int(i64),
    Real(f64),
    Bool(bool),
    Nil,
}

fn decode_dict(toks: &mut Tokens<'_>) -> Result<BTreeMap<String, Value>, ParseError> {
    let mut m = BTreeMap::new();
    let mut key: Option<String> = None;
    loop {
        let Some(tok) = toks.next_token()? else { return Err(ParseError("EOF".into())) };
        match tok {
            Token::Start(name) if name == "key" => key = Some(toks.element_text()?),
            Token::Start(name) => {
                let v = decode_value(toks, &name)?;
                // A value without a key is consumed and dropped.
                if let Some(k) = key.take() {
                    m.insert(k, v);
                }
            }
            Token::End(name) if name == "dict" => return Ok(m),
            _ => {}
        }
    }
}

fn decode_array(toks: &mut Tokens<'_>) -> Result<Vec<Value>, ParseError> {
    let mut arr = Vec::new();
    loop {
        let Some(tok) = toks.next_token()? else { return Err(ParseError("EOF".into())) };
        match tok {
            Token::Start(name) => arr.push(decode_value(toks, &name)?),
            Token::End(name) if name == "array" => return Ok(arr),
            _ => {}
        }
    }
}

fn decode_value(toks: &mut Tokens<'_>, name: &str) -> Result<Value, ParseError> {
    match name {
        "dict" => decode_dict(toks).map(Value::Dict),
        "array" => decode_array(toks).map(Value::Array),
        "string" => toks.element_text().map(Value::Str),
        "integer" => {
            let s = toks.element_text()?;
            // `n, _ := strconv.ParseInt(...)`: a failure is 0, a range error the clamped limit.
            Ok(Value::Int(match parse_int(s.trim()) {
                Ok(n) => n,
                Err(e) => e.value,
            }))
        }
        "real" => {
            let s = toks.element_text()?;
            Ok(Value::Real(s.trim().parse().unwrap_or(0.0)))
        }
        "true" => toks.skip().map(|()| Value::Bool(true)),
        "false" => toks.skip().map(|()| Value::Bool(false)),
        _ => toks.skip().map(|()| Value::Nil),
    }
}

/// Go's `intFromAny`: the first candidate that is a number (or a string holding one).
fn int_from_any(vs: &[Option<&Value>]) -> i64 {
    for v in vs.iter().flatten() {
        match v {
            Value::Int(n) => return *n,
            Value::Real(f) => return *f as i64,
            Value::Str(s) => {
                if let Ok(x) = parse_int(s.trim()) {
                    return x;
                }
            }
            _ => {}
        }
    }
    0
}

fn uint_from_any(vs: &[Option<&Value>]) -> u64 {
    u64::try_from(int_from_any(vs)).unwrap_or(0)
}

fn float_from_any(vs: &[Option<&Value>]) -> f64 {
    for v in vs.iter().flatten() {
        match v {
            Value::Real(f) => return *f,
            Value::Int(n) => return *n as f64,
            Value::Str(s) => {
                if let Ok(x) = s.trim().parse::<f64>() {
                    return x;
                }
            }
            _ => {}
        }
    }
    0.0
}

fn str_from_any(vs: &[Option<&Value>]) -> String {
    vs.iter()
        .flatten()
        .find_map(|v| matches!(v, Value::Str(s) if !s.is_empty()).then(|| if let Value::Str(s) = v { s.clone() } else { String::new() }))
        .unwrap_or_default()
}

enum Token {
    Start(String),
    End(String),
    Text(String),
}

/// A forgiving XML token stream: start and end tags (a self-closing tag is both), text with
/// entities decoded, and everything else (`<?...?>`, `<!...>`, comments) skipped.
struct Tokens<'a> {
    rest: &'a str,
    /// The end tag a self-closing tag still owes.
    pending_end: Option<String>,
}

impl<'a> Tokens<'a> {
    fn new(s: &'a str) -> Self {
        Tokens { rest: s, pending_end: None }
    }

    fn next_token(&mut self) -> Result<Option<Token>, ParseError> {
        if let Some(name) = self.pending_end.take() {
            return Ok(Some(Token::End(name)));
        }
        loop {
            if self.rest.is_empty() {
                return Ok(None);
            }
            let Some(rest) = self.rest.strip_prefix('<') else {
                let end = self.rest.find('<').unwrap_or(self.rest.len());
                let (text, rest) = self.rest.split_at(end);
                self.rest = rest;
                return Ok(Some(Token::Text(unescape(text))));
            };
            if let Some(body) = rest.strip_prefix("!--") {
                let end = body.find("-->").ok_or_else(|| ParseError("unterminated comment".into()))?;
                self.rest = &body[end + 3..];
                continue;
            }
            let end = rest.find('>').ok_or_else(|| ParseError("unexpected EOF inside a tag".into()))?;
            let (tag, after) = (&rest[..end], &rest[end + 1..]);
            self.rest = after;
            if tag.starts_with('?') || tag.starts_with('!') {
                continue;
            }
            if let Some(name) = tag.strip_prefix('/') {
                return Ok(Some(Token::End(local_name(name))));
            }
            let (tag, self_closing) = tag.strip_suffix('/').map_or((tag, false), |t| (t, true));
            let name = local_name(tag);
            if self_closing {
                self.pending_end = Some(name.clone());
            }
            return Ok(Some(Token::Start(name)));
        }
    }

    /// Go's `DecodeElement` into a string: the element's character data, nested elements
    /// ignored, up to its end tag.
    fn element_text(&mut self) -> Result<String, ParseError> {
        let mut text = String::new();
        let mut depth = 0usize;
        loop {
            let Some(tok) = self.next_token()? else { return Err(ParseError("EOF".into())) };
            match tok {
                Token::Text(t) if depth == 0 => text.push_str(&t),
                Token::Start(_) => depth += 1,
                Token::End(_) if depth == 0 => return Ok(text),
                Token::End(_) => depth -= 1,
                Token::Text(_) => {}
            }
        }
    }

    /// Go's `Decoder.Skip`: through the end tag of the element just started.
    fn skip(&mut self) -> Result<(), ParseError> {
        let mut depth = 0usize;
        loop {
            let Some(tok) = self.next_token()? else { return Err(ParseError("EOF".into())) };
            match tok {
                Token::Start(_) => depth += 1,
                Token::End(_) if depth == 0 => return Ok(()),
                Token::End(_) => depth -= 1,
                Token::Text(_) => {}
            }
        }
    }
}

/// The element name without a namespace prefix or attributes (Go's `Name.Local`).
fn local_name(tag: &str) -> String {
    let name = tag.split(|c: char| c.is_whitespace()).next().unwrap_or("");
    name.rsplit(':').next().unwrap_or(name).to_string()
}

/// The predefined and numeric entities; an unknown one stays as written, as non-strict Go does.
fn unescape(s: &str) -> String {
    if !s.contains('&') {
        return s.to_string();
    }
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(at) = rest.find('&') {
        out.push_str(&rest[..at]);
        rest = &rest[at..];
        let replaced = rest.find(';').filter(|semi| *semi <= 10).and_then(|semi| entity(&rest[1..semi]).map(|c| (c, semi)));
        match replaced {
            Some((c, semi)) => {
                out.push(c);
                rest = &rest[semi + 1..];
            }
            None => {
                out.push('&');
                rest = &rest[1..];
            }
        }
    }
    out.push_str(rest);
    out
}

fn entity(name: &str) -> Option<char> {
    match name {
        "amp" => Some('&'),
        "lt" => Some('<'),
        "gt" => Some('>'),
        "quot" => Some('"'),
        "apos" => Some('\''),
        _ => {
            let n = name.strip_prefix('#')?;
            let code = match n.strip_prefix(['x', 'X']) {
                Some(hex) => u32::from_str_radix(hex, 16).ok()?,
                None => n.parse().ok()?,
            };
            char::from_u32(code)
        }
    }
}

#[cfg(test)]
mod tests;
