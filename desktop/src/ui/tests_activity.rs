//! Headless runs of the Activity page: the sidebar row, the summary and its links, the table's
//! filters and open rows, the spawn-storm finding, and an older Spectra.

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::findings::Link;
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_activity::{activity_scene, activity_storm_scene, activity_unsupported_scene};
use crate::spawns::Kind;

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    // Nothing in these runs may start a real Spectra.
    s.activity.auto = false;
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(s, super::view, (980.0, 640.0), env);
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

fn text_of(rt: &Runtime<App>, key: &str) -> Option<String> {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

#[test]
fn the_activity_row_sits_under_machine_and_opens_the_page() {
    let mut rt = scene(sample_scene);
    let (sys, act, all) = (
        rt.find("system").unwrap(),
        rt.find("activity").unwrap(),
        rt.find("all").unwrap(),
    );
    assert!(sys.1 < act.1 && act.1 < all.1, "right under Machine");
    press(&mut rt, "activity");
    assert_eq!(rt.state.nav, Nav::Activity);
    assert!(rt.state.activity_open());
    assert!(rt.find("act-page").is_some(), "the Activity page shows");
    assert!(rt.find("procs").is_none(), "not a process list");
    assert!(has(&mut rt, "act-keep"), "with Keep recording");
    assert!(!rt.state.activity.keep_recording, "off by default");
    assert!(
        has(&mut rt, "act-empty"),
        "and says nothing is recorded yet"
    );
}

#[test]
fn keep_recording_says_what_it_does() {
    let mut rt = scene(activity_scene);
    press(&mut rt, "act-keep");
    assert!(rt.state.activity.keep_recording);
    press(&mut rt, "act-keep");
    assert!(!rt.state.activity.keep_recording);
    assert!(
        text_of(&rt, "act-status")
            .unwrap()
            .starts_with("Recording in rounds of 10 s"),
        "{:?}",
        text_of(&rt, "act-status")
    );
}

#[test]
fn the_summary_counts_the_last_minute_and_names_the_busiest_parents() {
    let mut rt = scene(activity_scene);
    let sum = rt.state.activity.summary(crate::sample::SAMPLE_NOW as f64);
    assert!(sum.starts > 0 && sum.exits > 0);
    assert!(has(&mut rt, "act-starts") && has(&mut rt, "act-exits"));
    // The prompt's git (and node) under zsh 1101, then the script's curl under bash 1110.
    let ranked: Vec<(u32, &str, usize)> = sum
        .parents
        .iter()
        .map(|p| (p.ppid, p.name.as_str(), p.count))
        .collect();
    assert_eq!(ranked[..2], [(1101, "zsh", 8), (1110, "bash", 7)]);
    assert_eq!(
        text_of(&rt, "act-parent-1110").as_deref(),
        Some("bash · 1110 started 7 processes")
    );
    // The parent is in the process feed: its row opens its page.
    press(&mut rt, "act-parent-1110");
    assert_eq!(rt.state.selected, Some(1110));
    rt.update(|s, _| s.back());
    settle(&mut rt);
    assert!(rt.state.activity_open(), "back returns to Activity");
    // "Show only these" filters the table to that parent.
    press(&mut rt, "act-parent-1110-filter");
    assert_eq!(rt.state.activity.parent, Some(1110));
    assert!(rt.state.activity.shown().iter().all(|e| e.ppid == 1110));
    press(&mut rt, "act-parent-clear");
    assert_eq!(rt.state.activity.parent, None);
}

#[test]
fn a_started_process_still_running_opens_its_page() {
    let mut rt = scene(activity_scene);
    let id = rt
        .state
        .activity
        .shown()
        .iter()
        .find(|e| e.pid == 1311 && e.kind == Kind::Start)
        .unwrap()
        .id();
    press(&mut rt, &format!("act-ev-{id}-open"));
    assert_eq!(rt.state.selected, Some(1311));
}

#[test]
fn a_row_opens_to_its_whole_command_line_and_ancestry() {
    let mut rt = scene(activity_scene);
    let gh = rt
        .state
        .activity
        .shown()
        .iter()
        .find(|e| e.pid == 1311)
        .unwrap()
        .id();
    rt.update(|s, _| s.activity.expanded = None);
    // Press the row's time, away from its links.
    press(&mut rt, &format!("act-ev-{gh}-time"));
    assert_eq!(rt.state.activity.expanded.as_deref(), Some(gh.as_str()));
    let cmd = rt.state.process(1311).unwrap().full_command_line.clone();
    assert!(!cmd.is_empty());
    assert_eq!(
        text_of(&rt, &format!("act-ev-{gh}-cmd")),
        Some(cmd),
        "the feed's command line, in full"
    );
}

#[test]
fn short_lived_only_and_the_filter_narrow_the_table() {
    let mut rt = scene(activity_scene);
    let all = rt.state.activity.shown().len();
    press(&mut rt, "act-short");
    assert!(rt.state.activity.short_only);
    let short = rt.state.activity.shown();
    assert!(!short.is_empty() && short.len() < all);
    assert!(
        short
            .iter()
            .filter(|e| e.kind == Kind::Exit)
            .all(|e| e.lifetime_ms.unwrap() < 2000)
    );
    assert!(
        short
            .iter()
            .all(|e| e.name != "rustc" && e.name != "mdworker_shared"),
        "long builds and workers go"
    );
    press(&mut rt, "act-short");
    rt.update(|s, _| s.activity.query = rune::TextEdit::new("rustc"));
    settle(&mut rt);
    assert!(rt.state.activity.shown().iter().all(|e| e.name == "rustc"));
    assert!(
        text_of(&rt, "act-count").unwrap().starts_with("28 of"),
        "{:?}",
        text_of(&rt, "act-count")
    );
}

#[test]
fn a_spawn_storm_is_an_overview_finding_that_opens_activity_filtered() {
    let mut rt = scene(activity_storm_scene);
    let storm = rt
        .state
        .findings
        .iter()
        .find(|f| f.rule == "spawn-storm")
        .expect("a storm finding")
        .clone();
    assert_eq!(storm.link, Link::Activity(1110));
    assert!(storm.title.starts_with("bash started 2"), "{}", storm.title);
    assert_eq!(
        rt.state.activity.parent,
        Some(1110),
        "the scene followed it"
    );
    assert!(has(&mut rt, "act-parent-chip"));
    rt.update(|s, _| s.show(Nav::Overview));
    settle(&mut rt);
    press(&mut rt, "subject-1110-spawn-storm");
    assert_eq!(rt.state.nav, Nav::Activity);
    assert_eq!(rt.state.activity.parent, Some(1110));
    assert!(rt.find("act-page").is_some());
}

#[test]
fn no_storm_on_an_ordinary_afternoon() {
    let rt = scene(activity_scene);
    assert!(rt.state.findings.iter().all(|f| f.rule != "spawn-storm"));
}

#[test]
fn an_older_spectra_gets_a_plain_notice() {
    let mut rt = scene(activity_unsupported_scene);
    assert!(has(&mut rt, "act-unsupported"));
    assert!(
        !has(&mut rt, "act-table") && !has(&mut rt, "act-summary"),
        "nothing to show under it"
    );
    assert!(has(&mut rt, "act-keep"));
}

#[test]
fn a_reused_pid_does_not_link_to_another_process_page() {
    let mut rt = scene(activity_scene);
    // The feed lists 1311 as something else now: the event's pid was reused.
    let id = rt
        .state
        .activity
        .shown()
        .iter()
        .find(|e| e.pid == 1311 && e.kind == Kind::Start)
        .unwrap()
        .id();
    assert!(rt.state.process(1311).is_some());
    rt.update(|s, _| {
        for e in s.activity.events.iter_mut().filter(|e| e.pid == 1311) {
            e.name = "reused-name".into();
        }
    });
    settle(&mut rt);
    let (x, y) = locate(&mut rt, &format!("act-ev-{id}-open"));
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(&mut rt);
    assert_ne!(rt.state.selected, Some(1311), "no link: the name differs");
}

#[test]
fn own_readings_hide_by_default_and_a_switch_shows_them() {
    let mut rt = scene(activity_scene);
    let all = rt.state.activity.events.len();
    rt.update(|s, _| {
        let mut e = s.activity.events[0].clone();
        e.pid = 777_001;
        e.own = true;
        s.activity.push([e]);
    });
    settle(&mut rt);
    assert!(rt.state.activity.shown().iter().all(|e| e.pid != 777_001));
    assert!(
        text_of(&rt, "act-count")
            .unwrap()
            .ends_with(&format!("of {all} events")),
        "{:?}",
        text_of(&rt, "act-count")
    );
    press(&mut rt, "act-own");
    assert!(rt.state.activity.show_own);
    assert!(rt.state.activity.shown().iter().any(|e| e.pid == 777_001));
}

#[test]
fn the_page_says_what_the_checks_can_miss() {
    let mut rt = scene(activity_scene);
    assert!(has(&mut rt, "act-blindspot"));
    let t = text_of(&rt, "act-blindspot").unwrap();
    assert!(
        t.contains("250 ms") && t.contains("every second") && t.contains("can be missed"),
        "{t}"
    );
}
