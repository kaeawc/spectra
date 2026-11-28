//! Sample states for the menu bar panel's headless scenes and tests (`--window menubar`): the
//! sample Mac with nothing to look at, the same Mac under memory pressure and throttled with
//! "Right now" lines, and the panel before anything was read. Every figure is the sample's own.

use crate::app::{App, init};
use crate::groups::Nav;
use crate::menubar::{MenuBar, snapshot};
use crate::sample_system::{system_pressure_scene, system_scene};
use crate::sample_whatswrong::{CALM, SAMPLE};
use crate::whatswrong::parse;

fn look(s: &mut App, json: &str, age: f64) {
    let r = parse(json).expect("the sample reads");
    s.whatswrong.report = Some((r, s.now - age));
}

/// The sample Mac's readings with nothing found and Spectra seeing nothing slow.
pub fn healthy_app() -> App {
    let mut s = init();
    system_scene(&mut s);
    s.findings.clear();
    look(&mut s, CALM, 3.0);
    s.show(Nav::Overview);
    s
}

/// The sample Mac under critical memory pressure, throttled, its startup disk nearly full, with
/// its findings and "Right now" lines.
pub fn alert_app() -> App {
    let mut s = init();
    system_pressure_scene(&mut s);
    look(&mut s, SAMPLE, 8.0);
    s.show(Nav::Overview);
    s
}

/// The panel on a healthy Mac.
pub fn menubar_scene(m: &mut MenuBar) {
    m.snap = snapshot(&healthy_app());
}

/// The panel while things need a look.
pub fn menubar_alert_scene(m: &mut MenuBar) {
    m.snap = snapshot(&alert_app());
}

/// The panel before the first reading landed.
pub fn menubar_reading_scene(m: &mut MenuBar) {
    m.snap = snapshot(&init());
}
