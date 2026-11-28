//! Other Macs: their processes and JVMs, read-only, through Spectra Remote.
//!
//! This app has no transport of its own. Each reading runs Spectra Remote's controller,
//! `spectra-remote call --target <host:port> --operation <op>`, which joins the tailnet as its
//! own node, checks the protocol and the result's schema, and prints the agent's response. The
//! agent on the other Mac (`spectra-remote-agent serve-tsnet`) decides who may ask, audits each
//! request, and answers from its Spectra daemon. The operations asked for are `health` (what the
//! agent offers), `process.list` and `jvm.list`, and nothing that changes the other Mac.
//!
//! The hosts are a list kept in `~/.spectra/desktop-hosts.json`, added and removed on the Hosts
//! page. A controller call brings up a tailnet node, which takes seconds, and two at once would
//! share one node's state, so calls run one at a time, only while the Hosts page or a host's page
//! is on screen and the window shows: the open host's processes every 10 s, its JVMs every 60 s,
//! and each listed host's health every 30 s. Leaving the page or quitting stops the call on its
//! way.

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use rune::TextEdit;
use serde_json::{Value, json};

use crate::app::App;
use crate::data::Jvm;
use crate::groups::Nav;
use crate::procs::Process;
use crate::run::{Ended, RunError, run_until};

/// The port `spectra-remote-agent serve-tsnet` listens on unless told otherwise.
pub const DEFAULT_PORT: u16 = 7878;
/// The controller prints the agent's response, at most 16 MiB of JSON, indented.
const MAX_OUTPUT: usize = 48 << 20;

pub const OP_HEALTH: &str = "health";
pub const OP_PROCESSES: &str = "process.list";
pub const OP_JVMS: &str = "jvm.list";
pub const SCHEMA_PROCESSES: &str = "spectra.process.list";
pub const SCHEMA_JVMS: &str = "spectra.jvm.list";

/// What is read from a host, and how often while its page (or the Hosts page) shows.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Kind {
    Health,
    Procs,
    Jvms,
}

impl Kind {
    pub fn op(self) -> &'static str {
        match self {
            Kind::Health => OP_HEALTH,
            Kind::Procs => OP_PROCESSES,
            Kind::Jvms => OP_JVMS,
        }
    }

    /// Seconds between the end of one reading and the start of the next.
    pub fn every(self) -> f64 {
        match self {
            Kind::Health => 30.0,
            Kind::Procs => 10.0,
            Kind::Jvms => 60.0,
        }
    }

    /// The controller's own timeout: joining the tailnet, then the agent's run. A JVM reading
    /// takes about 17 s on the other Mac when its daemon has none cached.
    pub fn deadline(self) -> Duration {
        Duration::from_secs(match self {
            Kind::Health => 45,
            Kind::Procs => 60,
            Kind::Jvms => 120,
        })
    }

    fn index(self) -> usize {
        match self {
            Kind::Health => 0,
            Kind::Procs => 1,
            Kind::Jvms => 2,
        }
    }
}

/// One Mac in the list: what it's called here and where its agent listens.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Host {
    pub name: String,
    /// `host:port` on the tailnet, as `spectra-remote call --target` takes it.
    pub target: String,
}

/// Checks a host as typed: a name, and a tailnet hostname, MagicDNS name or address with an
/// optional port (7878 when left out). Gives the target as the controller takes it.
pub fn check_host(name: &str, target: &str) -> Result<Host, String> {
    let name = name.trim();
    let target = target.trim();
    if target.is_empty() {
        return Err("Type the host's tailnet name, like work-mac or work-mac:7878.".into());
    }
    let (host, port) = split_target(target)?;
    let name = if name.is_empty() {
        host.clone()
    } else {
        name.to_owned()
    };
    if name.chars().count() > 64 || name.chars().any(char::is_control) {
        return Err("Keep the name to 64 characters, on one line.".into());
    }
    let target = if host.contains(':') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    };
    Ok(Host { name, target })
}

