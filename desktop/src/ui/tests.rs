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
    rt.find(key)
        .unwrap_or_else(|| panic!("nothing keyed {key} on screen"))
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
    rt.elements()
        .into_iter()
        .any(|e| e.text.as_deref() == Some(want))
}

#[test]
fn the_overview_leads_with_a_summary_and_findings() {
    let rt = sample();
    for key in [
        "summary-procs",
        "summary-cpu",
        "summary-memory",
        "findings-title",
    ] {
        assert!(rt.find(key).is_some(), "the overview shows {key}");
    }
    assert_eq!(text_of(&rt, "summary-procs-value").as_deref(), Some("55"));
    assert!(rt.find("subject-941-proc-invalid-signature").is_some());
    assert!(rt.find("too-old").is_none());
    assert!(
        rt.find("feed-status").is_none(),
        "fresh numbers need no status"
    );
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
    let pids: Vec<u32> = s
        .groups
        .iter()
        .flat_map(|g| g.pids.iter().copied())
        .collect();
    assert!(!pids.is_empty());
    assert!(
        pids.iter()
            .all(|pid| matches!(s.process(*pid).unwrap().role, Role::App | Role::AppHelper))
    );

    press(&mut rt, "rt-jvm");
    assert_eq!(rt.state.nav, Nav::Runtime(crate::procs::Runtime::Jvm));
    let groups: Vec<(&str, usize, Option<&str>)> = rt
        .state
        .groups
        .iter()
        .map(|g| (g.name.as_str(), g.pids.len(), g.hint.as_deref()))
        .collect();
    assert_eq!(
        groups,
        [
            ("java", 3, Some("temurin-21.jdk")),
            ("java", 1, Some("zulu-17.jdk"))
        ],
        "the three Temurin JVMs as one row, the Zulu java on its own"
    );
    assert!(
        shows_text(&rt, "temurin-21.jdk") && shows_text(&rt, "zulu-17.jdk"),
        "the folder tells them apart"
    );
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
    assert!(
        !rt.state.open_groups.contains(SLACK_ID),
        "a click on the row only moves the cursor"
    );
    assert_eq!(rt.state.app_page, None, "and opens no page");

    // A group that isn't an app toggles on a double click, once.
    let zsh = rt
        .state
        .groups
        .iter()
        .find(|g| g.name == "zsh")
        .unwrap()
        .id
        .clone();
    let row = format!("g:{zsh}");
    double_press(&mut rt, &row);
    assert!(
        rt.state.open_groups.contains(&zsh),
        "a double click toggles once"
    );
    double_press(&mut rt, &row);
    assert!(!rt.state.open_groups.contains(&zsh));
}

#[test]
fn a_double_click_on_an_app_opens_its_page_and_back_returns() {
    let mut rt = sample();
    press(&mut rt, "all");
    double_press(&mut rt, SLACK);
    assert_eq!(
        rt.state.app_page.as_deref(),
        Some("/Applications/Slack.app")
    );
    assert_eq!(rt.state.selected, None);
    assert!(
        !rt.state.open_groups.contains(SLACK_ID),
        "the group stays closed"
    );
    for key in [
        "ap-header",
        "ap-lane-cpu",
        "ap-lane-threads",
        "ap-children",
        "ap-live",
    ] {
        assert!(rt.find(key).is_some(), "the app page shows {key}");
    }
    assert_eq!(text_of(&rt, "ap-name").as_deref(), Some("Slack"));
    assert_eq!(
        text_of(&rt, "ap-meta")
            .as_deref()
            .map(|m| m.starts_with("4 processes")),
        Some(true)
    );
    // One of its processes, then back to the app, then back to the list.
    press(&mut rt, "ap-child-901");
    assert_eq!(rt.state.selected, Some(901));
    assert_eq!(back_label(&rt).as_deref(), Some("‹ Slack"));
    key(&mut rt, Key::Escape);
    assert_eq!(
        rt.state.app_page.as_deref(),
        Some("/Applications/Slack.app"),
        "back to the app page"
    );
    key(&mut rt, Key::Escape);
    assert_eq!(
        (rt.state.selected, rt.state.app_page.as_deref()),
        (None, None)
    );
    assert_eq!(rt.state.nav, Nav::All);
}

