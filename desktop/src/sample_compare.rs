//! Sample data for the Compare page's headless scenes and tests, from fixtures captured on a Mac
//! (tests/fixtures/compare) with the home folder written as `/Users/me`: the stored snapshots,
//! a diff between an old one and a newer one, a Mac with no snapshots, and reads that failed.

use crate::app::App;
use crate::compare::{Going, Shown, Taken, parse_diff, parse_list};
use crate::groups::Nav;
use crate::sample::sample_scene;

pub const LIST: &str = include_str!("../tests/fixtures/compare/list.json");
pub const DIFF: &str = include_str!("../tests/fixtures/compare/diff.json");
#[cfg(test)]
pub const DIFF_EMPTY: &str = include_str!("../tests/fixtures/compare/diff-empty.json");
#[cfg(test)]
pub const CREATE: &str = include_str!("../tests/fixtures/compare/create-output.txt");

fn compare_page(s: &mut App) {
    sample_scene(s);
    s.select(None);
    s.nav = Nav::Compare;
}

fn listed(s: &mut App) {
    s.compare.list = Some(parse_list(LIST).expect("the list fixture reads"));
}

/// The stored snapshots, and one just taken here.
pub fn compare_list_scene(s: &mut App) {
    compare_page(s);
    listed(s);
    s.compare.taken = Some(Ok(Taken {
        id: "snap-20261122T121540Z-1001".into(),
        baseline: false,
    }));
}

/// The oldest snapshot compared with now.
pub fn compare_scene(s: &mut App) {
    compare_page(s);
    listed(s);
    let from = s
        .compare
        .list
        .as_ref()
        .and_then(|l| l.last())
        .map(|o| o.id.clone());
    s.compare.from = from.clone();
    s.compare.shown = Some(Shown {
        from: from.unwrap_or_default(),
        to: None,
        diff: parse_diff(DIFF).expect("the diff fixture reads"),
        at: s.now - 20.0,
    });
    // The two biggest areas are open all the way.
    s.compare.expanded.insert("brew_formulae".into());
}

/// A comparison on its way, a minute in.
pub fn compare_diffing_scene(s: &mut App) {
    compare_page(s);
    listed(s);
    s.compare.from = s
        .compare
        .list
        .as_ref()
        .and_then(|l| l.get(3))
        .map(|o| o.id.clone());
    s.compare.diffing = Some(Going::shown(s.now - 62.0));
}

/// No snapshot stored yet.
pub fn compare_empty_scene(s: &mut App) {
    compare_page(s);
    s.compare.list = Some(Vec::new());
}

/// The list read, a diff and a snapshot all failed.
pub fn compare_failed_scene(s: &mut App) {
    compare_page(s);
    listed(s);
    s.compare.from = s
        .compare
        .list
        .as_ref()
        .and_then(|l| l.get(1))
        .map(|o| o.id.clone());
    s.compare.diff_error = Some("snapshot \"snap-20260515T011539Z-1007\": not found".into());
    s.compare.taken = Some(Err(
        "Spectra took the snapshot but couldn't save it (could not persist snapshot: unable to open database file)."
            .into(),
    ));
}

/// Spectra couldn't be asked at all.
pub fn compare_no_list_scene(s: &mut App) {
    compare_page(s);
    s.compare.list_error =
        Some("This Spectra has no `spectra snapshot list`. Update Spectra to use it.".into());
}