/// `work-mac`, `work-mac:7878`, `100.64.0.7:7878`, `[fd7a::1]:7878`.
fn split_target(t: &str) -> Result<(String, u16), String> {
    let bad_port = || format!("\"{t}\" has a port that isn't a number from 1 to 65535.");
    let (host, port) = if let Some(rest) = t.strip_prefix('[') {
        let (host, after) = rest
            .split_once(']')
            .ok_or_else(|| format!("\"{t}\" opens a [ it doesn't close."))?;
        let port = match after.strip_prefix(':') {
            Some(p) => Some(p),
            None if after.is_empty() => None,
            None => return Err(format!("\"{t}\" has something after its address.")),
        };
        (host.to_owned(), port)
    } else {
        match t.rsplit_once(':') {
            Some((h, p)) if !h.contains(':') => (h.to_owned(), Some(p)),
            Some(_) => return Err(format!("Put an IPv6 address in brackets: [{t}]:7878.")),
            None => (t.to_owned(), None),
        }
    };
    let port = match port {
        Some(p) => p
            .parse::<u16>()
            .ok()
            .filter(|p| *p > 0)
            .ok_or_else(bad_port)?,
        None => DEFAULT_PORT,
    };
    let ok = |c: char| c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | '_' | ':');
    if host.is_empty() || host.starts_with(['-', '.']) || !host.chars().all(ok) {
        return Err(format!(
            "\"{t}\" isn't a host name or address. Use the name the Mac has on your tailnet."
        ));
    }
    if host.contains(':') && !t.starts_with('[') {
        return Err(format!("Put an IPv6 address in brackets: [{host}]:7878."));
    }
    Ok((host, port))
}

/// Where the list is kept: `~/.spectra/desktop-hosts.json`, next to Spectra's own files.
pub fn default_file() -> Option<PathBuf> {
    let home = std::env::var_os("HOME").filter(|h| !h.is_empty())?;
    Some(Path::new(&home).join(".spectra").join("desktop-hosts.json"))
}

/// The list as written: `{"hosts":[{"name":…,"target":…}]}`. Entries that don't check out are
/// left out rather than failing the whole list.
pub fn parse_hosts(json: &str) -> Result<Vec<Host>, String> {
    let v: Value = serde_json::from_str(json).map_err(|e| format!("it isn't JSON ({e})"))?;
    let list = v
        .get("hosts")
        .and_then(Value::as_array)
        .ok_or("it has no \"hosts\" list")?;
    let mut out: Vec<Host> = Vec::new();
    for h in list {
        let s = |k: &str| h.get(k).and_then(Value::as_str).unwrap_or_default();
        if let Ok(host) = check_host(s("name"), s("target"))
            && !out.iter().any(|o| o.target == host.target)
        {
            out.push(host);
        }
    }
    Ok(out)
}

pub fn hosts_json(hosts: &[Host]) -> String {
    let list: Vec<Value> = hosts
        .iter()
        .map(|h| json!({"name": h.name, "target": h.target}))
        .collect();
    let mut s = serde_json::to_string_pretty(&json!({ "hosts": list })).unwrap_or_default();
    s.push('\n');
    s
}

/// Why a call gave no answer.
#[derive(Clone, Debug, PartialEq)]
pub struct Fail {
    /// `spectra-remote` couldn't be started: Spectra Remote isn't installed here.
    pub missing: bool,
    pub why: String,
}

impl Fail {
    fn new(why: impl Into<String>) -> Fail {
        Fail {
            missing: false,
            why: why.into(),
        }
    }
}

/// Asks a host's agent one operation and gives the response's `result`. Tests swap in a fake
/// that records what it was asked and runs nothing.
pub trait Remote: Send + Sync {
    fn call(
        &self,
        target: &str,
        op: &str,
        deadline: Duration,
        cancel: &AtomicBool,
    ) -> Result<Value, Fail>;
}

/// The controller's arguments: typed values only, never a shell line.
pub fn call_args(target: &str, op: &str, deadline: Duration) -> Vec<String> {
    vec![
        "call".into(),
        "--target".into(),
        target.into(),
        "--operation".into(),
        op.into(),
        "--timeout".into(),
        format!("{}s", deadline.as_secs().max(1)),
    ]
}

/// Spectra Remote's controller: the program to run, `SPECTRA_REMOTE_BIN` or `spectra-remote` on
/// `PATH`.
pub struct Controller {
    pub bin: String,
}

impl Controller {
    pub fn from_env() -> Controller {
        Controller {
            bin: std::env::var("SPECTRA_REMOTE_BIN").unwrap_or_else(|_| "spectra-remote".into()),
        }
    }
}

impl Remote for Controller {
    fn call(
        &self,
        target: &str,
        op: &str,
        deadline: Duration,
        cancel: &AtomicBool,
    ) -> Result<Value, Fail> {
        let bin = &self.bin;
        let mut cmd = Command::new(bin);
        cmd.args(call_args(target, op, deadline));
        // A moment past the controller's own timeout, so its message is the one shown.
        let out = run_until(cmd, deadline + Duration::from_secs(5), MAX_OUTPUT, cancel);
        match out {
            Ok(o) => answer(
                o.status.code(),
                &String::from_utf8_lossy(&o.stdout),
                &String::from_utf8_lossy(&o.stderr),
                target,
            ),
            Err(Ended::Cancelled) => Err(Fail::new("stopped")),
            Err(Ended::Failed(RunError::Spawn(e))) => Err(Fail {
                missing: true,
                why: format!(
                    "Couldn't run {bin}: {e}. Install Spectra Remote's controller (spectra-remote, from kaeawc/spectra-proxy) or set SPECTRA_REMOTE_BIN."
                ),
            }),
            Err(Ended::Failed(e)) => Err(Fail::new(crate::procs::run_failure(bin, e))),
        }
    }
}

