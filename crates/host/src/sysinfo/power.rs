//! Battery and thermal facts (Go's `power.go` and `linux.go`): `pmset` and `top` on macOS,
//! `/sys/class/power_supply` on Linux. Every source failure is absorbed; partial results are
//! still valid.

use std::path::Path;

use serde::Serialize;
use spectra_base::fs::Fs;
use spectra_base::gojson;
use spectra_base::hostos::Kind;
use spectra_base::proc::Runner;

use crate::cmdrun::succeeded;
use crate::goerr::{NumErrorKind, parse_int};

/// Battery and thermal facts (Go's `PowerState`, `docs/design/system-inventory.md#powerstate`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct PowerState {
    pub on_battery: bool,
    pub battery_pct: i64,
    /// Whether the machine has a battery at all, so a desktop, server or VM is not read as a
    /// battery at 0%. `None` when the power source could not be read.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub has_battery: Option<bool>,
    /// `nominal`, `fair`, `serious` or `critical`.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub thermal_pressure: String,
    pub thermal_throttled: bool,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub cpu_speed_limit_pct: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub lowest_cpu_speed_limit_pct: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub average_cpu_speed_limit_pct: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub percent_thermal_throttled: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub cpu_speed_limit_samples: Vec<i64>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub assertions: Vec<PowerAssertion>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub energy_top_users: Vec<EnergyUser>,
}

/// One entry from `top -l 1 -o power` (Go's `EnergyUser`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct EnergyUser {
    pub pid: i64,
    pub energy_impact: f64,
    pub command: String,
}

/// One `pmset` sleep or display assertion (Go's `PowerAssertion`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct PowerAssertion {
    #[serde(rename = "type")]
    pub kind: String,
    pub pid: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub name: String,
}

/// The raw macOS command output power collection parses (Go's `PowerSource`).
pub trait PowerSource: Send + Sync {
    fn battery(&self) -> Result<Vec<u8>, String>;
    fn thermal(&self) -> Result<Vec<u8>, String>;
    fn assertions(&self) -> Result<Vec<u8>, String>;
    fn energy_top(&self) -> Result<Vec<u8>, String>;
}

/// Shells out to the built-in macOS power tools (Go's `CommandPowerSource`).
pub struct CommandPowerSource<'a> {
    pub runner: &'a dyn Runner,
}

impl CommandPowerSource<'_> {
    fn run(&self, name: &str, args: &[&str]) -> Result<Vec<u8>, String> {
        let (out, err) = crate::cmdrun::bounded(self.runner, name, args);
        err.map_or(Ok(out), Err)
    }
}

impl PowerSource for CommandPowerSource<'_> {
    fn battery(&self) -> Result<Vec<u8>, String> {
        self.run("pmset", &["-g", "batt"])
    }

    fn thermal(&self) -> Result<Vec<u8>, String> {
        self.run("pmset", &["-g", "therm"])
    }

    fn assertions(&self) -> Result<Vec<u8>, String> {
        self.run("pmset", &["-g", "assertions"])
    }

    /// Two samples a second apart: `top` computes the power column as a delta between
    /// consecutive samples, so a single-sample run reports 0.0 for every pid.
    fn energy_top(&self) -> Result<Vec<u8>, String> {
        self.run("top", &["-l", "2", "-s", "1", "-n", "10", "-o", "power", "-stats", "pid,power,command"])
    }
}

/// Go's `CollectAssertions`: only the assertions sub-command, for callers that do not need
/// battery, thermal or `top`. A failure gives an empty list.
pub fn collect_assertions(runner: &dyn Runner) -> Vec<PowerAssertion> {
    match succeeded(runner, "pmset", &["-g", "assertions"]) {
        Some(out) => parse_assertions(&String::from_utf8_lossy(&out)),
        None => Vec::new(),
    }
}

/// Go's `CollectPower`: the Linux power-supply tree through `fs` on Linux, `pmset` and `top`
/// through `runner` elsewhere.
pub fn collect_power(os: Kind, runner: &dyn Runner, fs: &dyn Fs) -> PowerState {
    if os.resolve() == Kind::Linux {
        return collect_power_linux(fs, "/sys/class/power_supply");
    }
    collect(&CommandPowerSource { runner })
}

