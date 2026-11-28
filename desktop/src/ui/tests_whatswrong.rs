//! Headless runs of "Right now" on the Overview: its lines and their links, a finding it repeats
//! shown once, the nothing-wrong line, and a look from asking to landing with a fake Spectra.

use std::sync::{Arc, Mutex};

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::inspect::tests::Fake;
use crate::sample::sample_scene;
use crate::sample_whatswrong::{SAMPLE, right_now_clear_scene, right_now_scene};

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(s, super::view, (1100.0, 900.0), env);
    rt.on_key = Some(super::on_key);
    rt.frame(0.0);
    rt
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
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

fn press(rt: &mut Runtime<App>, key: &str) {
    assert!(has(rt, key), "nothing keyed {key} on screen");
    let (x, y) = rt.find(key).unwrap();
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

#[test]
fn right_now_sits_above_the_numbers_with_its_age_and_one_line_per_cause() {
    let mut rt = scene(right_now_scene);
    let (now, summary) = (rt.find("right-now").unwrap(), rt.find("summary").unwrap());
    assert!(now.1 < summary.1, "at the top of the Overview");
    assert_eq!(
        text_of(&mut rt, "right-now-age").as_deref(),
        Some("Checked 8 s ago")
    );
    assert_eq!(
        text_of(&mut rt, "ww-0-title").as_deref(),
        Some("Memory pressure is elevated")
    );
    assert_eq!(
        text_of(&mut rt, "ww-1-title").as_deref(),
        Some("Xcode uses 16% of machine CPU")
    );
    assert_eq!(
        text_of(&mut rt, "ww-1-evidence").as_deref(),
        Some(
            "1.6 cores busy across 3 processes, holding 2.3 GB; busiest: SourceKitService at 138%, XCBBuildService at 12%."
        )
    );
    assert_eq!(
        text_of(&mut rt, "ww-2-title").as_deref(),
        Some("A process is using heavy CPU")
    );
    assert_eq!(text_of(&mut rt, "ww-1-open").as_deref(), Some("Xcode"));
    assert_eq!(text_of(&mut rt, "ww-0-open").as_deref(), Some("Machine"));
}

#[test]
fn each_line_opens_the_page_that_shows_more() {
    let mut rt = scene(right_now_scene);
    press(&mut rt, "ww-1-open");
    assert_eq!(
        rt.state.app_page.as_deref(),
        Some("/Applications/Xcode.app")
    );
    let mut rt = scene(right_now_scene);
    press(&mut rt, "ww-0-open");
    assert_eq!(rt.state.nav, Nav::System);
}

#[test]
fn a_finding_right_now_repeats_shows_once() {
    let mut plain = scene(sample_scene);
    let busy = "finding-1300/proc-busy-cpu";
    assert!(has(&mut plain, busy), "the sample's rust-analyzer is busy");
    assert!(!has(&mut plain, "right-now"), "no look yet, no section");
    let mut rt = scene(right_now_scene);
    assert!(!has(&mut rt, busy), "the heavy-CPU line says it");
    assert_eq!(
        rt.state.right_now()[2].covers,
        ["1300/proc-busy-cpu"],
        "and carries its fix"
    );
    assert!(has(&mut rt, "ww-2-fix"));
}

fn says(rt: &mut Runtime<App>, words: &str) -> bool {
    settle(rt);
    rt.elements()
        .into_iter()
        .any(|e| e.text.as_deref() == Some(words))
}

#[test]
fn with_right_now_lines_and_no_findings_it_never_says_nothing_needs_attention() {
    let mut rt = scene(|s| {
        right_now_scene(s);
        s.findings.clear();
    });
    assert!(has(&mut rt, "ww-0"), "Right now still lists causes");
    assert!(!says(&mut rt, "Nothing needs attention"));
    assert_eq!(
        text_of(&mut rt, "findings-rest").as_deref(),
        Some("Nothing else needs attention.")
    );
    // With nothing in Right now either, everything is fine.
    let mut rt = scene(|s| {
        right_now_clear_scene(s);
        s.findings.clear();
    });
    assert!(says(&mut rt, "Nothing needs attention"));
}

#[test]
fn nothing_wrong_is_one_short_line() {
    let mut rt = scene(right_now_clear_scene);
    assert_eq!(
        text_of(&mut rt, "right-now-clear").as_deref(),
        Some("Nothing is slowing this Mac down right now.")
    );
    assert!(!has(&mut rt, "ww-0"));
    assert_eq!(
        text_of(&mut rt, "right-now-age").as_deref(),
        Some("Checked 3 s ago")
    );
}

#[test]
fn a_look_shows_it_is_asking_then_lands() {
    let mut rt = scene(sample_scene);
    let fake = Arc::new(Fake::with(&[("whatswrong", Ok(SAMPLE))]));
    let f = fake.clone();
    rt.update(move |s, _| {
        s.whatswrong.spectra = f;
        s.whatswrong.held = Some(Arc::new(Mutex::new(Vec::new())));
        s.whatswrong.auto = true;
        s.tick_whatswrong();
    });
    settle(&mut rt);
    assert_eq!(
        text_of(&mut rt, "right-now-checking").as_deref(),
        Some("Asking Spectra what's slowing this Mac down…")
    );
    rt.update(|s, _| s.run_held_whatswrong());
    settle(&mut rt);
    assert!(has(&mut rt, "ww-0"));
    assert!(!has(&mut rt, "right-now-checking"));
    assert_eq!(fake.calls(), [vec!["whatswrong", "--json"]]);
}

#[test]
fn a_first_look_that_fails_says_why() {
    let mut rt = scene(sample_scene);
    let fake = Arc::new(Fake::with(&[(
        "whatswrong",
        Err("spectra: unknown command \"whatswrong\""),
    )]));
    rt.update(move |s, _| {
        s.whatswrong.spectra = fake;
        s.whatswrong.held = Some(Arc::new(Mutex::new(Vec::new())));
        s.whatswrong.auto = true;
        s.tick_whatswrong();
        s.run_held_whatswrong();
    });
    settle(&mut rt);
    assert_eq!(
        text_of(&mut rt, "right-now-error").as_deref(),
        Some("Couldn't check what's slowing this Mac: spectra: unknown command \"whatswrong\"")
    );
}
