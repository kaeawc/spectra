//! The live readings an open page takes while it's open: network in and out from
//! `spectra network top --pid`, and energy impact from `spectra impact`. Each is parsed into one
//! point of a short series.

use std::collections::VecDeque;
use std::time::Duration;

use serde::Deserialize;

/// Points kept per series: a few minutes at one every few seconds.
pub const POINTS: usize = 60;
/// How long the page rests after a round lands before the next.
pub const REST: f64 = 2.0;
/// How long network top watches, in seconds.
pub const NET_SECS: u64 = 2;
pub const NET_DEADLINE: Duration = Duration::from_secs(15);
pub const IMPACT_DEADLINE: Duration = Duration::from_secs(10);

/// Asks for every pid's score, so the page's own are among them.
const IMPACT_TOP: &str = "100000";

pub fn net_args(pid: u32) -> Vec<String> {
    ["network", "top", "--pid", &pid.to_string(), "--json", "--duration", &format!("{NET_SECS}s")]
        .into_iter()
        .map(str::to_owned)
        .collect()
}

pub fn impact_args() -> Vec<String> {
    ["impact", "--json", "--top", IMPACT_TOP].into_iter().map(str::to_owned).collect()
}

/// Bytes a second in and out, averaged over the window.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct NetPoint {
    pub in_bps: f64,
    pub out_bps: f64,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct NetTotals {
    avg_in_bytes_per_sec: f64,
    avg_out_bytes_per_sec: f64,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct NetTop {
    samples: u64,
    totals: NetTotals,
}

/// The pid's (and its descendants') traffic over the window.
pub fn parse_net(json: &str) -> Result<NetPoint, String> {
    let t: NetTop = serde_json::from_str(json).map_err(|e| format!("Couldn't read Spectra's output: {e}"))?;
    if t.samples == 0 {
        return Err("Spectra measured no intervals".into());
    }
    Ok(NetPoint { in_bps: t.totals.avg_in_bytes_per_sec, out_bps: t.totals.avg_out_bytes_per_sec })
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct ImpactInput {
    pid: u32,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct Breakdown {
    total: f64,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct Impact {
    input: ImpactInput,
    breakdown: Breakdown,
}

/// The summed energy impact of `pids`, or `None` when Spectra scored none of them this round.
pub fn parse_impact(json: &str, pids: &[u32]) -> Result<Option<f64>, String> {
    let all: Vec<Impact> = serde_json::from_str(json).map_err(|e| format!("Couldn't read Spectra's output: {e}"))?;
    let mine: Vec<f64> = all.iter().filter(|i| pids.contains(&i.input.pid)).map(|i| i.breakdown.total).collect();
    Ok((!mine.is_empty()).then(|| mine.iter().sum()))
}

/// One series' latest points, and why it stopped if it did.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Series<T> {
    pub points: VecDeque<T>,
    /// Set once a reading failed; the page stops asking for this series until it's opened again.
    pub off: Option<String>,
}

impl<T> Series<T> {
    pub fn push(&mut self, v: T) {
        self.points.push_back(v);
        if self.points.len() > POINTS {
            self.points.pop_front();
        }
    }
}

/// A round's readings, as the worker hands them back. `None` is a series not asked for.
#[derive(Clone, Debug, PartialEq)]
pub struct Round {
    pub net: Option<Result<NetPoint, String>>,
    pub energy: Option<Result<Option<f64>, String>>,
}

/// `1.2 MB/s`, `48 KB/s`, `0 B/s`.
pub fn fmt_rate(bps: f64) -> String {
    let b = bps.max(0.0);
    if b < 1.0 {
        "0 B/s".into()
    } else if b < 1000.0 {
        format!("{b:.0} B/s")
    } else if b < 1000.0 * 1024.0 {
        format!("{:.0} KB/s", b / 1024.0)
    } else {
        format!("{:.1} MB/s", b / 1024.0 / 1024.0)
    }
}

pub fn fmt_score(v: f64) -> String {
    if v >= 10.0 { format!("{v:.0}") } else { format!("{v:.1}") }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    /// Shaped like `network top --pid N --json` (limitations shortened).
    pub const NET: &str = r#"{"duration_ms":2037,"interval_ms":1000,"samples":3,"samples_failed":0,"samples_skipped":0,
        "basis":"nettop","remotes_basis":"end-snapshot","pid":4242,
        "totals":{"bytes_in":5794615,"bytes_out":8540,"avg_in_bytes_per_sec":2840731.26,"avg_out_bytes_per_sec":4186.6,
                  "peak_in_bytes_per_sec":3819645.6,"peak_out_bytes_per_sec":5286.4,"peak_total_bytes_per_sec":3824932,"active_processes":1},
        "processes":[],"started":[],"stopped":[],"limitations":["nettop -t external: loopback traffic is not counted"]}"#;
    /// Shaped like `impact --json`.
    pub const IMPACT: &str = r#"[{"input":{"pid":900,"command":"Chat","interval_ns":1000000000,"billed_energy_j":0.0123,"interrupt_wakeups":23},
        "breakdown":{"total":6.25,"from_energy":1.23,"from_wakeups":0.023,"from_gpu":0,"from_assertions":5,"from_io":0}},
        {"input":{"pid":901,"command":"Chat Helper","interval_ns":1000000000},"breakdown":{"total":1.5}},
        {"input":{"pid":77,"command":"other","interval_ns":1000000000},"breakdown":{"total":20.1}}]"#;

    #[test]
    fn network_top_gives_the_average_in_and_out() {
        let p = parse_net(NET).unwrap();
        assert_eq!(p, NetPoint { in_bps: 2840731.26, out_bps: 4186.6 });
        assert_eq!(fmt_rate(p.in_bps), "2.7 MB/s");
        assert_eq!(fmt_rate(p.out_bps), "4 KB/s");
        assert_eq!(fmt_rate(0.2), "0 B/s");
        assert!(parse_net(r#"{"samples":0}"#).is_err(), "nothing measured");
        assert_eq!(net_args(9), ["network", "top", "--pid", "9", "--json", "--duration", "2s"]);
    }

    #[test]
    fn impact_sums_the_pages_pids() {
        assert_eq!(parse_impact(IMPACT, &[900, 901]).unwrap(), Some(7.75));
        assert_eq!(parse_impact(IMPACT, &[900]).unwrap(), Some(6.25));
        assert_eq!(parse_impact(IMPACT, &[5]).unwrap(), None, "not scored this round");
        assert!(parse_impact("{}", &[5]).is_err());
        assert_eq!(fmt_score(7.75), "7.8");
        assert_eq!(fmt_score(20.1), "20");
    }

    #[test]
    fn a_series_keeps_its_latest_points() {
        let mut s = Series::default();
        for i in 0..(POINTS + 5) {
            s.push(i);
        }
        assert_eq!(s.points.len(), POINTS);
        assert_eq!(s.points.front(), Some(&5));
    }
}
