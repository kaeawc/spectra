//! Sample data for the Crashes page's headless scenes and tests, from invented fixtures in the
//! shape of `spectra crash signatures|list|oom|inspect --json` (tests/fixtures/crashes), timed
//! around the sample's clock: a morning of repeated crashes, a clean reports folder, and one
//! report opened.

use crate::app::App;
use crate::crashes::{
    Crashes, Section, parse_inspect, parse_kills, parse_reports, parse_signatures,
};
use crate::groups::Nav;
use crate::procs::Process;
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
    // Clock times read in New York's winter offset, wherever the scene is rendered.
    s.timeline.local = |_| Some(-5 * 3600);
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

/// Invented processes with crashed processes' names, landed beside the sample's: the sync
/// daemon restarted two minutes after its last crash, and two widget hosts, one from yesterday.
pub fn land_crashed_running(s: &mut App) {
    let run = |pid: u32, name: &str, started: &str, cpu: f64, mb: f64| Process {
        pid,
        ppid: 1,
        uid: 501,
        user: "user".into(),
        command: name.into(),
        full_command_line: name.into(),
        cpu_pct: cpu,
        rss_kib: mb * 1024.0,
        thread_count: 6,
        started_at: started.into(),
        ..Process::default()
    };
    let mut all = s.procs.clone();
    all.extend([
        run(6120, "syncbridged", "2026-11-20T06:50:12-05:00", 3.4, 38.0),
        run(
            6230,
            "WidgetHostService",
            "2026-11-20T06:21:05-05:00",
            1.2,
            64.0,
        ),
        run(
            5480,
            "WidgetHostService",
            "2026-11-19T09:12:40-05:00",
            0.3,
            22.0,
        ),
    ]);
    s.land_procs(Ok(all));
}

/// Repeated crashes with the sync daemon and the widget host running again; the sync daemon's
/// row is picked, so it offers its page and a sample.
pub fn crashes_live_scene(s: &mut App) {
    crashes_scene(s);
    land_crashed_running(s);
    let file = s
        .crashes
        .data
        .as_ref()
        .and_then(|(c, _)| c.signatures.iter().find(|x| x.process == "syncbridged"))
        .map(|x| x.file.clone())
        .expect("the fixture has the sync daemon");
    s.crashes.picked = Some((Section::Signatures, file));
}

/// The strip with last night (19:00 to 04:00) picked: the lists keep to it.
pub fn crashes_scrub_scene(s: &mut App) {
    crashes_scene(s);
    let last = crate::crash_strip::BUCKETS - 1;
    s.pick_crash_window(last - 3, last - 1);
}

/// The widget host's crash opened, its system-library frames linking to their public source,
/// and a way to the Timeline around it.
pub fn crashes_report_links_scene(s: &mut App) {
    crashes_report_scene(s);
    land_crashed_running(s);
}