#[test]
fn return_on_an_app_row_opens_its_page_and_quit_shortcuts_ignore_the_hidden_cursor() {
    let mut rt = sample();
    press(&mut rt, "all");
    press(&mut rt, SLACK);
    key(&mut rt, Key::Enter);
    assert_eq!(
        rt.state.app_page.as_deref(),
        Some("/Applications/Slack.app")
    );
    assert_eq!(
        rt.state.shortcut_target(),
        None,
        "the list's cursor is under the page"
    );
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
    let at = rt
        .state
        .rows
        .iter()
        .position(|r| r.id == "p:1300")
        .expect("rust-analyzer is listed");
    let cur = rt
        .state
        .rows
        .iter()
        .position(|r| Some(r.id.as_str()) == rt.state.table.cursor())
        .unwrap();
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
    assert_eq!(
        text_of(&rt, "pd-name").as_deref(),
        Some("Code Helper (Plugin)")
    );
    assert!(
        rt.find("pd-child-1300").is_some(),
        "and lists what it started"
    );
    assert_eq!(
        text_of(&rt, "back/label")
            .or_else(|| back_label(&rt))
            .as_deref(),
        Some("‹ rust-analyzer")
    );
    key(&mut rt, Key::Escape);
    assert_eq!(rt.state.selected, Some(1300), "back to the child");
    rt.key(KeyEvent {
        key: Key::Command('['),
        mods: rune::Mods {
            cmd: true,
            ..rune::Mods::default()
        },
    });
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
    assert!(
        shows_text(&rt, "Developer ID · not verified"),
        "the list words it"
    );
    assert!(
        shows_text(&rt, "Apple"),
        "a checked one keeps its plain name"
    );
    let claude = rt.state.groups.iter().find(|g| g.name == "claude").unwrap();
    assert_eq!(
        claude.signing,
        crate::procs::Trust::Unverified(crate::procs::SignStatus::DeveloperId)
    );
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
    assert_eq!(
        text_of(&rt, "pd-signing").as_deref(),
        Some("no team · hardened runtime off")
    );
    assert!(
        text_of(&rt, "pd-signing-evidence")
            .unwrap()
            .contains("validated by codesign --verify")
    );
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
    assert!(
        rt.find("summary-procs").is_some(),
        "the overview keeps its summary"
    );
    assert!(rt.find("feed-status").is_some());
    assert!(
        rt.find("proc-error").is_none(),
        "no full-page error while there's a list"
    );
    // An empty reading is a failure too.
    rt.update(|s, _| s.land_procs(Ok(vec![])));
    settle(&mut rt);
    assert!(
        text_of(&rt, "feed-status")
            .unwrap()
            .ends_with("last reading failed: Spectra listed no processes")
    );
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
    assert_eq!(
        text_of(&rt, "pd-name").as_deref(),
        Some("Server"),
        "named for what it runs"
    );
    assert_eq!(text_of(&rt, "pd-verdict").as_deref(), Some("Heap 83% full"));
    for key in [
        "jd-detail",
        "jd-tile-heap",
        "jd-lane-heap",
        "jd-args",
        "pd-lane-cpu",
    ] {
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
        s.groups.iter().flat_map(|g| &g.pids).all(|pid| s
            .process(*pid)
            .unwrap()
            .sign_status()
            .is_unidentified())
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
    assert!(
        rt.find("p:4101").is_some(),
        "a lone match shows as its process"
    );
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

// Quit and Force quit. A recorder stands in for `ps` and `kill`: nothing here signals anything.

use crate::app::{Runner, Status};
use crate::sample::{force_confirm_scene, quit_list_scene, quit_root_scene, quit_scene};
use crate::signal::tests::Recorder;
use crate::signal::{SendError, Signal, Target};

/// `f`'s scene with a recorder that says every sample process is alive as listed, and a worker
/// that holds its jobs until [`release`].
fn recorded(f: fn(&mut App)) -> (Runtime<App>, Recorder) {
    let mut s = init();
    f(&mut s);
    let r = Recorder::default();
    for p in &s.procs {
        r.alive(p.pid, &p.started_at, &p.executable_path);
    }
    s.signals = std::sync::Arc::new(r.clone());
    s.runner = Runner::Held(Default::default());
    (runtime(s), r)
}

/// Lets the worker run what it holds (the `ps` and `kill`), then lands it.
fn release(rt: &mut Runtime<App>) {
    rt.update(|s, _| s.run_held());
    settle(rt);
}

/// Presses the confirmation's button and lets the worker finish.
fn send(rt: &mut Runtime<App>) {
    press(rt, "confirm-send");
    release(rt);
}

fn opt_cmd_q() -> KeyEvent {
    KeyEvent {
        key: Key::Command('q'),
        mods: rune::Mods {
            cmd: true,
            alt: true,
            ..rune::Mods::default()
        },
    }
}

#[test]
fn confirming_quit_sends_one_term_to_that_pid_and_polls_again() {
    let (mut rt, r) = recorded(quit_scene);
    press(&mut rt, "pd-quit");
    assert!(
        rt.find("confirm-dialog").is_some(),
        "the confirmation opens"
    );
    assert!(r.sent().is_empty(), "opening it sends nothing");
    assert!(shows_text(&rt, "Quit Slack?"));
    assert!(shows_text(
        &rt,
        "Slack (pid 900) will be asked to close. Unsaved changes may be lost."
    ));
    press(&mut rt, "confirm-send");
    assert!(rt.find("confirm-dialog").is_none());
    assert!(
        r.asked().is_empty() && r.sent().is_empty(),
        "ps and kill wait for the worker"
    );
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some("Checking that pid 900 is still Slack…")
    );
    assert!(
        rt.find("quit-outcome-dismiss").is_none(),
        "the answer has to land somewhere"
    );
    // Nothing more can be asked meanwhile, by any route.
    rt.update(|s, _| {
        s.ask(900, Signal::Kill);
        s.ask(1300, Signal::Term);
        s.dismiss_outcome();
    });
    rt.key(opt_cmd_q());
    settle(&mut rt);
    assert!(rt.state.confirm.is_none() && rt.state.checking());
    release(&mut rt);
    assert_eq!(r.sent(), [(900, Signal::Term)]);
    assert_eq!(r.asked(), [900], "checked who has the pid first");
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some("Asked Slack to quit")
    );
    assert!(
        rt.state.poke_rx.as_ref().unwrap().try_recv().is_ok(),
        "the feed is asked for a reading now"
    );
}