/// The first line the controller wrote to stderr, which is the whole of its message.
fn first_line(stderr: &str) -> String {
    stderr
        .lines()
        .map(str::trim)
        .find(|l| !l.is_empty())
        .unwrap_or("no message")
        .to_owned()
}

/// What a finished controller run means, by its exit code: 0 a validated response on stdout,
/// 1 the agent or the tailnet said no, 2 a request it wouldn't send, 3 an answer this protocol
/// can't read.
pub fn answer(code: Option<i32>, stdout: &str, stderr: &str, target: &str) -> Result<Value, Fail> {
    match code {
        Some(0) => {
            let mut v: Value = serde_json::from_str(stdout)
                .map_err(|e| Fail::new(format!("Couldn't read spectra-remote's output: {e}")))?;
            if let Some(err) = v.get("error") {
                let msg = err.get("message").and_then(Value::as_str).unwrap_or("");
                return Err(Fail::new(format!("{target} answered with an error: {msg}")));
            }
            match v.get_mut("result").map(Value::take) {
                Some(r) if !r.is_null() => Ok(r),
                _ => Err(Fail::new(
                    "spectra-remote printed a response with no result",
                )),
            }
        }
        Some(1) => Err(Fail::new(format!(
            "Couldn't get an answer from {target}: {}",
            first_line(stderr)
        ))),
        Some(2) => Err(Fail::new(format!(
            "spectra-remote wouldn't send the request: {}",
            first_line(stderr)
        ))),
        Some(3) => Err(Fail::new(format!(
            "{target} answered in a form this Spectra Remote can't read (update the agent or the controller): {}",
            first_line(stderr)
        ))),
        Some(n) => Err(Fail::new(format!(
            "spectra-remote exited with {n}: {}",
            first_line(stderr)
        ))),
        None => Err(Fail::new("spectra-remote was stopped by a signal")),
    }
}

/// What a host's agent says about itself in answer to `health`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Health {
    pub spectra_version: String,
    pub agent_version: String,
    /// The operations it offers this caller now.
    pub operations: Vec<String>,
}

impl Health {
    pub fn offers(&self, op: &str) -> bool {
        self.operations.iter().any(|o| o == op)
    }
}

pub fn parse_health(result: &Value) -> Result<Health, String> {
    let caps = result
        .get("capabilities")
        .ok_or("the agent's health answer has no capabilities")?;
    let s = |k: &str| {
        caps.get(k)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    let operations = caps
        .get("operations")
        .and_then(Value::as_array)
        .map(|ops| {
            ops.iter()
                .filter_map(|o| o.get("name").and_then(Value::as_str))
                .map(String::from)
                .collect()
        })
        .unwrap_or_default();
    Ok(Health {
        spectra_version: s("spectra_version"),
        agent_version: s("agent_version"),
        operations,
    })
}

/// A diagnostic result's `data` and the list under `field`, after checking its schema.
fn listed<'a>(result: &'a Value, schema: &str, field: &str) -> Result<&'a Value, String> {
    if let Some(name) = result.pointer("/schema/name").and_then(Value::as_str)
        && name != schema
    {
        return Err(format!("the agent answered with {name}, not {schema}"));
    }
    let data = result.get("data").ok_or("the agent's answer has no data")?;
    match data {
        Value::Array(_) => Ok(data),
        _ => match data.get(field) {
            Some(list @ Value::Array(_)) => Ok(list),
            Some(Value::Null) | None if field == "jvms" => Ok(&Value::Null),
            _ => Err(format!("the agent's answer has no {field} list")),
        },
    }
}

/// `process.list`'s rows, read by the same parser as this Mac's own list.
pub fn parse_procs(result: &Value) -> Result<Vec<Process>, String> {
    let list = listed(result, SCHEMA_PROCESSES, "processes")?;
    crate::procs::parse(&list.to_string()).map_err(|e| match e {
        crate::procs::ProcError::TooOld(why) | crate::procs::ProcError::Failed(why) => why,
    })
}

/// `jvm.list`'s entries, read by the same parser as this Mac's own JVMs.
pub fn parse_jvms(result: &Value) -> Result<Vec<Jvm>, String> {
    match listed(result, SCHEMA_JVMS, "jvms")? {
        Value::Null => Ok(Vec::new()),
        list => crate::data::parse(&list.to_string()),
    }
}

/// A reading and when it landed on the window clock.
#[derive(Clone, Debug)]
pub struct Reading<T> {
    pub value: Option<(T, f64)>,
    pub error: Option<String>,
}