/// Go's `PowerCollector.Collect`: the four probes run concurrently, so the wall clock is the
/// slowest single probe (about a second for `top -l 2`). A failed probe leaves its fields at
/// their zero values.
pub fn collect(src: &dyn PowerSource) -> PowerState {
    let (batt, therm, assertions, top) = std::thread::scope(|s| {
        let batt = s.spawn(|| src.battery());
        let therm = s.spawn(|| src.thermal());
        let assertions = s.spawn(|| src.assertions());
        let top = s.spawn(|| src.energy_top());
        // A panicking source is a failed probe.
        let ok = |h: std::thread::ScopedJoinHandle<'_, Result<Vec<u8>, String>>| h.join().ok().and_then(Result::ok);
        (ok(batt), ok(therm), ok(assertions), ok(top))
    });
    let text = |b: Option<Vec<u8>>| b.map(|b| String::from_utf8_lossy(&b).into_owned());
    let mut ps = PowerState::default();
    // Go applies each probe as it finishes; the probes touch disjoint fields, so the order is
    // immaterial.
    if let Some(s) = text(batt) {
        parse_batt(&s, &mut ps);
    }
    if let Some(s) = text(therm) {
        parse_therm(&s, &mut ps);
    }
    if let Some(s) = text(assertions) {
        ps.assertions = parse_assertions(&s);
    }
    if let Some(s) = text(top) {
        ps.energy_top_users = parse_energy_top(&s);
    }
    ps
}

/// Go's `parseEnergyTop`: `top -l N -o power -stats pid,power,command` rows. With N above 1
/// there are several sample blocks and only the last counts: `top` computes the power column as
/// a delta from the previous sample, so the first block is all zeros.
pub fn parse_energy_top(out: &str) -> Vec<EnergyUser> {
    let mut result: Vec<EnergyUser> = Vec::new();
    for line in out.split('\n') {
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() < 3 {
            continue;
        }
        if fields[0] == "PID" {
            result.clear();
            continue;
        }
        let Ok(pid) = parse_int(fields[0]) else { continue };
        // Go's ParseFloat reports a range error for an overflow, where Rust rounds to infinity.
        let Some(impact) = fields[1].parse::<f64>().ok().filter(|v| v.is_finite() || fields[1].to_ascii_lowercase().contains("inf")) else {
            continue;
        };
        result.push(EnergyUser { pid, energy_impact: impact, command: fields[2..].join(" ") });
    }
    result
}

/// Go's `parseBatt`: `on_battery` and `battery_pct` from `pmset -g batt`, e.g.
/// `Now drawing from 'Battery Power'` and `-InternalBattery-0 (id=...)  85%; discharging; ...`.
pub fn parse_batt(out: &str, ps: &mut PowerState) {
    ps.has_battery = Some(out.contains("InternalBattery"));
    for line in out.split('\n') {
        if line.contains("Battery Power") {
            ps.on_battery = true;
        }
        if let Some(pct) = extract_pct(line) {
            ps.battery_pct = pct;
        }
    }
}

/// Go's `extractPct`: the number of the first `\d+%` in `line`.
fn extract_pct(line: &str) -> Option<i64> {
    let b = line.as_bytes();
    let mut i = 0;
    while i < b.len() {
        if !b[i].is_ascii_digit() {
            i += 1;
            continue;
        }
        let start = i;
        while i < b.len() && b[i].is_ascii_digit() {
            i += 1;
        }
        if b.get(i) == Some(&b'%') {
            // `n, _ := strconv.Atoi(...)`: a range error still yields the clamped value.
            return Some(match parse_int(&line[start..i]) {
                Ok(n) => n,
                Err(e) if e.kind == NumErrorKind::Range => e.value,
                Err(_) => 0,
            });
        }
    }
    None
}

/// Go's `parseTherm`: thermal pressure and the CPU speed-limit samples from `pmset -g therm`,
/// e.g. `System thermal state: nominal` and `CPU_Speed_Limit = 100`.
pub fn parse_therm(out: &str, ps: &mut PowerState) {
    for line in out.split('\n') {
        let line = line.trim();
        if let Some(rest) = line.strip_prefix("System thermal state:") {
            ps.thermal_pressure = rest.trim().to_string();
            continue;
        }
        // Some macOS versions print `thermal state: X`.
        if let Some(rest) = line.strip_prefix("thermal state:") {
            ps.thermal_pressure = rest.trim().to_string();
            continue;
        }
        if line.contains("CPU_Speed_Limit")
            && let Some(limit) = parse_diagnostic_int(line)
        {
            ps.cpu_speed_limit_samples.push(limit);
        }
    }
    summarize_cpu_speed_limits(ps);
}

fn parse_diagnostic_int(line: &str) -> Option<i64> {
    let (_, value) = line.split_once('=')?;
    parse_int(value.trim()).ok()
}

