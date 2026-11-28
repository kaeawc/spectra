//! Go `flag`-compatible argument parsing: a port of the parts of Go's `flag.FlagSet` the CLI
//! uses (with `ContinueOnError`), plus `cmd/spectra/flagparse.go`'s
//! [`FlagSet::parse_interspersed`].
//!
//! Go's grammar is not getopt's: a flag has one name, written with one or two dashes (`-json`,
//! `--json`), and takes its value as `-name=v` or as the next argument; `-abc` is the flag named
//! `abc`, never `-a -b -c`. lexopt supplies the argument cursor (its raw-argument API, the way
//! its own `nonstandard` example reads non-getopt syntax) and Go's rules are applied to each raw
//! argument, so `-json`, `--json`, `-json=true` and `--json=true` all parse as Go parses them.
//!
//! Error messages, `-h`/`-help` handling and the usage text (`PrintDefaults`) match Go byte for
//! byte, because commands print them on stderr.

pub mod goconv;

use std::cell::RefCell;
use std::collections::{BTreeMap, BTreeSet};
use std::ffi::OsString;
use std::fmt;
use std::io::Write;
use std::rc::Rc;

use spectra_base::gojson::GoDuration;

use goconv::{NumError, PARSE_ERROR};

/// A flag's value (Go's `flag.Value`). The built-in types implement it; [`FlagSet::var`] takes
/// any other.
pub trait Value {
    /// Parses `s` into the value. The error text follows Go's `invalid value ... : <err>`.
    fn set(&mut self, s: &str) -> Result<(), String>;
    /// The value as text (Go's `String`); a flag's default is this at definition time.
    fn string(&self) -> String;
    /// Whether the flag is a switch that takes no argument (`-v`, not `-v true`).
    fn is_bool_flag(&self) -> bool {
        false
    }
    /// The placeholder `PrintDefaults` shows when the usage has no back-quoted name.
    fn type_name(&self) -> &'static str {
        "value"
    }
    /// `string()` of this type's zero value: a default equal to it is not printed.
    fn zero_string(&self) -> String {
        String::new()
    }
    /// Whether `PrintDefaults` quotes the default (`%q`), as Go does only for string flags.
    fn quote_default(&self) -> bool {
        false
    }
}

/// A value type with Go's parsing and printing for one of the built-in flag kinds.
trait GoType: Clone + Default + 'static {
    const TYPE_NAME: &'static str;
    const IS_BOOL: bool = false;
    const QUOTE: bool = false;
    fn parse(s: &str) -> Result<Self, String>;
    fn format(&self) -> String;
}

fn num_err(e: NumError) -> String {
    e.as_str().to_string()
}

impl GoType for bool {
    const TYPE_NAME: &'static str = "";
    const IS_BOOL: bool = true;
    fn parse(s: &str) -> Result<Self, String> {
        goconv::parse_bool(s).ok_or_else(|| PARSE_ERROR.to_string())
    }
    fn format(&self) -> String {
        self.to_string()
    }
}

impl GoType for String {
    const TYPE_NAME: &'static str = "string";
    const QUOTE: bool = true;
    fn parse(s: &str) -> Result<Self, String> {
        Ok(s.to_string())
    }
    fn format(&self) -> String {
        self.clone()
    }
}

impl GoType for i64 {
    const TYPE_NAME: &'static str = "int";
    fn parse(s: &str) -> Result<Self, String> {
        goconv::parse_int(s).map_err(num_err)
    }
    fn format(&self) -> String {
        self.to_string()
    }
}

impl GoType for u64 {
    const TYPE_NAME: &'static str = "uint";
    fn parse(s: &str) -> Result<Self, String> {
        goconv::parse_uint(s).map_err(num_err)
    }
    fn format(&self) -> String {
        self.to_string()
    }
}

impl GoType for f64 {
    const TYPE_NAME: &'static str = "float";
    fn parse(s: &str) -> Result<Self, String> {
        goconv::parse_float(s).map_err(num_err)
    }
    fn format(&self) -> String {
        goconv::format_float(*self)
    }
}

impl GoType for GoDuration {
    const TYPE_NAME: &'static str = "duration";
    fn parse(s: &str) -> Result<Self, String> {
        // Go's flag package reports every duration error as a plain parse error.
        goconv::parse_duration(s).map_err(|_| PARSE_ERROR.to_string())
    }
    fn format(&self) -> String {
        goconv::format_duration(*self)
    }
}

struct Typed<T>(Rc<RefCell<T>>);

