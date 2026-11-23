//! Headless runs of what the Crashes page offers beyond its lists: a live line on rows whose
//! process runs now, opening and sampling it, the strip of crashes over time (hover, press,
//! drag, Clear), source links on an opened report's frames, and the way to the Timeline around
//! a crash. Spectra is a fake throughout.

use std::sync::{Arc, Mutex};

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::crash_strip::BUCKETS;
use crate::crashes::tests::Fake;
use crate::groups::Nav;
use crate::inspect::{Exec, RunState};
use crate::sample_crashes::{
    crashes_live_scene, crashes_report_links_scene, crashes_scene, crashes_scrub_scene,
};
use crate::timeline::Window;
use crate::tools::Tool;

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    s.crashes.spectra = Arc::new(Fake::ok());
    s.crashes.threaded = false;
    s.timeline.spectra = Arc::new(Fake::new(&[]));
    s.timeline.threaded = false;
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

fn text_of(rt: &mut Runtime<App>, key: &str) -> String {
    has(rt, key);
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
        .unwrap_or_else(|| panic!("no text keyed {key}"))
}

/// Signatures in the fixture's order: syncbridged, searchindexerd, Sketchpad,
/// WidgetHostService, thumbnailagent, notehelper.
const SYNC: &str = "cr-sig-0";
const WIDGET: &str = "cr-sig-3";

#[test]
fn rows_whose_process_runs_now_say_so_and_others_dont() {
    let mut rt = scene(crashes_live_scene);
    assert_eq!(
        text_of(&mut rt, &format!("{SYNC}-live")),
        "Running now: pid 6120, up 9 min, 3.4% CPU, 38 MB, started 2 min after its last crash"
    );
    // Two widget hosts: the newer leads and the other is counted.
    let widget = text_of(&mut rt, &format!("{WIDGET}-live"));
    assert!(
        widget.starts_with("Running now: pid 6230, up 38 min"),
        "{widget}"
    );
    assert!(widget.ends_with("(1 more running)"), "{widget}");
    for gone in [
        "cr-sig-1-live",
        "cr-sig-2-live",
        "cr-sig-4-live",
        "cr-sig-5-live",
    ] {
        assert!(!has(&mut rt, gone), "{gone}");
    }
    // The same process's report row says it too.
    assert!(has(&mut rt, "cr-rep-0-live"));
    // Without the process list's processes, nothing is running.
    let mut quiet = scene(crashes_scene);
    assert!(!has(&mut quiet, &format!("{SYNC}-live")));
}

#[test]
fn open_process_opens_the_running_ones_page_and_back_returns() {
    let mut rt = scene(crashes_live_scene);
    assert!(
        !has(&mut rt, &format!("{WIDGET}-live-open")),
        "only on the picked row"
    );
    press(&mut rt, &format!("{SYNC}-live-open"));
    assert_eq!(rt.state.selected, Some(6120));
    assert_eq!(rt.state.nav, Nav::Crashes);
    rt.update(|s, _| s.back());
    settle(&mut rt);
    assert_eq!(rt.state.selected, None);
}

#[test]
fn sample_now_opens_the_page_and_starts_its_sample_tool() {
    let mut rt = scene(crashes_live_scene);
    let fake = Arc::new(crate::inspect::tests::Fake::all());
    let f = fake.clone();
    rt.update(move |s, _| {
        s.inspect.spectra = f;
        s.inspect.exec = Exec::Held(Arc::new(Mutex::new(Vec::new())));
    });
    press(&mut rt, &format!("{SYNC}-live-sample"));
    assert_eq!(rt.state.selected, Some(6120));
    assert!(matches!(
        rt.state.inspect.state(Tool::Sample),
        Some(RunState::Running { .. })
    ));
    rt.update(|s, _| s.run_held_inspect());
    assert!(matches!(
        rt.state.inspect.state(Tool::Sample),
        Some(RunState::Done(_))
    ));
    assert_eq!(fake.calls(), [vec!["sample", "-json", "6120"]]);
}

#[test]
fn hovering_a_bar_says_when_and_how_many() {
    let mut rt = scene(crashes_scene);
    assert_eq!(
        text_of(&mut rt, "cr-strip-status"),
        "Drag across the strip to show only those hours."
    );
    let (x, y) = locate(&mut rt, &format!("cr-strip-b{}", BUCKETS - 1));
    rt.pointer_move(x, y);
    settle(&mut rt);
    assert_eq!(rt.state.crashes.hover, Some(BUCKETS - 1));
    // 04:00 to 07:00 this morning: five reports, the memory kill among them counted once.
    assert_eq!(
        text_of(&mut rt, "cr-strip-status"),
        "04:00 to 07:00: 5 crashes"
    );
}

