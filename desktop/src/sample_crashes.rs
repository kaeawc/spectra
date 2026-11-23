//! Sample data for the Crashes page's headless scenes and tests, from invented fixtures in the
//! shape of `spectra crash signatures|list|oom|inspect --json` (tests/fixtures/crashes), timed
//! around the sample's clock: a morning of repeated crashes, a clean reports folder, and one
//! report opened.

use crate::app::App;
use crate::crashes::{
    Crashes, Section, parse_inspect, parse_kills, parse_reports, parse_signatures,
};
use crate::groups::Nav;
use crate::sample::sample_scene;

pub const SIGNATURES: &str = include_str!("../tests/fixtures/crashes/signatures.json");
pub const LIST: &str = include_str!("../tests/fixtures/crashes/list.json");
pub const OOM: &str = include_str!("../tests/fixtures/crashes/oom.json");
pub const INSPECT: &str = include_str!("../tests/fixtures/crashes/inspect.json");
pub const EMPTY: &str = include_str!("../tests/fixtures/crashes/empty.json");
pub const NULL: &str = include_str!("../tests/fixtures/crashes/null.json");

/// The page as the sidebar opens it, without running Spectra.
fn crashes_page(s: &mut App) {
    sample_scene(s);
    s.select(None);
    s.nav = Nav::Crashes;
}

fn landed(s: &mut App, signatures: &str, list: &str, oom: &str) {
    let c = Crashes {
        signatures: parse_signatures(signatures).expect("the signatures fixture reads"),
        reports: parse_reports(list).expect("the list fixture reads"),
        kills: parse_kills(oom).expect("the oom fixture reads"),
        ..Crashes::default()
    };
    // Read a minute ago.
    s.crashes.data = Some((c, (s.wall)() - 60));
}

/// Repeated crashes this morning, recent reports and two memory kills.
pub fn crashes_scene(s: &mut App) {
    crashes_page(s);
    landed(s, SIGNATURES, LIST, OOM);
}

/// Nothing in the reports folder.
pub fn crashes_empty_scene(s: &mut App) {
    crashes_page(s);
    landed(s, EMPTY, EMPTY, NULL);
}

/// The widget host's latest crash, picked and opened.
pub fn crashes_report_scene(s: &mut App) {
    crashes_scene(s);
    let file = s
        .crashes
        .data
        .as_ref()
        .and_then(|(c, _)| {
            c.signatures
                .iter()
                .find(|x| x.process == "WidgetHostService")
        })
        .map(|x| x.file.clone())
        .expect("the fixture has the widget host");
    s.crashes.picked = Some((Section::Signatures, file.clone()));
    let report = parse_inspect(INSPECT).expect("the inspect fixture reads");
    s.crashes.opened.insert(file, Ok(report));
}
