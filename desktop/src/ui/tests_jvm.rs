//! Headless runs of a JVM's page: its JVM tools pressed as a person would, with a fake Spectra.

use rune::{Env, Runtime, TextEdit};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::inspect::tests::Fake;
use crate::inspect::{Exec, RunState};
use crate::sample::sample_scene;
use crate::sample_jvm::CHECKOUT;
use crate::tools::Tool;

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

/// The page of `pid` in the sample, with `fake` for Spectra and its work held.
fn page(pid: u32, fake: Fake) -> (Runtime<App>, std::sync::Arc<Fake>) {
    let mut s = init();
    sample_scene(&mut s);
    s.expanded = None;
    let fake = std::sync::Arc::new(fake);
    s.inspect.spectra = fake.clone();
    s.inspect.exec = Exec::Held(Default::default());
    s.show(Nav::All);
    s.open(pid);
    (runtime(s), fake)
}

#[test]
fn a_jvms_page_leads_with_its_jvm_tools() {
    let (rt, _) = page(CHECKOUT, Fake::all());
    assert!(rt.find("tools-jvm").is_some());
    for tool in Tool::ALL
        .into_iter()
        .filter(|t| t.kind() == crate::tools::Kind::Jvm && t.has_row())
    {
        assert!(
            rt.find(&format!("tool-{}", tool.id())).is_some(),
            "{tool:?} has a row"
        );
    }
    assert!(
        rt.find("tool-suspects").is_none(),
        "analyses wait for a heap dump"
    );
}

#[test]
fn a_page_that_isnt_a_jvm_offers_no_jvm_tools() {
    let (rt, _) = page(1300, Fake::all());
    assert!(rt.find("tools").is_some());
    assert!(rt.find("tools-jvm").is_none());
    assert!(rt.find("tool-threads").is_none() && rt.find("tool-heapdump").is_none());
}

#[test]
fn a_thread_dump_shows_the_deadlock_first_and_filters() {
    let (mut rt, fake) = page(CHECKOUT, Fake::all());
    press(&mut rt, "tool-threads-run");
    run_held(&mut rt);
    assert_eq!(
        fake.calls(),
        [vec!["jvm", "thread-dump", "--json", &CHECKOUT.to_string()]]
    );
    assert_eq!(
        text_of(&rt, "threads-deadlock-0-title").as_deref(),
        Some("Deadlock: lab-worker-1, lab-worker-2")
    );
    assert!(rt.find("threads-counts").is_some());
    // The deadlocked threads lead the groups; a group opens to its frames.
    assert!(rt.find("threads-frames-0").is_none());
    press(&mut rt, "threads-group-0");
    assert!(rt.find("threads-frames-0").is_some());
    rt.update(|s, _| s.inspect.jvm.threads_query = TextEdit::new("no such thread"));
    settle(&mut rt);
    assert!(rt.find("threads-none").is_some());
    rt.update(|s, _| {
        s.inspect.jvm.threads_query = TextEdit::new("");
        s.inspect.jvm.threads_state = Some(crate::jvm_tools::ThreadState::Runnable);
    });
    settle(&mut rt);
    assert!(rt.find("threads-groups").is_some());
}

#[test]
fn a_second_histogram_shows_what_grew() {
    let (mut rt, _) = page(CHECKOUT, Fake::all());
    press(&mut rt, "tool-histogram-run");
    run_held(&mut rt);
    assert!(rt.find("histogram-hint").is_some() && rt.find("histogram-classes").is_some());
    press(&mut rt, "tool-histogram-run");
    run_held(&mut rt);
    assert!(rt.find("histogram-hint").is_none());
    assert!(
        rt.find("histogram-growth-none").is_some(),
        "the fake prints the same histogram twice"
    );
}

#[test]
fn every_jvm_tools_result_view_renders() {
    let (mut rt, _) = page(CHECKOUT, Fake::all());
    for (tool, key) in [
        (Tool::Gc, "gc-collectors"),
        (Tool::NativeMemory, "nmt-categories"),
        (Tool::Explain, "explain-findings"),
        (Tool::HeapDump, "heapdump-path"),
        (Tool::LeakSuspects, "suspects-0"),
        (Tool::Dominators, "dominators-classes"),
    ] {
        press(&mut rt, &format!("tool-{}-run", tool.id()));
        run_held(&mut rt);
        assert!(rt.find(key).is_some(), "{tool:?} shows {key}");
    }
    assert_eq!(
        text_of(&rt, "gc-collector").as_deref(),
        Some("Collector: G1, as its arguments ask")
    );
}

#[test]
fn nmt_off_says_so_plainly() {
    let mut fake = Fake::all();
    fake.replies.insert(
        "jvm nmt",
        Err(crate::jvm_tools::tests::NMT_OFF_ERR.trim().to_owned()),
    );
    let (mut rt, _) = page(CHECKOUT, fake);
    press(&mut rt, "tool-nmt-run");
    run_held(&mut rt);
    assert_eq!(
        text_of(&rt, "nmt-off").as_deref(),
        Some("Native memory tracking is off")
    );
    assert!(
        text_of(&rt, "nmt-off-why").is_some_and(|t| t.contains("-XX:NativeMemoryTracking=summary"))
    );
    assert!(
        rt.find("nmt-heap").is_some(),
        "the JVM's own view still shows"
    );
}

#[test]
fn a_recording_starts_stops_and_is_summarised_from_its_row() {
    let (mut rt, fake) = page(CHECKOUT, Fake::all());
    press(&mut rt, "tool-jfr-run");
    run_held(&mut rt);
    assert!(rt.find("jfr-recording").is_some(), "it says it's recording");
    press(&mut rt, "tool-jfr-stop-run");
    run_held(&mut rt);
    assert!(rt.find("jfr-recording").is_none());
    assert!(rt.find("jfr-events").is_some() && rt.find("jfr-path").is_some());
    let subs: Vec<String> = fake.calls().iter().map(|c| c[2].clone()).collect();
    assert_eq!(subs, ["start", "dump", "stop", "summary"]);
}

#[test]
fn leaving_a_recording_page_stops_the_recording() {
    let (mut rt, fake) = page(CHECKOUT, Fake::all());
    press(&mut rt, "tool-jfr-run");
    run_held(&mut rt);
    press(&mut rt, "back");
    run_held(&mut rt);
    assert_eq!(fake.calls().last().map(|c| c[2].as_str()), Some("stop"));
    assert!(rt.state.inspect.jvm.recording.is_none());
    assert!(matches!(
        rt.state.inspect.state(Tool::JfrStart),
        None | Some(RunState::Done(_))
    ));
}