impl<T> Default for Reading<T> {
    fn default() -> Self {
        Reading {
            value: None,
            error: None,
        }
    }
}

impl<T> Reading<T> {
    /// A good reading replaces the last; a failed one keeps it and says why.
    fn land(&mut self, r: Result<T, String>, now: f64) {
        match r {
            Ok(v) => {
                self.value = Some((v, now));
                self.error = None;
            }
            Err(e) => self.error = Some(e),
        }
    }

    pub fn get(&self) -> Option<&T> {
        self.value.as_ref().map(|(v, _)| v)
    }

    pub fn at(&self) -> Option<f64> {
        self.value.as_ref().map(|(_, at)| *at)
    }
}

/// One host and what has been read from it.
#[derive(Clone, Debug)]
pub struct HostState {
    /// Stable while the app runs, for keys and pages; the list's order can change.
    pub id: u64,
    pub host: Host,
    pub health: Reading<Health>,
    pub procs: Reading<Vec<Process>>,
    pub jvms: Reading<Vec<Jvm>>,
    /// When each kind is next due, on the window clock; 0 reads at once.
    next: [f64; 3],
}

impl HostState {
    fn new(id: u64, host: Host) -> HostState {
        HostState {
            id,
            host,
            health: Reading::default(),
            procs: Reading::default(),
            jvms: Reading::default(),
            next: [0.0; 3],
        }
    }

    /// Whether the last health answer offers `op`; `None` before one landed.
    pub fn offers(&self, op: &str) -> Option<bool> {
        self.health.get().map(|h| h.offers(op))
    }

    fn due(&self, kind: Kind, now: f64) -> bool {
        now >= self.next[kind.index()]
    }
}

struct Landed {
    job: u64,
    host: u64,
    kind: Kind,
    result: Result<Value, Fail>,
}

struct InFlight {
    job: u64,
    host: u64,
    kind: Kind,
    cancel: Arc<AtomicBool>,
}

/// The Hosts page and each host's page.
pub struct Hosts {
    pub remote: Arc<dyn Remote>,
    /// Off in tests: a call runs on the calling thread and lands on the next `tick_hosts`.
    pub threaded: bool,
    /// Where the list is kept; `None` keeps it in memory only (tests and sample scenes).
    pub file: Option<PathBuf>,
    pub list: Vec<HostState>,
    /// Spectra Remote's controller couldn't be started; the pages say how to install it.
    pub missing: Option<String>,
    /// The list file couldn't be read or written.
    pub file_error: Option<String>,
    pub name: TextEdit,
    pub target: TextEdit,
    pub add_error: Option<String>,
    pub query: TextEdit,
    /// Process groups opened on a host's page.
    pub open: HashSet<String>,
    next_id: u64,
    job: u64,
    inflight: Option<InFlight>,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
}

impl Default for Hosts {
    fn default() -> Hosts {
        Hosts::new()
    }
}

impl Hosts {
    pub fn new() -> Hosts {
        let (tx, rx) = channel();
        Hosts {
            remote: Arc::new(Controller::from_env()),
            threaded: true,
            file: None,
            list: Vec::new(),
            missing: None,
            file_error: None,
            name: TextEdit::new(""),
            target: TextEdit::new(""),
            add_error: None,
            query: TextEdit::new(""),
            open: HashSet::new(),
            next_id: 1,
            job: 0,
            inflight: None,
            tx,
            rx,
        }
    }

    /// Reads the list from `file`, which is then where changes are written. A missing file is
    /// an empty list.
    pub fn load(&mut self, file: PathBuf) {
        match std::fs::read_to_string(&file) {
            Ok(s) => match parse_hosts(&s) {
                Ok(hosts) => {
                    for h in hosts {
                        self.push(h);
                    }
                }
                Err(e) => self.file_error = Some(format!("Couldn't read {}: {e}.", file.display())),
            },
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => self.file_error = Some(format!("Couldn't read {}: {e}.", file.display())),
        }
        self.file = Some(file);
    }

    /// Adds a host to the list in memory.
    pub fn push(&mut self, host: Host) -> u64 {
        let id = self.next_id;
        self.next_id += 1;
        self.list.push(HostState::new(id, host));
        id
    }

    pub fn get(&self, id: u64) -> Option<&HostState> {
        self.list.iter().find(|h| h.id == id)
    }

    fn get_mut(&mut self, id: u64) -> Option<&mut HostState> {
        self.list.iter_mut().find(|h| h.id == id)
    }

    pub fn busy(&self, id: u64, kind: Kind) -> bool {
        self.inflight
            .as_ref()
            .is_some_and(|f| f.host == id && f.kind == kind)
    }

