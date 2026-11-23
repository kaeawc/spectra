//! Headless runs of the code view: "Show code" pressed on a crash report's frame, a sampled
//! function and a Java frame, the panel's marked line, Copy, and closing it. The tools are a
//! fake runner on a made-up filesystem throughout.

use std::sync::Arc;

use rune::{Env, Key, KeyEvent, Runtime};

use crate::app::{App, init};
use crate::codeview::State;
use crate::codeview::tests::FakeRunner;
use crate::codeview_tools::tests::FakeProbe;
use crate::codeview_tools::{Tool, Tools};
use crate::crashes::tests::Fake;
use crate::sample_codeview::{
    LLDB, codeview_jvm_scene, codeview_native_scene, codeview_none_scene,
};
use crate::sample_crashes::crashes_report_scene;

const PROCESS: &str = "/System/Library/PrivateFrameworks/WidgetHost.framework/XPCServices/WidgetHostService.xpc/WidgetHostService";
const JAVAP: &str = include_str!("../../tests/fixtures/codeview/javap.txt");

fn scene(f: fn(&mut App), r: FakeRunner, files: &[&str], tools: Tools) -> Runtime<App> {
    let mut s = init();
    s.crashes.spectra = Arc::new(Fake::ok());
    s.crashes.threaded = false;
    f(&mut s);
    s.codeview.runner = Arc::new(r);
    s.codeview.probe = Arc::new(FakeProbe::new(&[], files));
    s.codeview.set_tools(tools);
    s.codeview.threaded = false;
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
    // Lands what the press ran, as the app's poll does.
    rt.update(|s, _| {
        s.land_codeview();
    });
    settle(rt);
}

fn text_of(rt: &Runtime<App>, key: &str) -> String {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
        .unwrap_or_else(|| panic!("no text keyed {key}"))
}

/// The text of the first element whose text starts with `start`.
fn text_starting(rt: &Runtime<App>, start: &str) -> String {
    rt.elements()
        .into_iter()
        .filter_map(|e| e.text)
        .find(|t| t.starts_with(start))
        .unwrap_or_else(|| panic!("no text starting {start}"))
}

/// The widget host's report, opened, row key `cr-sig-3`: frame 0 is in libswiftCore (public
/// source), frame 1 in the made-up closed WidgetKitCore.
const ROW: &str = "cr-sig-3";

#[test]
fn a_closed_frame_offers_show_code_and_a_public_one_view_source() {
    let mut rt = scene(
        crashes_report_scene,
        FakeRunner::new(&[]),
        &[],
        Tools::default(),
    );
    assert!(has(&mut rt, &format!("{ROW}-frame-0-source")));
    assert!(!has(&mut rt, &format!("{ROW}-frame-0-code")));
    assert!(has(&mut rt, &format!("{ROW}-frame-1-code")));
    assert!(!has(&mut rt, &format!("{ROW}-frame-1-source")));
    // The app's own code has no public source either.
    assert!(has(&mut rt, &format!("{ROW}-frame-3-code")));
}

#[test]
fn show_code_disassembles_the_frame_with_its_offset_marked() {
    let mut rt = scene(
        crashes_report_scene,
        FakeRunner::new(&[("lldb", Ok(LLDB))]),
        &[PROCESS],
        Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]),
    );
    press(&mut rt, &format!("{ROW}-frame-1-code"));
    assert_eq!(text_of(&rt, "cv-tool"), "Disassembly from lldb");
    assert_eq!(
        text_of(&rt, "cv-mark-line"),
        "0x2a1c40f58 <+344>: brk    #0x1"
    );
    assert_eq!(
        text_of(&rt, "cv-note"),
        "The instruction at +344 is marked."
    );
    // It opens a little above the mark; the rest is a press away.
    assert!(has(&mut rt, "cv-above"));
    assert!(text_of(&rt, "cv-code-before").lines().count() == 12);
    press(&mut rt, "cv-above");
    assert!(!has(&mut rt, "cv-above"));
    assert!(text_of(&rt, "cv-code-before").starts_with("WidgetKitCore`TimelineCache.entry(for:):"));
    // Copy takes every line.
    let board = rune::clipboard::memory();
    rt.use_clipboard(&board);
    press(&mut rt, "cv-copy");
    let copied = board.text().map(|t| t.to_string()).unwrap_or_default();
    assert_eq!(copied.lines().count(), 97);
    assert!(copied.contains("<+344>: brk    #0x1"));
    // Closing it closes it.
    press(&mut rt, "close");
    assert!(rt.state.codeview.open.is_none());
    assert!(!has(&mut rt, "cv-code"));
}

