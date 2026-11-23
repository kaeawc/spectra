//! The whole machine at a glance: memory, thermal state, power, load, disks and network, each
//! read from Spectra on its own schedule off the UI thread, with recent history for the charts.
//!
//! Every reading is parsed field by field from JSON, so a field Spectra leaves out (or a Spectra
//! that names one differently) reads as unknown rather than failing the page. A failed reading
//! keeps the last good one and says so; one Spectra can't take on this system hides its part.

use std::collections::{HashSet, VecDeque};
use std::process::Command;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::time::Duration;

use serde_json::Value;

use crate::app::{App, fmt_ago};
use crate::procs::run_failure;
use crate::run::{Ended, RunError, run_until};

/// How long the memory, thermal and load readings rest between rounds.
pub const FAST: Duration = Duration::from_secs(5);
/// How long the power, network and disk readings rest between rounds.
pub const SLOW: Duration = Duration::from_secs(30);
/// Readings kept for the charts: six minutes at one every 5 s.
pub const SYSTEM_HISTORY: usize = 72;
/// How long one fast reading may take. Load lists every process, about 2 s on a busy machine.
const FAST_DEADLINE: Duration = Duration::from_secs(20);
/// How long one slow reading may take. Power samples for a second; disks run `df` and `mount`.
const SLOW_DEADLINE: Duration = Duration::from_secs(60);
/// Slow rounds the disk reading sits out after one that ran out of time: about five minutes.
const STORAGE_BACKOFF: u32 = 9;
/// Slow rounds the disk reading sits out after one that landed: free space barely moves, and one
/// reading can take most of a minute, so it's read about every five minutes.
const STORAGE_REST: u32 = 9;
/// The most a reading may print.
const MAX_OUTPUT: usize = 8 << 20;
/// `spectra storage` also sizes the home folder's Library, which can take many minutes. Only the
/// volumes are shown here, so it runs with an empty home and the sizing finds nothing to walk.
const EMPTY_HOME: &str = "/var/empty";

/// One of Spectra's machine-wide readings.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Memory,
    Thermal,
    Load,
    Power,
    Storage,
    Network,
}

impl Kind {
    /// The Spectra command, as `spectra <command>`.
    pub fn command(self) -> &'static str {
        match self {
            Kind::Memory => "memory",
            Kind::Thermal => "thermal",
            Kind::Load => "load",
            Kind::Power => "power",
            Kind::Storage => "storage",
            Kind::Network => "network",
        }
    }

    fn args(self) -> Vec<&'static str> {
        match self {
            Kind::Load => vec!["load", "--json", "--top", "1"],
            k => vec![k.command(), "--json"],
        }
    }

    /// Exit codes that carry a reading. `thermal` exits 3 when warm and 4 when throttled; `load`
    /// exits 3 to hold and 4 to back off. Both still print the reading.
    fn reads(self, code: Option<i32>) -> bool {
        matches!(
            (self, code),
            (_, Some(0)) | (Kind::Thermal | Kind::Load, Some(3 | 4))
        )
    }
}

/// Why a reading has no numbers.
#[derive(Clone, Debug, PartialEq)]
pub enum Fail {
    /// Spectra doesn't read this here (memory on Linux, or an older Spectra without the command).
    Unsupported(String),
    Failed(String),
}

impl Fail {
    pub fn why(&self) -> &str {
        match self {
            Fail::Unsupported(w) | Fail::Failed(w) => w,
        }
    }
}

/// The kernel's memory pressure level.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord)]
pub enum Pressure {
    Normal,
    Warning,
    Critical,
    #[default]
    Unknown,
}

impl Pressure {
    fn of(s: &str) -> Pressure {
        match s.trim().to_ascii_lowercase().as_str() {
            "normal" => Pressure::Normal,
            "warning" | "warn" => Pressure::Warning,
            "critical" | "urgent" => Pressure::Critical,
            _ => Pressure::Unknown,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Pressure::Normal => "Normal",
            Pressure::Warning => "Warning",
            Pressure::Critical => "Critical",
            Pressure::Unknown => "Unknown",
        }
    }
}

/// `spectra memory --json`, in bytes; counters are totals since boot.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Memory {
    pub physical: f64,
    pub wired: f64,
    pub free: f64,
    pub file_backed: f64,
    /// Memory apps allocated themselves (anonymous pages).
    pub app: f64,
    pub purgeable: f64,
    /// What the compressor holds, and what that would take uncompressed.
    pub compressed: f64,
    pub compressed_from: f64,
    pub ratio: f64,
    pub swap_total: f64,
    pub swap_used: f64,
    pub pressure: Pressure,
    pub free_pct: Option<f64>,
    pub compressions: f64,
    pub decompressions: f64,
    pub swap_ins: f64,
    pub swap_outs: f64,
}

impl Memory {
    /// What's in use: wired, app memory and the compressor, never more than there is.
    pub fn used(&self) -> f64 {
        let used = self.wired + self.app + self.compressed;
        if self.physical > 0.0 {
            used.min(self.physical)
        } else {
            used
        }
    }
}

/// The thermal verdict `spectra thermal` gives.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ThermalState {
    Nominal,
    Warm,
    Throttled,
    Unknown,
}

