//! The documented per-pid energy impact score (Go's `energy_impact.go`).

use std::fmt::Write as _;

use serde::{Deserialize, Serialize};
use spectra_base::gojson::{self, GoDuration};

use super::power::PowerAssertion;
use super::rusage::EnergyDelta;
use super::tasks::TaskPowerSample;
use crate::goerr::parse_int;

/// The per-pid power signal [`compute_impact`] scores: the union of what rusage (billed energy,
/// wakeups, disk) and powermetrics (GPU time) can provide. [`from_rusage`] and
/// [`from_task_sample`] build it from each source.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct ImpactInput {
    pub pid: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub command: String,
    #[serde(rename = "interval_ns", skip_serializing_if = "gojson::is_empty")]
    pub interval: GoDuration,
    #[serde(rename = "billed_energy_j", skip_serializing_if = "gojson::is_empty")]
    pub billed_energy_j: f64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub interrupt_wakeups: u64,
    #[serde(rename = "gpu_time_ns", skip_serializing_if = "gojson::is_empty")]
    pub gpu_time_ns: u64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub disk_bytes: u64,
}

/// The keys of an input record, all optional (a JSON `null` is as good as absent).
#[derive(Deserialize)]
struct Raw {
    pid: Option<i64>,
    command: Option<String>,
    interval_ns: Option<i64>,
    billed_energy_j: Option<f64>,
    interrupt_wakeups: Option<u64>,
    interrupt_wakeups_per_sec: Option<f64>,
    gpu_time_ns: Option<u64>,
    disk_bytes: Option<u64>,
}

impl ImpactInput {
    /// Go's `UnmarshalJSON`. It also accepts `interrupt_wakeups_per_sec`, the key the formula
    /// help printed before it named the real `interrupt_wakeups` field: the rate is turned
    /// back into a count over the interval so [`compute_impact`]'s math is unchanged. The
    /// canonical key wins whenever it is present, even as 0.
    ///
    /// Unlike Go's decoder, key matching is case-sensitive.
    pub fn from_json(data: &[u8]) -> Result<ImpactInput, serde_json::Error> {
        let raw: Raw = serde_json::from_slice(data)?;
        let mut input = ImpactInput {
            pid: raw.pid.unwrap_or(0),
            command: raw.command.unwrap_or_default(),
            interval: GoDuration(raw.interval_ns.unwrap_or(0)),
            billed_energy_j: raw.billed_energy_j.unwrap_or(0.0),
            interrupt_wakeups: 0,
            gpu_time_ns: raw.gpu_time_ns.unwrap_or(0),
            disk_bytes: raw.disk_bytes.unwrap_or(0),
        };
        if let Some(n) = raw.interrupt_wakeups {
            input.interrupt_wakeups = n;
        } else if let Some(rate) = raw.interrupt_wakeups_per_sec.filter(|r| *r > 0.0) {
            let mut count = rate;
            let seconds = duration_seconds(input.interval);
            if seconds > 0.0 {
                count *= seconds;
            }
            // Go: uint64(math.Round(count)), which saturates here rather than being undefined.
            input.interrupt_wakeups = count.round() as u64;
        }
        Ok(input)
    }
}

/// Go's `Duration.Seconds`: whole seconds plus the remainder as a fraction.
fn duration_seconds(d: GoDuration) -> f64 {
    (d.0 / 1_000_000_000) as f64 + (d.0 % 1_000_000_000) as f64 / 1e9
}

/// Go's `FromRusage`: a rusage delta (nanojoules, separate read and write bytes) as the
/// unit-normalised input.
pub fn from_rusage(d: &EnergyDelta) -> ImpactInput {
    ImpactInput {
        pid: d.pid,
        command: d.command.clone(),
        interval: d.interval,
        billed_energy_j: d.billed_energy_nj as f64 / 1e9,
        interrupt_wakeups: d.interrupt_wakeups,
        gpu_time_ns: 0,
        disk_bytes: d.disk_bytes_read.wrapping_add(d.disk_bytes_written),
    }
}

