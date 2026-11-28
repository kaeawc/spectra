//! Typed, read-only access to macOS property lists (Go's `internal/plistread`).
//!
//! Binary and XML plists are decoded with the `plist` crate and normalized into [`Value`]. The
//! file is read through the [`Fs`] seam, so a collector can point at a fixture tree. Unlike Go's
//! `howett.net/plist`, the OpenStep (ASCII) format is not supported and fails to decode.

pub mod prefs;

use std::collections::BTreeMap;
use std::fmt;
use std::io;
use std::path::Path;

use jiff::Timestamp;

use crate::fs::Fs;

/// The `/Library/Preferences` directory preference domains live in.
const PREFERENCE_ROOT: &str = "/Library/Preferences";

/// Suggested for surfacing in CLI error output next to [`Error::NeedsFullDiskAccess`].
pub const FULL_DISK_ACCESS_REMEDIATION: &str =
    "Grant Full Disk Access to your terminal in System Settings -> Privacy & Security -> Full Disk Access.";

/// Why a plist read failed.
#[derive(Debug)]
pub enum Error {
    /// A preference read macOS commonly denies until the caller has Full Disk Access (Go's
    /// `ErrNeedsFullDiskAccess`).
    NeedsFullDiskAccess {
        path: String,
        source: io::Error,
    },
    Io {
        path: String,
        source: io::Error,
    },
    /// The bytes are not a plist, or hold a value Spectra cannot represent. `path` is empty when
    /// decoding bytes directly.
    Decode {
        path: String,
        message: String,
    },
    KeyNotFound {
        path: String,
        key: String,
    },
    EmptyDomain,
}

impl Error {
    /// The Go `errors.Is(err, fs.ErrNotExist)` check: the file or the key is absent.
    pub fn is_not_exist(&self) -> bool {
        match self {
            Error::KeyNotFound { .. } => true,
            Error::Io { source, .. } => source.kind() == io::ErrorKind::NotFound,
            _ => false,
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::NeedsFullDiskAccess { path, source } => write!(f, "{path}: needs full disk access: {source}"),
            Error::Io { path, source } => write!(f, "{path}: {source}"),
            Error::Decode { path, message } if path.is_empty() => write!(f, "decode plist: {message}"),
            Error::Decode { path, message } => write!(f, "decode plist {path}: {message}"),
            Error::KeyNotFound { path, key } => write!(f, "plist key {key:?} in {path}: file does not exist"),
            Error::EmptyDomain => write!(f, "resolve domain: empty domain"),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Error::NeedsFullDiskAccess { source, .. } | Error::Io { source, .. } => Some(source),
            _ => None,
        }
    }
}

/// The concrete kind of a [`Value`].
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    String,
    Int,
    Float,
    Bool,
    Date,
    Data,
    Array,
    Dict,
}

/// A normalized plist value. It also stands in for Go's `Value.Any()` plain data.
#[derive(Clone, Debug, PartialEq)]
pub enum Value {
    String(String),
    Int(i64),
    Float(f64),
    Bool(bool),
    Date(Timestamp),
    Data(Vec<u8>),
    Array(Vec<Value>),
    Dict(BTreeMap<String, Value>),
}

impl Value {
    pub fn kind(&self) -> Kind {
        match self {
            Value::String(_) => Kind::String,
            Value::Int(_) => Kind::Int,
            Value::Float(_) => Kind::Float,
            Value::Bool(_) => Kind::Bool,
            Value::Date(_) => Kind::Date,
            Value::Data(_) => Kind::Data,
            Value::Array(_) => Kind::Array,
            Value::Dict(_) => Kind::Dict,
        }
    }

    /// A dictionary value by dotted path. An exact key containing dots wins over nesting; an
    /// empty path is the value itself.
    pub fn lookup(&self, path: &str) -> Option<&Value> {
        if path.is_empty() {
            return Some(self);
        }
        if let Value::Dict(d) = self
            && let Some(exact) = d.get(path)
        {
            return Some(exact);
        }
        let mut cur = self;
        for part in path.split('.') {
            let Value::Dict(d) = cur else { return None };
            if part.is_empty() {
                return None;
            }
            cur = d.get(part)?;
        }
        Some(cur)
    }
}

/// A decoded plist file plus basic file metadata.
#[derive(Clone, Debug, PartialEq)]
pub struct Domain {
    pub path: String,
    pub mtime: Option<Timestamp>,
    pub size: u64,
    pub root: Value,
}

/// Reads and decodes the plist file at `path`.
pub fn read(fs: &dyn Fs, path: &str) -> Result<Domain, Error> {
    let info = fs.metadata(Path::new(path)).map_err(|e| map_read_err(path, e))?;
    let data = fs.read(Path::new(path)).map_err(|e| map_read_err(path, e))?;
    let root = decode(&data).map_err(|e| match e {
        Error::Decode { message, .. } => Error::Decode { path: path.to_string(), message },
        other => other,
    })?;
    let mtime = info.modified.and_then(|(sec, nsec)| Timestamp::new(sec, i32::try_from(nsec).ok()?).ok());
    Ok(Domain { path: path.to_string(), mtime, size: info.len, root })
}

