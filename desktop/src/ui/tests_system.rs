//! Headless runs of the System page: the sidebar row, each part, the links to processes, and the
//! machine-wide findings on the overview.

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::findings::Link;
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_system::{
    system_failed_scene, system_linux_scene, system_pressure_scene, system_scene,
};

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
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
fn the_system_row_sits_in_the_sidebar_and_opens_the_page() {
    let mut rt = scene(sample_scene);
    assert!(
        rt.find("system").is_some(),
        "the System row is in the sidebar"
    );
    assert!(
        rt.find("cat-system").is_some(),
        "beside the System kind, which stays"
    );
    let (over, sys, all) = (
        rt.find("overview").unwrap(),
        rt.find("system").unwrap(),
        rt.find("all").unwrap(),
    );
    assert!(over.1 < sys.1 && sys.1 < all.1, "right under the overview");
    press(&mut rt, "system");
    assert_eq!(rt.state.nav, Nav::System);
    assert!(rt.find("sys-page").is_some(), "the System page shows");
    assert!(rt.find("procs").is_none(), "not a process list");
    // Even before any reading lands, each part says it's reading.
    assert!(has(&mut rt, "sys-mem-reading") && has(&mut rt, "sys-storage-reading"));
}

#[test]
fn the_system_page_shows_every_part() {
    let mut rt = scene(system_scene);
    for key in [
        "sys-cpu-use",
        "sys-load1",
        "sys-cpu-timeline",
        "sys-pressure-gauge",
        "sys-mem-timeline",
        "sys-lane-compressions",
        "sys-thermal-state",
        "sys-battery",
        "sys-vol-:System:Volumes:Data",
        "sys-vol-:Volumes:Backup",
        "sys-open-network",
        "sys-top-cpu",
        "sys-top-mem",
    ] {
        assert!(has(&mut rt, key), "the System page shows {key}");
    }
    assert_eq!(
        text_of(&rt, "sys-pressure").as_deref(),
        Some("Normal · 43% free for new work")
    );
    assert_eq!(
        text_of(&rt, "sys-battery").as_deref(),
        Some("82% · on power adapter")
    );
    assert!(
        !has(&mut rt, "sys-vol-:"),
        "the sealed system volume isn't listed"
    );
    assert!(
        !has(
            &mut rt,
            "sys-vol-:Library:Developer:CoreSimulator:Volumes:iOS_23F77"
        ),
        "nor a simulator image"
    );
    assert!(
        text_of(&rt, "sys-mem-per-reading")
            .unwrap()
            .ends_with("compressions")
    );
    for key in ["sys-mem-status", "sys-mem-error", "sys-fact-speed-limit"] {
        assert!(!has(&mut rt, key), "fresh, healthy readings need no {key}");
    }
}

#[test]
fn a_top_consumer_opens_its_process() {
    let mut rt = scene(system_scene);
    let busiest = rt
        .state
        .procs
        .iter()
        .max_by(|a, b| a.cpu_pct.total_cmp(&b.cpu_pct))
        .unwrap()
        .pid;
    press(&mut rt, &format!("sys-top-cpu-{busiest}"));
    assert_eq!(rt.state.selected, Some(busiest));
    press(&mut rt, "back");
    let largest = rt
        .state
        .procs
        .iter()
        .max_by(|a, b| a.rss_kib.total_cmp(&b.rss_kib))
        .unwrap()
        .pid;
    press(&mut rt, &format!("sys-top-mem-{largest}"));
    assert_eq!(rt.state.selected, Some(largest));
}

#[test]
fn critical_pressure_and_throttling_show_on_the_page_and_as_findings() {
    let mut rt = scene(system_pressure_scene);
    assert!(
        text_of(&rt, "sys-pressure")
            .unwrap()
            .starts_with("Critical")
    );
    assert!(has(&mut rt, "sys-fact-speed-limit"));
    assert_eq!(
        text_of(&rt, "sys-thermal-state").as_deref(),
        Some("Throttled")
    );
    let rules: Vec<&str> = rt
        .state
        .findings
        .iter()
        .filter(|f| f.link == Link::System)
        .map(|f| f.rule)
        .collect();
    for rule in [
        "sys-memory-pressure",
        "sys-swap-growing",
        "sys-thermal-throttled",
        "sys-volume-full",
    ] {
        assert!(rules.contains(&rule), "{rule} in {rules:?}");
    }
    // On the overview, beside the process findings, and each opens the System page.
    press(&mut rt, "overview");
    assert!(has(&mut rt, "finding-0/sys-memory-pressure"));
    assert!(
        has(&mut rt, "subject-941-proc-invalid-signature"),
        "the process findings still show"
    );
    press(&mut rt, "subject-0-sys-volume-full");
    assert_eq!(rt.state.nav, Nav::System);
}

#[test]
fn the_overview_keeps_its_findings_without_any_system_reading() {
    let rt = scene(sample_scene);
    assert!(rt.find("subject-941-proc-invalid-signature").is_some());
    assert!(
        rt.state.findings.iter().all(|f| f.link != Link::System),
        "nothing machine-wide yet"
    );
}

#[test]
fn a_failing_reading_keeps_the_last_good_one_and_says_why() {
    let mut rt = scene(system_failed_scene);
    assert_eq!(
        text_of(&rt, "sys-mem-status").as_deref(),
        Some(
            "Updated 40 s ago · last reading failed: spectra memory failed: host_statistics64 returned 5"
        )
    );
    assert!(
        has(&mut rt, "sys-pressure-gauge"),
        "the last good numbers stay"
    );
    assert!(
        has(&mut rt, "sys-storage-error"),
        "disks never read: a callout says why"
    );
    assert!(has(&mut rt, "sys-net-error"));
    assert!(
        !has(&mut rt, "sys-thermal-error"),
        "an older Spectra without thermal hides it"
    );
    assert!(has(&mut rt, "sys-battery"), "the battery still shows");
}

#[test]
fn on_linux_what_spectra_doesnt_read_is_left_out() {
    let mut rt = scene(system_linux_scene);
    assert!(
        !has(&mut rt, "sys-memory"),
        "no memory pressure reading: no memory part at all"
    );
    assert!(!has(&mut rt, "sys-battery"), "no battery");
    assert!(has(&mut rt, "sys-thermal-state"));
    assert!(has(&mut rt, "sys-vol-:") && has(&mut rt, "sys-vol-:home"));
    assert!(
        !has(&mut rt, "sys-vol-:dev:shm"),
        "memory-backed file systems aren't disks"
    );
    assert!(has(&mut rt, "sys-cpu-timeline") && has(&mut rt, "sys-open-network"));
}

#[test]
fn the_cpu_lane_needs_the_process_feed() {
    let mut rt = scene(system_scene);
    assert!(rt.state.has_processes());
    assert!(has(&mut rt, "sys-lane-cpu") && has(&mut rt, "sys-lane-load"));
    rt.state.procs.clear();
    rt.frame(1.0 / 60.0);
    assert!(!has(&mut rt, "sys-cpu-use"), "no tile without the feed");
    assert!(!has(&mut rt, "sys-lane-cpu"), "no flat 0% lane either");
    assert!(
        has(&mut rt, "sys-lane-load"),
        "load comes from Spectra, not the feed"
    );
}
