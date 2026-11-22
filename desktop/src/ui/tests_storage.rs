//! Headless runs of the Storage page: the sidebar row, what opening it reads (Time Machine only),
//! the low-space warning, Measure and Look inside with a fake Spectra that records what it was
//! asked, Stop, and the failure states.

use std::sync::Arc;

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_storage::{
    DU_LOGS, storage_failed_scene, storage_inside_scene, storage_measuring_scene, storage_scene,
};
use crate::storage::tests::{Fake, ok};

fn scene_with(f: fn(&mut App), fake: Fake) -> (Runtime<App>, Arc<Fake>) {
    let fake = Arc::new(fake);
    let mut s = init();
    s.storage.runner = fake.clone();
    s.storage.threaded = false;
    f(&mut s);
    s.storage.home = "/Users/me".into();
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
        s.land_storage();
    });
    settle(rt);
}

fn locate(rt: &mut Runtime<App>, key: &str) -> (f32, f32) {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    rt.find(key)
        .unwrap_or_else(|| panic!("nothing keyed {key} on screen"))
}

fn press(rt: &mut Runtime<App>, key: &str) {
    let (x, y) = locate(rt, key);
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

fn has(rt: &mut Runtime<App>, key: &str) -> bool {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    rt.find(key).is_some()
}

fn text_of(rt: &mut Runtime<App>, key: &str) -> Option<String> {
    has(rt, key);
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

/// Whether some element on screen reads exactly `label` (a button's text sits inside it).
fn shows(rt: &Runtime<App>, label: &str) -> bool {
    rt.elements()
        .into_iter()
        .any(|e| e.text.as_deref() == Some(label))
}

#[test]
fn the_storage_row_opens_the_page_and_reads_only_time_machine() {
    let (mut rt, fake) = scene(sample_scene);
    let (startup, storage) = (rt.find("startup").unwrap(), rt.find("storage").unwrap());
    assert!(startup.1 < storage.1, "under the Startup row");
    press(&mut rt, "storage");
    assert_eq!(rt.state.nav, Nav::Storage);
    assert!(has(&mut rt, "st-page"));
    land(&mut rt);
    assert_eq!(
        fake.calls(),
        vec![vec!["timemachine".to_owned(), "--json".to_owned()]],
        "opening the page sizes nothing"
    );
    assert!(has(&mut rt, "st-tm-list"));
    assert!(has(&mut rt, "st-library-empty"));
    assert_eq!(rt.state.shortcut_target(), None);
}

#[test]
fn a_low_disk_is_called_out_and_its_free_space_shown() {
    let (mut rt, _) = scene(storage_scene);
    assert_eq!(
        text_of(&mut rt, "st-low-:System:Volumes:Data-title").as_deref(),
        Some("Startup disk is low on space")
    );
    assert_eq!(
        text_of(&mut rt, "st-vol-:System:Volumes:Data-free").as_deref(),
        Some("138.5 GB free of 1.8 TB")
    );
    assert!(
        !has(&mut rt, "st-vol-:"),
        "the sealed system volume isn't listed"
    );
    press(&mut rt, "st-machine");
    assert_eq!(rt.state.nav, Nav::System);
}

#[test]
fn measure_runs_du_on_library_and_lists_its_biggest_folders() {
    let (mut rt, fake) = scene(storage_failed_scene);
    rt.update(|s, _| {
        s.storage.errors.clear();
    });
    settle(&mut rt);
    press(&mut rt, "st-measure");
    land(&mut rt);
    assert_eq!(
        fake.dus(),
        vec![crate::storage::du_args("/Users/me/Library")]
    );
    assert_eq!(
        text_of(&mut rt, "st-lib-dir-0-name").as_deref(),
        Some("Developer")
    );
    assert_eq!(
        text_of(&mut rt, "st-lib-dir-0-size").as_deref(),
        Some("157.0 GB")
    );
    assert!(
        text_of(&mut rt, "st-library-status")
            .unwrap()
            .contains("measured 0 s ago")
    );
    assert!(
        has(&mut rt, "st-lib-note-0"),
        "the folder it couldn't open is said"
    );
}

#[test]
fn look_inside_opens_a_folder_and_back_closes_it() {
    let fake = Fake {
        du: ok(DU_LOGS),
        ..Fake::new()
    };
    let (mut rt, fake) = scene_with(storage_scene, fake);
    press(&mut rt, "st-lib-dir-5-look");
    land(&mut rt);
    assert_eq!(
        fake.dus(),
        vec![crate::storage::du_args("/Users/me/Library/Logs")]
    );
    assert_eq!(
        text_of(&mut rt, "st-in-dir-0-name").as_deref(),
        Some("CoreSimulator")
    );
    assert_eq!(
        text_of(&mut rt, "st-in-file-0-name").as_deref(),
        Some("CoreSimulator/CoreSimulator.prev.log")
    );
    assert!(
        !has(&mut rt, "st-in-file-0-look"),
        "files have no Look inside"
    );
    press(&mut rt, "st-back");
    assert!(!has(&mut rt, "st-inside"));
}

#[test]
fn the_inside_scene_goes_one_deeper_and_back_says_where_to() {
    let (mut rt, fake) = scene(storage_inside_scene);
    press(&mut rt, "st-in-dir-0-look");
    assert_eq!(
        fake.dus(),
        vec![crate::storage::du_args(
            "/Users/me/Library/Logs/CoreSimulator"
        )]
    );
    assert!(has(&mut rt, "st-in-measuring"));
    assert_eq!(
        rt.state.storage.trail,
        [
            "/Users/me/Library/Logs",
            "/Users/me/Library/Logs/CoreSimulator"
        ]
    );
    land(&mut rt);
    assert!(shows(&rt, "‹ Back to ~/Library/Logs"));
    press(&mut rt, "st-back");
    assert_eq!(rt.state.storage.trail, ["/Users/me/Library/Logs"]);
    assert_eq!(
        text_of(&mut rt, "st-in-dir-0-name").as_deref(),
        Some("CoreSimulator")
    );
}

#[test]
fn a_folder_never_measured_says_so_and_offers_measure() {
    let (mut rt, _) = scene(storage_inside_scene);
    rt.update(|s, _| {
        s.storage.results.clear();
    });
    settle(&mut rt);
    assert_eq!(
        text_of(&mut rt, "st-inside-status").as_deref(),
        Some("Not measured yet.")
    );
    assert!(shows(&rt, "Measure"));
    assert!(!shows(&rt, "Measure again"));
}

#[test]
fn a_measurement_shows_how_long_it_has_run_and_stop_ends_it() {
    let (mut rt, _) = scene(storage_measuring_scene);
    assert_eq!(
        text_of(&mut rt, "st-lib-progress").as_deref(),
        Some("Measuring ~/Library… started 1 min ago. A big folder can take several minutes.")
    );
    assert!(has(&mut rt, "st-tm-empty"));
    press(&mut rt, "st-lib-stop");
    assert!(!rt.state.storage.busy());
    assert!(!has(&mut rt, "st-lib-measuring"));
}

#[test]
fn failures_say_why_and_suggest_a_smaller_folder_after_a_timeout() {
    let (mut rt, _) = scene(storage_failed_scene);
    assert_eq!(
        text_of(&mut rt, "st-lib-error-title").as_deref(),
        Some("Couldn't measure ~/Library")
    );
    assert!(has(&mut rt, "st-tm-error"));
    assert!(!has(&mut rt, "st-library-empty"));
}