    pub fn calling(&self) -> bool {
        self.inflight.is_some()
    }

    /// Stops the call on its way; its answer is dropped and it is asked again next time.
    pub fn cancel(&mut self) {
        if let Some(f) = self.inflight.take() {
            f.cancel.store(true, Ordering::SeqCst);
        }
    }

    /// The search words, lower-cased.
    pub fn search(&self) -> String {
        self.query.text.trim().to_lowercase()
    }

    /// Nothing is due on host `id` until `until`, so a sample scene shows what it was given.
    pub fn sample_rest(&mut self, id: u64, until: f64) {
        if let Some(h) = self.get_mut(id) {
            h.next = [until; 3];
        }
    }

    fn save(&mut self) {
        let Some(file) = &self.file else {
            return;
        };
        let hosts: Vec<Host> = self.list.iter().map(|h| h.host.clone()).collect();
        let written = file
            .parent()
            .map_or(Ok(()), std::fs::create_dir_all)
            .and_then(|_| std::fs::write(file, hosts_json(&hosts)));
        self.file_error = written
            .err()
            .map(|e| format!("Couldn't save the list to {}: {e}.", file.display()));
    }

    /// What to ask next, if anything: on a host's page its first health, then its processes,
    /// its JVMs and its health again as each falls due; on the Hosts page each host's health.
    fn next_call(&self, nav: Nav, now: f64) -> Option<(u64, Kind)> {
        match nav {
            Nav::Host(id) => {
                let h = self.get(id)?;
                if h.health.get().is_none() && h.due(Kind::Health, now) {
                    return Some((id, Kind::Health));
                }
                for kind in [Kind::Procs, Kind::Jvms] {
                    if h.offers(kind.op()) == Some(true) && h.due(kind, now) {
                        return Some((id, kind));
                    }
                }
                h.due(Kind::Health, now).then_some((id, Kind::Health))
            }
            Nav::Hosts => self
                .list
                .iter()
                .find(|h| h.due(Kind::Health, now))
                .map(|h| (h.id, Kind::Health)),
            _ => None,
        }
    }
}

impl App {
    /// Whether the Hosts page or a host's page is what the window shows.
    pub fn hosts_visible(&self) -> bool {
        matches!(self.nav, Nav::Hosts | Nav::Host(_)) && !self.on_page()
    }

    /// Adds the host typed into the form, if it checks out and isn't listed already.
    pub fn add_host(&mut self) {
        let h = &mut self.hosts;
        match check_host(&h.name.text, &h.target.text) {
            Ok(host) if h.list.iter().any(|o| o.host.target == host.target) => {
                h.add_error = Some(format!("{} is in the list already.", host.target));
            }
            Ok(host) => {
                h.push(host);
                h.add_error = None;
                h.name = TextEdit::new("");
                h.target = TextEdit::new("");
                h.save();
            }
            Err(e) => h.add_error = Some(e),
        }
    }

    /// Takes a host off the list (nothing changes on that Mac). Its page, if open, closes.
    pub fn remove_host(&mut self, id: u64) {
        if self.hosts.inflight.as_ref().is_some_and(|f| f.host == id) {
            self.hosts.cancel();
        }
        self.hosts.list.retain(|h| h.id != id);
        self.hosts.save();
        if self.nav == Nav::Host(id) {
            self.show(Nav::Hosts);
        }
    }

    /// Reads a host again now: its health, and on its page its processes and JVMs.
    pub fn refresh_host(&mut self, id: u64) {
        if let Some(h) = self.hosts.get_mut(id) {
            h.next = [0.0; 3];
        }
    }

    /// Opens or closes a process group on a host's page.
    pub fn toggle_host_group(&mut self, id: &str) {
        if !self.hosts.open.remove(id) {
            self.hosts.open.insert(id.to_owned());
        }
    }

    /// Lands answers, stops a call nothing on screen wants, and starts the next one due.
    pub fn tick_hosts(&mut self) -> bool {
        let mut changed = false;
        while let Ok(l) = self.hosts.rx.try_recv() {
            changed |= self.land_host(l);
        }
        let visible = self.hosts_visible() && self.window_shown();
        let wanted = |f: &InFlight, nav: Nav| match nav {
            Nav::Host(id) => f.host == id,
            Nav::Hosts => f.kind == Kind::Health,
            _ => false,
        };
        if let Some(f) = &self.hosts.inflight
            && !(visible && wanted(f, self.nav))
        {
            self.hosts.cancel();
            changed = true;
        }
        if visible
            && self.hosts.inflight.is_none()
            && let Some((id, kind)) = self.hosts.next_call(self.nav, self.now)
        {
            self.start_host_call(id, kind);
            changed = true;
        }
        changed
    }