#[test]
fn confirming_force_quit_sends_one_kill_and_warns_about_macos() {
    let (mut rt, r) = recorded(force_confirm_scene);
    assert!(shows_text(&rt, "Force quit trustd?"));
    assert!(shows_text(
        &rt,
        "trustd (pid 340) will be stopped immediately. Unsaved changes will be lost."
    ));
    assert_eq!(
        text_of(&rt, "confirm-line-1").as_deref(),
        Some("This is part of macOS and may restart or disrupt your session.")
    );
    send(&mut rt);
    assert_eq!(r.sent(), [(340, Signal::Kill)]);
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some("Stopping trustd")
    );
}

#[test]
fn cancel_esc_the_close_button_and_return_on_the_default_button_send_nothing() {
    let (mut rt, r) = recorded(quit_scene);
    press(&mut rt, "pd-quit");
    press(&mut rt, "confirm-cancel");
    assert!(rt.state.confirm.is_none());
    press(&mut rt, "pd-force");
    key(&mut rt, Key::Escape);
    assert!(rt.state.confirm.is_none(), "Esc cancels");
    assert_eq!(rt.state.selected, Some(900), "and doesn't also go back");
    press(&mut rt, "pd-force");
    press(&mut rt, "close");
    assert!(rt.state.confirm.is_none());
    // Cancel has focus: Return sends nothing, Space presses Cancel.
    press(&mut rt, "pd-force");
    let focused = rt
        .elements()
        .into_iter()
        .find(|e| e.focused)
        .and_then(|e| e.key);
    assert_eq!(focused.as_deref(), Some("confirm-cancel"));
    key(&mut rt, Key::Enter);
    assert!(r.sent().is_empty(), "Return isn't a yes");
    key(&mut rt, Key::Text(" ".into()));
    assert!(rt.state.confirm.is_none(), "Space pressed Cancel");
    assert!(r.sent().is_empty());
    assert!(rt.state.outcome.is_none());
}

#[test]
fn a_pid_that_changed_hands_or_ended_gets_nothing() {
    let (mut rt, r) = recorded(quit_scene);
    let exe = rt.state.process(900).unwrap().executable_path.clone();
    let cases = [
        (
            Some(("2026-11-19T08:00:05-05:00", exe.as_str())),
            "already ended; its pid now belongs to another process",
        ),
        (
            Some(("2026-11-19T08:00:04-05:00", "/usr/bin/node")),
            "pid 900 now runs /usr/bin/node, not Slack",
        ),
        (None, "That process has already ended. Nothing was sent."),
    ];
    for (now, says) in cases {
        r.forget(900);
        if let Some((at, comm)) = now {
            r.alive(900, at, comm);
        }
        press(&mut rt, "pd-force");
        send(&mut rt);
        let line = text_of(&rt, "quit-outcome-text").unwrap();
        assert!(line.contains(says), "{line}");
        assert!(matches!(
            rt.state.outcome.as_ref().unwrap().status,
            Status::Refused(_)
        ));
    }
    assert!(
        r.sent().is_empty(),
        "nothing was sent to a pid that isn't Slack's"
    );
}

/// Every way to ask for `pid`, straight to the guard: Rune ignores a press on a disabled
/// button, so pressing one would prove nothing about `ask`.
fn ask_every_way(rt: &mut Runtime<App>, pid: u32) {
    rt.update(move |s, _| {
        let t = Target::of(s.process(pid).expect("listed"));
        for signal in [Signal::Term, Signal::Kill] {
            s.ask(pid, signal);
            s.ask_target(t.clone(), signal);
        }
    });
    rt.key(opt_cmd_q());
    settle(rt);
}

#[test]
fn the_kernel_launchd_the_app_itself_and_what_its_readings_started_are_refused() {
    let (mut rt, r) = recorded(quit_scene);
    rt.update(|s, _| {
        let mut procs = s.procs.clone();
        let mut me = procs.iter().find(|p| p.pid == 930).unwrap().clone();
        me.pid = s.me.pid;
        me.command = "spectra-desktop".into();
        let mut reading = me.clone();
        reading.pid = 5001;
        reading.ppid = s.me.pid;
        reading.command = "spectra".into();
        // What the reading started, and what that started.
        let mut ps = reading.clone();
        ps.pid = 5002;
        ps.ppid = 5001;
        ps.command = "ps".into();
        let mut grandchild = ps.clone();
        grandchild.pid = 5003;
        grandchild.ppid = 5002;
        let mut kernel = me.clone();
        kernel.pid = 0;
        procs.extend([me, reading, ps, grandchild, kernel]);
        procs.sort_by_key(|p| p.pid);
        s.land_procs(Ok(procs));
    });
    // The guard lets an ordinary process through, so the refusals below are the guard's.
    rt.update(|s, _| s.ask(900, Signal::Term));
    assert!(rt.state.confirm.is_some());
    key(&mut rt, Key::Escape);
    for pid in [0, 1, 5000, 5001, 5002, 5003] {
        rt.update(move |s, _| s.open(pid));
        settle(&mut rt);
        assert!(rt.find("pd-refusal").is_some(), "pid {pid} says why");
        ask_every_way(&mut rt, pid);
        assert!(rt.state.confirm.is_none(), "pid {pid} can't be asked");
    }
    assert_eq!(
        text_of(&rt, "pd-refusal").as_deref(),
        Some("Spectra Desktop started it to take a reading; it ends on its own.")
    );
    rt.update(|s, _| s.open(1));
    settle(&mut rt);
    assert_eq!(
        text_of(&rt, "pd-refusal").as_deref(),
        Some("launchd starts and looks after everything else; Spectra Desktop won't stop it.")
    );
    assert!(r.sent().is_empty());
}