fn summarize_cpu_speed_limits(ps: &mut PowerState) {
    let samples = &ps.cpu_speed_limit_samples;
    let (Some(&latest), Some(&first)) = (samples.last(), samples.first()) else { return };
    let n = i64::try_from(samples.len()).unwrap_or(i64::MAX);
    let lowest = samples.iter().copied().fold(first, i64::min);
    let sum: i64 = samples.iter().sum();
    let throttled = i64::try_from(samples.iter().filter(|l| **l < 100).count()).unwrap_or(i64::MAX);
    ps.cpu_speed_limit_pct = latest;
    ps.lowest_cpu_speed_limit_pct = lowest;
    ps.average_cpu_speed_limit_pct = sum / n;
    ps.percent_thermal_throttled = (throttled * 100) / n;
    ps.thermal_throttled = throttled > 0;
}

/// RE2's `\s`.
fn is_space(c: u8) -> bool {
    matches!(c, b'\t' | b'\n' | 0x0c | b'\r' | b' ')
}

fn skip(b: &[u8], mut i: usize, pred: impl Fn(u8) -> bool) -> usize {
    while i < b.len() && pred(b[i]) {
        i += 1;
    }
    i
}

/// `pid (\d+)\(` on one line: the pid of the first match.
fn assertion_pid(line: &str) -> Option<i64> {
    let b = line.as_bytes();
    line.match_indices("pid ").find_map(|(at, _)| {
        let start = at + 4;
        let end = skip(b, start, |c| c.is_ascii_digit());
        (end > start && b.get(end) == Some(&b'(')).then(|| match parse_int(&line[start..end]) {
            Ok(n) => n,
            Err(e) => e.value,
        })
    })
}

/// `\]\s+[\d:]+\s+(\w+)` on one line: the word after the assertion id and its age.
fn assertion_type(line: &str) -> Option<&str> {
    let b = line.as_bytes();
    line.match_indices(']').find_map(|(at, _)| {
        let a = skip(b, at + 1, is_space);
        let t = skip(b, a, |c| c.is_ascii_digit() || c == b':');
        let w = skip(b, t, is_space);
        let end = skip(b, w, |c| c.is_ascii_alphanumeric() || c == b'_');
        (a > at + 1 && t > a && w > t && end > w).then(|| &line[w..end])
    })
}

/// `named:\s+"([^"]*)"` on one line.
fn assertion_name(line: &str) -> Option<&str> {
    let b = line.as_bytes();
    line.match_indices("named:").find_map(|(at, _)| {
        let a = skip(b, at + 6, is_space);
        if a == at + 6 || b.get(a) != Some(&b'"') {
            return None;
        }
        let rest = &line[a + 1..];
        rest.find('"').map(|close| &rest[..close])
    })
}

/// Go's `parseAssertions`: the active assertions in `pmset -g assertions`'s per-process
/// listing, e.g. ` pid 412(Slack): [0x000165b8000002f1] 00:00:04 PreventUserIdleSleep named: "audio" `.
pub fn parse_assertions(out: &str) -> Vec<PowerAssertion> {
    out.split('\n')
        .filter_map(|line| {
            let pid = assertion_pid(line)?;
            let kind = assertion_type(line)?.to_string();
            Some(PowerAssertion { kind, pid, name: assertion_name(line).unwrap_or("").to_string() })
        })
        .collect()
}

/// Go's `collectPowerLinux`: battery and AC state from a power-supply tree rooted at `root`.
/// Thermal pressure has no standard Linux equivalent, so those fields stay zero. Best-effort: a
/// missing or unreadable tree yields a zero state with `has_battery` unknown; a readable tree
/// with no Battery supply (servers, VMs) sets it false.
pub fn collect_power_linux(fs: &dyn Fs, root: &str) -> PowerState {
    let mut ps = PowerState::default();
    let Ok(entries) = fs.read_dir(Path::new(root)) else { return ps };
    let (mut ac_online, mut ac_seen, mut battery_seen, mut discharging) = (false, false, false, false);
    let field = |supply: &str, name: &str| -> String {
        fs.read(&Path::new(root).join(supply).join(name)).map(|b| String::from_utf8_lossy(&b).trim().to_string()).unwrap_or_default()
    };
    for e in entries {
        match field(&e.name, "type").as_str() {
            "Mains" | "USB" | "UPS" => {
                ac_seen = true;
                if field(&e.name, "online") == "1" {
                    ac_online = true;
                }
            }
            "Battery" => {
                battery_seen = true;
                if let Ok(pct) = parse_int(&field(&e.name, "capacity")) {
                    ps.battery_pct = pct;
                }
                if field(&e.name, "status").eq_ignore_ascii_case("Discharging") {
                    discharging = true;
                }
            }
            _ => {}
        }
    }
    // On battery when discharging, or when an AC supply exists but none is online. A machine
    // with no battery at all stays off battery.
    ps.on_battery = discharging || (battery_seen && ac_seen && !ac_online);
    ps.has_battery = Some(battery_seen);
    ps
}

#[cfg(test)]
mod tests;
