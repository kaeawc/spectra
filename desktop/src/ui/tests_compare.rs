//! Headless runs of the Compare page: the sidebar row, what opening it reads (the list only),
//! choosing and comparing with a fake Spectra that records what it was asked, taking a snapshot
//! only on a press, and the empty and failure states.

use std::sync::Arc;

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::compare::tests::{Fake, failed};
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_compare::{
    compare_diffing_scene, compare_empty_scene, compare_failed_scene, compare_list_scene,
    compare_no_list_scene, compare_scene,
};

fn scene_with(f: fn(&mut App), fake: Fake) -> (Runtime<App>, Arc<Fake>) {
    let fake = Arc::new(fake);
    let mut s = init();
    s.compare.runner = fake.clone();
    s.compare.threaded = false;
    f(&mut s);
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(s, super::view, (1100.0, 700.0), env);
    rt.on_key = Some(super::on_key);
    rt.frame(0.0);
    (rt, fake)
}

fn scene(f: fn(&mut App)) -> (Runtime<App>, Arc<Fake>) {
    scene_with(f, Fake::new())
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

/// Lands what the fake answered, as `poll` would.
fn land(rt: &mut Runtime<App>) {
    rt.update(|s, _| {
        s.land_compare();
    });
    settle(rt);
}

fn has(rt: &mut Runtime<App>, key: &str) -> bool {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    rt.find(key).is_some()
}

fn press(rt: &mut Runtime<App>, key: &str) {
    assert!(has(rt, key), "nothing keyed {key} on screen");
    let (x, y) = rt.find(key).unwrap();
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

fn text_of(rt: &mut Runtime<App>, key: &str) -> Option<String> {
    has(rt, key);
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

const OLDEST: &str = "snap-20260515T011539Z-1007";
const NEWEST: &str = "snap-20261122T121540Z-1001";

#[test]
fn the_compare_row_opens_the_page_and_reads_only_the_list() {
    let (mut rt, fake) = scene(sample_scene);
    let (storage, compare) = (rt.find("storage").unwrap(), rt.find("compare").unwrap());
    assert!(storage.1 < compare.1, "under the Storage row");
    press(&mut rt, "compare");
    assert_eq!(rt.state.nav, Nav::Compare);
    assert!(has(&mut rt, "cmp-page"));
    land(&mut rt);
    assert_eq!(
        fake.calls(),
        vec![vec!["snapshot".to_owned(), "list".into(), "--json".into()]],
        "opening the page compares and takes nothing"
    );
    assert!(has(&mut rt, "cmp-list"));
    assert!(has(&mut rt, &format!("cmp-row-{NEWEST}")));
    assert!(has(&mut rt, "cmp-take"), "Take a snapshot now is offered");
    assert!(!has(&mut rt, "cmp-result"), "nothing compared yet");
    assert_eq!(rt.state.shortcut_target(), None);
}

#[test]
fn picking_an_earlier_snapshot_and_pressing_compare_shows_the_differences() {
    let (mut rt, fake) = scene(compare_list_scene);
    assert!(rt.state.compare.from.is_none());
    press(&mut rt, &format!("cmp-from-{OLDEST}"));
    assert_eq!(rt.state.compare.from.as_deref(), Some(OLDEST));
    assert_eq!(fake.count("diff"), 0, "choosing runs nothing");
    press(&mut rt, "cmp-go");
    land(&mut rt);
    assert_eq!(
        fake.calls().last().unwrap(),
        &["snapshot", "diff", "--json", OLDEST, "live"]
    );
    assert!(has(&mut rt, "cmp-headline"));
    let head = text_of(&mut rt, "cmp-headline").unwrap();
    assert!(
        head.contains("areas") && head.ends_with("to now."),
        "{head}"
    );
    assert_eq!(
        text_of(&mut rt, "cmp-g-apps-title").as_deref(),
        Some("Apps")
    );
    assert_eq!(
        text_of(&mut rt, "cmp-g-apps-summary").as_deref(),
        Some("4 added · 4 removed · 6 changed")
    );
    assert!(has(&mut rt, "cmp-scope"));
    assert!(
        !has(&mut rt, "cmp-g-vpn"),
        "areas with nothing in them are left out"
    );
}

#[test]
fn two_stored_snapshots_can_be_compared() {
    let (mut rt, fake) = scene(compare_list_scene);
    press(&mut rt, &format!("cmp-from-{OLDEST}"));
    press(&mut rt, &format!("cmp-to-{NEWEST}"));
    press(&mut rt, "cmp-go");
    land(&mut rt);
    assert_eq!(
        fake.calls().last().unwrap(),
        &["snapshot", "diff", "--json", OLDEST, NEWEST]
    );
    // Back to now.
    press(&mut rt, "cmp-to-now");
    assert_eq!(rt.state.compare.to, None);
}

#[test]
fn compare_waits_until_an_earlier_one_is_chosen() {
    let (mut rt, fake) = scene(compare_list_scene);
    press(&mut rt, "cmp-go");
    assert_eq!(fake.count("diff"), 0);
    press(&mut rt, &format!("cmp-from-{OLDEST}"));
    press(&mut rt, &format!("cmp-to-{OLDEST}"));
    press(&mut rt, "cmp-go");
    assert_eq!(fake.count("diff"), 0, "not a snapshot against itself");
}

#[test]
fn a_long_group_shows_the_first_lines_and_all_on_a_press() {
    let (mut rt, _) = scene(compare_scene);
    assert!(has(&mut rt, "cmp-g-install_history-3"));
    assert!(
        !has(&mut rt, "cmp-g-apps-8"),
        "apps has 14 lines; eight show"
    );
    assert!(has(&mut rt, "cmp-g-apps-7"));
    press(&mut rt, "cmp-g-apps-toggle");
    assert!(has(&mut rt, "cmp-g-apps-13"));
    press(&mut rt, "cmp-g-apps-toggle");
    assert!(!has(&mut rt, "cmp-g-apps-13"));
}

#[test]
fn a_snapshot_is_taken_only_on_a_press_and_says_where_it_went() {
    let (mut rt, fake) = scene(compare_empty_scene);
    assert_eq!(fake.count("create"), 0, "showing the page writes nothing");
    assert!(has(&mut rt, "cmp-empty"));
    press(&mut rt, "cmp-take");
    land(&mut rt);
    assert_eq!(fake.calls()[0], ["snapshot", "create"]);
    let said = text_of(&mut rt, "cmp-taken").unwrap();
    assert!(
        said.contains(NEWEST) && said.contains("~/.spectra/spectra.db"),
        "{said}"
    );
    land(&mut rt);
    assert_eq!(fake.count("list"), 1, "the new snapshot is listed");
    assert!(has(&mut rt, "cmp-list"));
}

#[test]
fn a_baseline_is_kept_and_named() {
    let (mut rt, fake) = scene(compare_empty_scene);
    press(&mut rt, "cmp-take-baseline");
    land(&mut rt);
    assert_eq!(fake.calls()[0][..3], ["snapshot", "create", "--baseline"]);
    assert!(
        text_of(&mut rt, "cmp-taken")
            .unwrap()
            .starts_with("Saved baseline")
    );
}

#[test]
fn a_snapshot_that_failed_says_why_and_that_nothing_changed() {
    let fake = Fake {
        create: failed(1, "open /Users/me/.spectra/spectra.db: permission denied"),
        ..Fake::new()
    };
    let (mut rt, _) = scene_with(compare_empty_scene, fake);
    press(&mut rt, "cmp-take");
    land(&mut rt);
    assert!(has(&mut rt, "cmp-take-error"));
    let body = rt
        .elements()
        .into_iter()
        .filter_map(|e| e.text)
        .find(|t| t.contains("never runs sudo"))
        .expect("the reason is on screen");
    assert!(body.contains("permission denied") && body.contains("Nothing was changed"));
}

#[test]
fn a_comparison_on_its_way_shows_its_age_and_can_be_stopped() {
    let (mut rt, _) = scene(compare_diffing_scene);
    let said = text_of(&mut rt, "cmp-progress").unwrap();
    assert!(said.starts_with("Comparing… started 1 min ago"), "{said}");
    press(&mut rt, "cmp-stop");
    assert!(rt.state.compare.diffing.is_none());
    assert!(!has(&mut rt, "cmp-progress"));
}

#[test]
fn a_failed_comparison_says_so_and_a_missing_list_says_why() {
    let (mut rt, _) = scene(compare_failed_scene);
    assert!(has(&mut rt, "cmp-diff-error"));
    assert!(has(&mut rt, "cmp-take-error"));
    assert!(has(&mut rt, "cmp-list"), "the list is still there");
    let (mut rt, _) = scene(compare_no_list_scene);
    assert!(has(&mut rt, "cmp-list-error"));
    assert!(!has(&mut rt, "cmp-list"));
    assert!(!has(&mut rt, "cmp-go"));
}

#[test]
fn leaving_the_page_stops_a_comparison() {
    let (mut rt, _) = scene(compare_diffing_scene);
    press(&mut rt, "startup");
    assert!(rt.state.compare.diffing.is_none());
}
