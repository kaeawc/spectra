//! Headless runs of the Hosts pages with a fake Spectra Remote: what is asked and when, one call
//! at a time, nothing off the page or while hidden, adding and removing Macs, the read-only host
//! page, and the old-agent, unreachable and not-installed states.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use serde_json::Value;

use rune::{Env, Runtime, TextEdit};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::remote::tests::{Fake, HEALTH_OLD, fail, result_of};
use crate::remote::{Fail, OP_HEALTH, OP_JVMS, OP_PROCESSES, Remote, check_host};
use crate::sample::sample_scene;
use crate::sample_hosts::{
    host_group_scene, host_scene, host_unreachable_scene, host_unsupported_scene,
    hosts_empty_scene, hosts_missing_scene, hosts_scene,
};

fn runtime(s: App) -> Runtime<App> {
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(s, super::view, (1100.0, 800.0), env);
    rt.on_key = Some(super::on_key);
    rt.frame(0.0);
    rt
}

/// The sample Mac with one other Mac listed and nothing read from it yet.
fn fresh(fake: Fake) -> (Runtime<App>, Arc<Fake>, u64) {
    let fake = Arc::new(fake);
    let mut s = init();
    sample_scene(&mut s);
    s.select(None);
    s.hosts.remote = fake.clone();
    s.hosts.threaded = false;
    let id = s.hosts.push(check_host("Build Mac", "build-mac").unwrap());
    (runtime(s), fake, id)
}

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    runtime(s)
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..20 {
        rt.frame(1.0 / 60.0);
    }
}

/// One turn of the window's poll, `secs` later on its clock.
fn tick(rt: &mut Runtime<App>, secs: f64) {
    rt.update(move |s, _| {
        s.now += secs;
        s.tick_hosts();
    });
    settle(rt);
}

fn has(rt: &mut Runtime<App>, key: &str) -> bool {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    rt.find(key).is_some()
}

