//! Headless runs of the Memory and alerts page: the sidebar row, the memory jumps and their
//! links, the alert history, Acknowledge with a fake Spectra that records what it was asked,
//! and the states with nothing to show.

use std::sync::{Arc, Mutex};

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::growth::tests::{ALERTS, ANOMALIES};
use crate::inspect::tests::Fake;
use crate::sample_growth::{
    growth_empty_scene, growth_history_scene, growth_scene, growth_unavailable_scene,
};

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(s, super::view, (1100.0, 800.0), env);
    rt.on_key = Some(super::on_key);
    rt.frame(0.0);
    rt
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

fn press(rt: &mut Runtime<App>, key: &str) {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    let (x, y) = rt
        .find(key)
        .unwrap_or_else(|| panic!("nothing keyed {key} on screen"));
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

fn has(rt: &mut Runtime<App>, key: &str) -> bool {
    settle(rt);
    rt.find(key).is_some()
}

fn text_of(rt: &Runtime<App>, key: &str) -> Option<String> {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

#[test]
fn the_sidebar_row_opens_the_page_and_looks_at_once() {
    let mut rt = scene(crate::sample::sample_scene);
    press(&mut rt, "growth");
    assert_eq!(rt.state.nav, Nav::Growth);
    assert_eq!(rt.state.nav.title(), "Memory and alerts");
    assert!(has(&mut rt, "gr-page"));
    assert!(has(&mut rt, "gr-jumps-checking"), "before the first answer");
    assert!(has(&mut rt, "gr-alerts-checking"));
}

#[test]
fn jumps_say_now_and_usual_and_leave_out_what_is_not_a_jump() {
    let mut rt = scene(growth_scene);
    assert_eq!(
        text_of(&rt, "gr-jumps-title").as_deref(),
        Some("Memory jumps")
    );
    // Biggest z first; below-usual, gone and trivial rows are not listed.
    assert_eq!(
        text_of(&rt, "gr-jump-0-memory").as_deref(),
        Some("now 2.4 GB, usually 1.0 GB")
    );
    assert_eq!(
        text_of(&rt, "gr-jump-0-how").as_deref(),
        Some("far above its usual")
    );
    assert_eq!(
        text_of(&rt, "gr-jump-1-memory").as_deref(),
        Some("now 891 MB, usually 625 MB")
    );
    assert_eq!(
        text_of(&rt, "gr-jump-1-how").as_deref(),
        Some("well above its usual")
    );
    assert_eq!(
        text_of(&rt, "gr-jump-2-how").as_deref(),
        Some("above its usual")
    );
    assert!(!has(&mut rt, "gr-jump-3"));
    assert_eq!(rt.state.jump_rows().len(), 3);
    let words: String = rt
        .elements()
        .into_iter()
        .filter_map(|e| e.text)
        .collect::<Vec<_>>()
        .join(" ")
        .to_lowercase();
    assert!(!words.contains("growing"), "a jump is never called growth");
}

#[test]
fn a_jump_links_to_its_process_page() {
    let mut rt = scene(growth_scene);
    let name = text_of(&rt, "gr-jump-1-open").unwrap();
    assert!(name.ends_with("· 1401"), "{name}");
    press(&mut rt, "gr-jump-1-open");
    assert_eq!(rt.state.selected, Some(1401));
    // Back returns to this page.
    rt.update(|s, _| s.back());
    settle(&mut rt);
    assert!(has(&mut rt, "gr-page"));
}

#[test]
fn alerts_list_newest_first_with_plain_detail_and_their_state() {
    let mut rt = scene(growth_scene);
    assert_eq!(
        text_of(&rt, "gr-alert-0-title").as_deref(),
        Some("Host load is critical")
    );
    assert_eq!(
        text_of(&rt, "gr-alert-0-detail").as_deref(),
        Some("1-minute load 21.4 across 10 CPUs")
    );
    assert_eq!(text_of(&rt, "gr-alert-0-status").as_deref(), Some("Active"));
    assert_eq!(
        text_of(&rt, "gr-alert-0-when").as_deref(),
        Some("fired 7 min ago")
    );
    assert_eq!(
        text_of(&rt, "gr-alert-1-status").as_deref(),
        Some("Acknowledged")
    );
    assert_eq!(
        text_of(&rt, "gr-alert-2-title").as_deref(),
        Some("Load climbing")
    );
    assert_eq!(
        text_of(&rt, "gr-alert-2-status").as_deref(),
        Some("Cleared")
    );
    assert_eq!(
        text_of(&rt, "gr-alert-2-when").as_deref(),
        Some("fired 1 h ago · cleared 1 h ago")
    );
    // Only an active, unacknowledged alert has the button.
    assert!(has(&mut rt, "gr-alert-0-ack"));
    assert!(!has(&mut rt, "gr-alert-1-ack"));
    assert!(!has(&mut rt, "gr-alert-2-ack"));
    assert!(!has(&mut rt, "gr-alert-3-ack"));
}

fn faked(scene_fn: fn(&mut App), fake: Fake) -> (Runtime<App>, Arc<Fake>) {
    let fake = Arc::new(fake);
    let f = fake.clone();
    let mut rt = scene(scene_fn);
    rt.update(move |s, _| {
        s.growth.spectra = f;
        s.growth.held = Some(Arc::new(Mutex::new(Vec::new())));
    });
    settle(&mut rt);
    (rt, fake)
}

#[test]
fn acknowledge_runs_only_on_the_press_and_says_it_worked() {
    let (mut rt, fake) = faked(
        growth_scene,
        Fake::with(&[(
            "alerts ack",
            Ok(
                r#"{"id":"e1","title":"Host load is critical","state":"firing","acked_at":"2026-11-20T11:59:00Z"}"#,
            ),
        )]),
    );
    // Showing the page asks for nothing to be acknowledged.
    assert!(fake.calls().is_empty());
    press(&mut rt, "gr-alert-0-ack");
    assert_eq!(
        text_of(&rt, "gr-outcome-text").as_deref(),
        Some("Acknowledging “Host load is critical”…")
    );
    rt.update(|s, _| s.run_held_growth());
    settle(&mut rt);
    assert_eq!(
        text_of(&rt, "gr-outcome-text").as_deref(),
        Some("Acknowledged “Host load is critical”. Spectra's daemon has it recorded.")
    );
    assert_eq!(
        text_of(&rt, "gr-alert-0-status").as_deref(),
        Some("Acknowledged")
    );
    assert!(!has(&mut rt, "gr-alert-0-ack"), "nothing left to press");
    assert_eq!(fake.calls(), [vec!["alerts", "ack", "e1", "--json"]]);
}

#[test]
fn a_failed_acknowledge_says_why_and_the_button_stays() {
    let (mut rt, _) = faked(
        growth_scene,
        Fake::with(&[(
            "alerts ack",
            Err("Spectra's daemon: the connection dropped"),
        )]),
    );
    press(&mut rt, "gr-alert-0-ack");
    rt.update(|s, _| s.run_held_growth());
    settle(&mut rt);
    assert_eq!(
        text_of(&rt, "gr-outcome-text").as_deref(),
        Some(
            "Couldn't acknowledge “Host load is critical”: Spectra's daemon: the connection dropped"
        )
    );
    assert_eq!(text_of(&rt, "gr-alert-0-status").as_deref(), Some("Active"));
    assert!(has(&mut rt, "gr-alert-0-ack"));
}

#[test]
fn nothing_jumping_and_no_alerts_say_so_plainly() {
    let rt = scene(growth_empty_scene);
    assert_eq!(
        text_of(&rt, "gr-jumps-empty").as_deref(),
        Some("Nothing is jumping past its usual memory.")
    );
    assert_eq!(
        text_of(&rt, "gr-alerts-empty").as_deref(),
        Some("Spectra hasn't raised any alerts.")
    );
}

#[test]
fn no_stored_history_is_not_the_empty_state_and_not_an_error() {
    let mut rt = scene(growth_history_scene);
    assert_eq!(
        text_of(&rt, "gr-jumps-history").as_deref(),
        Some(
            "Spectra hasn't stored enough history yet. Jumps show once it has watched processes for a few minutes."
        )
    );
    assert!(!has(&mut rt, "gr-jumps-empty"));
    assert!(!has(&mut rt, "gr-jumps-error"));
    assert!(has(&mut rt, "gr-alert-0"), "the alerts still show");
}

#[test]
fn an_unavailable_daemon_says_so_and_the_memory_check_still_shows() {
    let mut rt = scene(growth_unavailable_scene);
    assert_eq!(
        text_of(&rt, "gr-alerts-error-title").as_deref(),
        Some("Spectra's daemon isn't available, so alerts can't be read")
    );
    assert!(!has(&mut rt, "gr-alerts-empty"));
    assert!(has(&mut rt, "gr-jump-0"));
}

#[test]
fn a_look_lands_on_the_page_from_real_shaped_answers() {
    let (mut rt, fake) = faked(
        crate::sample::sample_scene,
        Fake::with(&[("anomalies", Ok(ANOMALIES)), ("alerts --json", Ok(ALERTS))]),
    );
    rt.update(|s, _| {
        s.growth.auto = true;
        s.nav = Nav::Growth;
        s.show(Nav::Growth);
        s.tick_growth();
        s.run_held_growth();
    });
    settle(&mut rt);
    // The captured pids aren't on the sample machine, so no jump rows; the alerts are.
    assert!(has(&mut rt, "gr-jumps-empty"));
    assert!(has(&mut rt, "gr-alert-0"));
    assert_eq!(fake.calls().len(), 2);
}
