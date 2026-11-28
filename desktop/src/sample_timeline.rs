//! Sample data for the Timeline page's headless scenes and tests, from invented fixtures in the
//! shape of `spectra timeline --json` and `spectra updates --history --json` (tests/fixtures/timeline),
//! timed around the sample's clock: the last 24 hours with the load climbing four minutes ago,
//! a quiet hour, a reading that failed, and one on its way.

use crate::app::App;
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::system::{Load, Reading};
use crate::timeline::{Window, merge, parse_timeline, parse_updates};

pub const TIMELINE: &str = include_str!("../tests/fixtures/timeline/timeline.json");
pub const UPDATES: &str = include_str!("../tests/fixtures/timeline/updates.json");
pub const EMPTY: &str = include_str!("../tests/fixtures/timeline/timeline-empty.json");
pub const UPDATES_EMPTY: &str = include_str!("../tests/fixtures/timeline/updates-empty.json");

/// The page as the sidebar opens it, without running Spectra.
fn timeline_page(s: &mut App, w: Window) {
    sample_scene(s);
    s.select(None);
    s.nav = Nav::Timeline;
    s.timeline.window = w;
    // The sample's Mac is five hours behind UTC, wherever the scene is rendered.
    s.timeline.local = |_| Some(-5 * 3600);
}

fn landed(s: &mut App, w: Window, timeline: &str, updates: &str) {
    let wall = (s.wall)();
    let events = parse_timeline(timeline).expect("the timeline fixture reads");
    let installs = parse_updates(updates).expect("the updates fixture reads");
    let tl = merge(&events, Some(&installs), wall - w.secs());
    s.timeline.data = Some((tl, w, wall));
}

/// The 1-minute load on 10 cores, calm and then high for the last four minutes, filling the
/// six minutes of history the Machine page keeps.
pub fn climbing_load(s: &mut App) {
    let every = s.system.load.every.as_secs_f64();
    let high = (4.0 * 60.0 / every) as usize + 1;
    let calm = crate::system::SYSTEM_HISTORY.saturating_sub(high).max(1);
    // Readings landed `every` apart, the last one now.
    let start = s.now - every * (calm + high - 1) as f64;
    for i in 0..calm + high {
        let load1 = if i < calm { 3.0 } else { 18.0 };
        let at = start + every * i as f64;
        s.system
            .land(Reading::Load(Ok(Load { load1, cpus: 10 })), at, None::<f64>);
    }
}

/// The last 24 hours, the load having climbed four minutes ago.
pub fn timeline_scene(s: &mut App) {
    timeline_page(s, Window::Day);
    climbing_load(s);
    landed(s, Window::Day, TIMELINE, UPDATES);
}

/// A quiet hour: nothing started, logged or installed.
pub fn timeline_empty_scene(s: &mut App) {
    timeline_page(s, Window::Hour);
    landed(s, Window::Hour, EMPTY, UPDATES_EMPTY);
}

/// Reading a week ran out of time.
pub fn timeline_failed_scene(s: &mut App) {
    timeline_page(s, Window::Week);
    s.timeline.error = Some("spectra took longer than 90 s and was stopped".into());
}

/// A week being read for the first time.
pub fn timeline_reading_scene(s: &mut App) {
    timeline_page(s, Window::Week);
    s.timeline.sample_flight();
}
