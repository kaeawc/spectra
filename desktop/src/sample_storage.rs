//! Sample data for the Storage page's headless scenes and tests, from fixtures captured on a Mac
//! (tests/fixtures/storage) with the home folder written as `/Users/me`: the disks, ~/Library
//! measured, a folder looked inside, a measurement on its way, and reads that failed.

use crate::app::App;
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::storage::{Measured, Measuring, parse_du, parse_timemachine};
use crate::system::parse_storage;

pub const STORAGE: &str = include_str!("../tests/fixtures/storage/storage.json");
pub const DU_LIBRARY: &str = include_str!("../tests/fixtures/storage/du-library.json");
pub const DU_LOGS: &str = include_str!("../tests/fixtures/storage/du-logs.json");
pub const TM: &str = include_str!("../tests/fixtures/storage/timemachine.json");
pub const TM_SNAPSHOTS: &str = include_str!("../tests/fixtures/storage/timemachine-snapshots.json");

pub const HOME: &str = "/Users/me";

/// The page as the sidebar opens it, without running Spectra: the disks as the Machine page's
/// reading has them, and nothing measured yet.
fn storage_page(s: &mut App) {
    sample_scene(s);
    s.storage.home = HOME.into();
    let now = s.now;
    s.system.storage.land(parse_storage(STORAGE), now);
    s.select(None);
    s.nav = Nav::Storage;
}

fn measured(s: &mut App, path: &str, json: &str, ago: f64) {
    let du = parse_du(json).expect("the du fixture reads");
    let at = s.now - ago;
    s.storage.results.insert(path.into(), Measured { du, at });
}

/// ~/Library measured ten minutes ago, and three Time Machine snapshots with a backup running.
pub fn storage_scene(s: &mut App) {
    storage_page(s);
    measured(s, "/Users/me/Library", DU_LIBRARY, 600.0);
    s.storage.tm = Some(parse_timemachine(TM_SNAPSHOTS).expect("the timemachine fixture reads"));
}

/// ~/Library being measured for the first time, a minute and a quarter in; no snapshots.
pub fn storage_measuring_scene(s: &mut App) {
    storage_page(s);
    let started = s.now - 75.0;
    s.storage.measuring = Some(Measuring::shown("/Users/me/Library", started));
    s.storage.tm = Some(parse_timemachine(TM).expect("the timemachine fixture reads"));
}

/// Looking inside ~/Library/Logs, reached from ~/Library's rows.
pub fn storage_inside_scene(s: &mut App) {
    storage_scene(s);
    measured(s, "/Users/me/Library/Logs", DU_LOGS, 5.0);
    s.storage.trail = vec!["/Users/me/Library/Logs".into()];
}

/// Measuring ~/Library ran out of time and Time Machine couldn't be read.
pub fn storage_failed_scene(s: &mut App) {
    storage_page(s);
    s.storage.errors.insert(
        "/Users/me/Library".into(),
        "spectra took longer than 900 s and was stopped".into(),
    );
    s.storage.tm_error =
        Some("This Spectra has no `spectra timemachine`. Update Spectra to use it.".into());
}