/// Go's `FromTaskSample`: a powermetrics task sample as input. The caller supplies the interval
/// since powermetrics samples do not carry it. Producers with a matching rusage delta should
/// merge the two: rusage owns energy and wakeups, powermetrics owns GPU time.
pub fn from_task_sample(t: &TaskPowerSample, interval: GoDuration) -> ImpactInput {
    ImpactInput { pid: t.pid, command: t.command.clone(), interval, gpu_time_ns: t.gpu_ns, ..ImpactInput::default() }
}

/// What each signal contributes to [`compute_impact`]'s score (Go's `ImpactWeights`).
#[derive(Clone, Copy, Debug, PartialEq, Serialize)]
pub struct ImpactWeights {
    pub billed_energy_per_joule: f64,
    pub interrupt_wakeup_cost: f64,
    pub gpu_ns_cost: f64,
    pub assertion_penalty: f64,
    pub disk_byte_cost: f64,
}

/// Go's `DefaultWeights`, calibrated against `yes > /dev/null` so 1 J of billed energy is
/// about 100 score units (about one core-second on M-series).
pub const DEFAULT_WEIGHTS: ImpactWeights = ImpactWeights {
    billed_energy_per_joule: 100.0,
    interrupt_wakeup_cost: 0.001,
    gpu_ns_cost: 1e-7,
    assertion_penalty: 5.0,
    disk_byte_cost: 1e-9,
};

/// A score attributed to each contributing component, so a caller can show why a pid scored
/// high next to the headline number (Go's `ImpactBreakdown`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct ImpactBreakdown {
    pub total: f64,
    pub from_energy: f64,
    pub from_wakeups: f64,
    pub from_gpu: f64,
    pub from_assertions: f64,
    pub from_io: f64,
}

/// `PowerAssertion.Type` values that block deep idle, so the pmset parser and the impact scorer
/// cannot drift apart silently.
pub const ASSERTION_PREVENT_USER_IDLE_SYSTEM_SLEEP: &str = "PreventUserIdleSystemSleep";
pub const ASSERTION_PREVENT_USER_IDLE_DISPLAY_SLEEP: &str = "PreventUserIdleDisplaySleep";
pub const ASSERTION_PREVENT_USER_IDLE_SLEEP: &str = "PreventUserIdleSleep";
pub const ASSERTION_PREVENT_SYSTEM_SLEEP: &str = "PreventSystemSleep";
pub const ASSERTION_NO_IDLE_SLEEP: &str = "NoIdleSleepAssertion";
pub const ASSERTION_NO_DISPLAY_SLEEP: &str = "NoDisplaySleepAssertion";
pub const ASSERTION_USER_IS_ACTIVE: &str = "UserIsActive";

const IDLE_BLOCKING_ASSERTIONS: [&str; 7] = [
    ASSERTION_PREVENT_USER_IDLE_SYSTEM_SLEEP,
    ASSERTION_PREVENT_USER_IDLE_DISPLAY_SLEEP,
    ASSERTION_PREVENT_USER_IDLE_SLEEP,
    ASSERTION_PREVENT_SYSTEM_SLEEP,
    ASSERTION_NO_IDLE_SLEEP,
    ASSERTION_NO_DISPLAY_SLEEP,
    ASSERTION_USER_IS_ACTIVE,
];

/// Go's `ComputeImpact`: one input's score, plus the assertions held by the process. Pure: no
/// time, env or random terms.
pub fn compute_impact(input: &ImpactInput, assertions: &[PowerAssertion], w: &ImpactWeights) -> ImpactBreakdown {
    let mut b = ImpactBreakdown { from_energy: input.billed_energy_j * w.billed_energy_per_joule, ..ImpactBreakdown::default() };
    let mut wake_rate = input.interrupt_wakeups as f64;
    let seconds = duration_seconds(input.interval);
    if seconds > 0.0 {
        wake_rate /= seconds;
    }
    b.from_wakeups = wake_rate * w.interrupt_wakeup_cost;
    b.from_gpu = input.gpu_time_ns as f64 * w.gpu_ns_cost;
    b.from_io = input.disk_bytes as f64 * w.disk_byte_cost;
    for a in assertions {
        if a.pid == input.pid && IDLE_BLOCKING_ASSERTIONS.contains(&a.kind.as_str()) {
            b.from_assertions += w.assertion_penalty;
        }
    }
    b.total = b.from_energy + b.from_wakeups + b.from_gpu + b.from_assertions + b.from_io;
    b
}