impl ThermalState {
    pub fn label(self) -> &'static str {
        match self {
            ThermalState::Nominal => "Nominal",
            ThermalState::Warm => "Warm",
            ThermalState::Throttled => "Throttled",
            ThermalState::Unknown => "Unknown",
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Thermal {
    pub state: ThermalState,
    /// Spectra's own words on what it saw.
    pub evidence: String,
    /// The CPU speed limit, in percent, when the system reports one.
    pub speed_limit: Option<f64>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Load {
    pub load1: f64,
    pub cpus: u32,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Battery {
    /// Spectra leaves the percentage out when it reads 0.
    pub pct: Option<f64>,
    pub on_battery: bool,
}

/// `spectra power --json`: the battery, and the thermal pressure macOS reports with it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Power {
    /// `None` on a machine without one (or where Spectra can't read it).
    pub battery: Option<Battery>,
    pub thermal_pressure: Option<String>,
    pub throttled: bool,
    pub speed_limit: Option<f64>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Volume {
    pub mount: String,
    pub device: String,
    pub fs_type: String,
    pub read_only: bool,
    pub total: f64,
    pub used: f64,
    pub avail: f64,
}

impl Volume {
    /// How full it is, as `df` counts it: used against used plus what's still available. APFS
    /// volumes share their container, so the total says little on its own.
    pub fn used_pct(&self) -> f64 {
        let room = self.used + self.avail;
        if room > 0.0 {
            self.used / room * 100.0
        } else {
            0.0
        }
    }

    /// What a person calls it: the startup disk, an external disk's name, or where it's mounted.
    pub fn label(&self) -> String {
        match self.mount.as_str() {
            "/System/Volumes/Data" => "Startup disk".to_owned(),
            "/" => "Root".to_owned(),
            m => match m.strip_prefix("/Volumes/") {
                Some(name) if !name.contains('/') => name.to_owned(),
                _ => m.to_owned(),
            },
        }
    }

    /// Whether a person keeps files on it. macOS mounts many read-only system and simulator
    /// images (full by design); Linux mounts memory-backed file systems. Neither is listed.
    pub fn is_storage(&self) -> bool {
        const PSEUDO: [&str; 12] = [
            "tmpfs", "devtmpfs", "devfs", "squashfs", "overlay", "efivarfs", "proc", "sysfs",
            "autofs", "nullfs", "ramfs", "cgroup2",
        ];
        const HIDDEN: [&str; 6] = [
            "/System/Volumes/",
            "/private/var/",
            "/Library/Developer/",
            "/dev",
            "/run",
            "/snap/",
        ];
        let m = self.mount.as_str();
        if self.read_only
            || self.total < 1024.0 * 1024.0 * 1024.0
            || PSEUDO.contains(&self.fs_type.as_str())
        {
            return false;
        }
        m == "/System/Volumes/Data"
            || !HIDDEN.iter().any(|h| m.starts_with(h)) && !m.contains("/mounts/")
    }
}

/// One listening socket, or several on one port held by one process (IPv4 and IPv6 of the same
/// port are one row).
#[derive(Clone, Debug, PartialEq)]
pub struct Port {
    pub port: u32,
    pub proto: String,
    pub addrs: Vec<String>,
    pub pid: Option<u32>,
    pub command: String,
    pub user: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Network {
    pub iface: String,
    pub gateway: String,
    pub dns: Vec<String>,
    pub vpn: bool,
    pub vpn_ifaces: Vec<String>,
    pub established: u64,
    /// By port, then protocol.
    pub ports: Vec<Port>,
    /// The system proxies that are set, as `HTTPS proxy.example:8443`.
    pub proxy: Vec<String>,
}

/// One landed reading.
#[derive(Clone, Debug, PartialEq)]
pub enum Reading {
    Memory(Result<Memory, Fail>),
    Thermal(Result<Thermal, Fail>),
    Load(Result<Load, Fail>),
    Power(Result<Power, Fail>),
    Storage(Result<Vec<Volume>, Fail>),
    Network(Result<Network, Fail>),
}

/// Bytes as `512 MB`, `86.8 GB` or `1.8 TB`.
pub fn fmt_size(bytes: f64) -> String {
    let tib = 1024.0_f64.powi(4);
    if bytes >= tib * 0.9995 {
        format!("{:.1} TB", bytes / tib)
    } else {
        crate::procs::fmt_mem(bytes / 1024.0)
    }
}

fn num(v: &Value, key: &str) -> Option<f64> {
    let v = v.get(key)?;
    v.as_f64()
        .or_else(|| v.as_str().and_then(|s| s.trim().parse().ok()))
}

fn n0(v: &Value, key: &str) -> f64 {
    num(v, key).unwrap_or(0.0)
}

fn st(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

fn flag(v: &Value, key: &str) -> bool {
    v.get(key).and_then(Value::as_bool).unwrap_or(false)
}

fn strings(v: &Value, key: &str) -> Vec<String> {
    v.get(key)
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

/// The reading's JSON object, or why it isn't one.
fn object(json: &str, kind: Kind) -> Result<Value, Fail> {
    let v: Value = serde_json::from_str(json.trim()).map_err(|e| {
        Fail::Failed(format!(
            "Couldn't read Spectra's {} output: {e}",
            kind.command()
        ))
    })?;
    if v.is_object() {
        Ok(v)
    } else {
        Err(Fail::Failed(format!(
            "Spectra's {} output wasn't an object",
            kind.command()
        )))
    }
}

pub fn parse_memory(json: &str) -> Result<Memory, Fail> {
    let v = object(json, Kind::Memory)?;
    let swap = v.get("swap").cloned().unwrap_or(Value::Null);
    let m = Memory {
        physical: n0(&v, "physical_bytes"),
        wired: n0(&v, "wired"),
        free: n0(&v, "free"),
        file_backed: n0(&v, "file_backed"),
        app: n0(&v, "anonymous"),
        purgeable: n0(&v, "purgeable"),
        compressed: n0(&v, "compressor_occupied"),
        compressed_from: n0(&v, "compressor_stored"),
        ratio: n0(&v, "compressor_ratio"),
        swap_total: n0(&swap, "total_bytes"),
        swap_used: n0(&swap, "used_bytes"),
        pressure: Pressure::of(&st(&v, "pressure_level")),
        free_pct: num(&v, "pressure_free_percent"),
        compressions: n0(&v, "compressions"),
        decompressions: n0(&v, "decompressions"),
        swap_ins: n0(&v, "swap_ins"),
        swap_outs: n0(&v, "swap_outs"),
    };
    if m.physical <= 0.0 && m.pressure == Pressure::Unknown {
        return Err(Fail::Failed("Spectra's memory reading was empty".into()));
    }
    Ok(m)
}

pub fn parse_thermal(json: &str) -> Result<Thermal, Fail> {
    let v = object(json, Kind::Thermal)?;
    let state = match st(&v, "verdict").to_ascii_lowercase().as_str() {
        "nominal" => ThermalState::Nominal,
        "warm" => ThermalState::Warm,
        "throttled" => ThermalState::Throttled,
        _ => ThermalState::Unknown,
    };
    let limits = v.get("limits").cloned().unwrap_or(Value::Null);
    Ok(Thermal {
        state,
        evidence: st(&v, "evidence"),
        speed_limit: num(&limits, "cpu_speed_limit"),
    })
}

pub fn parse_load(json: &str) -> Result<Load, Fail> {
    let v = object(json, Kind::Load)?;
    let load1 = num(&v, "load1")
        .ok_or_else(|| Fail::Failed("Spectra's load reading had no 1-minute load".into()))?;
    Ok(Load {
        load1,
        cpus: n0(&v, "cpus").max(0.0) as u32,
    })
}

pub fn parse_power(json: &str) -> Result<Power, Fail> {
    let v = object(json, Kind::Power)?;
    let battery = v.get("battery").filter(|b| b.is_object()).map(|b| Battery {
        pct: num(b, "pct"),
        on_battery: flag(b, "on_battery"),
    });
    let thermal = v.get("thermal").cloned().unwrap_or(Value::Null);
    let pressure = st(&thermal, "pressure");
    Ok(Power {
        battery,
        thermal_pressure: (!pressure.is_empty()).then_some(pressure),
        throttled: flag(&thermal, "throttled"),
        speed_limit: num(&thermal, "cpu_speed_limit_pct"),
    })
}

/// Every volume Spectra lists, in its order; [`Volume::is_storage`] picks the ones to show.
pub fn parse_storage(json: &str) -> Result<Vec<Volume>, Fail> {
    let v = object(json, Kind::Storage)?;
    let vols = v
        .get("volumes")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    Ok(vols
        .iter()
        .filter(|x| x.is_object())
        .map(|x| Volume {
            mount: st(x, "mount_point"),
            device: st(x, "device"),
            fs_type: st(x, "fs_type"),
            read_only: flag(x, "read_only"),
            total: n0(x, "total_bytes"),
            used: n0(x, "used_bytes"),
            avail: n0(x, "avail_bytes"),
        })
        .filter(|x| !x.mount.is_empty())
        .collect())
}

pub fn parse_network(json: &str) -> Result<Network, Fail> {
    let v = object(json, Kind::Network)?;
    let raw = v
        .get("listening_ports")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let mut ports: Vec<Port> = Vec::new();
    for p in raw.iter().filter(|p| p.is_object()) {
        let Some(port) = num(p, "port").filter(|n| *n > 0.0).map(|n| n as u32) else {
            continue;
        };
        let pid = num(p, "pid").filter(|n| *n > 0.0).map(|n| n as u32);
        let proto = st(p, "proto");
        let addr = st(p, "local_addr");
        if let Some(same) = ports
            .iter_mut()
            .find(|q| q.port == port && q.proto == proto && q.pid == pid)
        {
            if !addr.is_empty() && !same.addrs.contains(&addr) {
                same.addrs.push(addr);
            }
            continue;
        }
        ports.push(Port {
            port,
            proto,
            addrs: if addr.is_empty() {
                Vec::new()
            } else {
                vec![addr]
            },
            pid,
            command: st(p, "command"),
            user: st(p, "user"),
        });
    }
    ports.sort_by(|a, b| {
        a.port
            .cmp(&b.port)
            .then_with(|| a.proto.cmp(&b.proto))
            .then(a.pid.cmp(&b.pid))
    });
    Ok(Network {
        iface: st(&v, "default_route_iface"),
        gateway: st(&v, "default_route_gw"),
        dns: strings(&v, "dns_servers"),
        vpn: flag(&v, "vpn_active"),
        vpn_ifaces: strings(&v, "vpn_interfaces"),
        established: n0(&v, "established_connections_count").max(0.0) as u64,
        ports,
        proxy: v.get("proxy").map(proxies).unwrap_or_default(),
    })
}

/// The proxies in Spectra's `proxy` object that are set, by scheme.
fn proxies(v: &Value) -> Vec<String> {
    [("http", "HTTP"), ("https", "HTTPS"), ("socks", "SOCKS")]
        .iter()
        .filter_map(|(key, label)| {
            let at = st(v, key);
            let at = at.trim();
            (!at.is_empty()).then(|| format!("{label} {at}"))
        })
        .collect()
}

/// What a run that didn't print a reading means. Spectra says when it can't read something on
/// this system; an older Spectra without the command treats it as a path to inspect.
pub fn classify(bin: &str, kind: Kind, code: Option<i32>, stderr: &str) -> Fail {
    let stderr = stderr.trim();
    let cmd = kind.command();
    let lower = stderr.to_ascii_lowercase();
    if lower.contains("not supported") || lower.contains("unsupported") {
        return Fail::Unsupported(format!("Spectra can't read {cmd} on this system."));
    }
    // A tool Spectra reads through isn't installed here (`pmset` on Linux).
    if lower.contains("executable file not found") {
        return Fail::Unsupported(format!("Spectra can't read {cmd} on this system."));
    }
    if stderr.contains(&format!("stat {cmd}:")) || lower.contains("unknown command") {
        return Fail::Unsupported(format!("This Spectra has no `spectra {cmd}`."));
    }
    match stderr.lines().next() {
        Some(first) => Fail::Failed(format!("{bin} {cmd} failed: {first}")),
        None => Fail::Failed(match code {
            Some(c) => format!("{bin} {cmd} exited with status {c} and printed nothing"),
            None => format!("{bin} {cmd} was stopped by a signal"),
        }),
    }
}

fn parse(kind: Kind, out: Result<String, Fail>) -> Reading {
    match kind {
        Kind::Memory => Reading::Memory(out.and_then(|j| parse_memory(&j))),
        Kind::Thermal => Reading::Thermal(out.and_then(|j| parse_thermal(&j))),
        Kind::Load => Reading::Load(out.and_then(|j| parse_load(&j))),
        Kind::Power => Reading::Power(out.and_then(|j| parse_power(&j))),
        Kind::Storage => Reading::Storage(out.and_then(|j| parse_storage(&j))),
        Kind::Network => Reading::Network(out.and_then(|j| parse_network(&j))),
    }
}

/// Runs `bin <kind> --json` with a deadline and parses what it printed. The flag says whether it
/// ran out of time.
pub fn fetch(bin: &str, kind: Kind, deadline: Duration) -> (Reading, bool) {
    fetch_until(bin, kind, deadline, &CANCEL)
}

/// Set when the app quits: every reading in flight is killed, and the reading threads end.
pub static CANCEL: AtomicBool = AtomicBool::new(false);
/// Set while the window is hidden: the reading threads wait instead of starting a round.
pub static PAUSED: AtomicBool = AtomicBool::new(false);

/// [`fetch`], also killing the reading once `cancel` is set.
pub fn fetch_until(
    bin: &str,
    kind: Kind,
    deadline: Duration,
    cancel: &AtomicBool,
) -> (Reading, bool) {
    let mut cmd = Command::new(bin);
    cmd.args(kind.args());
    if kind == Kind::Storage {
        cmd.env("HOME", EMPTY_HOME)
            .env("XDG_CACHE_HOME", EMPTY_HOME);
    }
    let mut timed_out = false;
    let out = match run_until(cmd, deadline, MAX_OUTPUT, cancel) {
        Err(Ended::Cancelled) => Err(Fail::Failed(format!(
            "{bin} {} was stopped",
            kind.command()
        ))),
        Err(Ended::Failed(e)) => {
            timed_out = matches!(e, RunError::Timeout(_));
            Err(Fail::Failed(run_failure(bin, e)))
        }
        Ok(out) => {
            let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
            if kind.reads(out.status.code()) && !stdout.trim().is_empty() {
                Ok(stdout)
            } else {
                Err(classify(
                    bin,
                    kind,
                    out.status.code(),
                    &String::from_utf8_lossy(&out.stderr),
                ))
            }
        }
    };
    (parse(kind, out), timed_out)
}

/// Starts the two reading threads: memory, thermal and load every `FAST`; power, network and
/// disks every `SLOW`. Each round starts only after the last landed. They stop once the app
/// stops listening.
pub fn spawn(tx: Sender<Reading>, wake: rune::Waker) {
    let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
    start(tx, wake, bin, &|name, job| {
        std::thread::Builder::new()
            .name(name.into())
            .spawn(job)
            .map(|_| ())
    });
}

type Spawner = dyn Fn(&str, Box<dyn FnOnce() + Send>) -> std::io::Result<()>;

/// [`spawn`], with the way a thread starts handed in. A thread that can't start leaves an error
/// on each of its readings, so the page says why instead of reading "Reading…" forever.
fn start(tx: Sender<Reading>, wake: rune::Waker, bin: String, spawner: &Spawner) {
    let rounds: [(&str, &[Kind], Duration, Duration); 2] = [
        (
            "spectra-system-fast",
            &[Kind::Memory, Kind::Thermal, Kind::Load],
            FAST,
            FAST_DEADLINE,
        ),
        (
            "spectra-system-slow",
            &[Kind::Power, Kind::Network, Kind::Storage],
            SLOW,
            SLOW_DEADLINE,
        ),
    ];
    for (name, kinds, rest, deadline) in rounds {
        let (job_tx, job_wake, bin) = (tx.clone(), wake.clone(), bin.clone());
        let job = move || {
            let (tx, wake) = (job_tx, job_wake);
            let mut skip_storage = 0;
            loop {
                while PAUSED.load(Ordering::SeqCst) && !CANCEL.load(Ordering::SeqCst) {
                    std::thread::sleep(Duration::from_millis(500));
                }
                for &kind in kinds {
                    if CANCEL.load(Ordering::SeqCst) {
                        return;
                    }
                    if kind == Kind::Storage && skip_storage > 0 {
                        skip_storage -= 1;
                        continue;
                    }
                    let (reading, timed_out) = fetch(&bin, kind, deadline);
                    if kind == Kind::Storage {
                        skip_storage = if timed_out {
                            STORAGE_BACKOFF
                        } else {
                            STORAGE_REST
                        };
                    }
                    if tx.send(reading).is_err() {
                        return;
                    }
                    wake();
                }
                std::thread::sleep(rest);
            }
        };
        if let Err(e) = spawner(name, Box::new(job)) {
            for &kind in kinds {
                let why = Fail::Failed(format!(
                    "Couldn't start a thread to read {}: {e}.",
                    kind.command()
                ));
                let _ = tx.send(parse(kind, Err(why)));
            }
            wake();
        }
    }
}

/// One reading's latest good value, when it landed, and what went wrong since.
#[derive(Clone, Debug)]
pub struct Section<T> {
    pub last: Option<T>,
    /// The window clock when the last good reading landed.
    pub updated: Option<f64>,
    pub error: Option<Fail>,
    /// How often it's read, for telling when its numbers are getting old.
    pub every: Duration,
}

impl<T> Section<T> {
    fn new(every: Duration) -> Section<T> {
        Section {
            last: None,
            updated: None,
            error: None,
            every,
        }
    }

    /// Takes a reading; a failure keeps the last good one. True when it was good.
    pub fn land(&mut self, r: Result<T, Fail>, now: f64) -> bool {
        match r {
            Ok(v) => {
                self.last = Some(v);
                self.updated = Some(now);
                self.error = None;
                true
            }
            Err(e) => {
                self.error = Some(e);
                false
            }
        }
    }

    /// Whether Spectra doesn't read this here: the part is hidden rather than left blank.
    pub fn unsupported(&self) -> bool {
        self.last.is_none() && matches!(self.error, Some(Fail::Unsupported(_)))
    }

    /// Whether the numbers can't be trusted as current: the last reading failed, or the last
    /// good one is older than three rounds.
    pub fn stale(&self, now: f64) -> bool {
        match self.updated {
            None => true,
            Some(at) => self.error.is_some() || now - at > 3.0 * (self.every.as_secs_f64() + 2.0),
        }
    }

    /// Whether no reading, good or bad, has landed yet.
    pub fn waiting(&self) -> bool {
        self.last.is_none() && self.error.is_none()
    }

    /// When the numbers are from, once that's worth saying: the last reading failed, or none
    /// has landed for three rounds.
    pub fn status(&self, now: f64) -> Option<String> {
        let ago = now - self.updated?;
        let when = format!("Updated {} ago", fmt_ago(ago));
        match &self.error {
            Some(e) => Some(format!(
                "{when} · last reading failed: {}",
                e.why().trim_end_matches('.')
            )),
            None if ago > 3.0 * (self.every.as_secs_f64() + 2.0) => Some(when),
            None => None,
        }
    }
}

/// One memory reading, for the charts.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct MemPoint {
    pub free_pct: f64,
    pub compressed: f64,
    pub swap_used: f64,
    /// Totals since boot; [`counter_deltas`] turns them into per-reading counts.
    pub swap_ins: f64,
    pub swap_outs: f64,
    pub compressions: f64,
}

/// CPU in use (summed over every process, from the process feed) and the 1-minute load, read
/// together every `FAST`.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct CpuPoint {
    /// None when there was no process feed to sum it from.
    pub cpu: Option<f64>,
    pub load1: f64,
    /// The window clock when it landed: readings rest while the window is hidden and can fail,
    /// so they aren't always `FAST` apart.
    pub at: f64,
}

/// What changed between consecutive readings of a counter. A counter that went backwards (the
/// machine restarted, or Spectra's source reset) counts as nothing rather than negative.
pub fn counter_deltas(values: &[f64]) -> Vec<f64> {
    values.windows(2).map(|w| (w[1] - w[0]).max(0.0)).collect()
}

/// Normal memory readings in a row before a pressure finding clears.
pub const CALM_READINGS: u32 = 3;
/// A full volume's finding clears once it is below this many percent.
pub const VOLUME_CLEAR: f64 = 88.0;
/// A volume is full from this many percent.
pub const VOLUME_WARN: f64 = 90.0;

/// Readings swap growth is judged over: a minute at one every 5 s.
pub const SWAP_WINDOW: usize = 13;
/// The least swap must grow over `SWAP_WINDOW` to count as growing.
pub const SWAP_GROWTH: f64 = 64.0 * 1024.0 * 1024.0;

/// Bytes of swap gained over the last `SWAP_WINDOW` readings, when it is in use and grew by at
/// least `SWAP_GROWTH`.
pub fn swap_growth(history: &VecDeque<MemPoint>) -> Option<f64> {
    if history.len() < SWAP_WINDOW {
        return None;
    }
    let first = history[history.len() - SWAP_WINDOW].swap_used;
    let last = history.back()?.swap_used;
    (last > 0.0 && last - first >= SWAP_GROWTH).then_some(last - first)
}

/// Every machine-wide reading and its history.
pub struct System {
    pub memory: Section<Memory>,
    pub thermal: Section<Thermal>,
    pub load: Section<Load>,
    pub power: Section<Power>,
    pub storage: Section<Vec<Volume>>,
    pub network: Section<Network>,
    pub mem_history: VecDeque<MemPoint>,
    pub cpu_history: VecDeque<CpuPoint>,
    /// The memory pressure a finding is showing, and the Normal readings in a row since.
    pub held_pressure: Option<Pressure>,
    pub calm_readings: u32,
    /// Mounts a finding is showing as full.
    pub full_mounts: HashSet<String>,
    pub rx: Option<Receiver<Reading>>,
    pub tx: Option<Sender<Reading>>,
}

impl Default for System {
    fn default() -> System {
        System::new()
    }
}

fn push<T>(h: &mut VecDeque<T>, v: T) {
    h.push_back(v);
    if h.len() > SYSTEM_HISTORY {
        h.pop_front();
    }
}

impl System {
    pub fn new() -> System {
        let (tx, rx) = channel();
        System {
            memory: Section::new(FAST),
            thermal: Section::new(FAST),
            load: Section::new(FAST),
            power: Section::new(SLOW),
            storage: Section::new(SLOW),
            network: Section::new(SLOW),
            mem_history: VecDeque::new(),
            cpu_history: VecDeque::new(),
            held_pressure: None,
            calm_readings: 0,
            full_mounts: HashSet::new(),
            rx: Some(rx),
            tx: Some(tx),
        }
    }

    /// Lands a reading at window time `now`; `cpu` is the CPU in use the process feed last saw,
    /// or `None` when there is no process feed.
    pub fn land(&mut self, r: Reading, now: f64, cpu: impl Into<Option<f64>>) {
        let cpu = cpu.into();
        match r {
            Reading::Memory(m) => {
                if self.memory.land(m, now)
                    && let Some(m) = &self.memory.last
                {
                    let point = MemPoint {
                        free_pct: m.free_pct.unwrap_or(0.0),
                        compressed: m.compressed,
                        swap_used: m.swap_used,
                        swap_ins: m.swap_ins,
                        swap_outs: m.swap_outs,
                        compressions: m.compressions,
                    };
                    push(&mut self.mem_history, point);
                    self.track_pressure(m.pressure);
                }
            }
            Reading::Load(l) => {
                if self.load.land(l, now)
                    && let Some(l) = &self.load.last
                {
                    push(
                        &mut self.cpu_history,
                        CpuPoint {
                            cpu,
                            load1: l.load1,
                            at: now,
                        },
                    );
                }
            }
            Reading::Thermal(t) => {
                self.thermal.land(t, now);
            }
            Reading::Power(p) => {
                self.power.land(p, now);
            }
            Reading::Storage(s) => {
                if self.storage.land(s, now) {
                    self.track_full();
                }
            }
            Reading::Network(n) => {
                self.network.land(n, now);
            }
        }
    }

    /// A pressure finding appears on Warning or Critical and clears after `CALM_READINGS`
    /// Normal readings in a row, so one calm reading doesn't make it flicker.
    fn track_pressure(&mut self, now: Pressure) {
        match now {
            Pressure::Warning | Pressure::Critical => {
                self.held_pressure = Some(now);
                self.calm_readings = 0;
            }
            Pressure::Normal if self.held_pressure.is_some() => {
                self.calm_readings += 1;
                if self.calm_readings >= CALM_READINGS {
                    self.held_pressure = None;
                    self.calm_readings = 0;
                }
            }
            _ => {}
        }
    }

    /// A volume is full above `VOLUME_WARN` percent and stops being full below `VOLUME_CLEAR`.
    fn track_full(&mut self) {
        let mut keep = HashSet::new();
        for v in self.volumes() {
            let used = v.used_pct();
            if used >= VOLUME_WARN || (used >= VOLUME_CLEAR && self.full_mounts.contains(&v.mount))
            {
                keep.insert(v.mount.clone());
            }
        }
        self.full_mounts = keep;
    }

    /// Swap-ins, swap-outs and compressions between the last two memory readings.
    pub fn per_reading(&self) -> Option<(f64, f64, f64)> {
        let n = self.mem_history.len();
        if n < 2 {
            return None;
        }
        let (a, b) = (self.mem_history[n - 2], self.mem_history[n - 1]);
        let d = |x: f64, y: f64| (y - x).max(0.0);
        Some((
            d(a.swap_ins, b.swap_ins),
            d(a.swap_outs, b.swap_outs),
            d(a.compressions, b.compressions),
        ))
    }

    /// The volumes a person keeps files on.
    pub fn volumes(&self) -> Vec<&Volume> {
        self.storage
            .last
            .iter()
            .flatten()
            .filter(|v| v.is_storage())
            .collect()
    }

    /// The thermal state from `spectra thermal`, or from what `spectra power` reported when
    /// the thermal reading has none.
    pub fn thermal_state(&self) -> Option<ThermalState> {
        if let Some(t) = &self.thermal.last {
            return Some(t.state);
        }
        let p = self.power.last.as_ref()?;
        if p.throttled {
            return Some(ThermalState::Throttled);
        }
        p.thermal_pressure.as_ref().map(|_| ThermalState::Nominal)
    }

    /// The CPU speed limit, from either reading.
    pub fn speed_limit(&self) -> Option<f64> {
        self.thermal
            .last
            .as_ref()
            .and_then(|t| t.speed_limit)
            .or_else(|| self.power.last.as_ref().and_then(|p| p.speed_limit))
    }

    /// Every part's status line, to tell when one changes.
    pub fn statuses(&self, now: f64) -> [Option<String>; 6] {
        [
            self.memory.status(now),
            self.thermal.status(now),
            self.load.status(now),
            self.power.status(now),
            self.storage.status(now),
            self.network.status(now),
        ]
    }
}

impl App {
    /// Lands one machine-wide reading and refreshes the findings.
    pub fn land_reading(&mut self, r: Reading) {
        self.system
            .land(r, self.now, self.has_processes().then_some(self.stats.cpu));
        self.refresh_findings();
    }

    /// Lands what the reading threads sent. True when anything landed.
    pub fn land_system(&mut self) -> bool {
        let mut landed = Vec::new();
        while let Some(r) = self.system.rx.as_ref().and_then(|rx| rx.try_recv().ok()) {
            landed.push(r);
        }
        let any = !landed.is_empty();
        for r in landed {
            self.system
                .land(r, self.now, self.has_processes().then_some(self.stats.cpu));
        }
        if any {
            self.refresh_findings();
        }
        any
    }
}

#[cfg(test)]
pub mod tests {
    use super::*;

    /// Trimmed from `spectra memory --json` on a 128 GB Mac.
    pub const MEMORY: &str = r#"{
      "physical_bytes": 137438953472, "page_size_bytes": 16384, "wired": 12144508928,
      "active": 60461907968, "inactive": 59522465792, "speculative": 1153499136,
      "free": 2456305664, "purgeable": 1186545664, "file_backed": 48307650560,
      "anonymous": 72830222336, "compressor_occupied": 1876557824,
      "compressor_stored": 3895721984, "compressor_ratio": 2.0759935740727804,
      "swap": {"total_bytes": 0, "used_bytes": 0, "free_bytes": 0, "encrypted": true},
      "pressure_level": "Normal", "pressure_free_percent": 2.626478672027588,
      "compressions": 536130, "decompressions": 272671, "swap_ins": 0, "swap_outs": 0,
      "collected_at": "2026-11-21T22:11:01.019903Z"
    }"#;

    /// Trimmed from `spectra thermal --json` (exit 0).
    pub const THERMAL: &str = r#"{"os": "darwin", "verdict": "nominal",
      "evidence": "no CPU speed limit or thermal warning recorded", "findings": [], "limits": {},
      "notes": ["pmset printed no CPU limit fields (common on Apple silicon)"], "samples": 1}"#;

    /// Trimmed from `spectra power --json`: no `thermal` block, a battery at 27%.
    pub const POWER: &str = r#"{"interval_ms": 1000, "mode": "baseline",
      "sampled_at": "2026-11-21T17:11:03.937323-05:00", "battery": {"on_battery": true, "pct": 27},
      "assertions": [{"type": "PreventUserIdleSystemSleep", "pid": 22266, "name": "caffeinate command-line tool"}],
      "processes": [{"pid": 547, "command": "mds_stores", "impact": {"total": 55.3}, "source": "top"}]}"#;

    /// Trimmed from `spectra load --json` (exit 3, hold).
    pub const LOAD: &str = r#"{"verdict": "hold", "reason": "1-minute load 50.40 is at or above the limit 24.00 (1.5 x 16 CPUs)",
      "exit_code": 3, "load1": 50.4, "load_limit": 24, "cpus": 16, "available_gb": 58.6,
      "top_cpu": [{"pid": 12105, "command": "storagestate.test", "cpu_pct": 575.6, "rss_kib": 35696}],
      "process_count": 2342}"#;

    /// Trimmed from `spectra storage --json`: the sealed system, the data volume, a simulator
    /// image, an unmounted share and a system helper volume.
    pub const STORAGE: &str = r#"{"volumes": [
      {"device": "/dev/disk3s1s1", "mount_point": "/", "fs_type": "apfs", "read_only": true,
       "total_bytes": 1995218165760, "used_bytes": 16610639872, "avail_bytes": 190168330240},
      {"device": "/dev/disk3s4", "mount_point": "/System/Volumes/Update", "fs_type": "apfs",
       "total_bytes": 1995218165760, "used_bytes": 1032736768, "avail_bytes": 190168330240},
      {"device": "/dev/disk3s5", "mount_point": "/System/Volumes/Data", "fs_type": "apfs",
       "total_bytes": 1995218165760, "used_bytes": 1768009695232, "avail_bytes": 190168330240},
      {"device": "/dev/disk5s1", "mount_point": "/Library/Developer/CoreSimulator/Volumes/iOS_23F77",
       "fs_type": "apfs", "read_only": true, "total_bytes": 24167579648, "used_bytes": 23519797248,
       "avail_bytes": 588173312},
      {"mount_point": "/Volumes/Rex", "total_bytes": 0, "used_bytes": 0, "avail_bytes": 0},
      {"device": "/dev/disk8s1", "mount_point": "/Volumes/Backup", "fs_type": "apfs",
       "total_bytes": 4000000000000, "used_bytes": 1000000000000, "avail_bytes": 3000000000000}
    ], "user_library_bytes": 0, "app_caches_bytes": 0}"#;

    /// Trimmed from `spectra network --json`: IPv4 and IPv6 of one port, two ports of one pid.
    pub const NETWORK: &str = r#"{"default_route_iface": "en0", "default_route_gw": "172.20.0.1",
      "dns_servers": ["172.20.0.1"], "proxy": {},
      "listening_ports": [
        {"port": 7000, "proto": "tcp", "local_addr": "*", "pid": 683, "command": "ControlCe", "user": "me"},
        {"port": 5000, "proto": "tcp", "local_addr": "*", "pid": 683, "command": "ControlCe", "user": "me"},
        {"port": 5583, "proto": "tcp", "local_addr": "127.0.0.1", "pid": 7498, "command": "qemu-syst", "user": "me"},
        {"port": 5583, "proto": "tcp", "local_addr": "[::1]", "pid": 7498, "command": "qemu-syst", "user": "me"},
        {"port": 0, "proto": "udp"},
        "not an object"
      ],
      "established_connections_count": 143, "vpn_active": false}"#;

    #[test]
    fn parses_memory() {
        let m = parse_memory(MEMORY).unwrap();
        assert_eq!(m.pressure, Pressure::Normal);
        assert_eq!(m.physical, 137_438_953_472.0);
        assert_eq!(m.app, 72_830_222_336.0, "anonymous memory is app memory");
        assert_eq!(m.compressed, 1_876_557_824.0);
        assert!((m.ratio - 2.076).abs() < 1e-3);
        assert_eq!((m.swap_used, m.swap_total), (0.0, 0.0));
        assert!((m.free_pct.unwrap() - 2.626).abs() < 1e-3);
        assert_eq!(m.compressions, 536_130.0);
        assert_eq!(
            m.used(),
            12_144_508_928.0 + 72_830_222_336.0 + 1_876_557_824.0
        );
    }

    #[test]
    fn memory_parses_tolerantly() {
        let m = parse_memory(
            r#"{"physical_bytes": "1024", "pressure_level": "Critical", "swap": null}"#,
        )
        .unwrap();
        assert_eq!(
            (m.physical, m.pressure, m.free_pct),
            (1024.0, Pressure::Critical, None)
        );
        assert_eq!(
            parse_memory(r#"{"pressure_level": "WARN"}"#)
                .unwrap()
                .pressure,
            Pressure::Warning
        );
        assert!(
            matches!(parse_memory("{}"), Err(Fail::Failed(_))),
            "an empty reading is no reading"
        );
        assert!(matches!(parse_memory("[1]"), Err(Fail::Failed(_))));
        assert!(matches!(parse_memory("not json"), Err(Fail::Failed(_))));
    }

    #[test]
    fn parses_thermal_load_and_power() {
        let t = parse_thermal(THERMAL).unwrap();
        assert_eq!((t.state, t.speed_limit), (ThermalState::Nominal, None));
        assert!(t.evidence.starts_with("no CPU speed limit"));
        let hot = parse_thermal(r#"{"verdict": "throttled", "limits": {"cpu_speed_limit": 62}}"#)
            .unwrap();
        assert_eq!(
            (hot.state, hot.speed_limit),
            (ThermalState::Throttled, Some(62.0))
        );
        assert_eq!(
            parse_thermal(r#"{"verdict": "melting"}"#).unwrap().state,
            ThermalState::Unknown
        );

        assert_eq!(
            parse_load(LOAD).unwrap(),
            Load {
                load1: 50.4,
                cpus: 16
            }
        );
        assert!(
            parse_load(r#"{"cpus": 4}"#).is_err(),
            "a load reading needs its load"
        );

        let p = parse_power(POWER).unwrap();
        assert_eq!(
            p.battery,
            Some(Battery {
                pct: Some(27.0),
                on_battery: true
            })
        );
        assert_eq!(
            (p.thermal_pressure.as_deref(), p.throttled, p.speed_limit),
            (None, false, None)
        );
        let linux = parse_power(r#"{"interval_ms": 1000, "mode": "baseline"}"#).unwrap();
        assert_eq!(linux.battery, None, "no battery is no battery, not 0%");
        let hot = parse_power(
            r#"{"thermal": {"pressure": "serious", "throttled": true, "cpu_speed_limit_pct": 70}}"#,
        )
        .unwrap();
        assert_eq!(
            (
                hot.thermal_pressure.as_deref(),
                hot.throttled,
                hot.speed_limit
            ),
            (Some("serious"), true, Some(70.0))
        );
    }

    #[test]
    fn storage_lists_only_volumes_people_keep_files_on() {
        let all = parse_storage(STORAGE).unwrap();
        assert_eq!(all.len(), 6);
        let shown: Vec<String> = all
            .iter()
            .filter(|v| v.is_storage())
            .map(Volume::label)
            .collect();
        assert_eq!(shown, ["Startup disk", "Backup"]);
        let data = &all[2];
        assert!(
            (data.used_pct() - 90.29).abs() < 0.01,
            "used against used plus available: {}",
            data.used_pct()
        );
        let linux = Volume {
            mount: "/".into(),
            device: "/dev/sda1".into(),
            fs_type: "ext4".into(),
            read_only: false,
            total: 5e11,
            used: 1e11,
            avail: 4e11,
        };
        assert!(linux.is_storage());
        assert!(
            !Volume {
                fs_type: "tmpfs".into(),
                mount: "/tmp".into(),
                ..linux.clone()
            }
            .is_storage()
        );
        assert!(
            !Volume {
                mount: "/run/user/1000".into(),
                ..linux.clone()
            }
            .is_storage()
        );
        assert_eq!(linux.label(), "Root");
        assert!(
            parse_storage(r#"{"user_library_bytes": 0}"#)
                .unwrap()
                .is_empty(),
            "no volumes is an empty list"
        );
    }

    #[test]
    fn network_merges_one_port_on_both_address_families() {
        let n = parse_network(NETWORK).unwrap();
        assert_eq!(
            (n.iface.as_str(), n.gateway.as_str(), n.established, n.vpn),
            ("en0", "172.20.0.1", 143, false)
        );
        assert_eq!(n.dns, ["172.20.0.1"]);
        let ports: Vec<(u32, Option<u32>, usize)> = n
            .ports
            .iter()
            .map(|p| (p.port, p.pid, p.addrs.len()))
            .collect();
        assert_eq!(
            ports,
            [
                (5000, Some(683), 1),
                (5583, Some(7498), 2),
                (7000, Some(683), 1)
            ],
            "sorted, merged, junk skipped"
        );
        assert_eq!(n.ports[1].addrs, ["127.0.0.1", "[::1]"]);
        let bare = parse_network("{}").unwrap();
        assert!(bare.ports.is_empty() && bare.dns.is_empty());
    }

    #[test]
    fn counters_turn_into_per_reading_deltas_and_a_reset_counts_as_nothing() {
        assert_eq!(
            counter_deltas(&[10.0, 15.0, 15.0, 40.0, 3.0, 5.0]),
            [5.0, 0.0, 25.0, 0.0, 2.0]
        );
        assert!(counter_deltas(&[1.0]).is_empty());

        let mut s = System::new();
        let mem = |swap_ins: f64, compressions: f64| Memory {
            physical: 1.0,
            pressure: Pressure::Normal,
            swap_ins,
            swap_outs: swap_ins / 2.0,
            compressions,
            ..Memory::default()
        };
        assert_eq!(s.per_reading(), None);
        s.land(Reading::Memory(Ok(mem(100.0, 1000.0))), 0.0, 0.0);
        s.land(Reading::Memory(Ok(mem(130.0, 1600.0))), 5.0, 0.0);
        assert_eq!(s.per_reading(), Some((30.0, 15.0, 600.0)));
        s.land(Reading::Memory(Ok(mem(4.0, 20.0))), 10.0, 0.0);
        assert_eq!(s.per_reading(), Some((0.0, 0.0, 0.0)), "the counters reset");
    }

    #[test]
    fn a_failed_reading_keeps_the_last_good_one_and_says_so() {
        let mut s = System::new();
        s.land(Reading::Memory(parse_memory(MEMORY)), 100.0, 0.0);
        assert_eq!(s.memory.status(102.0), None, "fresh");
        s.land(
            Reading::Memory(Err(Fail::Failed("spectra memory failed: boom.".into()))),
            105.0,
            0.0,
        );
        assert!(s.memory.last.is_some(), "the last good reading stays");
        assert_eq!(
            s.mem_history.len(),
            1,
            "a failure adds nothing to the chart"
        );
        assert_eq!(
            s.memory.status(112.0).as_deref(),
            Some("Updated 12 s ago · last reading failed: spectra memory failed: boom")
        );
        s.land(Reading::Memory(parse_memory(MEMORY)), 115.0, 0.0);
        assert_eq!(s.memory.status(116.0), None, "a good reading clears it");
        assert_eq!(
            s.memory.status(160.0).as_deref(),
            Some("Updated 45 s ago"),
            "a hung reading leaves it old"
        );

        let mut never = System::new();
        never.land(
            Reading::Storage(Err(Fail::Failed("took too long".into()))),
            0.0,
            0.0,
        );
        assert_eq!(
            never.storage.status(5.0),
            None,
            "nothing landed yet: the page says why instead"
        );
        assert!(!never.storage.unsupported() && !never.storage.waiting());
    }

    #[test]
    fn what_spectra_cant_read_here_is_unsupported_not_failed() {
        let f = classify(
            "spectra",
            Kind::Memory,
            Some(1),
            "memory state collection not supported\n",
        );
        assert!(matches!(f, Fail::Unsupported(_)));
        let old = classify(
            "spectra",
            Kind::Thermal,
            Some(1),
            "thermal: stat thermal: no such file or directory\n",
        );
        assert!(
            matches!(old, Fail::Unsupported(_)),
            "an older Spectra takes the command for a path"
        );
        let f = classify(
            "spectra",
            Kind::Power,
            Some(1),
            "pmset: exit status 1\nmore",
        );
        assert_eq!(
            f,
            Fail::Failed("spectra power failed: pmset: exit status 1".into())
        );
        assert_eq!(
            classify("spectra", Kind::Network, Some(2), ""),
            Fail::Failed("spectra network exited with status 2 and printed nothing".into())
        );

        let mut s = System::new();
        s.land(Reading::Memory(Err(f.clone())), 0.0, 0.0);
        assert!(!s.memory.unsupported());
        s.land(
            Reading::Memory(Err(Fail::Unsupported("no".into()))),
            0.0,
            0.0,
        );
        assert!(s.memory.unsupported());
    }

    #[test]
    fn swap_counts_as_growing_only_in_use_and_rising_over_a_minute() {
        let gib = 1024.0 * 1024.0 * 1024.0;
        let h = |used: &[f64]| {
            used.iter()
                .map(|u| MemPoint {
                    swap_used: *u,
                    ..MemPoint::default()
                })
                .collect::<VecDeque<_>>()
        };
        assert_eq!(swap_growth(&h(&[gib; 5])), None, "too few readings");
        let flat = h(&[gib; SWAP_WINDOW]);
        assert_eq!(swap_growth(&flat), None, "in use but steady");
        let mut rising: Vec<f64> = (0..SWAP_WINDOW)
            .map(|i| gib + i as f64 * 16.0 * 1024.0 * 1024.0)
            .collect();
        assert_eq!(swap_growth(&h(&rising)), Some(192.0 * 1024.0 * 1024.0));
        rising.insert(0, 0.0);
        assert!(
            swap_growth(&h(&rising)).is_some(),
            "judged over the last minute only"
        );
        assert_eq!(swap_growth(&h(&[0.0; SWAP_WINDOW])), None);
    }

    fn script(name: &str, body: &str) -> std::path::PathBuf {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!(
            "spectra-desktop-system-{}-{name}",
            std::process::id()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("spectra");
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
        path
    }

    #[test]
    fn thermal_exit_3_and_4_are_readings_not_failures() {
        let warm = script(
            "warm",
            r#"echo '{"verdict": "warm", "evidence": "warning level 1"}'; exit 3"#,
        );
        let (r, timed_out) = fetch(
            warm.to_str().unwrap(),
            Kind::Thermal,
            Duration::from_secs(10),
        );
        assert!(!timed_out);
        assert!(
            matches!(
                r,
                Reading::Thermal(Ok(Thermal {
                    state: ThermalState::Warm,
                    ..
                }))
            ),
            "{r:?}"
        );
        let hot = script(
            "hot",
            r#"echo '{"verdict": "throttled", "limits": {"cpu_speed_limit": 55}}'; exit 4"#,
        );
        let (r, _) = fetch(
            hot.to_str().unwrap(),
            Kind::Thermal,
            Duration::from_secs(10),
        );
        assert!(
            matches!(
                r,
                Reading::Thermal(Ok(Thermal {
                    state: ThermalState::Throttled,
                    speed_limit: Some(55.0),
                    ..
                }))
            ),
            "{r:?}"
        );
        let held = script("held", r#"echo '{"load1": 30, "cpus": 8}'; exit 3"#);
        assert!(matches!(
            fetch(held.to_str().unwrap(), Kind::Load, Duration::from_secs(10)).0,
            Reading::Load(Ok(_))
        ));
        // Exit 3 means something else for memory.
        let bad = script(
            "bad",
            r#"echo '{"physical_bytes": 1}'; echo 'went wrong' >&2; exit 3"#,
        );
        let (r, _) = fetch(bad.to_str().unwrap(), Kind::Memory, Duration::from_secs(10));
        assert!(matches!(r, Reading::Memory(Err(Fail::Failed(_)))), "{r:?}");
        let err = script("err", r#"echo 'thermal: no zones' >&2; exit 1"#);
        assert!(matches!(
            fetch(
                err.to_str().unwrap(),
                Kind::Thermal,
                Duration::from_secs(10)
            )
            .0,
            Reading::Thermal(Err(Fail::Failed(_)))
        ));
        for p in [warm, hot, held, bad, err] {
            let _ = std::fs::remove_dir_all(p.parent().unwrap());
        }
    }

    #[test]
    fn a_reading_that_hangs_is_stopped_at_its_deadline() {
        let hang = script("hang", "sleep 5");
        let (r, timed_out) = fetch(
            hang.to_str().unwrap(),
            Kind::Memory,
            Duration::from_millis(200),
        );
        assert!(timed_out);
        assert!(matches!(r, Reading::Memory(Err(Fail::Failed(_)))));
        let _ = std::fs::remove_dir_all(hang.parent().unwrap());
    }

    #[test]
    fn disks_are_read_with_an_empty_home() {
        let echo = script(
            "home",
            r#"printf '{"volumes": [{"mount_point": "%s"}]}' "$HOME""#,
        );
        let (r, _) = fetch(
            echo.to_str().unwrap(),
            Kind::Storage,
            Duration::from_secs(10),
        );
        let Reading::Storage(Ok(v)) = r else {
            panic!("{r:?}")
        };
        assert_eq!(
            v[0].mount, EMPTY_HOME,
            "so Spectra doesn't size the Library folder"
        );
        let _ = std::fs::remove_dir_all(echo.parent().unwrap());
    }

    /// Needs a Spectra with every machine-wide command: `SPECTRA_BIN=… cargo test -- --ignored`.
    #[test]
    #[ignore]
    fn reads_every_live_system_feed() {
        let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
        for kind in [
            Kind::Memory,
            Kind::Thermal,
            Kind::Load,
            Kind::Power,
            Kind::Network,
            Kind::Storage,
        ] {
            let (r, _) = fetch(&bin, kind, Duration::from_secs(120));
            println!("{kind:?}: {r:?}");
            let failed = match &r {
                Reading::Memory(r) => r.as_ref().err().cloned(),
                Reading::Thermal(r) => r.as_ref().err().cloned(),
                Reading::Load(r) => r.as_ref().err().cloned(),
                Reading::Power(r) => r.as_ref().err().cloned(),
                Reading::Storage(r) => r.as_ref().err().cloned(),
                Reading::Network(r) => r.as_ref().err().cloned(),
            };
            assert!(
                !matches!(failed, Some(Fail::Failed(_))),
                "{kind:?} failed: {failed:?}"
            );
        }
    }

    #[test]
    fn setting_the_flag_stops_a_long_reading_promptly() {
        use std::sync::Arc;
        let slow = script("slow", "sleep 30");
        let cancel = Arc::new(AtomicBool::new(false));
        let flag = cancel.clone();
        let t = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(300));
            flag.store(true, Ordering::SeqCst);
        });
        let start = std::time::Instant::now();
        let (r, timed_out) = fetch_until(
            slow.to_str().unwrap(),
            Kind::Memory,
            Duration::from_secs(20),
            &cancel,
        );
        t.join().unwrap();
        assert!(
            start.elapsed() < Duration::from_secs(5),
            "took {:?}",
            start.elapsed()
        );
        assert!(!timed_out);
        assert!(
            matches!(r, Reading::Memory(Err(Fail::Failed(ref w))) if w.contains("stopped")),
            "{r:?}"
        );
    }

    #[test]
    fn a_thread_that_cant_start_leaves_an_error_on_every_section() {
        let (tx, rx) = channel();
        let wake: rune::Waker = std::sync::Arc::new(|| {});
        start(tx, wake, "spectra".into(), &|_, _| {
            Err(std::io::Error::other("out of threads"))
        });
        let mut s = System::new();
        let mut n = 0;
        while let Ok(r) = rx.try_recv() {
            s.land(r, 0.0, None);
            n += 1;
        }
        assert_eq!(n, 6);
        for why in [
            s.memory.error.as_ref(),
            s.thermal.error.as_ref(),
            s.load.error.as_ref(),
            s.power.error.as_ref(),
            s.storage.error.as_ref(),
            s.network.error.as_ref(),
        ] {
            assert!(
                why.is_some_and(|w| w.why().contains("out of threads")),
                "{why:?}"
            );
        }
        assert!(!s.memory.waiting());
    }

    #[test]
    fn no_cpu_point_without_the_process_feed() {
        let mut s = System::new();
        let load = |v| Reading::Load(Ok(Load { load1: v, cpus: 8 }));
        s.land(load(1.0), 0.0, None);
        s.land(load(2.0), 5.0, 40.0);
        let cpus: Vec<_> = s.cpu_history.iter().map(|p| p.cpu).collect();
        assert_eq!(cpus, [None, Some(40.0)]);
    }
}