#[test]
fn another_users_process_is_off_with_the_reason_shown() {
    let (mut rt, r) = recorded(quit_root_scene);
    assert_eq!(
        text_of(&rt, "pd-refusal").as_deref(),
        Some("Owned by root; Spectra Desktop can't stop it")
    );
    ask_every_way(&mut rt, 301);
    assert!(rt.state.confirm.is_none());
    assert!(r.sent().is_empty());
}

#[test]
fn an_app_rows_quit_is_its_main_process_and_other_groups_have_none() {
    let (mut rt, r) = recorded(quit_list_scene);
    assert_eq!(
        rt.state.cursor_target(),
        Some(900),
        "Slack's main process, not a helper"
    );
    assert_eq!(text_of(&rt, "list-target").as_deref(), Some("Slack · 900"));
    press(&mut rt, "list-quit");
    assert_eq!(
        rt.state.confirm.as_ref().map(|c| (c.target.pid, c.signal)),
        Some((900, Signal::Term))
    );
    assert!(shows_text(&rt, "Quit Slack?"));
    press(&mut rt, "confirm-cancel");
    // Several zsh are no one process.
    let zsh = rt
        .state
        .groups
        .iter()
        .find(|g| g.name == "zsh")
        .unwrap()
        .id
        .clone();
    rt.update(move |s, _| s.table.select(format!("g:{zsh}")));
    settle(&mut rt);
    assert_eq!(rt.state.cursor_target(), None);
    assert!(rt.find("list-quit").is_none(), "no group action");
    assert!(rt.find("list-no-target").is_some());
    rt.key(opt_cmd_q());
    settle(&mut rt);
    assert!(rt.state.confirm.is_none());
    // An app whose main process isn't running has none either.
    rt.update(|s, _| {
        let procs = s.procs.iter().filter(|p| p.pid != 900).cloned().collect();
        s.land_procs(Ok(procs));
        s.table.select(SLACK);
    });
    settle(&mut rt);
    assert_eq!(rt.state.cursor_target(), None, "only helpers left");
    // A helper on its own row is its own target.
    rt.update(|s, _| s.table.select("p:901"));
    assert_eq!(rt.state.cursor_target(), Some(901));
    assert!(r.sent().is_empty());
}

#[test]
fn a_double_press_on_the_confirm_button_sends_once() {
    let (mut rt, r) = recorded(quit_scene);
    press(&mut rt, "pd-force");
    double_press(&mut rt, "confirm-send");
    release(&mut rt);
    assert_eq!(r.sent(), [(900, Signal::Kill)]);
    // Twice in one go, as a key repeat might, and again while the first is being checked.
    rt.update(|s, _| s.ask(900, Signal::Kill));
    rt.update(|s, _| {
        s.confirm();
        s.confirm();
        s.ask(900, Signal::Kill);
        s.confirm();
    });
    release(&mut rt);
    release(&mut rt);
    assert_eq!(r.sent().len(), 2, "one per confirmation");
}

#[test]
fn the_shortcut_only_opens_the_confirmation() {
    let (mut rt, r) = recorded(quit_list_scene);
    for _ in 0..5 {
        rt.key(opt_cmd_q());
        settle(&mut rt);
    }
    assert_eq!(
        rt.state.confirm.as_ref().map(|c| (c.target.pid, c.signal)),
        Some((900, Signal::Term))
    );
    assert!(r.sent().is_empty(), "a held shortcut never sends");
    key(&mut rt, Key::Escape);
    // On a page it acts on the page's process.
    rt.update(|s, _| s.open(1300));
    settle(&mut rt);
    rt.key(opt_cmd_q());
    settle(&mut rt);
    assert_eq!(rt.state.confirm.as_ref().map(|c| c.target.pid), Some(1300));
    // Without the shortcut's modifiers, Q is just a key.
    key(&mut rt, Key::Escape);
    rt.key(KeyEvent {
        key: Key::Command('q'),
        mods: rune::Mods {
            cmd: true,
            ..rune::Mods::default()
        },
    });
    key(&mut rt, Key::Text("q".into()));
    assert!(rt.state.confirm.is_none());
    assert!(r.sent().is_empty());
}

/// Opens the code view's panel on the page as it is, without running anything.
fn open_code_panel(rt: &mut Runtime<App>) {
    rt.update(|s, _| {
        let req = crate::codeview_plan::Request::Script {
            frame: "at /srv/web/a.js:2:1".into(),
        };
        s.codeview.open = Some(crate::codeview::Panel {
            title: req.title(),
            request: req,
            origin: s.origin(),
            state: crate::codeview::State::Running,
            show_all: false,
        });
    });
    settle(rt);
}

#[test]
fn shortcuts_keep_working_while_the_code_view_is_open() {
    let (mut rt, _) = recorded(quit_scene);
    open_code_panel(&mut rt);
    // ⌥⌘Q still opens Quit, and Esc answers Quit before it closes the panel.
    rt.key(opt_cmd_q());
    settle(&mut rt);
    assert_eq!(rt.state.confirm.as_ref().map(|c| c.target.pid), Some(900));
    key(&mut rt, Key::Escape);
    assert!(rt.state.confirm.is_none());
    assert!(rt.state.codeview.open.is_some());
    key(&mut rt, Key::Escape);
    assert!(rt.state.codeview.open.is_none());
    assert_eq!(rt.state.selected, Some(900), "Esc closed only the panel");
    // The sidebar toggle.
    open_code_panel(&mut rt);
    let was = rt.state.sidebar.is_open();
    rt.key(KeyEvent {
        key: Key::Command('b'),
        mods: rune::Mods {
            cmd: true,
            shift: true,
            ..rune::Mods::default()
        },
    });
    settle(&mut rt);
    assert_ne!(rt.state.sidebar.is_open(), was);
    // Cmd-[ goes back.
    rt.key(KeyEvent {
        key: Key::Command('['),
        mods: rune::Mods {
            cmd: true,
            ..rune::Mods::default()
        },
    });
    settle(&mut rt);
    assert_eq!(rt.state.selected, None);
}