/// Reads one top-level or dotted dictionary key from a plist file.
pub fn read_key(fs: &dyn Fs, path: &str, key: &str) -> Result<Value, Error> {
    let d = read(fs, path)?;
    match d.root.lookup(key) {
        Some(v) => Ok(v.clone()),
        None => Err(Error::KeyNotFound { path: path.to_string(), key: key.to_string() }),
    }
}

/// The standard macOS preference plist path for a domain.
///
/// Absolute paths and strings ending in `.plist` are returned cleaned. For normal domains,
/// `current_host == false` maps to `/Library/Preferences/<domain>.plist`; `true` resolves the first
/// existing `ByHost/<domain>.*.plist` when present, then falls back to the unsuffixed ByHost path.
pub fn resolve_domain(fs: &dyn Fs, domain: &str, current_host: bool) -> Result<String, Error> {
    let domain = domain.trim();
    if domain.is_empty() {
        return Err(Error::EmptyDomain);
    }
    if domain.starts_with('/') || domain.ends_with(".plist") {
        return Ok(clean(domain));
    }
    if !current_host {
        return Ok(format!("{PREFERENCE_ROOT}/{domain}.plist"));
    }
    let by_host = format!("{PREFERENCE_ROOT}/ByHost");
    // Go's filepath.Glob(ByHost/<domain>.*.plist): sorted matches, I/O errors mean no matches.
    let (prefix, suffix) = (format!("{domain}."), ".plist");
    let first = fs.read_dir(Path::new(&by_host)).ok().and_then(|entries| {
        entries
            .into_iter()
            .map(|e| e.name)
            .find(|n| n.len() >= prefix.len() + suffix.len() && n.starts_with(&prefix) && n.ends_with(suffix))
    });
    Ok(match first {
        Some(name) => clean(&format!("{by_host}/{name}")),
        None => format!("{by_host}/{domain}.plist"),
    })
}

/// Parses plist bytes (binary or XML).
pub fn decode(data: &[u8]) -> Result<Value, Error> {
    let raw = ::plist::Value::from_reader(io::Cursor::new(data)).map_err(|e| decode_err(e.to_string()))?;
    value_from(&raw)
}

fn decode_err(message: impl Into<String>) -> Error {
    Error::Decode { path: String::new(), message: message.into() }
}

fn value_from(raw: &::plist::Value) -> Result<Value, Error> {
    use ::plist::Value as P;
    Ok(match raw {
        P::String(s) => Value::String(s.clone()),
        P::Boolean(b) => Value::Bool(*b),
        P::Real(f) => Value::Float(*f),
        P::Data(d) => Value::Data(d.clone()),
        P::Integer(i) => Value::Int(int_value(i.as_signed(), i.as_unsigned())?),
        P::Uid(u) => Value::Int(int_value(None, Some(u.get()))?),
        P::Date(d) => Value::Date(date_value(*d)?),
        P::Array(items) => {
            let mut out = Vec::with_capacity(items.len());
            for (i, elem) in items.iter().enumerate() {
                out.push(value_from(elem).map_err(|e| decode_err(format!("array[{i}]: {}", inner(&e))))?);
            }
            Value::Array(out)
        }
        P::Dictionary(d) => {
            let mut out = BTreeMap::new();
            for (k, elem) in d {
                out.insert(k.clone(), value_from(elem).map_err(|e| decode_err(format!("dict[{k}]: {}", inner(&e))))?);
            }
            Value::Dict(out)
        }
        _ => return Err(decode_err("unsupported plist value")),
    })
}

fn inner(e: &Error) -> String {
    match e {
        Error::Decode { message, .. } => message.clone(),
        other => other.to_string(),
    }
}

fn int_value(signed: Option<i64>, unsigned: Option<u64>) -> Result<i64, Error> {
    match (signed, unsigned) {
        (Some(v), _) => Ok(v),
        (None, Some(u)) => i64::try_from(u).map_err(|_| decode_err(format!("integer {u} overflows int64"))),
        (None, None) => Err(decode_err("integer out of range")),
    }
}

fn date_value(d: ::plist::Date) -> Result<Timestamp, Error> {
    Timestamp::try_from(std::time::SystemTime::from(d)).map_err(|e| decode_err(format!("date out of range: {e}")))
}

fn map_read_err(path: &str, source: io::Error) -> Error {
    if needs_full_disk_access_path(path) && source.kind() == io::ErrorKind::PermissionDenied {
        Error::NeedsFullDiskAccess { path: path.to_string(), source }
    } else {
        Error::Io { path: path.to_string(), source }
    }
}

fn needs_full_disk_access_path(path: &str) -> bool {
    clean(path).starts_with(&format!("{PREFERENCE_ROOT}/"))
}

/// Go's `filepath.Clean` for slash-separated paths.
fn clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_string();
    }
    let rooted = path.starts_with('/');
    let mut out: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." if out.last().is_some_and(|l| *l != "..") => {
                out.pop();
            }
            ".." if rooted => {}
            p => out.push(p),
        }
    }
    let joined = out.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_string(),
        (false, false) => joined,
    }
}

#[cfg(test)]
mod tests;