fn press(rt: &mut Runtime<App>, key: &str) {
    assert!(has(rt, key), "nothing keyed {key} on screen");
    let (x, y) = rt.find(key).unwrap();
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

fn text_of(rt: &mut Runtime<App>, key: &str) -> Option<String> {
    has(rt, key);
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

#[test]
fn the_other_macs_row_opens_the_list_and_checks_each_host() {
    let (mut rt, fake, id) = fresh(Fake::full());
    tick(&mut rt, 1.0);
    assert!(fake.ops().is_empty(), "nothing is asked off the page");
    let (toolchains, hosts) = (rt.find("toolchains").unwrap(), rt.find("hosts").unwrap());
    assert!(toolchains.1 < hosts.1, "after the Toolchains row");
    assert!(
        has(&mut rt, &format!("host-row-{id}")),
        "each Mac has a row"
    );
    press(&mut rt, "hosts");
    assert_eq!(rt.state.nav, Nav::Hosts);
    tick(&mut rt, 0.0);
    // The call landed on the next turn.
    tick(&mut rt, 0.0);
    assert_eq!(fake.ops(), [OP_HEALTH], "the list only asks for health");
    let status = text_of(&mut rt, &format!("host-{id}-status")).unwrap();
    assert!(status.starts_with("Connected · Spectra 0.42.0"), "{status}");
    // Health again only after 30 s.
    tick(&mut rt, 20.0);
    tick(&mut rt, 0.0);
    assert_eq!(fake.ops().len(), 1);
    tick(&mut rt, 11.0);
    assert_eq!(fake.ops().len(), 2);
}

#[test]
fn a_hosts_page_reads_health_then_processes_then_jvms_one_at_a_time() {
    let (mut rt, fake, id) = fresh(Fake::full());
    rt.update(move |s, _| s.show(Nav::Host(id)));
    for _ in 0..6 {
        tick(&mut rt, 0.0);
    }
    assert_eq!(fake.ops(), [OP_HEALTH, OP_PROCESSES, OP_JVMS]);
    assert_eq!(fake.calls.lock().unwrap()[0].0, "build-mac:7878");
    assert!(has(&mut rt, "host-table"));
    assert!(has(&mut rt, "host-jvm-2210"));
    let summary = text_of(&mut rt, "host-summary").unwrap();
    assert!(summary.starts_with("15 processes"), "{summary}");
    // Processes every 10 s; JVMs only every 60 s; health every 30 s.
    tick(&mut rt, 10.0);
    tick(&mut rt, 0.0);
    assert_eq!(fake.ops()[3..], [OP_PROCESSES]);
    for _ in 0..3 {
        tick(&mut rt, 10.0);
        tick(&mut rt, 0.0);
    }
    assert!(fake.ops().iter().filter(|o| *o == OP_HEALTH).count() >= 2);
    assert_eq!(fake.ops().iter().filter(|o| *o == OP_JVMS).count(), 1);
}

#[test]
fn leaving_the_page_or_hiding_the_window_asks_nothing() {
    let (mut rt, fake, id) = fresh(Fake::full());
    rt.update(move |s, _| s.show(Nav::Host(id)));
    tick(&mut rt, 0.0);
    rt.update(|s, _| s.show(Nav::Toolchains));
    let n = fake.ops().len();
    for _ in 0..5 {
        tick(&mut rt, 60.0);
    }
    assert_eq!(fake.ops().len(), n, "off the page");
    rt.update(move |s, _| {
        s.show(Nav::Host(id));
        s.visible = false;
    });
    for _ in 0..5 {
        tick(&mut rt, 60.0);
    }
    assert_eq!(fake.ops().len(), n, "hidden");
    rt.update(|s, _| s.visible = true);
    tick(&mut rt, 0.0);
    assert_eq!(fake.ops().len(), n + 1, "shown again, it asks at once");
}

/// Waits until its call is cancelled, as a controller stuck joining the tailnet would.
struct Stuck {
    stopped: Arc<AtomicBool>,
}

impl Remote for Stuck {
    fn call(&self, _: &str, _: &str, _: Duration, cancel: &AtomicBool) -> Result<Value, Fail> {
        let start = Instant::now();
        while !cancel.load(Ordering::SeqCst) && start.elapsed() < Duration::from_secs(10) {
            std::thread::sleep(Duration::from_millis(5));
        }
        self.stopped
            .store(cancel.load(Ordering::SeqCst), Ordering::SeqCst);
        Err(Fail {
            missing: false,
            why: "stopped".into(),
        })
    }
}

#[test]
fn a_call_in_flight_is_stopped_when_its_page_goes() {
    let (mut rt, _, id) = fresh(Fake::full());
    let stopped = Arc::new(AtomicBool::new(false));
    let remote = Stuck {
        stopped: Arc::clone(&stopped),
    };
    rt.update(move |s, _| {
        s.hosts.remote = Arc::new(remote);
        s.hosts.threaded = true;
        s.show(Nav::Host(id));
        s.tick_hosts();
        assert!(s.hosts.calling());
        s.show(Nav::Overview);
        s.tick_hosts();
        assert!(!s.hosts.calling(), "leaving stops the call");
    });
    let start = Instant::now();
    while !stopped.load(Ordering::SeqCst) && start.elapsed() < Duration::from_secs(5) {
        std::thread::sleep(Duration::from_millis(5));
    }
    assert!(
        stopped.load(Ordering::SeqCst),
        "the controller run was cancelled"
    );
    // Its late answer lands on nothing.
    tick(&mut rt, 0.0);
    assert!(rt.state.hosts.get(id).unwrap().health.error.is_none());
}

#[test]
fn the_host_page_is_read_only_and_groups_open() {
    let mut rt = scene(host_scene);
    assert!(has(&mut rt, "host-readonly"));
    assert_eq!(rt.state.shortcut_target(), None, "⌥⌘Q has nothing to quit");
    assert!(!has(&mut rt, "quit"), "no Quit on another Mac");
    let java = rt
        .elements()
        .into_iter()
        .filter_map(|e| e.key)
        .find(|k| k.starts_with("host-group-exe:java"))
        .expect("the java processes are one row");
    assert!(!has(&mut rt, "host-proc-2288"));
    press(&mut rt, &java);
    assert!(
        has(&mut rt, "host-proc-2288"),
        "opening the group lists its processes"
    );
    let mut rt = scene(host_group_scene);
    assert!(has(&mut rt, "host-proc-2210"));
}

#[test]
fn the_search_narrows_the_hosts_processes() {
    let mut rt = scene(host_scene);
    rt.update(|s, _| s.hosts.query = TextEdit::new("runner"));
    settle(&mut rt);
    let rows: Vec<String> = rt
        .elements()
        .into_iter()
        .filter_map(|e| e.key)
        .filter(|k| k.starts_with("host-group-"))
        .collect();
    assert_eq!(rows.len(), 2, "{rows:?}");
    rt.update(|s, _| s.hosts.query = TextEdit::new("nothing-like-this"));
    assert!(has(&mut rt, "host-no-match"));
}

#[test]
fn an_old_agent_says_it_cant_list_processes_and_isnt_asked() {
    let fake = Fake::new().with(OP_HEALTH, Ok(result_of(HEALTH_OLD)));
    let (mut rt, fake, id) = fresh(fake);
    rt.update(move |s, _| s.show(Nav::Host(id)));
    for _ in 0..4 {
        tick(&mut rt, 0.0);
    }
    assert_eq!(fake.ops(), [OP_HEALTH]);
    assert!(has(&mut rt, "host-unsupported"));
    assert!(has(&mut rt, "host-unsupported-title"));
    let mut rt = scene(host_unsupported_scene);
    assert!(has(&mut rt, "host-unsupported"));
}

#[test]
fn an_unreachable_host_says_why_and_how_to_check() {
    let (mut rt, _, id) = fresh(Fake::new().with(
        OP_HEALTH,
        fail("Couldn't get an answer from build-mac:7878: dial tcp: i/o timeout"),
    ));
    rt.update(move |s, _| s.show(Nav::Host(id)));
    tick(&mut rt, 0.0);
    tick(&mut rt, 0.0);
    let status = text_of(&mut rt, "host-status").unwrap();
    assert!(
        status.starts_with("Unreachable · Couldn't get an answer"),
        "{status}"
    );
    assert!(has(&mut rt, "host-unreachable-hint"));
    let mut rt = scene(host_unreachable_scene);
    assert!(has(&mut rt, "host-unreachable-hint"));
}

#[test]
fn a_failed_process_reading_keeps_the_last_list_and_says_so() {
    let (mut rt, fake, id) = fresh(Fake::full());
    rt.update(move |s, _| s.show(Nav::Host(id)));
    for _ in 0..4 {
        tick(&mut rt, 0.0);
    }
    fake.answers
        .lock()
        .unwrap()
        .insert(OP_PROCESSES.into(), fail("agent timed out"));
    tick(&mut rt, 10.0);
    tick(&mut rt, 0.0);
    assert!(has(&mut rt, "host-table"), "the last list stays");
    let line = text_of(&mut rt, "host-procs-stale").unwrap();
    assert!(
        line.contains("last reading failed: agent timed out."),
        "{line}"
    );
}

#[test]
fn a_missing_controller_says_how_to_install_it() {
    let missing = Err(Fail {
        missing: true,
        why: "Couldn't run spectra-remote: not found".into(),
    });
    let (mut rt, _, _) = fresh(Fake::new().with(OP_HEALTH, missing));
    rt.update(|s, _| s.show(Nav::Hosts));
    tick(&mut rt, 0.0);
    tick(&mut rt, 0.0);
    assert!(has(&mut rt, "hosts-missing"));
    let mut rt = scene(hosts_missing_scene);
    assert!(has(&mut rt, "hosts-missing"));
}

#[test]
fn macs_are_added_checked_and_removed() {
    let mut rt = scene(hosts_empty_scene);
    assert!(has(&mut rt, "hosts-empty"));
    rt.update(|s, _| s.hosts.target = TextEdit::new("-x"));
    press(&mut rt, "hosts-add");
    assert!(has(&mut rt, "hosts-add-error"), "a flag isn't a host");
    assert!(rt.state.hosts.list.is_empty());
    rt.update(|s, _| {
        s.hosts.name = TextEdit::new("Work");
        s.hosts.target = TextEdit::new("work-mac");
    });
    press(&mut rt, "hosts-add");
    assert_eq!(rt.state.hosts.list.len(), 1);
    assert_eq!(rt.state.hosts.list[0].host.target, "work-mac:7878");
    assert!(rt.state.hosts.target.text.is_empty(), "the form clears");
    rt.update(|s, _| s.hosts.target = TextEdit::new("work-mac:7878"));
    press(&mut rt, "hosts-add");
    assert!(
        text_of(&mut rt, "hosts-add-error")
            .unwrap()
            .contains("in the list already")
    );
    let id = rt.state.hosts.list[0].id;
    rt.update(move |s, _| s.show(Nav::Host(id)));
    rt.update(move |s, _| s.remove_host(id));
    settle(&mut rt);
    assert!(rt.state.hosts.list.is_empty());
    assert_eq!(rt.state.nav, Nav::Hosts, "its page closes");
}

#[test]
fn the_list_shows_each_hosts_state() {
    let mut rt = scene(hosts_scene);
    let ids: Vec<u64> = rt.state.hosts.list.iter().map(|h| h.id).collect();
    let status =
        |rt: &mut Runtime<App>, id: u64| text_of(rt, &format!("host-{id}-status")).unwrap();
    assert!(status(&mut rt, ids[0]).starts_with("Connected"));
    assert!(
        text_of(&mut rt, &format!("host-{}-offers", ids[1]))
            .unwrap()
            .contains("no process list")
    );
    assert!(status(&mut rt, ids[2]).starts_with("Unreachable"));
    press(&mut rt, &format!("host-{}-open", ids[0]));
    assert_eq!(rt.state.nav, Nav::Host(ids[0]));
}