#[test]
fn typing_in_the_search_field_triggers_nothing() {
    let (mut rt, r) = recorded(quit_list_scene);
    press(&mut rt, "search");
    for k in ["q", "u", "i", "t"] {
        key(&mut rt, Key::Text(k.into()));
    }
    for k in [Key::Backspace, Key::Delete, Key::Enter] {
        key(&mut rt, k);
    }
    assert!(rt.state.confirm.is_none(), "no confirmation from typing");
    assert_eq!(rt.state.query.text, "qui", "the field took the keys");
    assert!(r.sent().is_empty());
}

/// Lands the scene's processes again `secs` later, with or without Slack.
fn reading(rt: &mut Runtime<App>, secs: f64, slack: bool) {
    rt.update(move |s, _| {
        s.now += secs;
        let procs = s
            .procs
            .iter()
            .filter(|p| slack || p.pid != 900)
            .cloned()
            .collect();
        s.land_procs(Ok(procs));
    });
    settle(rt);
}

#[test]
fn a_quit_that_isnt_taken_offers_force_quit() {
    let (mut rt, r) = recorded(quit_scene);
    press(&mut rt, "pd-quit");
    send(&mut rt);
    reading(&mut rt, 2.0, true);
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some("Asked Slack to quit"),
        "two seconds is patience"
    );
    assert!(rt.find("quit-outcome-force").is_none());
    press(&mut rt, "pd-quit");
    assert!(rt.state.confirm.is_none(), "already asked");
    reading(&mut rt, 4.0, true);
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some("Slack is still running 5 s after being asked to quit")
    );
    // `ps` is asked, off the UI thread, whether it's a zombie; it isn't.
    release(&mut rt);
    assert_eq!(
        rt.state.outcome.as_ref().unwrap().status,
        Status::StillRunning
    );
    press(&mut rt, "quit-outcome-force");
    assert_eq!(
        rt.state.confirm.as_ref().map(|c| c.signal),
        Some(Signal::Kill),
        "it only opens the confirmation"
    );
    assert_eq!(r.sent(), [(900, Signal::Term)]);
    send(&mut rt);
    assert_eq!(r.sent(), [(900, Signal::Term), (900, Signal::Kill)]);
}

#[test]
fn a_quit_process_is_said_to_have_quit_and_its_page_offers_back() {
    let (mut rt, _) = recorded(quit_scene);
    press(&mut rt, "pd-quit");
    send(&mut rt);
    reading(&mut rt, 2.0, false);
    assert_eq!(rt.state.outcome.as_ref().unwrap().status, Status::Ended);
    assert_eq!(rt.state.selected, Some(900), "the page stays to say so");
    assert!(rt.find("ended-page").is_some());
    assert!(rt.find("pd-page").is_none());
    assert_eq!(text_of(&rt, "ended-said").as_deref(), Some("Slack quit"));
    press(&mut rt, "ended-back");
    assert_eq!(rt.state.selected, None);
    assert_eq!(rt.state.nav, Nav::Category(Category::Apps));
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some("Slack quit"),
        "the list says it too"
    );
    press(&mut rt, "quit-outcome-dismiss");
    assert!(rt.find("quit-outcome").is_none());
}

#[test]
fn a_failed_kill_says_why() {
    let (mut rt, r) = recorded(quit_scene);
    r.fail_kill(SendError::NotSent(
        "kill failed: Operation not permitted".into(),
    ));
    press(&mut rt, "pd-quit");
    send(&mut rt);
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some("Couldn't quit Slack: kill failed: Operation not permitted.")
    );
    assert!(matches!(
        rt.state.outcome.as_ref().unwrap().status,
        Status::Failed(_)
    ));
    assert!(
        rt.state.poke_rx.as_ref().unwrap().try_recv().is_err(),
        "nothing was sent, so no hurry"
    );
}

#[test]
fn an_invalid_signature_points_to_force_quit_on_its_page() {
    let (mut rt, r) = recorded(sample_scene);
    rt.update(|s, _| s.expanded = Some("941/proc-invalid-signature".into()));
    settle(&mut rt);
    press(&mut rt, "fix-open-941");
    assert_eq!(rt.state.selected, Some(941), "it opens the page");
    assert!(rt.state.confirm.is_none(), "and does nothing more");
    assert!(r.sent().is_empty());
}

/// Quit Slack and let the send go through.
fn quit_slack(rt: &mut Runtime<App>) {
    press(rt, "pd-quit");
    send(rt);
    assert!(matches!(
        rt.state.outcome.as_ref().unwrap().status,
        Status::Asked { .. }
    ));
}

