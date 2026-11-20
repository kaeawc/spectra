//! Headless runs of the real view: press what a person would press, then look for keyed elements.

use rune::{Env, Key, KeyEvent, Runtime, TextEdit};

use crate::app::{App, init};
use crate::data::{Gc, Jvm};
use crate::groups::{Category, Nav};
use crate::procs::{ProcError, Role};
use crate::sample::{all_scene, detail_scene, old_scene, sample_scene, signed_scene, stale_scene};

const SLACK: &str = "g:app::Applications:Slack.app";
const SLACK_ID: &str = "app::Applications:Slack.app";

fn runtime(state: App) -> Runtime<App> {
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(state, super::view, (980.0, 640.0), env);
    rt.on_key = Some(super::on_key);
    rt.frame(0.0);
    rt
}

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    runtime(s)
}

fn sample() -> Runtime<App> {
    scene(sample_scene)
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

/// Scrolls `key` into view and says where it is.
fn locate(rt: &mut Runtime<App>, key: &str) -> (f32, f32) {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    rt.find(key).unwrap_or_else(|| panic!("nothing keyed {key} on screen"))
}

/// Scrolls `key` into view, then presses it.
fn press(rt: &mut Runtime<App>, key: &str) {
    let (x, y) = locate(rt, key);
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

/// Two quick presses on `key`.
fn double_press(rt: &mut Runtime<App>, key: &str) {
    let (x, y) = locate(rt, key);
    for _ in 0..2 {
        rt.pointer_down(x, y);
        rt.pointer_up(x, y);
    }
    settle(rt);
}

fn key(rt: &mut Runtime<App>, k: Key) {
    rt.key(KeyEvent::from(k));
    settle(rt);
}

fn text_of(rt: &Runtime<App>, key: &str) -> Option<String> {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

fn shows_text(rt: &Runtime<App>, want: &str) -> bool {
    rt.elements().into_iter().any(|e| e.text.as_deref() == Some(want))
}

#[test]
fn the_overview_leads_with_a_summary_and_findings() {
    let rt = sample();
    for key in ["summary-procs", "summary-cpu", "summary-memory", "findings-title"] {
        assert!(rt.find(key).is_some(), "the overview shows {key}");
    }
    assert_eq!(text_of(&rt, "summary-procs-value").as_deref(), Some("52"));
    assert!(rt.find("subject-941-proc-invalid-signature").is_some());
    assert!(rt.find("too-old").is_none());
    assert!(rt.find("feed-status").is_none(), "fresh numbers need no status");
}

#[test]
fn a_sidebar_category_filters_the_list() {
    let mut rt = sample();
    press(&mut rt, "cat-apps");
    assert_eq!(rt.state.nav, Nav::Category(Category::Apps));
    assert!(rt.find("procs").is_some(), "the list shows");
    assert!(rt.find(SLACK).is_some(), "Slack is an app");
    assert!(rt.find("p:1340").is_none(), "python3 is not");
    let s = &rt.state;
    let pids: Vec<u32> = s.groups.iter().flat_map(|g| g.pids.iter().copied()).collect();
    assert!(!pids.is_empty());
    assert!(
        pids.iter()
            .all(|pid| matches!(s.process(*pid).unwrap().role, Role::App | Role::AppHelper))
    );

    press(&mut rt, "rt-jvm");
    assert_eq!(rt.state.nav, Nav::Runtime(crate::procs::Runtime::Jvm));
    let groups: Vec<(&str, usize, Option<&str>)> =
        rt.state.groups.iter().map(|g| (g.name.as_str(), g.pids.len(), g.hint.as_deref())).collect();
    assert_eq!(
        groups,
        [("java", 3, Some("temurin-21.jdk")), ("java", 1, Some("zulu-17.jdk"))],
        "the three Temurin JVMs as one row, the Zulu java on its own"
    );
    assert!(shows_text(&rt, "temurin-21.jdk") && shows_text(&rt, "zulu-17.jdk"), "the folder tells them apart");
}

#[test]
fn the_triangle_opens_a_group_and_a_double_click_toggles_it_once() {
    let mut rt = sample();
    press(&mut rt, "all");
    assert!(rt.find("p:901").is_none(), "closed");
    press(&mut rt, &format!("twisty-{SLACK_ID}"));
    assert!(rt.state.open_groups.contains(SLACK_ID));
    assert!(rt.find("p:901").is_some(), "its helper is listed under it");
    assert_eq!(rt.state.selected, None, "opening a group opens no page");
    press(&mut rt, &format!("twisty-{SLACK_ID}"));
    assert!(rt.find("p:901").is_none());

    press(&mut rt, SLACK);
    assert!(!rt.state.open_groups.contains(SLACK_ID), "a click on the row only moves the cursor");
    double_press(&mut rt, SLACK);
    assert!(rt.state.open_groups.contains(SLACK_ID), "a double click toggles once");
    double_press(&mut rt, SLACK);
    assert!(!rt.state.open_groups.contains(SLACK_ID));
}

#[test]
fn arrow_keys_move_the_cursor_and_return_opens() {
    let mut rt = sample();
    press(&mut rt, "all");
    let first = rt.state.rows[0].id.clone();
    press(&mut rt, &first);
    assert_eq!(rt.state.table.cursor(), Some(first.as_str()));
    assert_eq!(rt.state.selected, None, "a click opens nothing");
    for _ in 0..rt.state.rows.len().min(12) {
        key(&mut rt, Key::Down);
        assert_eq!(rt.state.selected, None, "arrowing onto a row opens nothing");
        assert!(rt.state.open_groups.is_empty(), "nor toggles a group");
    }
    // Back to a process row, then Return.
    let at = rt.state.rows.iter().position(|r| r.id == "p:1300").expect("rust-analyzer is listed");
    let cur = rt.state.rows.iter().position(|r| Some(r.id.as_str()) == rt.state.table.cursor()).unwrap();
    for _ in 0..cur.abs_diff(at) {
        key(&mut rt, if at < cur { Key::Up } else { Key::Down });
    }
    assert_eq!(rt.state.table.cursor(), Some("p:1300"));
    key(&mut rt, Key::Enter);
    assert_eq!(rt.state.selected, Some(1300), "Return opens it");
    key(&mut rt, Key::Escape);
    assert_eq!(rt.state.selected, None, "Esc goes back");
    assert_eq!(rt.state.nav, Nav::All);
}

#[test]
fn a_double_click_opens_a_row_and_back_returns() {
    let mut rt = sample();
    press(&mut rt, "all");
    double_press(&mut rt, "p:1300");
    assert_eq!(rt.state.selected, Some(1300));
    for key in [
        "pd-header",
        "pd-facts",
        "pd-lane-cpu",
        "pd-lane-mem",
        "pd-cmd",
        "pd-evidence",
    ] {
        assert!(rt.find(key).is_some(), "the page shows {key}");
    }
    assert_eq!(text_of(&rt, "pd-name").as_deref(), Some("rust-analyzer"));
    assert_eq!(
        text_of(&rt, "pd-verdict").as_deref(),
        Some("Busy: 137.8% CPU for over a minute")
    );
    press(&mut rt, "back");
    assert_eq!(rt.state.selected, None);
    assert_eq!(rt.state.nav, Nav::All, "back where it was");
}

#[test]
fn the_parent_link_opens_the_parent_and_back_returns_to_the_child() {
    let mut rt = sample();
    press(&mut rt, "all");
    double_press(&mut rt, "p:1300");
    assert_eq!(
        text_of(&rt, "pd-parent").as_deref(),
        Some("Code Helper (Plugin) · 921")
    );
    press(&mut rt, "pd-parent");
    assert_eq!(rt.state.selected, Some(921));
    assert_eq!(text_of(&rt, "pd-name").as_deref(), Some("Code Helper (Plugin)"));
    assert!(
        rt.find("pd-child-1300").is_some(),
        "and lists what it started"
    );
    assert_eq!(text_of(&rt, "back/label").or_else(|| back_label(&rt)).as_deref(), Some("‹ rust-analyzer"));
    key(&mut rt, Key::Escape);
    assert_eq!(rt.state.selected, Some(1300), "back to the child");
    rt.key(KeyEvent { key: Key::Command('['), mods: rune::Mods { cmd: true, ..rune::Mods::default() } });
    settle(&mut rt);
    assert_eq!(rt.state.selected, None, "Cmd-[ goes back too, to the list");
    double_press(&mut rt, "p:1300");
    press(&mut rt, "pd-parent");
    press(&mut rt, "pd-child-1310");
    assert_eq!(rt.state.selected, Some(1310));
    press(&mut rt, "back");
    assert_eq!(rt.state.selected, Some(921));
}

/// The back button's text, wherever Rune puts it under the button.
fn back_label(rt: &Runtime<App>) -> Option<String> {
    rt.elements()
        .into_iter()
        .filter(|e| e.text.as_deref().is_some_and(|t| t.starts_with('‹')))
        .find_map(|e| e.text)
}

#[test]
fn an_unverified_signature_says_so_in_the_list_and_on_the_page() {
    let mut rt = scene(all_scene);
    assert!(shows_text(&rt, "Developer ID · not verified"), "the list words it");
    assert!(shows_text(&rt, "Apple"), "a checked one keeps its plain name");
    let claude = rt.state.groups.iter().find(|g| g.name == "claude").unwrap();
    assert_eq!(claude.signing, crate::procs::Trust::Unverified(crate::procs::SignStatus::DeveloperId));
    rt = scene(signed_scene);
    assert_eq!(rt.state.selected, Some(1200));
    assert_eq!(
        text_of(&rt, "pd-signing").as_deref(),
        Some("team Q6L2SF6YDW as displayed, not verified · hardened runtime on")
    );
    let evidence = text_of(&rt, "pd-signing-evidence").unwrap();
    assert!(evidence.contains("not validated"), "{evidence}");
    // An Apple process's page reads as checked.
    rt.update(|s, _| s.open(152));
    settle(&mut rt);
    assert_eq!(text_of(&rt, "pd-signing").as_deref(), Some("no team · hardened runtime off"));
    assert!(text_of(&rt, "pd-signing-evidence").unwrap().contains("validated by codesign --verify"));
}

#[test]
fn a_failed_reading_keeps_the_sidebar_and_says_how_old_the_numbers_are() {
    let mut rt = scene(stale_scene);
    for key in ["all", "cat-apps", "rt-jvm", "procs"] {
        assert!(rt.find(key).is_some(), "still shows {key}");
    }
    assert_eq!(
        text_of(&rt, "feed-status").as_deref(),
        Some("Updated 14 s ago · last reading failed: spectra process failed: no processes found")
    );
    press(&mut rt, "overview");
    assert!(rt.find("summary-procs").is_some(), "the overview keeps its summary");
    assert!(rt.find("feed-status").is_some());
    assert!(rt.find("proc-error").is_none(), "no full-page error while there's a list");
    // An empty reading is a failure too.
    rt.update(|s, _| s.land_procs(Ok(vec![])));
    settle(&mut rt);
    assert!(text_of(&rt, "feed-status").unwrap().ends_with("last reading failed: Spectra listed no processes"));
}

#[test]
fn a_reused_pid_closes_its_page() {
    let mut rt = scene(signed_scene);
    assert_eq!(rt.state.selected, Some(1200));
    rt.update(|s, _| {
        let mut procs = s.procs.clone();
        let p = procs.iter_mut().find(|p| p.pid == 1200).unwrap();
        p.started_at = "2026-11-20T11:59:00-05:00".into();
        p.command = "node".into();
        s.land_procs(Ok(procs));
    });
    settle(&mut rt);
    assert_eq!(rt.state.selected, None, "pid 1200 is someone else now");
    assert!(rt.find("pd-page").is_none());
}

#[test]
fn without_signatures_the_list_has_no_signature_column() {
    let mut s = init();
    all_scene(&mut s);
    let mut procs = s.procs.clone();
    for p in &mut procs {
        p.signing = None;
    }
    s.land_procs(Ok(procs));
    let rt = runtime(s);
    assert!(rt.find("procs").is_some());
    assert!(!shows_text(&rt, "Signature"), "Linux has none to show");
    let rt = scene(all_scene);
    assert!(shows_text(&rt, "Signature"));
}

#[test]
fn a_spectra_too_old_to_classify_says_so_and_jvms_still_work() {
    let mut rt = scene(old_scene);
    assert!(rt.find("too-old").is_some());
    assert_eq!(
        text_of(&rt, "too-old-title").as_deref(),
        Some("Spectra is too old to list every process")
    );
    assert!(rt.find("all").is_none(), "no process list to offer");
    assert!(rt.find("subject-4102-jvm-heap-pressure").is_some());
    press(&mut rt, "jvm-4101");
    assert_eq!(rt.state.selected, Some(4101));
    for key in [
        "jd-header",
        "jd-tile-heap",
        "jd-tile-gc-time",
        "jd-lane-heap",
        "jd-lane-old",
        "jd-lane-gc",
        "jd-lane-threads",
        "jd-args",
    ] {
        assert!(rt.find(key).is_some(), "the JVM page shows {key}");
    }
    assert_eq!(
        text_of(&rt, "jd-verdict").as_deref(),
        Some("Heap 83% full"),
        "the verdict is its worst finding"
    );
    assert!(
        rt.find("jd-tile-heap-since").is_some(),
        "a pinned baseline shows deltas"
    );
    assert!(rt.find("subject-4101-jvm-heap-pressure").is_some());
    assert!(rt.find("subject-4102-jvm-heap-pressure").is_none());
    press(&mut rt, "overview");
    assert_eq!(rt.state.selected, None);
    assert!(rt.find("jd-detail").is_none());
    assert!(matches!(rt.state.proc_error, Some(ProcError::TooOld(_))));
}

#[test]
fn a_jvms_process_page_holds_its_jvm_numbers() {
    let rt = scene(detail_scene);
    assert_eq!(text_of(&rt, "pd-name").as_deref(), Some("Server"), "named for what it runs");
    assert_eq!(text_of(&rt, "pd-verdict").as_deref(), Some("Heap 83% full"));
    for key in ["jd-detail", "jd-tile-heap", "jd-lane-heap", "jd-args", "pd-lane-cpu"] {
        assert!(rt.find(key).is_some(), "the page shows {key}");
    }
    assert!(rt.find("subject-4101-jvm-heap-pressure").is_some());
    assert!(rt.find("subject-4102-jvm-heap-pressure").is_none());
}

#[test]
fn a_cards_subject_opens_its_process_without_toggling_the_card() {
    let mut rt = sample();
    let expanded = rt.state.expanded.clone();
    assert!(expanded.is_some());
    press(&mut rt, "subject-4102-jvm-heap-pressure");
    assert_eq!(rt.state.selected, Some(4102));
    assert_eq!(rt.state.expanded, expanded, "the card stays as it was");
    assert!(rt.find("jd-detail").is_some());
}

#[test]
fn the_unsigned_finding_lists_those_processes() {
    let mut rt = sample();
    press(&mut rt, "subject-0-proc-unidentified");
    assert_eq!(rt.state.nav, Nav::Unidentified);
    let s = &rt.state;
    assert!(
        s.groups
            .iter()
            .flat_map(|g| &g.pids)
            .all(|pid| s.process(*pid).unwrap().sign_status().is_unidentified())
    );
    assert!(rt.find("p:1340").is_some(), "python3 is unsigned");
}

#[test]
fn a_search_narrows_the_list() {
    let mut rt = sample();
    press(&mut rt, "all");
    rt.update(|s, _| {
        s.query = TextEdit::new("slack");
        s.rebuild();
    });
    settle(&mut rt);
    assert_eq!(rt.state.groups.len(), 1);
    assert!(rt.find(SLACK).is_some());
    assert!(rt.find("p:1300").is_none());
    rt.update(|s, _| {
        s.query = TextEdit::new("4101");
        s.rebuild();
    });
    settle(&mut rt);
    assert!(rt.find("p:4101").is_some(), "a lone match shows as its process");
}

#[test]
fn the_arguments_open_one_flag_per_line() {
    let mut rt = scene(detail_scene);
    assert!(rt.find("jd-arg-0").is_none(), "collapsed by default");
    press(&mut rt, "jd-args/trigger");
    assert!(rt.state.args_open);
    let flags: Vec<Option<String>> = (0..4)
        .map(|i| text_of(&rt, &format!("jd-arg-{i}")))
        .collect();
    let want = [
        "-Xmx2g",
        "-Xms512m",
        "-XX:+UseG1GC",
        "-Dspring.profiles.active=prod",
    ];
    assert_eq!(flags, want.map(|s| Some(s.to_owned())));
    press(&mut rt, "jd-args/trigger");
    assert!(!rt.state.args_open);
    assert!(rt.find("jd-arg-0").is_none());
}

#[test]
fn one_reading_says_its_still_collecting() {
    let mut s = init();
    let jvm = Jvm {
        pid: 7,
        main_class: "a.App".into(),
        vm_args: "-Xmx1g".into(),
        gc: Some(Gc {
            ou: 1024.0,
            ..Gc::default()
        }),
        ..Jvm::default()
    };
    s.land(Ok(vec![jvm]));
    s.selected = Some(7);
    let rt = runtime(s);
    assert!(rt.find("jd-timeline-collecting").is_some());
    assert!(rt.find("jd-lane-heap").is_none());
}
