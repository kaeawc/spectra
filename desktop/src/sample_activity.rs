//! Sample process starts and exits for the Activity page's headless scenes and tests: a few
//! minutes of an ordinary afternoon, a deploy script that starts hundreds of processes a
//! minute, and an older Spectra without `spectra spawns`.

use crate::app::App;
use crate::findings::Link;
use crate::groups::Nav;
use crate::sample::{SAMPLE_NOW, sample_scene};
use crate::spawns::{Fail, parse};

/// One event line as `spectra spawns --json` prints it, `ago` seconds before the sample's
/// noon, with Spectra's own 250 ms polling steps.
fn line(
    kind: &str,
    ago: f64,
    pid: u32,
    ppid: u32,
    name: &str,
    chain: &[&str],
    lived_ms: Option<i64>,
) -> String {
    let ms = ((12.0 * 3600.0 - ago) * 1000.0).round() as i64;
    let ms = ms - ms % 250;
    let (h, m, s, frac) = (ms / 3_600_000, ms / 60_000 % 60, ms / 1000 % 60, ms % 1000);
    let chain = chain
        .iter()
        .map(|c| format!("\"{c}\""))
        .collect::<Vec<_>>()
        .join(",");
    let lived = lived_ms.map_or_else(String::new, |l| {
        format!(
            ",\"lifetime_ms\":{l},\"lifetime_max_error_ms\":{}",
            l.min(1000)
        )
    });
    format!(
        "{{\"event\":\"{kind}\",\"time\":\"2026-11-20T{h:02}:{m:02}:{s:02}.{frac:03}Z\",\"pid\":{pid},\"ppid\":{ppid},\"command\":\"{name}\",\"parent_chain\":[{chain}]{lived}}}"
    )
}

/// A process that started `ago` seconds ago and exited after `lived` seconds, if it has.
fn both(
    out: &mut Vec<(f64, String)>,
    ago: f64,
    lived: Option<f64>,
    pid: u32,
    ppid: u32,
    name: &str,
    chain: &[&str],
) {
    out.push((ago, line("start", ago, pid, ppid, name, chain, None)));
    if let Some(l) = lived.filter(|l| *l <= ago) {
        out.push((
            ago - l,
            line(
                "exit",
                ago - l,
                pid,
                ppid,
                name,
                chain,
                Some((l * 1000.0) as i64),
            ),
        ));
    }
}

const ZSH: &[&str] = &["zsh", "Terminal", "launchd"];
const BASH: &[&str] = &["bash", "zsh", "Terminal", "launchd"];
const CARGO: &[&str] = &["cargo", "zsh", "Terminal", "launchd"];
const LAUNCHD: &[&str] = &["launchd"];

/// Three minutes of an ordinary afternoon: a shell prompt running git, a build running rustc,
/// Spotlight workers, and a few long-lived starts the process feed lists.
fn ordinary() -> Vec<(f64, String)> {
    let mut out = Vec::new();
    for i in 0..22u32 {
        let ago = 178.0 - f64::from(i) * 8.1;
        both(
            &mut out,
            ago,
            Some(0.3 + f64::from(i % 3) * 0.1),
            20_000 + i * 3,
            1101,
            "git",
            ZSH,
        );
    }
    for i in 0..14u32 {
        let ago = 120.0 - f64::from(i) * 3.3;
        both(
            &mut out,
            ago,
            Some(4.0 + f64::from(i % 4) * 1.5),
            21_000 + i,
            7700,
            "rustc",
            CARGO,
        );
    }
    for i in 0..5u32 {
        both(
            &mut out,
            150.0 - f64::from(i) * 31.0,
            Some(28.0),
            22_000 + i,
            1,
            "mdworker_shared",
            LAUNCHD,
        );
    }
    for i in 0..9u32 {
        let ago = 70.0 - f64::from(i) * 6.0;
        both(&mut out, ago, Some(0.6), 23_000 + i, 1110, "curl", BASH);
    }
    both(&mut out, 132.0, None, 7700, 1101, "cargo", ZSH);
    both(
        &mut out,
        44.0,
        None,
        1311,
        1102,
        "gh",
        &["zsh", "Terminal", "launchd"],
    );
    both(&mut out, 12.0, None, 1320, 1101, "node", ZSH);
    both(
        &mut out,
        9.0,
        Some(1.2),
        23_500,
        1,
        "softwareupdated",
        LAUNCHD,
    );
    out
}

/// A deploy script's loop: `bash` (pid 1110) starts `sleep` and `curl` about four times a
/// second for the last minute and a half.
fn storm() -> Vec<(f64, String)> {
    let mut out = Vec::new();
    for i in 0..360u32 {
        let ago = 90.0 - f64::from(i) * 0.25;
        let name = if i % 2 == 0 { "sleep" } else { "curl" };
        both(
            &mut out,
            ago,
            Some(0.25 + f64::from(i % 3) * 0.1),
            30_000 + i,
            1110,
            name,
            BASH,
        );
    }
    out
}

/// Lands `lines` as rounds already recorded, oldest first.
fn land(s: &mut App, mut lines: Vec<(f64, String)>) {
    lines.sort_by(|a, b| b.0.total_cmp(&a.0));
    let text = lines
        .into_iter()
        .map(|(_, l)| l)
        .collect::<Vec<_>>()
        .join("\n");
    let events = parse(&text).expect("sample events parse");
    debug_assert!(events.iter().all(|e| e.at <= SAMPLE_NOW as f64));
    s.activity.push(events);
    s.activity.rounds = 18;
    s.activity.updated = Some(-4.0);
    s.refresh_findings();
}

/// The sample overview with Activity's sample made inert: nothing records on its own.
fn base(s: &mut App) {
    sample_scene(s);
    s.activity.auto = false;
}

/// An ordinary afternoon on the Activity page, recording, with one row open.
pub fn activity_scene(s: &mut App) {
    base(s);
    land(s, ordinary());
    s.show(Nav::Activity);
    s.activity.sample_flight();
    s.activity.expanded = s
        .activity
        .shown()
        .iter()
        .find(|e| e.name == "rustc" && e.kind == crate::spawns::Kind::Exit)
        .map(|e| e.id());
}

/// A deploy script starting hundreds of processes a minute: its spawn-storm finding followed
/// to the Activity page, filtered to that script.
pub fn activity_storm_scene(s: &mut App) {
    base(s);
    let mut lines = ordinary();
    lines.extend(storm());
    land(s, lines);
    s.follow(Link::Activity(1110));
    s.activity.sample_flight();
}

/// An older Spectra without `spectra spawns`.
pub fn activity_unsupported_scene(s: &mut App) {
    base(s);
    s.show(Nav::Activity);
    s.activity.error = Some(Fail::Unsupported(
        "This Spectra has no `spectra spawns`. Update Spectra to see processes start and exit."
            .into(),
    ));
}