#[test]
fn a_failed_reading_leaves_a_quit_as_asked() {
    let (mut rt, _) = recorded(quit_scene);
    quit_slack(&mut rt);
    let asked = rt.state.outcome.as_ref().unwrap().status.clone();
    rt.update(|s, _| {
        s.now += 10.0;
        s.land_procs(Err(ProcError::Failed("spectra process failed".into())));
    });
    settle(&mut rt);
    assert_eq!(
        rt.state.outcome.as_ref().unwrap().status,
        asked,
        "a failure says nothing about Slack"
    );
    rt.update(|s, _| s.land_procs(Ok(vec![])));
    settle(&mut rt);
    assert_eq!(
        rt.state.outcome.as_ref().unwrap().status,
        asked,
        "nor does an empty reading"
    );
    // The next good reading does.
    reading(&mut rt, 0.0, true);
    assert_eq!(
        rt.state.outcome.as_ref().unwrap().status,
        Status::StillRunning
    );
}

#[test]
fn the_same_pid_with_a_new_start_has_ended() {
    let (mut rt, _) = recorded(quit_scene);
    quit_slack(&mut rt);
    rt.update(|s, _| {
        let mut procs = s.procs.clone();
        let p = procs.iter_mut().find(|p| p.pid == 900).unwrap();
        p.started_at = "2026-11-20T11:59:00-05:00".into();
        s.land_procs(Ok(procs));
    });
    settle(&mut rt);
    assert_eq!(rt.state.outcome.as_ref().unwrap().status, Status::Ended);
    assert_eq!(text_of(&rt, "ended-said").as_deref(), Some("Slack quit"));
}

/// Slack still listed `STILL_AFTER` seconds after Quit, with `ps`'s answer landed.
fn still_running(rt: &mut Runtime<App>) {
    quit_slack(rt);
    reading(rt, 6.0, true);
    release(rt);
}

#[test]
fn the_banners_force_quit_opens_nothing_once_the_pid_changed_hands() {
    let (mut rt, r) = recorded(quit_scene);
    still_running(&mut rt);
    let drawn = rt.state.outcome.as_ref().unwrap().target.clone();
    let (x, y) = locate(&mut rt, "quit-outcome-force");
    // A reading lands between the draw and the press: pid 900 is now someone else's.
    rt.state.land_procs(Ok({
        let mut procs = rt.state.procs.clone();
        let p = procs.iter_mut().find(|p| p.pid == 900).unwrap();
        p.started_at = "2026-11-20T11:59:00-05:00".into();
        p.command = "node".into();
        p.app = None;
        procs
    }));
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(&mut rt);
    assert!(
        rt.state.confirm.is_none(),
        "no confirmation naming the new process"
    );
    // The guard itself, whether or not the press reached the old button.
    rt.update(move |s, _| s.ask_target(drawn.clone(), Signal::Kill));
    assert!(rt.state.confirm.is_none());
    // Asked about the new process by pid, it names the new process.
    rt.update(|s, _| s.ask(900, Signal::Kill));
    assert_eq!(
        rt.state.confirm.as_ref().map(|c| c.target.name.as_str()),
        Some("node")
    );
    assert_eq!(r.sent(), [(900, Signal::Term)]);
}

#[test]
fn the_banners_force_quit_opens_the_confirmation_while_it_still_matches() {
    let (mut rt, _) = recorded(quit_scene);
    still_running(&mut rt);
    let drawn = rt.state.outcome.as_ref().unwrap().target.clone();
    rt.update(move |s, _| s.ask_target(drawn.clone(), Signal::Kill));
    assert_eq!(
        rt.state.confirm.as_ref().map(|c| (c.target.pid, c.signal)),
        Some((900, Signal::Kill))
    );
}

#[test]
fn a_kill_that_times_out_may_have_been_sent_and_is_watched() {
    let (mut rt, r) = recorded(quit_scene);
    r.fail_kill(SendError::Unsure("kill took longer than 5 s".into()));
    press(&mut rt, "pd-quit");
    send(&mut rt);
    assert_eq!(
        text_of(&rt, "quit-outcome-text").as_deref(),
        Some(
            "Quit may have been sent to Slack (kill took longer than 5 s); watching whether it ends"
        )
    );
    assert!(
        rt.state.poke_rx.as_ref().unwrap().try_recv().is_ok(),
        "the feed is asked for a reading now"
    );
    rt.update(|s, _| s.ask(900, Signal::Term));
    assert!(
        rt.state.confirm.is_none(),
        "a second Quit could be a second signal"
    );
    reading(&mut rt, 2.0, false);
    assert_eq!(rt.state.outcome.as_ref().unwrap().status, Status::Ended);
    assert_eq!(text_of(&rt, "ended-said").as_deref(), Some("Slack quit"));
}

#[test]
fn a_kill_that_times_out_and_leaves_it_running_says_so_later() {
    let (mut rt, r) = recorded(quit_scene);
    r.fail_kill(SendError::Unsure("kill took longer than 5 s".into()));
    press(&mut rt, "pd-quit");
    send(&mut rt);
    reading(&mut rt, 6.0, true);
    assert_eq!(
        rt.state.outcome.as_ref().unwrap().status,
        Status::StillRunning
    );
}