impl<T: GoType> Value for Typed<T> {
    fn set(&mut self, s: &str) -> Result<(), String> {
        *self.0.borrow_mut() = T::parse(s)?;
        Ok(())
    }
    fn string(&self) -> String {
        self.0.borrow().format()
    }
    fn is_bool_flag(&self) -> bool {
        T::IS_BOOL
    }
    fn type_name(&self) -> &'static str {
        T::TYPE_NAME
    }
    fn zero_string(&self) -> String {
        T::default().format()
    }
    fn quote_default(&self) -> bool {
        T::QUOTE
    }
}

/// Reads a flag's current value after [`FlagSet::parse`] (Go's `*T` from `fs.Bool` and kin).
#[derive(Debug)]
pub struct Handle<T>(Rc<RefCell<T>>);

impl<T: Clone> Handle<T> {
    pub fn get(&self) -> T {
        self.0.borrow().clone()
    }
}

impl<T> Clone for Handle<T> {
    fn clone(&self) -> Self {
        Handle(Rc::clone(&self.0))
    }
}

/// One defined flag (Go's `flag.Flag`).
pub struct Flag {
    pub name: String,
    pub usage: String,
    /// The default as text.
    pub def_value: String,
    value: Box<dyn Value>,
}

impl Flag {
    pub fn value(&self) -> &dyn Value {
        self.value.as_ref()
    }
}

impl fmt::Debug for Flag {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Flag").field("name", &self.name).field("def_value", &self.def_value).field("value", &self.value.string()).finish()
    }
}

/// Why [`FlagSet::parse`] stopped.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum FlagError {
    /// `-h` or `-help` was given and not defined (Go's `flag.ErrHelp`); the usage was printed.
    Help,
    /// Anything else; the message and the usage were printed.
    Invalid(String),
}

impl fmt::Display for FlagError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            FlagError::Help => f.write_str("flag: help requested"),
            FlagError::Invalid(msg) => f.write_str(msg),
        }
    }
}

impl std::error::Error for FlagError {}

/// A custom usage printer (Go's `fs.Usage = func() {...}`).
pub type UsageFn = Box<dyn Fn(&FlagSet, &mut dyn Write)>;

/// A set of flags (Go's `flag.NewFlagSet(name, flag.ContinueOnError)`). Output goes to the
/// writer passed to [`FlagSet::parse`], the counterpart of `fs.SetOutput`.
pub struct FlagSet {
    name: String,
    formal: BTreeMap<String, Flag>,
    actual: BTreeSet<String>,
    args: Vec<String>,
    parsed: bool,
    usage: Option<UsageFn>,
}

impl fmt::Debug for FlagSet {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("FlagSet").field("name", &self.name).field("formal", &self.formal).field("args", &self.args).finish()
    }
}

impl FlagSet {
    pub fn new(name: impl Into<String>) -> Self {
        FlagSet { name: name.into(), formal: BTreeMap::new(), actual: BTreeSet::new(), args: Vec::new(), parsed: false, usage: None }
    }

    pub fn name(&self) -> &str {
        &self.name
    }

    fn typed<T: GoType>(&mut self, name: &str, default: T, usage: &str) -> Handle<T> {
        let cell = Rc::new(RefCell::new(default));
        self.var(name, usage, Box::new(Typed(Rc::clone(&cell))));
        Handle(cell)
    }

    pub fn bool(&mut self, name: &str, default: bool, usage: &str) -> Handle<bool> {
        self.typed(name, default, usage)
    }

    pub fn string(&mut self, name: &str, default: &str, usage: &str) -> Handle<String> {
        self.typed(name, default.to_string(), usage)
    }

    /// Go's `fs.Int` (64-bit on every platform Spectra ships for).
    pub fn int(&mut self, name: &str, default: i64, usage: &str) -> Handle<i64> {
        self.typed(name, default, usage)
    }

    pub fn int64(&mut self, name: &str, default: i64, usage: &str) -> Handle<i64> {
        self.typed(name, default, usage)
    }

    /// Go's `fs.Uint` (64-bit on every platform Spectra ships for).
    pub fn uint(&mut self, name: &str, default: u64, usage: &str) -> Handle<u64> {
        self.typed(name, default, usage)
    }

    pub fn uint64(&mut self, name: &str, default: u64, usage: &str) -> Handle<u64> {
        self.typed(name, default, usage)
    }

    pub fn float64(&mut self, name: &str, default: f64, usage: &str) -> Handle<f64> {
        self.typed(name, default, usage)
    }

    pub fn duration(&mut self, name: &str, default: GoDuration, usage: &str) -> Handle<GoDuration> {
        self.typed(name, default, usage)
    }