/// One scored input (Go's `ScoredImpact`).
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct ScoredImpact {
    pub input: ImpactInput,
    pub breakdown: ImpactBreakdown,
}

/// Go's `ScoreImpacts`: rows by total descending; ties break by pid ascending so the output is
/// fully reproducible.
pub fn score_impacts(inputs: &[ImpactInput], assertions: &[PowerAssertion], w: &ImpactWeights) -> Vec<ScoredImpact> {
    let mut out: Vec<ScoredImpact> =
        inputs.iter().map(|i| ScoredImpact { input: i.clone(), breakdown: compute_impact(i, assertions, w) }).collect();
    // Go's sort.SliceStable; a NaN total (not reachable from JSON) sorts last so the order stays
    // total.
    out.sort_by(|a, b| {
        let (x, y) = (a.breakdown.total, b.breakdown.total);
        let by_total = match (x.is_nan(), y.is_nan()) {
            (true, true) => std::cmp::Ordering::Equal,
            (true, false) => std::cmp::Ordering::Greater,
            (false, true) => std::cmp::Ordering::Less,
            (false, false) => y.partial_cmp(&x).unwrap_or(std::cmp::Ordering::Equal),
        };
        by_total.then(a.input.pid.cmp(&b.input.pid))
    });
    out
}

/// One tunable knob: the env-spec short name, where it lives in an [`ImpactWeights`], and the
/// help line that documents it. Keeping the three together stops the override parser, the help
/// text and the defaults drifting apart.
struct WeightKey {
    name: &'static str,
    get: fn(&mut ImpactWeights) -> &mut f64,
    help: &'static str,
}

const WEIGHT_KEYS: [WeightKey; 5] = [
    WeightKey {
        name: "energy",
        get: |w| &mut w.billed_energy_per_joule,
        help: "energy     = billed_energy_J            × {}  (per joule)",
    },
    WeightKey {
        name: "wake", get: |w| &mut w.interrupt_wakeup_cost, help: "wakeups    = interrupt_wakeups/interval × {}  (per wakeup/s)"
    },
    WeightKey { name: "gpu", get: |w| &mut w.gpu_ns_cost, help: "gpu        = gpu_time_ns                × {}" },
    WeightKey { name: "assert", get: |w| &mut w.assertion_penalty, help: "assertions = idle_blocking_count        × {}  (additive)" },
    WeightKey { name: "io", get: |w| &mut w.disk_byte_cost, help: "io         = disk_bytes                 × {}" },
];

/// Go's `ParseWeightOverrides`: `key=value,key=value` on top of `base`, which is not changed.
pub fn parse_weight_overrides(spec: &str, base: &ImpactWeights) -> Result<ImpactWeights, String> {
    let mut out = *base;
    let spec = spec.trim();
    if spec.is_empty() {
        return Ok(out);
    }
    for pair in spec.split(',') {
        let pair = pair.trim();
        if pair.is_empty() {
            continue;
        }
        let Some((key, raw)) = pair.split_once('=') else {
            return Err(format!("impact weight {}: expected key=value", go_quote(pair)));
        };
        let key = key.trim();
        let Some(k) = WEIGHT_KEYS.iter().find(|k| k.name == key) else {
            return Err(format!("impact weight {}: unknown key (want energy|wake|gpu|assert|io)", go_quote(key)));
        };
        let v = parse_float(raw.trim()).map_err(|e| format!("impact weight {}: {e}", go_quote(key)))?;
        *(k.get)(&mut out) = v;
    }
    Ok(out)
}

/// `%q` for the plain identifiers and specs these messages carry.
fn go_quote(s: &str) -> String {
    format!("{s:?}")
}

