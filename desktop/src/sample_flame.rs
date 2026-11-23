//! Sample scenes for the CPU sample's flame chart on rust-analyzer's page: the whole process,
//! zoomed into one frame, and a sample with no stacks to draw.

use crate::app::App;
use crate::groups::Nav;
use crate::inspect::RunState;
use crate::sample::{sample_live, sample_scene};
use crate::tools::{Report, Tool};

/// A sample of a busy rust-analyzer with unnamed frames and stacks Spectra didn't list.
pub const UNNAMED: &str = include_str!("../tests/fixtures/flame/unnamed.json");
/// What `spectra sample --json` printed for a process it caught no stacks in.
pub const EMPTY: &str = r#"{"pid":1300,"process":"rust-analyzer","duration_ms":1000,"interval_ms":10,
    "total_samples":0,"stack_count":0,"stacks":[],
    "main_thread":{"found":false,"verdict":"unknown","reason":"no main thread (com.apple.main-thread) found in the sample"}}"#;

fn with_sample(s: &mut App, json: &str) {
    sample_scene(s);
    s.expanded = None;
    s.show(Nav::All);
    s.open(1300);
    sample_live(s);
    let report = Tool::Sample.parse(json).expect("the sample output parses");
    s.inspect
        .put(Tool::Sample, RunState::Done(Box::new(report)));
}

/// The frame a scene zooms into.
pub const ZOOM: &str = "hir_ty::infer::InferenceContext::infer_body";

pub fn flame_scene(s: &mut App) {
    with_sample(s, UNNAMED);
}

pub fn flame_zoomed_scene(s: &mut App) {
    with_sample(s, UNNAMED);
    if let Some(run) = s.inspect.runs.get_mut(&Tool::Sample)
        && let RunState::Done(report) = &mut run.state
        && let Report::Sample(r) = report.as_mut()
        && let Some(n) = r.flame.nodes.iter().position(|n| n.name == ZOOM)
    {
        r.flame.press(n);
    }
}

pub fn flame_unavailable_scene(s: &mut App) {
    with_sample(s, EMPTY);
}