#[test]
fn esc_closes_the_panel_without_leaving_the_page() {
    let mut rt = scene(
        crashes_report_scene,
        FakeRunner::new(&[("lldb", Ok(LLDB))]),
        &[PROCESS],
        Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]),
    );
    let nav = rt.state.nav;
    press(&mut rt, &format!("{ROW}-frame-1-code"));
    assert!(rt.state.codeview.open.is_some());
    rt.key(KeyEvent::from(Key::Escape));
    settle(&mut rt);
    assert!(rt.state.codeview.open.is_none());
    assert_eq!(rt.state.nav, nav);
}

#[test]
fn no_tool_says_why_and_what_to_install() {
    let mut rt = scene(
        crashes_report_scene,
        FakeRunner::new(&[]),
        &[PROCESS],
        Tools::default(),
    );
    press(&mut rt, &format!("{ROW}-frame-1-code"));
    assert!(has(&mut rt, "cv-none"));
    let why = text_starting(&rt, "No code view for this frame.");
    assert!(why.starts_with("No code view for this frame."), "{why}");
    assert!(
        why.contains("shared cache") || why.contains("xcode-select"),
        "{why}"
    );
    assert!(!has(&mut rt, "cv-copy"));
}

#[test]
fn a_sampled_function_opens_the_view() {
    let mut rt = scene(
        crate::sample_flame::flame_scene,
        FakeRunner::new(&[]),
        &[],
        Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]),
    );
    press(&mut rt, "flame-heavy-0-code");
    let p = rt.state.codeview.open.as_ref().expect("the panel opened");
    // The sample process's executable isn't on this made-up disk.
    assert!(
        matches!(&p.state, State::Nothing(w) if w.contains("executable")),
        "{:?}",
        p.state
    );
}

#[test]
fn a_java_frame_shows_its_bytecode() {
    let mut rt = scene(
        crate::sample_jvm::tool_threads_scene,
        FakeRunner::new(&[("javap", Ok(JAVAP))]),
        &[],
        Tools::with(&[(Tool::Javap, "/usr/bin/javap")]),
    );
    let key = rt
        .elements()
        .into_iter()
        .filter_map(|e| e.key)
        .find(|k| k.starts_with("threads-frames-") && k.ends_with("-code"))
        .expect("an open thread group lists a Java frame with code");
    press(&mut rt, &key);
    let p = rt.state.codeview.open.as_ref().expect("the panel opened");
    assert!(p.title.contains('('), "{}", p.title);
    assert!(
        matches!(&p.state, State::Shown(sh) if sh.what == "Bytecode from javap"),
        "{:?}",
        p.state
    );
}

#[test]
fn the_scenes_draw_their_panels() {
    let mut rt = scene(
        codeview_native_scene,
        FakeRunner::new(&[]),
        &[],
        Tools::default(),
    );
    assert_eq!(
        text_of(&rt, "cv-mark-line"),
        "0x2a1c40f58 <+344>: brk    #0x1"
    );
    let mut rt2 = scene(
        codeview_jvm_scene,
        FakeRunner::new(&[]),
        &[],
        Tools::default(),
    );
    assert_eq!(text_of(&rt2, "cv-tool"), "Bytecode from javap");
    assert_eq!(text_of(&rt2, "cv-mark-line").trim(), "28: aload_0");
    assert!(has(&mut rt2, "cv-tried"));
    let rt3 = scene(
        codeview_none_scene,
        FakeRunner::new(&[]),
        &[],
        Tools::default(),
    );
    assert!(text_starting(&rt3, "No code view").contains("needs lldb"));
    assert!(has(&mut rt, "cv-copy"));
}