#[test]
fn pressing_one_bar_keeps_the_lists_to_it() {
    let mut rt = scene(crashes_scene);
    press(&mut rt, &format!("cr-strip-b{}", BUCKETS - 2));
    let strip = rt.state.crash_strip().unwrap();
    assert_eq!(
        rt.state.crashes.range,
        Some(strip.window(BUCKETS - 2, BUCKETS - 2))
    );
    // 01:00 to 04:00: only the search indexer's report.
    assert!(has(&mut rt, "cr-rep-0"));
    assert!(!has(&mut rt, "cr-rep-1"));
    assert!(text_of(&mut rt, "cr-rep-0-name").contains("searchindexerd"));
}

#[test]
fn dragging_across_bars_keeps_the_lists_to_them_and_clear_resets() {
    let mut rt = scene(crashes_scene);
    let last = BUCKETS - 1;
    let (x0, y) = locate(&mut rt, &format!("cr-strip-b{}", last - 1));
    let (x1, _) = locate(&mut rt, &format!("cr-strip-b{}", last - 3));
    rt.pointer_down(x0, y);
    rt.pointer_move((x0 + x1) / 2.0, y);
    rt.pointer_move(x1, y);
    settle(&mut rt);
    assert_eq!(rt.state.crashes.drag, Some((last - 1, last - 3)));
    assert!(
        rt.state.crashes.range.is_none(),
        "nothing kept until it lets go"
    );
    rt.pointer_up(x1, y);
    settle(&mut rt);
    let strip = rt.state.crash_strip().unwrap();
    assert_eq!(
        rt.state.crashes.range,
        Some(strip.window(last - 3, last - 1))
    );
    assert_eq!(rt.state.crashes.drag, None);
    // Off the strip, it says what the window holds.
    rt.pointer_move(x1, y + 200.0);
    settle(&mut rt);
    assert_eq!(rt.state.crashes.hover, None);
    assert_eq!(
        text_of(&mut rt, "cr-strip-status"),
        "Showing Nov 19, 19:00 to 04:00: 2 crashes"
    );
    assert!(text_of(&mut rt, "cr-oom-0-name").contains("previewrenderer"));
    press(&mut rt, "cr-strip-clear");
    assert_eq!(rt.state.crashes.range, None);
    assert!(!has(&mut rt, "cr-strip-clear"));
    assert!(text_of(&mut rt, "cr-rep-0-name").contains("syncbridged"));
}

#[test]
fn the_scrub_scene_shows_a_window_picked() {
    let mut rt = scene(crashes_scrub_scene);
    assert!(rt.state.crashes.range.is_some());
    assert!(has(&mut rt, "cr-strip-clear"));
    assert!(text_of(&mut rt, "cr-strip-status").starts_with("Showing Nov 19, 19:00 to 04:00"));
}

#[test]
fn system_library_frames_link_to_their_public_source_on_press() {
    let mut rt = scene(crashes_report_links_scene);
    // Frame 0 is in libswiftCore; 1 to 4 are the widget's own code; 5 is libdispatch.
    assert!(has(&mut rt, &format!("{WIDGET}-frame-0-source")));
    for own in 1..5 {
        assert!(has(&mut rt, &format!("{WIDGET}-frame-{own}")));
        assert!(
            !has(&mut rt, &format!("{WIDGET}-frame-{own}-source")),
            "{own}"
        );
    }
    assert!(has(&mut rt, &format!("{WIDGET}-frame-5-source")));
    assert!(
        rt.take_url_requests().is_empty(),
        "nothing opens until pressed"
    );
    press(&mut rt, &format!("{WIDGET}-frame-0-source"));
    assert_eq!(
        rt.take_url_requests(),
        ["https://github.com/search?q=repo%3Aswiftlang%2Fswift%20_assertionFailure&type=code"]
    );
}

