//! Headless runs of a page's Logs: pressed as a person would, with a fake Spectra.

use rune::{Env, Runtime, TextEdit};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::inspect::tests::Fake;
use crate::inspect::{Exec, RunState};
use crate::logs::{FOLLOW_REST, Level};
use crate::sample::sample_scene;
use crate::sample_logs::ANALYZER_LOGS;

fn runtime(state: App) -> Runtime<App> {
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(state, crate::ui::view, (980.0, 640.0), env);
    rt.on_key = Some(crate::ui::on_key);
    rt.frame(0.0);
    rt
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

fn press(rt: &mut Runtime<App>, key: &str) {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    let (x, y) = rt
        .find(key)
        .unwrap_or_else(|| panic!("nothing keyed {key} on screen"));
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

fn text_of(rt: &Runtime<App>, key: &str) -> Option<String> {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

fn run_held(rt: &mut Runtime<App>) {
    rt.update(|s, _| s.run_held_inspect());
    settle(rt);
}

fn faked(reply: Result<&'static str, &'static str>) -> (App, std::sync::Arc<Fake>) {
    let mut s = init();
    sample_scene(&mut s);
    s.expanded = None;
    let fake = std::sync::Arc::new(Fake::with(&[("logs", reply)]));
    s.inspect.spectra = fake.clone();
    s.inspect.exec = Exec::Held(Default::default());
    (s, fake)
}

/// The page of `pid` in the sample, with Spectra's logs answering `reply`.
fn page(
    pid: u32,
    reply: Result<&'static str, &'static str>,
) -> (Runtime<App>, std::sync::Arc<Fake>) {
    let (mut s, fake) = faked(reply);
    s.show(Nav::All);
    s.open(pid);
    (runtime(s), fake)
}

fn args(fake: &Fake) -> Vec<String> {
    fake.calls().last().cloned().unwrap_or_default()
}

#[test]
fn show_logs_reads_the_process_by_name_and_lists_the_newest_first() {
    let (mut rt, fake) = page(1300, Ok(ANALYZER_LOGS));
    assert!(rt.find("logs-rows").is_none(), "nothing runs on its own");
    assert!(fake.calls().is_empty());
    press(&mut rt, "logs-run");
    assert!(rt.find("logs-running").is_some());
    run_held(&mut rt);
    assert_eq!(
        args(&fake),
        [
            "logs",
            "--json",
            "--process",
            "rust-analyzer",
            "--last",
            "5m",
            "--top",
            "2000"
        ]
    );
    assert_eq!(
        text_of(&rt, "logs-count").as_deref(),
        Some("6 of 6 entries · newest first")
    );
    assert!(
        rt.find("logs-row-5").is_some() && rt.find("logs-row-6").is_none(),
        "the count line isn't a row"
    );
    assert!(
        rt.elements()
            .iter()
            .any(|e| e.text.as_deref() == Some("Fault")),
        "a fault says so in words"
    );
    rt.update(|s, _| s.inspect.logs.filter = TextEdit::new("fsevents"));
    settle(&mut rt);
    assert_eq!(
        text_of(&rt, "logs-count").as_deref(),
        Some("2 of 6 entries · newest first")
    );
    rt.update(|s, _| s.inspect.logs.filter = TextEdit::new("no such entry"));
    settle(&mut rt);
    assert!(rt.find("logs-no-match").is_some());
}

#[test]
fn picking_a_level_or_window_reads_again_with_it() {
    let (mut rt, fake) = page(1300, Ok(ANALYZER_LOGS));
    rt.update(|s, _| s.set_logs_level(Level::Error));
    assert!(
        fake.calls().is_empty(),
        "before the first read, picking only picks"
    );
    press(&mut rt, "logs-run");
    run_held(&mut rt);
    assert_eq!(args(&fake)[6..], ["--level", "Error", "--top", "2000"]);
    assert_eq!(
        text_of(&rt, "logs-count").as_deref(),
        Some("2 of 2 entries · newest first"),
        "only the error and the fault"
    );
    rt.update(|s, _| s.set_logs_window(crate::logs::Window::Hour));
    run_held(&mut rt);
    assert_eq!(args(&fake)[4..6], ["--last", "1h"]);
}

#[test]
fn no_entries_a_timeout_and_a_shared_name_are_said_plainly() {
    let (mut rt, _) = page(1300, Ok(r#"{"entries":null,"clock":{}}"#));
    press(&mut rt, "logs-run");
    run_held(&mut rt);
    assert_eq!(
        text_of(&rt, "logs-none").as_deref(),
        Some("No entries from rust-analyzer in the last 5 minutes.")
    );

    let (mut rt, _) = page(1300, Err("spectra took longer than 30 s and was stopped"));
    press(&mut rt, "logs-run");
    run_held(&mut rt);
    // The error row holds an icon and the sentence; the sentence is its text child.
    assert!(rt.find("logs-error").is_some());
    assert!(rt.elements().into_iter().any(|e| {
        e.text
            .is_some_and(|t| t.starts_with("The log query took longer than 30 s"))
    }));

    // Five zsh processes: the log can't tell them apart, so the page keeps its own pid's.
    let zsh = r#"{"entries":[{"timestamp":"2026-11-20T06:50:00-05:00","pid":1101,"message_type":"Error","event_message":"mine"},
                             {"timestamp":"2026-11-20T06:51:00-05:00","pid":1102,"message_type":"Error","event_message":"another's"}]}"#;
    let (mut rt, _) = page(1101, Ok(zsh));
    assert!(
        text_of(&rt, "logs-about").is_some_and(|t| t.contains("5 running processes are named zsh"))
    );
    press(&mut rt, "logs-run");
    run_held(&mut rt);
    assert_eq!(
        text_of(&rt, "logs-others").as_deref(),
        Some("Left out 1 entries from other processes named zsh.")
    );
    assert_eq!(
        text_of(&rt, "logs-count").as_deref(),
        Some("1 of 1 entries · newest first")
    );
}

#[test]
fn follow_reads_a_short_window_while_the_page_is_open_and_stops_when_it_closes() {
    let (mut rt, fake) = page(1300, Ok(ANALYZER_LOGS));
    rt.update(|s, _| s.inspect.auto = true);
    press(&mut rt, "logs-follow");
    run_held(&mut rt);
    assert_eq!(fake.calls().len(), 1, "Follow reads the window first");
    rt.update(|s, _| s.tick_logs());
    assert_eq!(fake.calls().len(), 1, "it rests between rounds");
    rt.update(|s, _| {
        s.now += FOLLOW_REST + 1.0;
        s.tick_logs();
    });
    run_held(&mut rt);
    assert_eq!(args(&fake)[4..6], ["--last", "1m"]);
    assert_eq!(
        text_of(&rt, "logs-count").as_deref(),
        Some("6 of 6 entries · newest first · following"),
        "nothing newer: no repeats"
    );
    press(&mut rt, "back");
    rt.update(|s, _| {
        s.now += FOLLOW_REST + 1.0;
        s.sync_inspector();
        s.tick_logs();
    });
    run_held(&mut rt);
    assert_eq!(fake.calls().len(), 2, "no round after the page closed");
    assert!(!rt.state.inspect.logs.follow);
}

#[test]
fn follow_reads_nothing_while_the_window_is_hidden_and_goes_on_once_shown() {
    let (mut rt, fake) = page(1300, Ok(ANALYZER_LOGS));
    rt.update(|s, _| s.inspect.auto = true);
    press(&mut rt, "logs-follow");
    run_held(&mut rt);
    let logs = |f: &Fake| f.calls().iter().filter(|c| c[0] == "logs").count();
    assert_eq!(logs(&fake), 1);
    rt.update(|s, _| s.visible = false);
    for _ in 0..5 {
        rt.update(|s, _| {
            s.now += FOLLOW_REST + 1.0;
            s.tick_live();
            s.tick_logs();
        });
        run_held(&mut rt);
    }
    assert_eq!(logs(&fake), 1, "no Follow round while hidden");
    assert!(rt.state.inspect.logs.follow, "Follow stays on");
    rt.update(|s, _| {
        s.visible = true;
        s.tick_live();
    });
    run_held(&mut rt);
    assert_eq!(logs(&fake), 2, "shown again, the next round runs");
}

#[test]
fn cancel_stops_the_query_and_its_answer_is_dropped() {
    let (mut rt, _) = page(1300, Ok(ANALYZER_LOGS));
    press(&mut rt, "logs-run");
    press(&mut rt, "logs-cancel");
    assert!(rt.find("logs-cancelled").is_some());
    run_held(&mut rt);
    assert!(matches!(
        rt.state.inspect.logs.state,
        Some(RunState::Cancelled)
    ));
    assert!(rt.find("logs-rows").is_none());
}

#[test]
fn an_app_page_reads_its_main_process() {
    let (mut s, fake) = faked(Ok(ANALYZER_LOGS));
    s.show(Nav::All);
    s.open_app("/Applications/Slack.app");
    let mut rt = runtime(s);
    let lead = rt
        .state
        .app_lead("/Applications/Slack.app")
        .expect("Slack runs in the sample");
    let name = crate::logs::process_name(rt.state.process(lead).unwrap());
    press(&mut rt, "logs-run");
    run_held(&mut rt);
    assert_eq!(args(&fake)[3], name);
}