/// `strconv.ParseFloat(s, 64)`: Go's grammar for decimal input, whose range error Rust would
/// round to infinity.
fn parse_float(s: &str) -> Result<f64, String> {
    let quoted = go_quote(s);
    let unsigned = s.strip_prefix(['+', '-']).unwrap_or(s);
    let is_nan = unsigned.eq_ignore_ascii_case("nan");
    if is_nan && unsigned.len() != s.len() {
        return Err(format!("strconv.ParseFloat: parsing {quoted}: invalid syntax"));
    }
    let v: f64 = s.parse().map_err(|_| format!("strconv.ParseFloat: parsing {quoted}: invalid syntax"))?;
    let literal_inf = unsigned.eq_ignore_ascii_case("inf") || unsigned.eq_ignore_ascii_case("infinity");
    if v.is_infinite() && !literal_inf {
        return Err(format!("strconv.ParseFloat: parsing {quoted}: value out of range"));
    }
    Ok(v)
}

/// Go's `LoadWeights`: [`DEFAULT_WEIGHTS`] with any `SPECTRA_IMPACT_WEIGHTS` overrides, read
/// through the injected `getenv`.
pub fn load_weights(getenv: impl Fn(&str) -> String) -> Result<ImpactWeights, String> {
    parse_weight_overrides(&getenv("SPECTRA_IMPACT_WEIGHTS"), &DEFAULT_WEIGHTS)
}

/// Go's `ImpactFormulaHelp`: the formula and the current weights, for `--help`.
pub fn impact_formula_help(w: &ImpactWeights) -> String {
    let mut b = String::from("Energy impact formula (per pid, per sample):\n");
    b.push_str("  score = energy + wakeups + gpu + assertions + io\n");
    b.push_str("\nComponents:\n");
    let mut w = *w;
    for k in &WEIGHT_KEYS {
        let _ = writeln!(b, "  {}", k.help.replace("{}", &format_g(*(k.get)(&mut w))));
    }
    b.push_str("\nInput record keys (--from-json):\n");
    b.push_str("  pid, command, interval_ns, billed_energy_j, interrupt_wakeups, gpu_time_ns, disk_bytes\n");
    b.push_str("\nOverride weights without recompiling via env, e.g.\n");
    b.push_str("  SPECTRA_IMPACT_WEIGHTS=energy=120,wake=0.002,gpu=2e-7,assert=10,io=1e-9\n");
    b
}

/// Go's `%g` of a float64: the shortest text that round-trips, in `%e` form when the decimal
/// exponent is below -4 or at least 6 (the precision `%g` uses for the shortest form).
fn format_g(f: f64) -> String {
    if f.is_nan() {
        return "NaN".into();
    }
    if f.is_infinite() {
        return if f > 0.0 { "+Inf".into() } else { "-Inf".into() };
    }
    let sign = if f.is_sign_negative() { "-" } else { "" };
    if f == 0.0 {
        return format!("{sign}0");
    }
    // Rust's `{:e}` prints the shortest round-tripping digits, as Go's shortest mode does.
    let e = format!("{:e}", f.abs());
    let (mantissa, exp) = e.split_once('e').unwrap_or((&e, "0"));
    let digits: Vec<char> = mantissa.chars().filter(char::is_ascii_digit).collect();
    let exp: i64 = parse_int(exp).unwrap_or(0);
    let nd = i64::try_from(digits.len()).unwrap_or(i64::MAX);
    if !(-4..6).contains(&exp) {
        let mut out = format!("{sign}{}", digits[0]);
        if digits.len() > 1 {
            out.push('.');
            out.extend(&digits[1..]);
        }
        let (esign, eabs) = if exp < 0 { ('-', -exp) } else { ('+', exp) };
        let _ = write!(out, "e{esign}{eabs:02}");
        return out;
    }
    let dp = exp + 1;
    let digit = |i: i64| usize::try_from(i).ok().and_then(|i| digits.get(i).copied()).unwrap_or('0');
    let mut out = String::from(sign);
    if dp > 0 {
        (0..dp).for_each(|i| out.push(digit(i)));
    } else {
        out.push('0');
    }
    if nd - dp > 0 {
        out.push('.');
        (0..nd - dp).for_each(|i| out.push(digit(dp + i)));
    }
    out
}

#[cfg(test)]
mod tests;