#[test]
fn see_what_changed_then_opens_the_timeline_around_the_crash() {
    let mut rt = scene(crashes_report_links_scene);
    let at = rt
        .state
        .crashes
        .opened
        .values()
        .find_map(|r| r.as_ref().ok().and_then(|r| r.at))
        .unwrap();
    press(&mut rt, &format!("{WIDGET}-report-timeline"));
    let s = &rt.state;
    assert_eq!(s.nav, Nav::Timeline);
    // The crash was 40 minutes before the sample's clock: the last hour holds it.
    assert_eq!(s.timeline.window, Window::Hour);
    assert_eq!(s.timeline.anchor, Some(at));
    assert!(has(&mut rt, "tl-crash"));
    // Leaving the Timeline forgets the crash.
    rt.update(|s, _| s.show(Nav::Crashes));
    settle(&mut rt);
    assert_eq!(rt.state.timeline.anchor, None);
}

#[test]
fn the_timeline_window_comes_back_after_an_anchored_visit() {
    let mut rt = scene(crashes_report_links_scene);
    rt.update(|s, _| {
        s.timeline.window = Window::Hour;
        let old = (s.wall)() - 3 * 86_400;
        s.timeline_at_crash(old);
    });
    settle(&mut rt);
    assert_eq!(rt.state.nav, Nav::Timeline);
    assert_eq!(rt.state.timeline.window, Window::Week);
    rt.update(|s, _| s.show(Nav::Crashes));
    settle(&mut rt);
    assert_eq!(rt.state.timeline.anchor, None);
    rt.update(|s, _| s.show(Nav::Timeline));
    settle(&mut rt);
    assert_eq!(rt.state.timeline.window, Window::Hour);
}

#[test]
fn a_window_with_nothing_in_it_says_so_and_leaving_forgets_it() {
    let mut rt = scene(crashes_scene);
    rt.update(|s, _| {
        s.crashes.range = Some((0, 1));
        s.crashes.drag = Some((1, 2));
    });
    settle(&mut rt);
    for key in ["cr-sigs-none", "cr-reps-none", "cr-ooms-none"] {
        assert_eq!(text_of(&mut rt, key), "None in the chosen hours.", "{key}");
    }
    rt.update(|s, _| s.show(Nav::Timeline));
    settle(&mut rt);
    assert_eq!(rt.state.crashes.range, None);
    assert_eq!(rt.state.crashes.drag, None);
}

#[test]
fn a_pid_that_has_exited_since_is_not_opened_or_sampled() {
    let mut rt = scene(crashes_live_scene);
    let live = rt.state.running_named("syncbridged").unwrap();
    let file = rt.state.crashes.picked.clone().unwrap().1;
    let sec = rt.state.crashes.picked.clone().unwrap().0;
    let without: Vec<_> = rt
        .state
        .procs
        .iter()
        .filter(|p| p.pid != 6120)
        .cloned()
        .collect();
    let with_all = rt.state.procs.clone();
    rt.update(move |s, _| s.land_procs(Ok(without)));
    rt.update(move |s, _| {
        s.open_crashed(sec, &file, 6120, "syncbridged", live.started);
        s.sample_crashed(sec, &file, 6120, "syncbridged", live.started);
    });
    settle(&mut rt);
    assert_eq!(rt.state.selected, None);
    assert!(has(&mut rt, &format!("{SYNC}-gone")));
    assert_eq!(
        text_of(&mut rt, &format!("{SYNC}-gone")),
        "It has exited since."
    );
    // The pid reused by another process, or by the same name started later, is not it either.
    let live = {
        rt.update(move |s, _| s.land_procs(Ok(with_all)));
        rt.state.running_named("syncbridged").unwrap()
    };
    let (sec, file) = rt.state.crashes.picked.clone().unwrap();
    let mut reused = rt.state.procs.clone();
    for p in reused.iter_mut().filter(|p| p.pid == 6120) {
        p.started_at = "2026-11-20T07:30:00-05:00".into();
    }
    rt.update(move |s, _| s.land_procs(Ok(reused)));
    let f = file.clone();
    rt.update(move |s, _| s.open_crashed(sec, &f, 6120, "syncbridged", live.started));
    assert_eq!(rt.state.selected, None);
    let mut renamed = rt.state.procs.clone();
    for p in renamed.iter_mut().filter(|p| p.pid == 6120) {
        p.command = "otherd".into();
    }
    rt.update(move |s, _| s.land_procs(Ok(renamed)));
    rt.update(move |s, _| s.open_crashed(sec, &file, 6120, "syncbridged", None));
    assert_eq!(rt.state.selected, None);
}
