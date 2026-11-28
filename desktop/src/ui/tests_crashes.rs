//! Headless runs of the Crashes page: the sidebar row reads the three listings once, leaving and
//! coming back doesn't read again until Refresh (or the reading is old), picking a row and
//! pressing "Open report" decodes that report and shows it under that row, and the empty and
//! failed states, with a fake Spectra.

use std::sync::Arc;

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::crashes::tests::{Fake, INSPECT_RESOURCE, LIST, SIGNATURES};
use crate::crashes::{STALE, Section, inspect_args, list_args, oom_args, signatures_args};
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_crashes::{crashes_empty_scene, crashes_report_scene, crashes_scene};

fn scene_with(f: fn(&mut App), fake: Fake) -> (Runtime<App>, Arc<Fake>) {
    let fake = Arc::new(fake);
    let mut s = init();
    s.crashes.spectra = fake.clone();
    s.crashes.threaded = false;
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

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

/// Lands what the fake answered, as `poll` would.
fn land(rt: &mut Runtime<App>) {
    rt.update(|s, _| {
        s.land_crashes();
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

fn texts(rt: &mut Runtime<App>) -> Vec<String> {
    rt.elements().into_iter().filter_map(|e| e.text).collect()
}

fn listings() -> Vec<Vec<String>> {
    vec![signatures_args(), list_args(), oom_args()]
}

fn opened(rt: &mut Runtime<App>) {
    press(rt, "crashes");
    land(rt);
}

#[test]
fn the_sidebar_row_opens_the_page_and_reads_the_three_listings_once() {
    let (mut rt, fake) = scene_with(sample_scene, Fake::ok());
    assert!(
        fake.calls().is_empty(),
        "nothing runs before the page opens"
    );
    opened(&mut rt);
    assert_eq!(rt.state.nav, Nav::Crashes);
    assert_eq!(fake.calls(), listings());
    // Frames alone read nothing more: no timer.
    for _ in 0..120 {
        rt.frame(1.0);
    }
    land(&mut rt);
    assert_eq!(fake.calls().len(), 3);
    // The most frequent signature leads, in plain words, with its count and when last seen.
    assert_eq!(
        text_of(&mut rt, "cr-sig-0-name").as_deref(),
        Some("syncbridged")
    );
    let what = text_of(&mut rt, "cr-sig-0-what").unwrap();
    assert!(what.starts_with("killed · 14 times · last "), "{what}");
    assert!(has(&mut rt, "cr-rep-0") && has(&mut rt, "cr-oom-1"));
}

#[test]
fn leaving_and_coming_back_reads_again_only_on_refresh_or_once_old() {
    let (mut rt, fake) = scene_with(sample_scene, Fake::ok());
    opened(&mut rt);
    assert_eq!(fake.calls().len(), 3);
    rt.update(|s, _| s.show(Nav::All));
    press(&mut rt, "crashes");
    land(&mut rt);
    let crash_calls = |f: &Fake| f.calls().iter().filter(|c| c[0] == "crash").count();
    assert_eq!(crash_calls(&fake), 3, "a fresh reading stands");
    press(&mut rt, "cr-refresh");
    land(&mut rt);
    assert_eq!(crash_calls(&fake), 6, "Refresh always reads");
    // Once the reading is STALE seconds old, opening the page reads again.
    rt.update(|s, _| {
        let at = &mut s.crashes.data.as_mut().unwrap().1;
        *at -= STALE;
        s.show(Nav::All);
        s.show(Nav::Crashes);
    });
    land(&mut rt);
    assert_eq!(crash_calls(&fake), 9);
}

#[test]
fn leaving_the_page_drops_a_reading_on_its_way() {
    let (mut rt, fake) = scene_with(
        |s| {
            crashes_empty_scene(s);
            s.crashes.data = None;
            s.crashes.sample_flight();
        },
        Fake::ok(),
    );
    assert!(has(&mut rt, "cr-reading"));
    rt.update(|s, _| s.show(Nav::All));
    land(&mut rt);
    assert!(!rt.state.crashes.reading());
    assert!(fake.calls().is_empty());
}

#[test]
fn open_report_decodes_the_picked_row_and_lands_under_it() {
    let thumb = "/Users/user/Library/Logs/DiagnosticReports/thumbnailagent-2026-11-20-050244.ips";
    let widget =
        "/Users/user/Library/Logs/DiagnosticReports/WidgetHostService-2026-11-20-062000.ips";
    let mut fake = Fake::ok();
    fake.replies
        .insert(format!("inspect {thumb}"), Ok(INSPECT_RESOURCE.to_owned()));
    let (mut rt, fake) = scene_with(crashes_scene, fake);
    // No row is picked, so nothing offers to open.
    assert!(
        !rt.elements()
            .iter()
            .any(|e| e.text.as_deref() == Some("Open report"))
    );
    // WidgetHostService is the fourth signature.
    press(&mut rt, "cr-sig-3");
    assert_eq!(
        rt.state.crashes.picked,
        Some((Section::Signatures, widget.to_owned()))
    );
    assert!(fake.calls().is_empty(), "picking alone runs nothing");
    press(&mut rt, "cr-sig-3-open");
    land(&mut rt);
    assert_eq!(fake.calls(), [inspect_args(widget)]);
    assert!(has(&mut rt, "cr-sig-3-report"));
    let thread = text_of(&mut rt, "cr-sig-3-report-thread").unwrap();
    assert_eq!(
        thread,
        "Crashed thread: Thread 2 · render · com.example.widget.render"
    );
    let all = texts(&mut rt);
    assert!(all.iter().any(|t| t.contains("TimelineCache.entry(for:)")));
    assert!(all.iter().any(|t| t == "Trace/BPT trap: 5"));
    assert!(all.iter().any(|t| t.starts_with("And 4 more frames")));
    // The thumbnail agent's recent report: its own answer lands on its own row.
    let thumb_row = rt
        .state
        .crashes
        .data
        .as_ref()
        .unwrap()
        .0
        .reports
        .iter()
        .position(|r| r.file == thumb)
        .unwrap();
    press(&mut rt, &format!("cr-rep-{thumb_row}"));
    assert!(
        !has(&mut rt, "cr-sig-3-report"),
        "only the picked row shows its report"
    );
    press(&mut rt, &format!("cr-rep-{thumb_row}-open"));
    land(&mut rt);
    assert!(has(&mut rt, &format!("cr-rep-{thumb_row}-report-nothread")));
    let opened = &rt.state.crashes.opened;
    assert_eq!(
        opened[widget].as_ref().unwrap().process,
        "WidgetHostService"
    );
    assert_eq!(opened[thumb].as_ref().unwrap().process, "notehelper");
}

#[test]
fn a_report_picked_away_from_still_lands_on_its_own_file() {
    let widget =
        "/Users/user/Library/Logs/DiagnosticReports/WidgetHostService-2026-11-20-062000.ips";
    let (mut rt, _) = scene_with(crashes_scene, Fake::ok());
    rt.update(|s, _| {
        s.crashes.threaded = false;
        s.pick_crash(Section::Signatures, widget);
        // Runs and queues its answer at once (unthreaded); pick another row before it lands.
        s.open_crash_report();
        s.pick_crash(Section::Reports, "/elsewhere.ips");
    });
    land(&mut rt);
    assert!(rt.state.crashes.opened[widget].is_ok());
    assert!(!rt.state.crashes.opened.contains_key("/elsewhere.ips"));
}

#[test]
fn a_report_that_wont_decode_says_why() {
    let widget =
        "/Users/user/Library/Logs/DiagnosticReports/WidgetHostService-2026-11-20-062000.ips";
    let mut fake = Fake::ok();
    fake.replies.insert(
        "inspect".into(),
        Err("parse report: not an .ips crash report".into()),
    );
    let (mut rt, _) = scene_with(crashes_scene, fake);
    rt.update(move |s, _| s.pick_crash(Section::Signatures, widget));
    settle(&mut rt);
    press(&mut rt, "cr-sig-3-open");
    land(&mut rt);
    let all = texts(&mut rt);
    assert!(
        all.iter()
            .any(|t| t == "Couldn't open the report: parse report: not an .ips crash report"),
        "{all:?}"
    );
}

#[test]
fn the_report_scene_shows_the_opened_report() {
    let (mut rt, fake) = scene_with(crashes_report_scene, Fake::ok());
    assert!(has(&mut rt, "cr-sig-3-report-head"));
    let head = text_of(&mut rt, "cr-sig-3-report-head").unwrap();
    assert!(
        head.starts_with("WidgetHostService 2.4 (pid 4821) stopped on a check "),
        "{head}"
    );
    assert!(has(&mut rt, "cr-sig-3-report-frames"));
    assert!(fake.calls().is_empty());
}

#[test]
fn an_empty_folder_says_so() {
    let (mut rt, _) = scene_with(crashes_empty_scene, Fake::ok());
    let none = text_of(&mut rt, "cr-none").unwrap();
    assert!(
        none.starts_with("No crashes in the reports folder."),
        "{none}"
    );
    assert!(!has(&mut rt, "cr-sigs"));
}

#[test]
fn a_failed_reading_says_why_and_a_failed_section_only_notes_it() {
    let fake = Fake::new(&[(
        "signatures",
        Err("spectra crash signatures: cannot resolve the home directory; pass --dir"),
    )]);
    let (mut rt, _) = scene_with(sample_scene, fake);
    opened(&mut rt);
    let all = texts(&mut rt);
    assert!(all.iter().any(|t| t
        == "Couldn't read the crash reports: spectra crash signatures: cannot resolve the home directory; pass --dir"));
    assert!(has(&mut rt, "cr-error-hint"));

    let partial = Fake::new(&[
        ("signatures", Ok(SIGNATURES)),
        ("list", Ok(LIST)),
        ("oom", Err("spectra took longer than 45 s and was stopped")),
    ]);
    let (mut rt, _) = scene_with(sample_scene, partial);
    opened(&mut rt);
    assert!(has(&mut rt, "cr-sig-0") && has(&mut rt, "cr-rep-0"));
    assert!(has(&mut rt, "cr-ooms-error"));
    assert!(!has(&mut rt, "cr-error"));
}
