//! Headless runs of the Toolchains page: the sidebar row reads once, a return reads nothing,
//! Refresh reads again, sections open and close, the search narrows rows, the flags, and the
//! empty, failed and reading states, with a fake Spectra.

use std::sync::Arc;

use rune::{Env, Runtime, TextEdit};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_toolchains::{
    FULL, toolchains_empty_scene, toolchains_failed_scene, toolchains_filtered_scene,
    toolchains_reading_scene, toolchains_scene,
};
use crate::toolchains::tests::{Fake, failed, ok};

fn scene_with(f: fn(&mut App), fake: Fake) -> (Runtime<App>, Arc<Fake>) {
    let fake = Arc::new(fake);
    let mut s = init();
    s.toolchains.runner = fake.clone();
    s.toolchains.threaded = false;
    s.toolchains.exists = |_| true;
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
    scene_with(f, Fake::new(ok(FULL)))
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

fn land(rt: &mut Runtime<App>) {
    rt.update(|s, _| {
        s.land_toolchains();
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

/// The section headers on the page, top to bottom, as the person reads them.
fn headers(rt: &mut Runtime<App>) -> Vec<String> {
    rt.elements()
        .into_iter()
        .filter(|e| e.key.as_deref() == Some("trigger"))
        .filter_map(|e| e.label)
        .collect()
}

#[test]
fn the_toolchains_row_opens_the_page_and_reads_once() {
    let (mut rt, fake) = scene(sample_scene);
    let (compare, tc) = (rt.find("compare").unwrap(), rt.find("toolchains").unwrap());
    assert!(compare.1 < tc.1, "after the Compare row");
    press(&mut rt, "toolchains");
    assert_eq!(rt.state.nav, Nav::Toolchains);
    assert!(has(&mut rt, "tc-page"));
    land(&mut rt);
    assert_eq!(fake.count(), 1);
    assert!(has(&mut rt, "tc-sec-jdks"));
    assert_eq!(rt.state.shortcut_target(), None);
    // Away and back: nothing more is read.
    press(&mut rt, "compare");
    press(&mut rt, "toolchains");
    land(&mut rt);
    assert_eq!(fake.count(), 1, "a return to the page doesn't read");
    assert!(has(&mut rt, "tc-sec-jdks"));
    press(&mut rt, "tc-refresh");
    land(&mut rt);
    assert_eq!(fake.count(), 2, "Refresh reads again");
}

#[test]
fn sections_lead_with_java_and_runtimes_and_count_their_rows() {
    let (mut rt, _) = scene(toolchains_scene);
    assert_eq!(
        headers(&mut rt),
        [
            "Java (3)",
            "Node (2)",
            "Python (2)",
            "Go (1)",
            "Ruby (1)",
            "Rust (2)",
            "Build tools (3)",
            "Homebrew formulae (8)",
            "Homebrew casks (2)",
            "Homebrew taps (2)"
        ]
    );
    assert_eq!(
        text_of(&mut rt, "tc-count").as_deref(),
        Some("26 installed")
    );
}

#[test]
fn java_and_runtime_rows_show_and_homebrew_starts_closed_until_pressed() {
    let (mut rt, _) = scene(toolchains_scene);
    assert!(has(&mut rt, "tc-row-jdks-0"));
    assert!(has(&mut rt, "tc-row-node-1"));
    assert!(
        !has(&mut rt, "tc-row-brew_formulae-0"),
        "Homebrew is closed"
    );
    press(&mut rt, "tc-sec-brew_formulae/trigger");
    assert!(has(&mut rt, "tc-row-brew_formulae-0"));
    assert_eq!(
        text_of(&mut rt, "tc-row-brew_formulae-4-marks").as_deref(),
        Some("pinned")
    );
    press(&mut rt, "tc-sec-jdks/trigger");
    assert!(!has(&mut rt, "tc-row-jdks-0"), "Java closed on a press");
    press(&mut rt, "tc-sec-jdks/trigger");
    assert!(has(&mut rt, "tc-row-jdks-0"));
}

#[test]
fn a_search_keeps_matching_rows_opens_their_sections_and_drops_the_rest() {
    let (mut rt, _) = scene(toolchains_filtered_scene);
    assert!(has(&mut rt, "tc-row-jdks-0"));
    assert!(has(&mut rt, "tc-row-jdks-1"));
    assert!(!has(&mut rt, "tc-row-jdks-2"), "Java 21 doesn't match 17");
    assert_eq!(
        headers(&mut rt),
        ["Java (2 of 3)"],
        "only sections with a match remain"
    );
    rt.update(|s, _| s.toolchains.query = TextEdit::new("ripgrep"));
    settle(&mut rt);
    assert!(
        has(&mut rt, "tc-row-brew_formulae-0"),
        "a match opens Homebrew"
    );
    assert_eq!(text_of(&mut rt, "tc-count").as_deref(), Some("1 of 26"));
    rt.update(|s, _| s.toolchains.query = TextEdit::new("zzz"));
    settle(&mut rt);
    let none = text_of(&mut rt, "tc-no-match").unwrap();
    assert!(none.contains("\"zzz\""), "{none}");
}

#[test]
fn several_jdks_of_one_version_are_flagged_on_the_page() {
    let (mut rt, _) = scene(toolchains_scene);
    let f = text_of(&mut rt, "tc-flag-0").unwrap();
    assert!(f.starts_with("Java 17 is installed 2 times"), "{f}");
}

#[test]
fn a_java_home_that_is_gone_is_flagged_on_the_page() {
    let (mut rt, _) = scene(|s| {
        toolchains_scene(s);
        s.toolchains
            .data
            .as_mut()
            .unwrap()
            .0
            .check_java_home(|_| false);
    });
    let flags: Vec<String> = (0..3)
        .filter_map(|i| text_of(&mut rt, &format!("tc-flag-{i}")))
        .collect();
    assert!(
        flags
            .iter()
            .any(|f| f.starts_with("JAVA_HOME points to /Users/user/") && f.contains("isn't there")),
        "{flags:?}"
    );
}

#[test]
fn nothing_found_says_so() {
    let (mut rt, _) = scene(toolchains_empty_scene);
    let t = text_of(&mut rt, "tc-empty").unwrap();
    assert!(t.starts_with("Spectra found no Java installs"), "{t}");
    assert!(!has(&mut rt, "tc-flags"));
}

#[test]
fn a_failed_read_says_why_and_refresh_tries_again() {
    let (mut rt, fake) = scene_with(
        toolchains_failed_scene,
        Fake::new(failed(1, "brew: command not found")),
    );
    assert!(has(&mut rt, "tc-error"));
    let title = text_of(&mut rt, "tc-error-title").unwrap();
    assert_eq!(title, "Couldn't read the toolchains");
    press(&mut rt, "tc-refresh");
    land(&mut rt);
    assert_eq!(fake.count(), 1);
    let (mut rt2, _) = scene(toolchains_failed_scene);
    press(&mut rt2, "tc-refresh");
    land(&mut rt2);
    assert!(!has(&mut rt2, "tc-error"), "a good read clears the failure");
    assert!(has(&mut rt2, "tc-sec-jdks"));
}

#[test]
fn the_first_read_shows_it_is_reading_and_leaves_refresh_off() {
    let (mut rt, fake) = scene(toolchains_reading_scene);
    assert!(has(&mut rt, "tc-reading"));
    assert_eq!(fake.count(), 0);
    assert!(!has(&mut rt, "tc-sec-jdks"));
}