    fn land_host(&mut self, l: Landed) -> bool {
        if self.hosts.inflight.as_ref().map(|f| f.job) != Some(l.job) {
            return false;
        }
        self.hosts.inflight = None;
        let now = self.now;
        match &l.result {
            Err(f) if f.missing => self.hosts.missing = Some(f.why.clone()),
            _ => self.hosts.missing = None,
        }
        let Some(h) = self.hosts.get_mut(l.host) else {
            return true;
        };
        h.next[l.kind.index()] = now + l.kind.every();
        let result = l.result.map_err(|f| f.why);
        match l.kind {
            Kind::Health => h.health.land(result.and_then(|v| parse_health(&v)), now),
            Kind::Procs => h.procs.land(result.and_then(|v| parse_procs(&v)), now),
            Kind::Jvms => h.jvms.land(result.and_then(|v| parse_jvms(&v)), now),
        }
        true
    }

    fn start_host_call(&mut self, id: u64, kind: Kind) {
        let Some(target) = self.hosts.get(id).map(|h| h.host.target.clone()) else {
            return;
        };
        let wake = self.wake.clone();
        let h = &mut self.hosts;
        h.job += 1;
        let job = h.job;
        let cancel = Arc::new(AtomicBool::new(false));
        h.inflight = Some(InFlight {
            job,
            host: id,
            kind,
            cancel: Arc::clone(&cancel),
        });
        let (remote, tx) = (Arc::clone(&h.remote), h.tx.clone());
        if !h.threaded {
            let result = remote.call(&target, kind.op(), kind.deadline(), &cancel);
            let _ = tx.send(Landed {
                job,
                host: id,
                kind,
                result,
            });
            return;
        }
        // Quit waits for this from now until it's done, even before the thread starts.
        let hold = crate::run::pending();
        let result_tx = Arc::new(Mutex::new(Some(tx.clone())));
        let on_thread = Arc::clone(&result_tx);
        let started = std::thread::Builder::new()
            .name("spectra-remote".into())
            .spawn(move || {
                let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    remote.call(&target, kind.op(), kind.deadline(), &cancel)
                }))
                .unwrap_or_else(|_| Err(Fail::new("the working thread failed")));
                if let Some(tx) = on_thread.lock().ok().and_then(|mut t| t.take()) {
                    let _ = tx.send(Landed {
                        job,
                        host: id,
                        kind,
                        result,
                    });
                }
                drop(hold);
                if let Some(wake) = wake {
                    wake();
                }
            });
        if let Err(e) = started {
            let _ = tx.send(Landed {
                job,
                host: id,
                kind,
                result: Err(Fail::new(format!(
                    "Couldn't start a thread to ask the host: {e}."
                ))),
            });
        }
    }
}

#[cfg(test)]
pub mod tests {
    use super::*;
    use std::collections::HashMap;

    pub const HEALTH: &str = include_str!("../tests/fixtures/remote/health.json");
    pub const HEALTH_OLD: &str = include_str!("../tests/fixtures/remote/health-old.json");
    pub const PROCESSES: &str = include_str!("../tests/fixtures/remote/process-list.json");
    pub const JVMS: &str = include_str!("../tests/fixtures/remote/jvm-list.json");

    /// The `result` of a printed response.
    pub fn result_of(response: &str) -> Value {
        answer(Some(0), response, "", "work-mac:7878").expect("the fixture is a response")
    }

    /// Answers each operation from a table and records every call.
    pub struct Fake {
        pub answers: Mutex<HashMap<String, Result<Value, Fail>>>,
        pub calls: Mutex<Vec<(String, String)>>,
    }

    impl Fake {
        pub fn new() -> Fake {
            Fake {
                answers: Mutex::new(HashMap::new()),
                calls: Mutex::new(Vec::new()),
            }
        }

        pub fn with(self, op: &str, r: Result<Value, Fail>) -> Fake {
            self.answers.lock().unwrap().insert(op.into(), r);
            self
        }

        /// Every operation the current agent offers, answered from the fixtures.
        pub fn full() -> Fake {
            Fake::new()
                .with(OP_HEALTH, Ok(result_of(HEALTH)))
                .with(OP_PROCESSES, Ok(result_of(PROCESSES)))
                .with(OP_JVMS, Ok(result_of(JVMS)))
        }

        pub fn ops(&self) -> Vec<String> {
            self.calls
                .lock()
                .unwrap()
                .iter()
                .map(|c| c.1.clone())
                .collect()
        }
    }

    impl Remote for Fake {
        fn call(&self, target: &str, op: &str, _: Duration, _: &AtomicBool) -> Result<Value, Fail> {
            self.calls.lock().unwrap().push((target.into(), op.into()));
            self.answers
                .lock()
                .unwrap()
                .get(op)
                .cloned()
                .unwrap_or_else(|| Err(Fail::new(format!("unsupported operation {op}"))))
        }
    }

