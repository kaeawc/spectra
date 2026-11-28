//! Headless runs of the CPU sample's flame chart: drawn from a sample run through a fake Spectra,
//! widths in proportion to samples, zooming in and out, and a sample with nothing to draw.

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::inspect::tests::Fake;
use crate::inspect::{Exec, RunState};
use crate::sample::sample_scene;
use crate::sample_flame::{UNNAMED, ZOOM, flame_scene, flame_unavailable_scene};
use crate::tools::{Report, Tool};

fn runtime(s: App) -> Runtime<App> {
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(s, crate::ui::view, (1100.0, 700.0), env);
    rt.on_key = Some(crate::ui::on_key);
    rt.frame(0.0);
    rt
}

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    runtime(s)
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

fn width(rt: &Runtime<App>, key: &str) -> f32 {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .map(|e| e.rect[2])
        .unwrap_or_else(|| panic!("nothing keyed {key}"))
}

fn has(rt: &Runtime<App>, key: &str) -> bool {
    rt.elements().iter().any(|e| e.key.as_deref() == Some(key))
}

/// The tree index of the frame named `name` in the scene's sample.
fn node(name: &str) -> usize {
    let Report::Sample(r) = Tool::Sample.parse(UNNAMED).unwrap() else {
        unreachable!()
    };
    r.flame.nodes.iter().position(|n| n.name == name).unwrap()
}

fn zoomed_into(rt: &Runtime<App>) -> Option<usize> {
    match rt.state.inspect.state(Tool::Sample) {
        Some(RunState::Done(r)) => match r.as_ref() {
            Report::Sample(r) => r.flame.zoomed().then_some(r.flame.view.root),
            _ => None,
        },
        _ => None,
    }
}

#[test]
fn a_sample_run_through_spectra_lands_with_its_flame_chart() {
    let mut s = init();
    sample_scene(&mut s);
    s.expanded = None;
    let fake = std::sync::Arc::new(Fake::with(&[("sample", Ok(UNNAMED))]));
    s.inspect.spectra = fake.clone();
    s.inspect.exec = Exec::Held(Default::default());
    s.show(Nav::All);
    s.open(1300);
    let mut rt = runtime(s);
    assert!(
        !has(&rt, "flame"),
        "nothing is sampled until Run is pressed"
    );
    press(&mut rt, "tool-sample-run");
    rt.update(|s, _| s.run_held_inspect());
    settle(&mut rt);
    assert_eq!(fake.calls(), [vec!["sample", "-json", "1300"]]);
    assert!(has(&rt, "flame"));
    assert!(has(&rt, "flame-heaviest-0"));
    assert!(
        has(&rt, "sample-stacks-0"),
        "the busiest stacks stay listed"
    );
}

#[test]
fn frames_are_as_wide_as_their_share_and_the_notes_say_what_isnt_named_or_listed() {
    let mut rt = scene(flame_scene);
    locate(&mut rt, "flame");
    let whole = width(&rt, "flame-cell-0");
    // Thread_2201 holds 191 of 412 samples.
    let thread = width(&rt, &format!("flame-cell-{}", node("Thread_2201")));
    let want = whole * 191.0 / 412.0;
    assert!((thread - want).abs() < 2.0, "{thread} ≈ {want}");
    assert!(has(&rt, "flame-unlisted"));
    assert!(has(&rt, "flame-unnamed"));
    assert!(!has(&rt, "flame-back"), "the whole process needs no Back");
    // Four one-sample frames merge into one block.
    assert!(rt.elements().iter().any(|e| {
        e.key
            .as_deref()
            .is_some_and(|k| k.starts_with("flame-more-"))
    }));
    assert!(has(&rt, "flame-heaviest-14"));
    assert!(!has(&rt, "flame-heaviest-15"), "fifteen at most");
}

#[test]
fn pressing_a_frame_zooms_into_it_and_back_and_whole_process_zoom_out() {
    let mut rt = scene(flame_scene);
    let infer = node(ZOOM);
    let solve = node("chalk_solve::solve::Solver::solve");
    press(&mut rt, &format!("flame-cell-{infer}"));
    assert_eq!(zoomed_into(&rt), Some(infer));
    assert!(has(&rt, "flame-focus"));
    // The zoomed frame now spans the chart, and the thread above it is gone.
    assert!(!has(&rt, &format!("flame-cell-{}", node("Thread_2202"))));
    press(&mut rt, &format!("flame-cell-{solve}"));
    assert_eq!(zoomed_into(&rt), Some(solve));
    press(&mut rt, "flame-back");
    assert_eq!(zoomed_into(&rt), Some(infer));
    // The top bar zooms out too.
    press(&mut rt, &format!("flame-cell-{infer}"));
    assert_eq!(zoomed_into(&rt), None);
    press(&mut rt, &format!("flame-cell-{solve}"));
    press(&mut rt, "flame-whole");
    assert_eq!(zoomed_into(&rt), None);
    assert!(!has(&rt, "flame-focus"));
}

#[test]
fn a_sample_with_no_stacks_says_theres_nothing_to_draw() {
    let mut rt = scene(flame_unavailable_scene);
    locate(&mut rt, "flame-unavailable");
    assert!(!has(&rt, "flame"));
    assert!(!has(&rt, "flame-heaviest"));
}