#[test]
fn a_zombie_waits_for_its_parent_and_gets_no_force_quit() {
    let (mut rt, r) = recorded(quit_scene);
    quit_slack(&mut rt);
    let started = rt.state.process(900).unwrap().started_at.clone();
    let ppid = rt.state.process(900).unwrap().ppid;
    r.zombie(900, &started);
    reading(&mut rt, 6.0, true);
    release(&mut rt);
    let parent = rt
        .state
        .process(ppid)
        .map(|p| format!("{} (pid {ppid})", crate::signal::display_name(p)));
    let parent = parent.unwrap_or_else(|| format!("its parent (pid {ppid})"));
    assert_eq!(
        rt.state.outcome.as_ref().unwrap().status,
        Status::Waiting {
            parent: parent.clone()
        }
    );
    assert_eq!(
        text_of(&rt, "quit-outcome-text"),
        Some(format!(
            "Slack has exited and is waiting for {parent} to collect it; Force quit can't help"
        ))
    );
    assert!(
        rt.find("quit-outcome-force").is_none(),
        "Force quit can't help"
    );
    assert!(text_of(&rt, "pd-refusal").unwrap().contains("waiting for"));
    ask_every_way(&mut rt, 900);
    assert!(rt.state.confirm.is_none());
    // Once collected, it's gone.
    reading(&mut rt, 2.0, false);
    assert_eq!(rt.state.outcome.as_ref().unwrap().status, Status::Ended);
    assert_eq!(r.sent(), [(900, Signal::Term)]);
}

#[test]
fn a_zombie_found_at_confirmation_gets_nothing() {
    let (mut rt, r) = recorded(quit_scene);
    let started = rt.state.process(900).unwrap().started_at.clone();
    r.zombie(900, &started);
    press(&mut rt, "pd-force");
    send(&mut rt);
    assert!(
        text_of(&rt, "quit-outcome-text")
            .unwrap()
            .contains("waiting for its parent")
    );
    assert!(r.sent().is_empty());
}

#[test]
fn an_answer_for_a_dismissed_outcome_is_dropped() {
    let (mut rt, r) = recorded(quit_scene);
    still_running(&mut rt);
    let started = rt.state.process(900).unwrap().started_at.clone();
    r.zombie(900, &started);
    // Another reading queues another look at pid 900 (now a zombie). The outcome is then dismissed
    // and a newer one for the same process is StillRunning, so only the job number tells the
    // old look from the new outcome's.
    rt.update(|s, _| {
        if let Some(o) = &mut s.outcome {
            o.status = Status::Asked { at: s.now };
        }
    });
    reading(&mut rt, 6.0, true);
    let stale = rt.state.outcome.clone().unwrap();
    assert_eq!(stale.status, Status::StillRunning);
    rt.update(|s, _| {
        s.dismiss_outcome();
        s.outcome = Some(stale);
    });
    release(&mut rt);
    assert_eq!(
        rt.state.outcome.as_ref().unwrap().status,
        Status::StillRunning
    );
}

fn confirmed_quit(f: impl FnOnce(&mut App)) -> Runtime<App> {
    let (mut rt, _) = recorded(quit_scene);
    rt.update(|s, _| f(s));
    press(&mut rt, "pd-quit");
    press(&mut rt, "confirm-send");
    rt
}

fn failed_text(rt: &Runtime<App>) -> String {
    match &rt.state.outcome.as_ref().unwrap().status {
        Status::Failed(why) => why.clone(),
        other => panic!("expected Failed, got {other:?}"),
    }
}

struct Panics;

impl crate::signal::Signals for Panics {
    fn identify(&self, _: u32) -> Result<Option<crate::signal::Identity>, String> {
        panic!("a bug in the check");
    }

    fn send(&self, _: u32, _: Signal) -> Result<(), SendError> {
        unreachable!()
    }
}

#[test]
fn a_worker_that_panics_lands_failed_instead_of_checking_forever() {
    let mut rt = confirmed_quit(|s| s.signals = std::sync::Arc::new(Panics));
    assert!(rt.state.checking());
    release(&mut rt);
    assert!(failed_text(&rt).contains("internal error"));
    assert!(!rt.state.checking());
    rt.update(|s, _| s.dismiss_outcome());
    assert!(rt.state.outcome.is_none(), "it can be dismissed");
}