    pub fn fail(why: &str) -> Result<Value, Fail> {
        Err(Fail::new(why))
    }

    #[test]
    fn a_host_takes_the_default_port_and_keeps_its_own() {
        let h = check_host("", "work-mac").unwrap();
        assert_eq!(h.name, "work-mac");
        assert_eq!(h.target, "work-mac:7878");
        let h = check_host(" Build box ", " build.tail1234.ts.net:9000 ").unwrap();
        assert_eq!(h.name, "Build box");
        assert_eq!(h.target, "build.tail1234.ts.net:9000");
        assert_eq!(
            check_host("", "100.64.0.7").unwrap().target,
            "100.64.0.7:7878"
        );
        assert_eq!(
            check_host("", "[fd7a:115c::1]:7000").unwrap().target,
            "[fd7a:115c::1]:7000"
        );
    }

    #[test]
    fn a_host_that_could_be_read_as_a_flag_or_a_path_is_refused() {
        for bad in [
            "",
            "-x",
            "--operation=inspect",
            "work mac",
            "work-mac:0",
            "work-mac:70000",
            "work-mac:port",
            "a/b",
            "fd7a::1",
            "[fd7a::1",
            "[fd7a::1]x",
            ".hidden",
            "host;rm",
        ] {
            assert!(check_host("x", bad).is_err(), "{bad:?} should be refused");
        }
        assert!(check_host(&"n".repeat(65), "work-mac").is_err());
        assert!(check_host("two\nlines", "work-mac").is_err());
    }

    #[test]
    fn the_controller_gets_typed_arguments_only() {
        assert_eq!(
            call_args("work-mac:7878", OP_PROCESSES, Duration::from_secs(60)),
            [
                "call",
                "--target",
                "work-mac:7878",
                "--operation",
                "process.list",
                "--timeout",
                "60s"
            ]
        );
    }

    #[test]
    fn the_list_file_round_trips_and_skips_what_does_not_check_out() {
        let hosts = vec![
            check_host("Work", "work-mac").unwrap(),
            check_host("", "build:9000").unwrap(),
        ];
        assert_eq!(parse_hosts(&hosts_json(&hosts)).unwrap(), hosts);
        let mixed = r#"{"hosts":[{"name":"ok","target":"ok-mac"},{"name":"bad","target":"-x"},{"target":"ok-mac:7878"},{}]}"#;
        assert_eq!(
            parse_hosts(mixed).unwrap(),
            [check_host("ok", "ok-mac").unwrap()]
        );
        assert!(parse_hosts("[]").is_err());
        assert!(parse_hosts("nope").is_err());
    }

