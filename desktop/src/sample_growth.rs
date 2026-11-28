//! Sample "Memory and alerts" for headless scenes and tests: memory jumps among the sample
//! machine's processes, the daemon's alert history, and the states with nothing to show. The
//! shapes are `spectra anomalies --json` and the daemon's `alerts.list`
//! (tests/fixtures/growth holds real ones).

use std::sync::Arc;

use crate::app::App;
use crate::groups::Nav;
use crate::growth::{Note, parse_alerts, parse_anomalies};
use crate::inspect::Inert;
use crate::sample::sample_scene;

/// Four processes above their usual memory (the last is gone from the sample machine), one
/// below it, and one that barely moved. Memory is in KiB.
pub const JUMPS: &str = r#"[
  {"key": "4101", "latest": 2516582, "baseline_mean": 1048576, "baseline_stddev": 40000, "z": 36.7, "samples": 42},
  {"key": "1401", "latest": 912384, "baseline_mean": 640000, "baseline_stddev": 38000, "z": 7.2, "samples": 31},
  {"key": "1300", "latest": 704512, "baseline_mean": 655360, "baseline_stddev": 15000, "z": 3.3, "samples": 18},
  {"key": "1200", "latest": 310272, "baseline_mean": 420000, "baseline_stddev": 9000, "z": -12.2, "samples": 25},
  {"key": "1201", "latest": 205824, "baseline_mean": 205000, "baseline_stddev": 40, "z": 20.6, "samples": 25},
  {"key": "9999", "latest": 900000, "baseline_mean": 100000, "baseline_stddev": 1000, "z": 800.0, "samples": 12}
]"#;

/// The daemon's alerts: one critical and one warning going, one acknowledged, and some cleared.
pub const ALERTS: &str = r#"[
  {"id": "e1", "key": "load.high", "severity": "critical", "title": "Host load is critical",
   "detail": "1-minute load 21.4 across 10 CPUs", "state": "firing", "fired_at": "2026-11-20T11:52:10Z"},
  {"id": "e2", "key": "disk.free", "severity": "warn", "title": "Low disk space",
   "detail": "6.1 GB free", "state": "firing", "fired_at": "2026-11-20T11:20:00Z", "acked_at": "2026-11-20T11:41:00Z"},
  {"id": "e3", "key": "load.climbing", "severity": "warn", "title": "Load climbing",
   "detail": "1-minute load 9.8 is rising", "state": "resolved", "fired_at": "2026-11-20T10:05:30Z",
   "resolved_at": "2026-11-20T10:21:00Z"},
  {"id": "e4", "key": "kind.count:gradle", "severity": "warn", "title": "Many gradle processes",
   "detail": "3 processes", "state": "resolved", "fired_at": "2026-11-19T22:14:00Z",
   "resolved_at": "2026-11-19T22:40:00Z"}
]"#;

fn open(s: &mut App) {
    sample_scene(s);
    s.growth.spectra = Arc::new(Inert);
    s.expanded = None;
    s.nav = Nav::Growth;
}

fn land_jumps(s: &mut App, json: &str) {
    let found = parse_anomalies(json).expect("the sample reads");
    s.growth.jumps = Some((found, s.now - 12.0));
}

fn land_alerts(s: &mut App, json: &str) {
    let list = parse_alerts(json).expect("the sample reads");
    s.growth.alerts = Some((list, s.now - 12.0));
}

/// Memory jumps and the alert history.
pub fn growth_scene(s: &mut App) {
    open(s);
    land_jumps(s, JUMPS);
    land_alerts(s, ALERTS);
}

/// Nothing jumping, no alerts yet.
pub fn growth_empty_scene(s: &mut App) {
    open(s);
    land_jumps(s, "null");
    land_alerts(s, "[]");
}

/// Spectra hasn't stored enough process history for a baseline; the alerts read.
pub fn growth_history_scene(s: &mut App) {
    open(s);
    s.growth.jumps_note = Some(Note::NotEnough);
    land_alerts(s, ALERTS);
}

/// Spectra's daemon isn't answering, so there are no alerts; the memory check still read.
pub fn growth_unavailable_scene(s: &mut App) {
    open(s);
    land_jumps(s, JUMPS);
    s.growth.alerts_error = Some(
        "spectra daemon is not running (start it with `spectra daemon start` or install it with `spectra daemon install`)"
            .into(),
    );
}