    /// Defines a flag with a custom [`Value`] (Go's `fs.Var`). Panics, as Go does, when the name
    /// begins with `-`, contains `=`, or is already defined.
    pub fn var(&mut self, name: &str, usage: &str, value: Box<dyn Value>) {
        assert!(!name.starts_with('-'), "flag {} begins with -", goconv::quote(name));
        assert!(!name.contains('='), "flag {} contains =", goconv::quote(name));
        if self.formal.contains_key(name) {
            if self.name.is_empty() {
                panic!("flag redefined: {name}");
            }
            panic!("{} flag redefined: {name}", self.name);
        }
        let flag = Flag { name: name.to_string(), usage: usage.to_string(), def_value: value.string(), value };
        self.formal.insert(name.to_string(), flag);
    }

    /// Replaces the usage printer that `-h` and parse errors call.
    pub fn set_usage(&mut self, usage: UsageFn) {
        self.usage = Some(usage);
    }

    /// Prints the usage: the custom printer, or Go's `Usage of <name>:` and [`Self::print_defaults`].
    pub fn usage(&self, out: &mut dyn Write) {
        if let Some(usage) = &self.usage {
            usage(self, out);
            return;
        }
        // Output errors are ignored, as Go's fmt.Fprintf calls are.
        let _ = if self.name.is_empty() { writeln!(out, "Usage:") } else { writeln!(out, "Usage of {}:", self.name) };
        self.print_defaults(out);
    }

    /// Go's `PrintDefaults`: every flag, sorted by name, with its usage and non-zero default.
    pub fn print_defaults(&self, out: &mut dyn Write) {
        for flag in self.formal.values() {
            let _ = out.write_all(default_line(flag).as_bytes());
        }
    }

    /// Looks a defined flag up by name.
    pub fn lookup(&self, name: &str) -> Option<&Flag> {
        self.formal.get(name)
    }

    /// Sets a defined flag as if it were given on the command line (Go's `fs.Set`).
    pub fn set(&mut self, name: &str, value: &str) -> Result<(), String> {
        let flag = self.formal.get_mut(name).ok_or_else(|| format!("no such flag -{name}"))?;
        flag.value.set(value)?;
        self.actual.insert(name.to_string());
        Ok(())
    }

    /// Visits the flags that were set, sorted by name (Go's `Visit`).
    pub fn visit(&self, mut f: impl FnMut(&Flag)) {
        self.actual.iter().filter_map(|n| self.formal.get(n)).for_each(&mut f);
    }

    /// Visits every defined flag, sorted by name (Go's `VisitAll`).
    pub fn visit_all(&self, f: impl FnMut(&Flag)) {
        self.formal.values().for_each(f);
    }

    /// Whether [`Self::parse`] has been called.
    pub fn parsed(&self) -> bool {
        self.parsed
    }

    /// The arguments left after the flags.
    pub fn args(&self) -> &[String] {
        &self.args
    }

    /// The `i`th remaining argument, or `""` (Go's `fs.Arg`).
    pub fn arg(&self, i: usize) -> &str {
        self.args.get(i).map_or("", String::as_str)
    }

    pub fn narg(&self) -> usize {
        self.args.len()
    }

    /// How many flags were set.
    pub fn nflag(&self) -> usize {
        self.actual.len()
    }

    /// Parses flags up to the first non-flag argument or `--` (Go's `fs.Parse`). Errors and usage
    /// go to `out`.
    pub fn parse(&mut self, arguments: &[String], out: &mut dyn Write) -> Result<(), FlagError> {
        self.parsed = true;
        let mut parser = lexopt::Parser::from_args(arguments.iter().map(OsString::from));
        let mut raw = parser.raw_args().expect("a parser that has not started has no pending value");
        let result = loop {
            match self.parse_one(&mut raw, out) {
                Ok(true) => {}
                Ok(false) => break Ok(()),
                Err(e) => break Err(e),
            }
        };
        self.args = raw.as_slice().iter().map(|a| a.to_string_lossy().into_owned()).collect();
        result
    }

    /// Parses flags anywhere among the positional arguments; everything after a `--` is
    /// positional. Returns the positional arguments (`cmd/spectra/flagparse.go`'s
    /// `parseInterspersed`).
    pub fn parse_interspersed(&mut self, arguments: &[String], out: &mut dyn Write) -> Result<Vec<String>, FlagError> {
        let mut pos = Vec::new();
        let mut args = arguments.to_vec();
        loop {
            self.parse(&args, out)?;
            let rest = self.args.clone();
            if rest.is_empty() {
                return Ok(pos);
            }
            let used = args.len() - rest.len();
            if used > 0 && args[used - 1] == "--" {
                pos.extend(rest);
                return Ok(pos);
            }
            pos.push(rest[0].clone());
            args = rest[1..].to_vec();
        }
    }