    #[test]
    fn loading_and_saving_use_the_file() {
        let dir = std::env::temp_dir().join(format!("spectra-hosts-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let file = dir.join("sub").join("desktop-hosts.json");
        let mut h = Hosts::new();
        h.load(file.clone());
        assert!(
            h.list.is_empty() && h.file_error.is_none(),
            "missing is empty"
        );
        h.push(check_host("Work", "work-mac").unwrap());
        h.save();
        assert!(h.file_error.is_none());
        let mut again = Hosts::new();
        again.load(file.clone());
        assert_eq!(again.list[0].host.target, "work-mac:7878");
        std::fs::write(&file, "{").unwrap();
        let mut broken = Hosts::new();
        broken.load(file);
        assert!(broken.file_error.unwrap().contains("isn't JSON"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn exit_codes_become_plain_reasons() {
        let err = |code, stderr: &str| answer(Some(code), "", stderr, "work-mac:7878").unwrap_err();
        assert_eq!(
            err(1, "dial tcp: no route to host\nmore").why,
            "Couldn't get an answer from work-mac:7878: dial tcp: no route to host"
        );
        assert!(
            err(3, "unsupported result schema")
                .why
                .contains("can't read")
        );
        assert!(
            err(2, "call requires --target")
                .why
                .contains("wouldn't send")
        );
        assert!(!err(1, "x").missing);
        assert!(answer(None, "", "", "t").is_err());
        let e = answer(Some(0), "not json", "", "t").unwrap_err();
        assert!(e.why.contains("Couldn't read"));
        let e = answer(
            Some(0),
            r#"{"protocol_version":"v1","request_id":"a","error":{"code":"timeout","message":"took too long"}}"#,
            "",
            "work-mac:7878",
        )
        .unwrap_err();
        assert_eq!(e.why, "work-mac:7878 answered with an error: took too long");
    }

    #[test]
    fn the_controller_runs_with_its_arguments_and_its_exit_code_counts() {
        let call = |name: &str, body: &str| {
            let bin = crate::run::tests::script(name, body);
            let c = Controller {
                bin: bin.to_string_lossy().into_owned(),
            };
            c.call(
                "work-mac:7878",
                OP_HEALTH,
                Duration::from_secs(5),
                &AtomicBool::new(false),
            )
        };
        // It gets exactly the typed arguments, and its printed response is read.
        let echo = format!(
            r#"[ "$*" = "call --target work-mac:7878 --operation health --timeout 5s" ] || exit 9
cat <<'EOF'
{HEALTH}
EOF"#
        );
        let r = call("remote-ok", &echo).unwrap();
        assert!(parse_health(&r).unwrap().offers(OP_PROCESSES));
        let e = call("remote-down", "echo 'tsnet: no route' >&2; exit 1").unwrap_err();
        assert_eq!(
            e.why,
            "Couldn't get an answer from work-mac:7878: tsnet: no route"
        );
        let missing = Controller {
            bin: "/nonexistent/spectra-remote".into(),
        }
        .call(
            "t:1",
            OP_HEALTH,
            Duration::from_secs(1),
            &AtomicBool::new(false),
        )
        .unwrap_err();
        assert!(missing.missing && missing.why.contains("SPECTRA_REMOTE_BIN"));
        let cancel = AtomicBool::new(true);
        let bin = crate::run::tests::script("remote-hang", "exec sleep 30");
        let c = Controller {
            bin: bin.to_string_lossy().into_owned(),
        };
        let start = std::time::Instant::now();
        assert!(
            c.call("t:1", OP_HEALTH, Duration::from_secs(20), &cancel)
                .is_err()
        );
        assert!(
            start.elapsed() < Duration::from_secs(5),
            "a cancelled call stops"
        );
    }

    /// Needs Spectra Remote and a reachable agent: `SPECTRA_REMOTE_BIN=… SPECTRA_REMOTE_TARGET=host:7878
    /// cargo test -- --ignored remote_live --nocapture`.
    #[test]
    #[ignore]
    fn remote_live() {
        let Ok(target) = std::env::var("SPECTRA_REMOTE_TARGET") else {
            return;
        };
        let c = Controller::from_env();
        let no = AtomicBool::new(false);
        let health = parse_health(
            &c.call(&target, OP_HEALTH, Kind::Health.deadline(), &no)
                .expect("health"),
        )
        .expect("a health answer");
        println!("{target}: {health:?}");
        if health.offers(OP_PROCESSES) {
            let r = c.call(&target, OP_PROCESSES, Kind::Procs.deadline(), &no);
            let procs = parse_procs(&r.expect("process.list")).expect("processes");
            println!("{} processes", procs.len());
            assert!(!procs.is_empty());
        }
        if health.offers(OP_JVMS) {
            let r = c.call(&target, OP_JVMS, Kind::Jvms.deadline(), &no);
            println!(
                "{} JVMs",
                parse_jvms(&r.expect("jvm.list")).expect("jvms").len()
            );
        }
    }

    #[test]
    fn health_says_what_the_agent_offers() {
        let h = parse_health(&result_of(HEALTH)).unwrap();
        assert!(h.offers(OP_PROCESSES) && h.offers(OP_JVMS));
        assert!(!h.spectra_version.is_empty() && !h.agent_version.is_empty());
        let old = parse_health(&result_of(HEALTH_OLD)).unwrap();
        assert!(old.offers(OP_HEALTH) && !old.offers(OP_PROCESSES));
        assert!(parse_health(&json!({})).is_err());
    }

    #[test]
    fn process_and_jvm_lists_read_through_the_local_parsers() {
        let procs = parse_procs(&result_of(PROCESSES)).unwrap();
        assert!(procs.len() > 5);
        assert!(
            procs.windows(2).all(|w| w[0].pid < w[1].pid),
            "sorted by pid"
        );
        let jvms = parse_jvms(&result_of(JVMS)).unwrap();
        assert!(!jvms.is_empty());
        // A bare list as data, and a JVM list with nothing in it.
        let rows = result_of(PROCESSES)["data"]["processes"].clone();
        assert_eq!(
            parse_procs(&json!({"data": rows})).unwrap().len(),
            procs.len()
        );
        assert!(
            parse_jvms(&json!({"data": {"jvms": null}}))
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn a_wrong_schema_or_missing_list_is_refused() {
        let mut r = result_of(PROCESSES);
        r["schema"]["name"] = json!("spectra.inspect");
        assert!(parse_procs(&r).unwrap_err().contains("spectra.inspect"));
        assert!(parse_procs(&json!({"data": {}})).is_err());
        assert!(parse_procs(&json!({})).is_err());
        assert!(
            parse_procs(&json!({"data": {"processes": []}}))
                .unwrap_err()
                .contains("no processes")
        );
    }
}
