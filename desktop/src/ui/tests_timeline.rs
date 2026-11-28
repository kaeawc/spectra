//! Headless runs of the Timeline page: the sidebar row reads both commands once, a window
//! change reads again, Refresh, the kind filter, the "around when things got slow" marker, rows
//! that open a process or an app, and the empty and failed states, with a fake Spectra.

use std::sync::Arc;

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_timeline::{
    climbing_load, timeline_empty_scene, timeline_failed_scene, timeline_reading_scene,
    timeline_scene,
};
use crate::timeline::tests::{Fake, TIMELINE, UPDATES};
use crate::timeline::{Filter, Kind, Window, timeline_args, updates_args};

fn scene_with(f: fn(&mut App), fake: Fake) -> (Runtime<App>, Arc<Fake>) {
    let fake = Arc::new(fake);
    let mut s = init();
    s.timeline.spectra = fake.clone();
    s.timeline.threaded = false;
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

fn ok() -> Fake {
    Fake::new(Ok(TIMELINE), Ok(UPDATES))
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

/// Lands what the fake answered, as `poll` would.
fn land(rt: &mut Runtime<App>) {
    rt.update(|s, _| {
        s.land_timeline();
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

/// Opens the page from the sidebar, as a person would, on the sample's processes.
fn opened(rt: &mut Runtime<App>) {
    press(rt, "timeline");
    land(rt);
}

#[test]
fn the_sidebar_row_opens_the_page_and_reads_both_once() {
    let (mut rt, fake) = scene_with(sample_scene, ok());
    assert!(
        fake.calls().is_empty(),
        "nothing runs before the page opens"
    );
    opened(&mut rt);
    assert_eq!(rt.state.nav, Nav::Timeline);
    assert_eq!(
        fake.calls(),
        [timeline_args(Window::Hour), updates_args(Window::Hour)]
    );
    // Frames alone read nothing more: no timer.
    for _ in 0..120 {
        rt.frame(1.0);
    }
    land(&mut rt);
    assert_eq!(fake.calls().len(), 2);
    assert!(has(&mut rt, "tl-count"));
}

#[test]
fn a_window_change_and_refresh_read_again() {
    let (mut rt, fake) = scene_with(sample_scene, ok());
    opened(&mut rt);
    rt.update(|s, _| s.set_timeline_window(Window::Day));
    land(&mut rt);
    assert_eq!(fake.calls()[2], timeline_args(Window::Day));
    assert_eq!(
        fake.calls()[3],
        ["updates", "--history", "--since", "24h", "--json"]
    );
    // Picking the same window again reads nothing.
    rt.update(|s, _| s.set_timeline_window(Window::Day));
    assert_eq!(fake.calls().len(), 4);
    press(&mut rt, "tl-refresh");
    land(&mut rt);
    assert_eq!(fake.calls().len(), 6);
    assert_eq!(rt.state.timeline.data.as_ref().unwrap().1, Window::Day);
}

#[test]
fn leaving_the_page_drops_a_reading_on_its_way() {
    let (mut rt, fake) = scene_with(timeline_reading_scene, ok());
    assert!(has(&mut rt, "tl-reading"));
    rt.update(|s, _| s.show(Nav::All));
    land(&mut rt);
    assert!(rt.state.timeline.reading().is_none());
    assert!(fake.calls().is_empty());
}

#[test]
fn the_scene_marks_rows_from_just_before_the_load_climbed() {
    let (mut rt, _) = scene_with(timeline_scene, ok());
    assert!(has(&mut rt, "tl-slow"));
    let marked: Vec<String> = rt
        .elements()
        .into_iter()
        .filter_map(|e| e.key)
        .filter(|k| k.starts_with("tl-mark-"))
        .collect();
    assert_eq!(marked.len(), 5, "{marked:?}");
    // Without a climb, nothing is marked.
    let (mut calm, _) = scene_with(
        |s| {
            timeline_scene(s);
            s.system = crate::system::System::new();
        },
        ok(),
    );
    assert!(!has(&mut calm, "tl-slow"));
}

#[test]
fn rows_lead_to_running_processes_and_apps() {
    let (mut rt, _) = scene_with(timeline_scene, ok());
    let rows = rt.state.timeline.data.as_ref().unwrap().0.rows.clone();
    let start = rows.iter().find(|r| r.pid == Some(901)).unwrap();
    let key = format!("tl-go-{}", start.id());
    assert!(has(&mut rt, &key));
    assert!(
        rt.elements()
            .into_iter()
            .any(|e| e.text.as_deref() == Some("Open process"))
    );
    press(&mut rt, &key);
    assert_eq!(rt.state.selected, Some(901));
    rt.update(|s, _| s.back());
    settle(&mut rt);
    assert_eq!(rt.state.nav, Nav::Timeline);
    // The Slack install opens Slack's app page.
    let install = rows
        .iter()
        .find(|r| r.kind == Kind::Install && r.install.as_ref().unwrap().name == "Slack")
        .unwrap();
    press(&mut rt, &format!("tl-go-{}", install.id()));
    assert_eq!(
        rt.state.app_page.as_deref(),
        Some("/Applications/Slack.app")
    );
    // A process that isn't running has no way through.
    let gone = rows
        .iter()
        .find(|r| r.process == "Example Sync Agent")
        .unwrap();
    assert!(!has(&mut rt, &format!("tl-go-{}", gone.id())));
}

#[test]
fn the_kind_filter_narrows_the_rows() {
    let (mut rt, _) = scene_with(timeline_scene, ok());
    let all = text_of(&mut rt, "tl-count").unwrap();
    rt.update(|s, _| s.set_timeline_filter(Filter::Only(Kind::Install)));
    settle(&mut rt);
    let installs = text_of(&mut rt, "tl-count").unwrap();
    assert!(installs.starts_with("2 of "), "{installs}");
    assert_ne!(all, installs);
    let rows = rt.state.timeline.data.as_ref().unwrap().0.rows.clone();
    let start = rows.iter().find(|r| r.kind == Kind::Start).unwrap();
    assert!(!has(&mut rt, &format!("tl-row-{}", start.id())));
}

#[test]
fn empty_and_failed_say_so() {
    let (mut rt, _) = scene_with(timeline_empty_scene, ok());
    assert!(
        text_of(&mut rt, "tl-none")
            .unwrap()
            .starts_with("Nothing changed in the last hour")
    );
    let (mut rt, _) = scene_with(timeline_failed_scene, ok());
    assert!(has(&mut rt, "tl-error"));
    assert!(has(&mut rt, "tl-error-hint"));
}

#[test]
fn a_failed_history_still_shows_the_timeline() {
    let (mut rt, _) = scene_with(
        |s| {
            sample_scene(s);
            climbing_load(s);
        },
        Fake::new(Ok(TIMELINE), Err("updates: system_profiler failed")),
    );
    rt.update(|s, _| s.set_timeline_window(Window::Day));
    rt.update(|s, _| s.show(Nav::Timeline));
    land(&mut rt);
    assert!(has(&mut rt, "tl-updates-error"));
    assert!(has(&mut rt, "tl-rows"));
}

#[test]
fn a_failed_timeline_shows_why() {
    let (mut rt, _) = scene_with(
        sample_scene,
        Fake::new(
            Err("spectra took longer than 90 s and was stopped"),
            Ok(UPDATES),
        ),
    );
    opened(&mut rt);
    assert!(
        text_of(&mut rt, "tl-error").is_none_or(|t| t.contains("90 s")),
        "the error row"
    );
    assert!(has(&mut rt, "tl-error"));
    assert_eq!(
        rt.state.timeline.error.as_deref(),
        Some("spectra took longer than 90 s and was stopped")
    );
}

#[test]
fn a_start_whose_pid_now_runs_under_another_name_does_not_link() {
    let (mut rt, _) = scene_with(
        |s| {
            timeline_scene(s);
            // pid 901 is Slack Helper (Renderer) in the sample; say it started as something else.
            let tl = &mut s.timeline.data.as_mut().unwrap().0;
            let row = tl.rows.iter_mut().find(|r| r.pid == Some(901)).unwrap();
            row.process = "Example Worker".into();
        },
        ok(),
    );
    let row = rt.state.timeline.data.as_ref().unwrap().0.rows.clone();
    let row = row.iter().find(|r| r.pid == Some(901)).unwrap();
    assert!(rt.state.process(901).is_some(), "the pid itself still runs");
    assert_eq!(rt.state.timeline_go(row), None);
    assert!(has(&mut rt, &format!("tl-row-{}", row.id())));
    assert!(!has(&mut rt, &format!("tl-go-{}", row.id())));
}

#[test]
fn a_stale_load_reading_doesnt_say_it_is_still_slow() {
    let (mut rt, _) = scene_with(timeline_scene, ok());
    let fresh = text_of(&mut rt, "tl-slow").unwrap_or_default();
    let all = |rt: &Runtime<App>| {
        rt.elements()
            .into_iter()
            .filter_map(|e| e.text)
            .collect::<Vec<_>>()
            .join("\n")
    };
    assert!(all(&rt).contains("and is still there"), "{fresh}");
    // Ten minutes pass with no load reading (the window hidden): the note says how old it is.
    rt.update(|s, _| s.now += 600.0);
    settle(&mut rt);
    let text = all(&rt);
    assert!(!text.contains("and is still there"), "{text}");
    assert!(
        text.contains("the last load reading was 10 min ago"),
        "{text}"
    );
}