    /// Go's `parseOne`: consumes one flag (and its value) and reports whether it did.
    fn parse_one(&mut self, raw: &mut lexopt::RawArgs<'_>, out: &mut dyn Write) -> Result<bool, FlagError> {
        let Some(s) = raw.peek().map(|a| a.to_string_lossy().into_owned()) else { return Ok(false) };
        let b = s.as_bytes();
        if b.len() < 2 || b[0] != b'-' {
            return Ok(false);
        }
        let mut minuses = 1;
        if b[1] == b'-' {
            minuses = 2;
            if b.len() == 2 {
                // "--" ends the flags.
                raw.next();
                return Ok(false);
            }
        }
        let name = &s[minuses..];
        if name.is_empty() || name.starts_with('-') || name.starts_with('=') {
            return Err(self.fail(out, format!("bad flag syntax: {s}")));
        }
        raw.next();
        // `=` cannot be first, so the search starts at byte 1. Splitting at an ASCII `=` always
        // lands on a char boundary.
        let (name, value) = match name.as_bytes()[1..].iter().position(|&c| c == b'=') {
            Some(i) => (&name[..=i], Some(name[i + 2..].to_string())),
            None => (name, None),
        };
        let Some(is_bool) = self.formal.get(name).map(|f| f.value.is_bool_flag()) else {
            if name == "help" || name == "h" {
                self.usage(out);
                return Err(FlagError::Help);
            }
            return Err(self.fail(out, format!("flag provided but not defined: -{name}")));
        };
        if is_bool {
            self.set_bool(name, value, out)?;
        } else {
            let Some(value) = value.or_else(|| raw.next().map(|a| a.to_string_lossy().into_owned())) else {
                return Err(self.fail(out, format!("flag needs an argument: -{name}")));
            };
            if let Err(e) = self.set_value(name, &value) {
                return Err(self.fail(out, format!("invalid value {} for flag -{name}: {e}", goconv::quote(&value))));
            }
        }
        self.actual.insert(name.to_string());
        Ok(true)
    }

    fn set_bool(&mut self, name: &str, value: Option<String>, out: &mut dyn Write) -> Result<(), FlagError> {
        match value {
            Some(v) => self
                .set_value(name, &v)
                .map_err(|e| self.fail(out, format!("invalid boolean value {} for -{name}: {e}", goconv::quote(&v)))),
            None => self.set_value(name, "true").map_err(|e| self.fail(out, format!("invalid boolean flag {name}: {e}"))),
        }
    }

    fn set_value(&mut self, name: &str, value: &str) -> Result<(), String> {
        match self.formal.get_mut(name) {
            Some(flag) => flag.value.set(value),
            None => Err(format!("no such flag -{name}")),
        }
    }

    /// Go's `failf`: prints the message and the usage, and returns the error.
    fn fail(&self, out: &mut dyn Write, msg: String) -> FlagError {
        let _ = writeln!(out, "{msg}");
        self.usage(out);
        FlagError::Invalid(msg)
    }
}

/// Go's `UnquoteUsage`: the back-quoted name in the usage (with the quotes removed from the
/// usage), else the value's type name, or `""` for a switch.
pub fn unquote_usage(flag: &Flag) -> (String, String) {
    let usage = &flag.usage;
    if let Some(i) = usage.find('`')
        && let Some(len) = usage[i + 1..].find('`')
    {
        let name = &usage[i + 1..i + 1 + len];
        return (name.to_string(), format!("{}{}{}", &usage[..i], name, &usage[i + 2 + len..]));
    }
    let name = if flag.value.is_bool_flag() { "" } else { flag.value.type_name() };
    (name.to_string(), usage.clone())
}

fn default_line(flag: &Flag) -> String {
    let mut b = format!("  -{}", flag.name);
    let (name, usage) = unquote_usage(flag);
    if !name.is_empty() {
        b.push(' ');
        b.push_str(&name);
    }
    // A one-letter switch keeps its usage on the same line; "  -x" is four bytes.
    if b.len() <= 4 {
        b.push('\t');
    } else {
        b.push_str("\n    \t");
    }
    b.push_str(&usage.replace('\n', "\n    \t"));
    if flag.def_value != flag.value.zero_string() {
        if flag.value.quote_default() {
            b.push_str(&format!(" (default {})", goconv::quote(&flag.def_value)));
        } else {
            b.push_str(&format!(" (default {})", flag.def_value));
        }
    }
    b.push('\n');
    b
}

#[cfg(test)]
mod tests;