#[test]
fn a_panic_on_a_real_worker_thread_lands_failed_too() {
    let mut rt = confirmed_quit(|s| {
        s.signals = std::sync::Arc::new(Panics);
        s.runner = Runner::Thread;
    });
    for _ in 0..500 {
        rt.update(|s, _| {
            s.land_work();
        });
        if !rt.state.checking() {
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    assert!(failed_text(&rt).contains("internal error"));
}

#[test]
fn a_thread_that_cannot_start_lands_failed() {
    let mut rt = confirmed_quit(|s| s.runner = Runner::NoThreads);
    rt.update(|s, _| {
        s.land_work();
    });
    assert!(failed_text(&rt).contains("Couldn't start a thread"));
}

#[test]
fn an_unlisted_pid_is_said_to_have_ended() {
    let (rt, _) = recorded(quit_scene);
    assert_eq!(
        rt.state.refusal(4_000_000).as_deref(),
        Some("That process has already ended.")
    );
}

// The inspector: tools and live readings with a fake Spectra.

use crate::inspect::tests::Fake;
use crate::inspect::{Exec, RunState};
use crate::tools::Tool;

/// rust-analyzer's page (the sample's own process) with `fake` for Spectra and its work held.
fn inspecting(fake: Fake) -> (Runtime<App>, std::sync::Arc<Fake>) {
    let mut s = init();
    sample_scene(&mut s);
    s.expanded = None;
    let fake = std::sync::Arc::new(fake);
    s.inspect.spectra = fake.clone();
    s.inspect.exec = Exec::Held(Default::default());
    s.inspect.auto = true;
    s.show(Nav::All);
    s.open(1300);
    (runtime(s), fake)
}

fn run_held(rt: &mut Runtime<App>) {
    rt.update(|s, _| s.run_held_inspect());
    settle(rt);
}

#[test]
fn a_tool_runs_from_its_button_and_shows_its_result() {
    let (mut rt, fake) = inspecting(Fake::all());
    assert!(rt.find("tools").is_some(), "the page has its tools");
    press(&mut rt, "tool-sample-run");
    assert!(
        rt.find("tool-sample-running").is_some(),
        "it says it's running"
    );
    assert!(rt.find("tool-sample-cancel").is_some());
    run_held(&mut rt);
    assert!(rt.find("tool-sample-result").is_some());
    assert!(rt.find("sample-stacks-0").is_some(), "its top stacks");
    assert_eq!(
        text_of(&rt, "sample-verdict").as_deref(),
        Some("Idle: waiting for work")
    );
    assert_eq!(fake.calls(), [vec!["sample", "-json", "1300"]]);
}

#[test]
fn every_tools_result_view_renders() {
    let (mut rt, _) = inspecting(Fake::all());
    for (tool, key) in [
        (Tool::Hang, "hang-verdict"),
        (Tool::Vmmap, "vmmap-regions"),
        (Tool::Vmregions, "vmregions-top"),
        (Tool::Memtrace, "memtrace-verdict"),
        (Tool::Lineage, "lineage-tree"),
        (Tool::Owner, "owner-0"),
        (Tool::Runtime, "runtime-steps"),
    ] {
        press(&mut rt, &format!("tool-{}-run", tool.id()));
        run_held(&mut rt);
        assert!(rt.find(key).is_some(), "{tool:?} shows {key}");
    }
    assert_eq!(
        text_of(&rt, "hang-verdict").as_deref(),
        Some("Blocked on a lock")
    );
}

#[test]
fn cancel_stops_a_tool_and_nothing_else_runs_meanwhile() {
    let (mut rt, fake) = inspecting(Fake::all());
    press(&mut rt, "tool-memtrace-run");
    press(&mut rt, "tool-lineage-run");
    assert!(
        rt.state.inspect.state(Tool::Lineage).is_none(),
        "lineage waits: one tool at a time"
    );
    press(&mut rt, "tool-memtrace-cancel");
    assert_eq!(
        rt.state.inspect.state(Tool::Memtrace),
        Some(&RunState::Cancelling)
    );
    run_held(&mut rt);
    assert!(rt.find("tool-memtrace-cancelled").is_some());
    assert_eq!(fake.calls().len(), 1, "only memtrace was asked");
    press(&mut rt, "tool-lineage-run");
    run_held(&mut rt);
    assert!(rt.find("lineage-tree").is_some());
}

#[test]
fn a_failed_tool_shows_why() {
    let (mut rt, _) = inspecting(Fake::with(&[(
        "vmmap",
        Err("vmmap: cannot examine process 1300"),
    )]));
    press(&mut rt, "tool-vmmap-run");
    run_held(&mut rt);
    assert!(rt.find("tool-vmmap-error").is_some());
    assert!(shows_text(&rt, "vmmap: cannot examine process 1300"));
}

#[test]
fn root_tools_are_off_with_the_reason_and_pressing_runs_nothing() {
    let (mut rt, fake) = inspecting(Fake::all());
    assert_eq!(
        text_of(&rt, "tool-spindump-refusal").as_deref(),
        Some("Needs root; Spectra Desktop doesn't run sudo")
    );
    press(&mut rt, "tool-spindump-run");
    press(&mut rt, "tool-lsmp-run");
    run_held(&mut rt);
    assert!(fake.calls().is_empty());
    // A root process: the tools that read its threads or memory are off too.
    rt.update(|s, _| s.open(301));
    settle(&mut rt);
    assert!(text_of(&rt, "tool-sample-refusal").is_some_and(|t| t.starts_with("Owned by root")));
    assert!(text_of(&rt, "tool-lineage-refusal").is_none());
}

#[test]
fn live_readings_feed_the_charts_while_the_page_is_open() {
    let (mut rt, _) = inspecting(Fake::with(&[
        ("network", Ok(crate::live::tests::NET)),
        ("impact", Err("impact: not available here")),
    ]));
    // Past the rest and the backoff each time; the energy reading fails four times and stops.
    for i in 0..4 {
        rt.update(move |s, _| {
            s.now = 100.0 + 100.0 * i as f64;
            s.tick_inspector();
        });
        run_held(&mut rt);
    }
    assert_eq!(rt.state.inspect.net.points.len(), 4);
    assert!(rt.find("live-lane-in").is_some() && rt.find("live-lane-out").is_some());
    assert_eq!(
        text_of(&rt, "live-lane-in-now").as_deref(),
        Some("2.7 MB/s")
    );
    assert_eq!(
        text_of(&rt, "pd-energy-off").as_deref(),
        Some("Energy impact: impact: not available here")
    );
    // Readings also feed the CPU, memory and threads lanes.
    assert!(rt.find("pd-lane-threads").is_some());
    assert_eq!(text_of(&rt, "pd-lane-threads-now").as_deref(), Some("18"));
    // Leaving stops them.
    press(&mut rt, "back");
    rt.update(|s, _| {
        s.now += 60.0;
        s.tick_inspector();
    });
    assert_eq!(rt.state.inspect.subject, None);
    assert!(rt.state.inspect.net.points.is_empty());
}

#[test]
fn a_jvms_page_keeps_its_jvm_part_and_gets_tools() {
    let rt = scene(detail_scene);
    assert!(rt.find("jd-detail").is_some());
    assert!(rt.find("tools").is_some());
}
