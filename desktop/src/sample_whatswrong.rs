//! Sample "Right now" for the Overview's headless scenes and tests: what Spectra says about the
//! sample machine (a busy editor and elevated memory pressure), and a machine with nothing
//! wrong. The shape is `spectra whatswrong --json`'s (tests/fixtures/whatswrong holds a real one).

use crate::app::App;
use crate::sample::sample_scene;
use crate::whatswrong::parse;

/// The sample machine as Spectra sees it: the editor's family (rust-analyzer is its busiest
/// member, so the heavy-CPU line about it is the same news), and memory pressure.
pub const SAMPLE: &str = r#"{
  "causes": [
    {"severity": "medium", "title": "Memory pressure is elevated", "detail": "swap in use: 1843 MB"},
    {"severity": "medium", "title": "Visual Studio Code uses 16% of machine CPU",
     "detail": "158% CPU (1.6 cores busy) across 5 processes; heaviest: rust-analyzer (pid 1300, 138%), Code Helper (Plugin) (pid 921, 12%)"},
    {"severity": "medium", "title": "A process is using heavy CPU", "detail": "rust-analyzer (pid 1300) at 138% CPU"}
  ],
  "families": [
    {"key": "app:Visual Studio Code", "kind": "app", "name": "Visual Studio Code", "members": 5,
     "cpu_pct": 157.6, "share_pct": 62.0, "rss_kib": 2412544, "age_seconds": 5400,
     "heaviest": [
       {"pid": 1300, "command": "rust-analyzer", "cpu_pct": 138.0},
       {"pid": 921, "command": "Code Helper (Plugin)", "cpu_pct": 11.5},
       {"pid": 922, "command": "Code Helper (Renderer)", "cpu_pct": 4.0}
     ]}
  ],
  "trend": "load 6.10/5.20/4.80; memory pressure elevated in the last 5 min"
}"#;

/// What Spectra prints when nothing stands out.
pub const CALM: &str = r#"{"causes": null, "signals": {"load": {"memory_pressure": "normal"}}}"#;

fn land(s: &mut App, json: &str, age: f64) {
    let r = parse(json).expect("the sample reads");
    s.whatswrong.report = Some((r, s.now - age));
}

/// The overview with "Right now" from a look 8 s ago.
pub fn right_now_scene(s: &mut App) {
    sample_scene(s);
    land(s, SAMPLE, 8.0);
    s.expanded = None;
}

/// The overview when Spectra sees nothing slowing the machine.
pub fn right_now_clear_scene(s: &mut App) {
    sample_scene(s);
    land(s, CALM, 3.0);
    s.expanded = None;
}
