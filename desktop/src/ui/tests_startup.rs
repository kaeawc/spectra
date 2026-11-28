//! Headless runs of the Startup page: the sidebar row, the read and its failure, the filters,
//! the link to a running item's process, and Disable / Enable behind its confirmation, with a
//! fake Spectra that records what it was asked and changes nothing.

use std::sync::Arc;

use rune::{Env, Key, KeyEvent, Runtime};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_startup::{startup_failed_scene, startup_scene};
use crate::startup::tests::Fake;
use crate::startup::{ChangeStatus, Ran};

const BREW: &str = "su-com.github.domt4.homebrew-autoupdate";
const TOOLBOX: &str = "su-com.jetbrains.toolbox";
const TOOLBOX_LABEL: &str = "com.jetbrains.toolbox";
const TOOLBOX_PLIST: &str = "/Users/me/Library/LaunchAgents/com.jetbrains.toolbox.plist";
const WATCHMAN: &str = "su-com.github.facebook.watchman";

fn scene_with(f: fn(&mut App), fake: Fake) -> (Runtime<App>, Arc<Fake>) {
    let fake = Arc::new(fake);
    let mut s = init();
    s.startup.runner = fake.clone();
    s.startup.threaded = false;
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
    scene_with(f, Fake::new())
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

/// Lands what the fake answered, as `poll` would.
fn land(rt: &mut Runtime<App>) {
    rt.update(|s, _| {
        s.land_startup();
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

#[test]
fn the_startup_row_sits_under_the_machine_and_opens_the_page_with_a_read() {
    let (mut rt, fake) = scene(sample_scene);
    let (sys, startup, all) = (
        rt.find("system").unwrap(),
        rt.find("startup").unwrap(),
        rt.find("all").unwrap(),
    );
    assert!(
        sys.1 < startup.1 && startup.1 < all.1,
        "right under the Machine row"
    );
    press(&mut rt, "startup");
    assert_eq!(rt.state.nav, Nav::Startup);
    assert!(has(&mut rt, "su-page"));
    land(&mut rt);
    let calls = fake.calls();
    assert_eq!(
        calls.len(),
        2,
        "one listing and one services read: {calls:?}"
    );
    assert_eq!(calls[0], ["startup", "list", "--json", "--scope", "all"]);
    assert_eq!(calls[1], ["services", "--json", "--domain", "all"]);
    assert!(
        fake.changes().is_empty(),
        "opening the page changes nothing"
    );
    assert_eq!(
        text_of(&mut rt, "su-group-app-_Applications_Docker.app").as_deref(),
        Some("Docker")
    );
    assert!(has(&mut rt, "su-group-unowned"));
    assert!(has(&mut rt, "su-group-services"));
    assert_eq!(
        text_of(&mut rt, "su-com.docker.vmnetd-what").as_deref(),
        Some("Launch daemon · At boot")
    );
    assert_eq!(
        text_of(&mut rt, "su-com.docker.vmnetd-run").as_deref(),
        Some("Running · pid 597")
    );
    assert_eq!(
        text_of(&mut rt, &format!("{TOOLBOX}-run")).as_deref(),
        Some("Not running")
    );
    // Refresh reads again.
    press(&mut rt, "su-refresh");
    land(&mut rt);
    assert_eq!(fake.calls().len(), 4);
}

#[test]
fn a_failed_read_says_why_and_try_again_reads_again() {
    let (mut rt, fake) = scene(startup_failed_scene);
    assert_eq!(
        text_of(&mut rt, "su-error-title").as_deref(),
        Some("Couldn't read what starts at login")
    );
    assert!(
        rt.elements()
            .iter()
            .any(|e| e.text.as_deref() == Some("spectra took longer than 60 s and was stopped"))
    );
    press(&mut rt, "su-retry");
    land(&mut rt);
    assert_eq!(fake.calls().len(), 2);
    assert!(rt.state.startup.error.is_none());
    assert!(has(&mut rt, TOOLBOX), "the list landed");
    assert!(!has(&mut rt, "su-error"));
}

#[test]
fn a_failed_refresh_keeps_the_list_and_says_why() {
    let fake = Fake {
        list: Ok(Ran {
            code: Some(1),
            stdout: String::new(),
            stderr: "startup: launchctl print system: exit status 5\n".into(),
        }),
        ..Fake::new()
    };
    let (mut rt, _) = scene_with(startup_scene, fake);
    press(&mut rt, "su-refresh");
    land(&mut rt);
    assert_eq!(
        rt.state.startup.error.as_deref(),
        Some("startup: launchctl print system: exit status 5")
    );
    assert!(has(&mut rt, "su-error"));
    assert!(has(&mut rt, "su-retry"));
    assert!(has(&mut rt, TOOLBOX), "the list from before stays");
}

#[test]
fn the_filters_keep_running_items_and_showing_apple_reads_it() {
    let (mut rt, fake) = scene(startup_scene);
    assert!(
        rt.state.startup.not_apple,
        "Apple's items are hidden at first"
    );
    assert!(has(&mut rt, &format!("{TOOLBOX}-run")));
    press(&mut rt, "su-running");
    assert!(rt.state.startup.running_only);
    assert!(!has(&mut rt, TOOLBOX), "not running");
    assert!(has(&mut rt, WATCHMAN), "running");
    press(&mut rt, "su-running");
    press(&mut rt, "su-not-apple");
    land(&mut rt);
    assert!(!rt.state.startup.not_apple);
    assert_eq!(
        fake.calls()[0].last().map(String::as_str),
        Some("--include-apple")
    );
    // The fake answers the same listing; the services' Apple jobs now show.
    assert!(has(&mut rt, "su-com.apple.Finder"));
    press(&mut rt, "su-not-apple");
    assert!(!has(&mut rt, "su-com.apple.Finder"));
    assert_eq!(
        fake.calls().len(),
        2,
        "hiding Apple again is a filter, not a read"
    );
}

#[test]
fn a_running_item_opens_its_process_page_when_the_list_has_it() {
    let (mut rt, _) = scene(startup_scene);
    rt.update(|s, _| {
        let l = s.startup.listing.as_mut().unwrap();
        l.items
            .iter_mut()
            .find(|i| i.label == "com.jetbrains.toolbox")
            .unwrap()
            .pid = Some(940);
    });
    settle(&mut rt);
    press(&mut rt, &format!("{TOOLBOX}-run"));
    assert_eq!(rt.state.selected, Some(940));
    rt.update(|s, _| s.back());
    settle(&mut rt);
    assert_eq!(
        rt.state.nav,
        Nav::Startup,
        "back returns to the Startup page"
    );
    assert!(has(&mut rt, "su-page"));
}

#[test]
fn disable_sends_exactly_one_change_with_the_label_after_the_confirmation() {
    let (mut rt, fake) = scene(startup_scene);
    press(&mut rt, &format!("{WATCHMAN}-act"));
    assert!(fake.changes().is_empty(), "the button only asks");
    let ask = rt
        .state
        .startup
        .confirm
        .clone()
        .expect("the confirmation is open");
    assert_eq!(ask.item.label, "com.github.facebook.watchman");
    let line = text_of(&mut rt, "su-confirm-line-0").unwrap();
    assert!(
        line.contains("watchman (com.github.facebook.watchman) won't start again"),
        "{line}"
    );
    assert!(
        text_of(&mut rt, "su-confirm-line-1")
            .unwrap()
            .contains("running now (pid 1114)")
    );
    let focused = rt
        .elements()
        .into_iter()
        .find(|e| e.focused)
        .and_then(|e| e.key);
    assert_eq!(
        focused.as_deref(),
        Some("su-confirm-cancel"),
        "Cancel has focus"
    );
    let (x, y) = locate(&mut rt, "su-confirm-send");
    for _ in 0..2 {
        rt.pointer_down(x, y);
        rt.pointer_up(x, y);
    }
    settle(&mut rt);
    assert_eq!(
        fake.changes(),
        vec![vec![
            "startup",
            "disable",
            "--json",
            "--scope",
            "user",
            "--",
            "com.github.facebook.watchman"
        ]],
        "one press, one change"
    );
    land(&mut rt);
    assert_eq!(fake.changes().len(), 1, "landing sends no other change");
    let out = text_of(&mut rt, "su-outcome-text").unwrap();
    assert!(out.starts_with("watchman is disabled."), "{out}");
    let reads = fake.calls().iter().filter(|c| c[1] == "list").count();
    assert_eq!(reads, 1, "the list is read again after the change");
    press(&mut rt, "su-outcome-dismiss");
    assert!(rt.state.startup.outcome.is_none());
}

#[test]
fn cancel_and_esc_send_nothing() {
    let (mut rt, fake) = scene(startup_scene);
    press(&mut rt, &format!("{TOOLBOX}-act"));
    press(&mut rt, "su-confirm-cancel");
    assert!(rt.state.startup.confirm.is_none());
    press(&mut rt, &format!("{TOOLBOX}-act"));
    rt.key(KeyEvent::from(Key::Escape));
    settle(&mut rt);
    assert!(rt.state.startup.confirm.is_none(), "Esc cancels");
    assert_eq!(rt.state.nav, Nav::Startup);
    press(&mut rt, &format!("{TOOLBOX}-act"));
    rt.key(KeyEvent::from(Key::Enter));
    settle(&mut rt);
    land(&mut rt);
    assert!(
        fake.calls().is_empty(),
        "nothing was run: {:?}",
        fake.calls()
    );
    assert!(rt.state.startup.outcome.is_none());
}

#[test]
fn a_disabled_agent_offers_enable() {
    let (mut rt, fake) = scene(startup_scene);
    assert!(has(&mut rt, &format!("{BREW}-disabled")));
    assert_eq!(
        text_of(&mut rt, &format!("{BREW}-act")),
        None,
        "a button, its label inside"
    );
    press(&mut rt, &format!("{BREW}-act"));
    assert_eq!(
        rt.state.startup.confirm.as_ref().map(|a| a.action),
        Some(crate::startup::Action::Enable)
    );
    press(&mut rt, "su-confirm-send");
    assert_eq!(
        fake.changes(),
        vec![vec![
            "startup",
            "enable",
            "--json",
            "--scope",
            "user",
            "--",
            "com.github.domt4.homebrew-autoupdate"
        ]]
    );
}

#[test]
fn system_items_and_login_items_show_the_action_off_with_why() {
    let (mut rt, fake) = scene(startup_scene);
    assert_eq!(
        text_of(&mut rt, "su-com.docker.vmnetd-why").as_deref(),
        Some("Needs an administrator; Spectra Desktop doesn't run sudo")
    );
    assert!(
        text_of(&mut rt, "su-com.docker.helper-why")
            .unwrap()
            .contains("Login Items")
    );
    press(&mut rt, "su-com.docker.vmnetd-act");
    assert!(
        rt.state.startup.confirm.is_none(),
        "an off button opens nothing"
    );
    rt.update(|s, _| {
        let path = s
            .startup
            .listing
            .as_ref()
            .and_then(|l| l.items.iter().find(|i| i.label == "com.docker.vmnetd"))
            .map(|i| i.plist_path.clone())
            .unwrap();
        s.ask_startup("com.docker.vmnetd", &path, crate::startup::Action::Disable)
    });
    assert!(
        rt.state.startup.confirm.is_none(),
        "nor does asking for it directly"
    );
    assert!(
        !has(&mut rt, "su-com.valvesoftware.steam.ipctool-act"),
        "services have no action"
    );
    assert!(fake.calls().is_empty());
}

#[test]
fn a_refused_or_failed_change_shows_what_spectra_said() {
    let fake = Fake {
        change: Ok(Ran {
            code: Some(3),
            stdout: crate::sample_startup::REFUSED_JSON.into(),
            stderr: String::new(),
        }),
        ..Fake::new()
    };
    let (mut rt, fake) = scene_with(startup_scene, fake);
    press(&mut rt, &format!("{TOOLBOX}-act"));
    press(&mut rt, "su-confirm-send");
    land(&mut rt);
    let out = text_of(&mut rt, "su-outcome-text").unwrap();
    assert!(
        out.starts_with(
            "Spectra didn't disable JetBrains Toolbox: the label is defined by 2 plists"
        ),
        "{out}"
    );
    assert!(matches!(
        rt.state.startup.outcome.as_ref().unwrap().status,
        ChangeStatus::Refused(_)
    ));
    assert!(
        !fake.calls().iter().any(|c| c[1] == "list"),
        "nothing changed, so no re-read"
    );

    let fake = Fake {
        change: Err(
            "Couldn't run spectra: No such file or directory. Install Spectra or set SPECTRA_BIN."
                .into(),
        ),
        ..Fake::new()
    };
    let (mut rt, _) = scene_with(startup_scene, fake);
    press(&mut rt, &format!("{TOOLBOX}-act"));
    press(&mut rt, "su-confirm-send");
    land(&mut rt);
    let out = text_of(&mut rt, "su-outcome-text").unwrap();
    assert!(
        out.starts_with("Couldn't disable JetBrains Toolbox: Couldn't run spectra"),
        "{out}"
    );
}

#[test]
fn while_a_change_is_on_its_way_nothing_else_can_be_asked() {
    let (mut rt, fake) = scene(startup_scene);
    press(&mut rt, &format!("{TOOLBOX}-act"));
    press(&mut rt, "su-confirm-send");
    // Not landed yet: the outcome says it's sending.
    assert!(rt.state.startup.sending());
    rt.update(|s, _| {
        s.ask_startup(
            "com.github.facebook.watchman",
            "/Users/me/Library/LaunchAgents/com.github.facebook.watchman.plist",
            crate::startup::Action::Disable,
        )
    });
    assert!(rt.state.startup.confirm.is_none());
    rt.update(|s, _| s.dismiss_startup_outcome());
    assert!(
        rt.state.startup.outcome.is_some(),
        "its answer has to land somewhere"
    );
    land(&mut rt);
    assert_eq!(fake.changes().len(), 1);
}

#[test]
fn the_row_stays_off_until_the_re_read_after_a_change_lands() {
    let fake = Fake::new();
    let gate = Arc::clone(&fake.gate);
    let (mut rt, fake) = scene_with(startup_scene, fake);
    press(&mut rt, &format!("{TOOLBOX}-act"));
    press(&mut rt, "su-confirm-send");
    // The change lands Done; its re-read runs on a thread that the gate holds.
    let held = gate.lock().unwrap();
    rt.state.startup.threaded = true;
    rt.update(|s, _| {
        s.land_startup();
    });
    settle(&mut rt);
    assert!(matches!(
        rt.state.startup.outcome.as_ref().unwrap().status,
        ChangeStatus::Done(_)
    ));
    assert!(rt.state.startup.busy(), "the old rows are still on screen");
    assert!(!rt.state.startup.sending());
    rt.update(|s, _| {
        s.ask_startup(
            TOOLBOX_LABEL,
            TOOLBOX_PLIST,
            crate::startup::Action::Disable,
        )
    });
    assert!(
        rt.state.startup.confirm.is_none(),
        "nothing opens meanwhile"
    );
    press(&mut rt, &format!("{BREW}-act"));
    assert!(
        rt.state.startup.confirm.is_none(),
        "an off button opens nothing"
    );
    assert!(
        !has(&mut rt, "su-outcome-dismiss"),
        "nor can the outcome go"
    );
    drop(held);
    for _ in 0..200 {
        rt.update(|s, _| {
            s.land_startup();
        });
        if !rt.state.startup.busy() {
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    assert!(!rt.state.startup.busy(), "the re-read landed");
    settle(&mut rt);
    press(&mut rt, &format!("{TOOLBOX}-act"));
    assert!(rt.state.startup.confirm.is_some(), "the row is back");
    assert_eq!(fake.changes().len(), 1);
}

#[test]
fn two_rows_sharing_a_label_act_only_on_their_own() {
    let (mut rt, fake) = scene(startup_scene);
    rt.update(|s, _| {
        let l = s.startup.listing.as_mut().unwrap();
        let mut twin = l
            .items
            .iter()
            .find(|i| i.label == TOOLBOX_LABEL)
            .unwrap()
            .clone();
        twin.plist_path = "/Users/me/Library/LaunchAgents/elsewhere.plist".into();
        twin.name = "Twin".into();
        twin.duplicate = false;
        for i in l.items.iter_mut().filter(|i| i.label == TOOLBOX_LABEL) {
            i.duplicate = false;
        }
        l.items.push(twin);
        s.ask_startup(
            TOOLBOX_LABEL,
            "/Users/me/Library/LaunchAgents/elsewhere.plist",
            crate::startup::Action::Disable,
        );
    });
    let ask = rt.state.startup.confirm.clone().expect("the twin asks");
    assert_eq!(ask.item.display_name(), "Twin");
    assert_eq!(
        ask.item.plist_path,
        "/Users/me/Library/LaunchAgents/elsewhere.plist"
    );
    assert!(fake.calls().is_empty());
}

#[test]
fn quit_shortcut_does_not_reach_a_hidden_process_row_on_the_startup_page() {
    let (mut rt, _) = scene(sample_scene);
    press(&mut rt, "all");
    let id = "p:1300";
    press(&mut rt, id);
    assert_eq!(rt.state.table.cursor(), Some(id));
    assert_eq!(
        rt.state.shortcut_target(),
        Some(1300),
        "on the list it has one"
    );
    rt.update(|s, _| s.show(Nav::Startup));
    assert_eq!(rt.state.shortcut_target(), None);
}
