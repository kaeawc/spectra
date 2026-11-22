//! A process page's tools: each of Spectra's per-process tools with a Run button, what it's
//! doing while it runs, and its result or why it failed. One runs at a time; nothing runs on
//! its own. A JVM's page leads with its JVM tools (`jvm_tools.rs`).

use rune::{Align, Cursor, El, col, el, icon, row, text};
use rune_kit::{Button, ButtonVariant, Kit, Spinner};

use super::card::{dot, ink};
use super::chart::{Lane, Mark, lanes};
use super::detail::section;
use crate::app::App;
use crate::findings::Severity;
use crate::inspect::RunState;
use crate::procs::{fmt_cpu, fmt_mem};
use crate::tools::{
    HangReport, Kind, LineageReport, MainThread, MemtraceReport, OwnerReport, RegionsReport, Report, RuntimeReport,
    SampleReport, Tool, VmmapReport, fmt_bytes,
};

#[path = "jvm_tools.rs"]
mod jvm;
#[cfg(test)]
#[path = "tests_jvm.rs"]
mod tests_jvm;

/// Stacks and regions listed in a result.
const ROWS: usize = 10;

pub fn panel(s: &App, pid: u32, k: &Kit) -> El {
    let c = &k.c;
    let mut body = col().key("tools").w_full().gap(14.0);
    let is_jvm = s.jvm(pid).is_some();
    for kind in Kind::ALL.into_iter().filter(|k| is_jvm || *k != Kind::Jvm) {
        let mut list = col()
            .key(format!("tools-{}", kind.label().to_lowercase()))
            .w_full()
            .radius(k.radius.md)
            .fill(c.layer_01)
            .border(1.0, c.border_weak);
        for (i, tool) in Tool::ALL.into_iter().filter(|t| t.kind() == kind && t.has_row()).enumerate() {
            if i > 0 {
                list = list.child(el().w_full().h(1.0).fill(c.border_weak));
            }
            list = list.child(tool_row(s, tool, pid, k));
        }
        body = body.child(
            col()
                .w_full()
                .gap(6.0)
                .child(text(kind.label()).font(k.ty.text_xsmall_medium).color(c.text_faint))
                .child(list),
        );
    }
    section("Tools", body, k)
}

/// What a tool's button says: its verb, or that it can run again.
fn verb(tool: Tool, again: bool) -> &'static str {
    match (tool, again) {
        (Tool::HeapDump, true) => "Take another",
        (Tool::JfrStart, _) => "Start recording",
        (t, false) => t.verb(),
        (_, true) => "Run again",
    }
}

/// What pressing a tool's button does, in a line.
fn press_help(s: &App, tool: Tool, pid: u32) -> String {
    match tool {
        Tool::HeapDump => format!("Writes the heap to {}", crate::jvm_tools::out_dir().display()),
        Tool::LeakSuspects | Tool::Dominators => format!("Reads {}", s.heap_dump_path().unwrap_or_default()),
        Tool::JfrStart => format!("Starts a recording named {} on pid {pid}", crate::jvm_tools::RECORDING),
        Tool::JfrStop => format!("Writes the recording to {} and stops it", crate::jvm_tools::out_dir().display()),
        t if t.kind() == Kind::Jvm => format!("Runs spectra {}", t.args(pid).join(" ")),
        t => format!("Run spectra {} on pid {pid}", t.id()),
    }
}

/// A tool's button: Run (or its verb), Cancel while it runs, or off with the reason.
fn button(s: &App, tool: Tool, pid: u32, k: &Kit) -> El {
    let id = tool.id();
    let state = s.inspect.state(tool);
    let refusal = s.tool_refusal(tool);
    let busy = s.inspect.busy();
    match state {
        _ if refusal.is_some() => Button::new("Run")
            .variant(ButtonVariant::Outline)
            .disabled(true)
            .help(refusal.clone().unwrap_or_default())
            .build(k)
            .key(format!("tool-{id}-run")),
        Some(RunState::Running { .. }) => Button::new("Cancel")
            .variant(ButtonVariant::Outline)
            .help(format!("Stop {}", tool.label().to_lowercase()))
            .on_press(move |s: &mut App| s.cancel_tool(tool))
            .build(k)
            .key(format!("tool-{id}-cancel")),
        Some(RunState::Cancelling) => Button::new("Cancelling…")
            .variant(ButtonVariant::Outline)
            .disabled(true)
            .build(k)
            .key(format!("tool-{id}-cancel")),
        _ => {
            let again = matches!(state, Some(RunState::Done(_) | RunState::Failed(_) | RunState::Cancelled));
            let waiting = busy.filter(|b| *b != tool);
            Button::new(verb(tool, again))
                .variant(ButtonVariant::Outline)
                .disabled(waiting.is_some())
                .help(match waiting {
                    Some(b) => format!("Waiting for {} to finish", b.label().to_lowercase()),
                    None => press_help(s, tool, pid),
                })
                .on_press(move |s: &mut App| s.run_tool(tool))
                .build(k)
                .key(format!("tool-{id}-run"))
        }
    }
}

fn tool_row(s: &App, tool: Tool, pid: u32, k: &Kit) -> El {
    let c = &k.c;
    let id = tool.id();
    let state = s.inspect.state(tool);
    // A recording's row stops it while it runs.
    let pressed = if tool == Tool::JfrStart && jvm::stopping_or_recording(s) { Tool::JfrStop } else { tool };
    let refusal = if pressed == tool { s.tool_refusal(tool) } else { None };
    let button = button(s, pressed, pid, k);
    let about = col()
        .grow(1.0)
        .min_w(0.0)
        .gap(2.0)
        .child(text(tool.label()).font(k.ty.ui_medium).color(if refusal.is_some() { c.text_muted } else { c.text_base }))
        .child(text(tool.about()).font(k.ty.small).color(c.text_muted).wrap());
    let mut head = row().w_full().gap(12.0).center_y().child(about);
    if let Some(why) = refusal {
        head = head.child(
            row()
                .gap(4.0)
                .center_y()
                .min_w(0.0)
                .shrink()
                .max_w(320.0)
                .child(icon(k.glyph.lock, 12.0).color(c.icon_muted))
                .child(
                    text(why.clone())
                        .key(format!("tool-{id}-refusal"))
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .truncate()
                        .shrink()
                        .min_w(0.0)
                        .help_when_truncated(why),
                ),
        );
    }
    let mut r = col()
        .key(format!("tool-{id}"))
        .w_full()
        .px(16.0)
        .py(10.0)
        .gap(10.0)
        .child(head.child(button));
    // A recording that started says so instead of its start's result.
    if let Some(state) = state.filter(|st| !(tool == Tool::JfrStart && matches!(st, RunState::Done(_)))) {
        r = r.child(status(s, tool, state, k));
    }
    if let Some(more) = jvm::below(s, tool, pid, k) {
        r = r.child(more);
    }
    r
}

fn status(s: &App, tool: Tool, state: &RunState, k: &Kit) -> El {
    let c = &k.c;
    let id = tool.id();
    match state {
        RunState::Running { .. } | RunState::Cancelling => {
            let line = if matches!(state, RunState::Cancelling) {
                "Stopping Spectra…".to_owned()
            } else {
                format!("{} Stopped after {} s if it hasn't finished.", tool.running(), tool.deadline().as_secs())
            };
            row()
                .key(format!("tool-{id}-running"))
                .gap(8.0)
                .center_y()
                .child(Spinner::new().size(12.0).build(k))
                .child(text(line).font(k.ty.small).color(c.text_muted).wrap())
        }
        RunState::Cancelled => text("Cancelled.")
            .key(format!("tool-{id}-cancelled"))
            .font(k.ty.small)
            .color(c.text_muted),
        RunState::Failed(why) => failed(&format!("tool-{id}-error"), why, k),
        RunState::Done(report) => col()
            .key(format!("tool-{id}-result"))
            .w_full()
            .gap(10.0)
            .child(result(s, report, k)),
    }
}

/// A failure, in the danger colour and marked with a cross so it reads without colour.
pub fn failed(key: &str, why: &str, k: &Kit) -> El {
    let ink = k.c.danger.fg;
    row()
        .key(key.to_owned())
        .w_full()
        .gap(6.0)
        .items_start()
        .child(icon(k.glyph.circle_x, 13.0).color(ink))
        .child(
            text(why.to_owned())
                .font(k.ty.small)
                .color(ink)
                .selectable(true)
                .selection_color(k.selection())
                .wrap_anywhere()
                .grow(1.0)
                .min_w(0.0),
        )
}

fn result(s: &App, report: &Report, k: &Kit) -> El {
    match report {
        Report::Sample(r) => sample(r, k),
        Report::Hang(r) => hang(r, k),
        Report::Vmmap(r) => vmmap(r, k),
        Report::Regions(r) => regions(r, k),
        Report::Memtrace(r) => memtrace(r, k),
        Report::Lineage(r) => lineage(s, r, k),
        Report::Owner(r) => owner(r, k),
        Report::Runtime(r) => runtime(r, k),
        Report::Threads(r) => jvm::threads(s, r, k),
        Report::Histogram(r) => jvm::histogram(s, r, k),
        Report::Gc(r) => jvm::gc(s, r, k),
        Report::NativeMemory(r) => jvm::native_memory(r, k),
        Report::Explain(r) => jvm::explain(r, k),
        Report::HeapDump(r) => jvm::heap_dump(r, k),
        Report::Leaks(r) => jvm::leaks(r, k),
        Report::Dominators(r) => jvm::dominators(r, k),
        Report::JfrStarted => el(),
        Report::Jfr(r) => jvm::recording(r, k),
        // Shown by the page's Logs section, never in a tool row.
        Report::Logs(_) => el(),
    }
}

fn line(v: impl Into<String>, k: &Kit) -> El {
    text(v.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}

fn mono(v: impl Into<String>, k: &Kit) -> El {
    text(v.into())
        .font(k.ty.mono)
        .color(k.c.text_base)
        .selectable(true)
        .selection_color(k.selection())
        .wrap_anywhere()
}

/// A verdict in a line: a dot and words in its colour.
fn verdict(key: &str, words: String, severity: Severity, k: &Kit) -> El {
    row()
        .gap(8.0)
        .center_y()
        .child(dot(severity, k))
        .child(text(words).key(key.to_owned()).font(k.ty.ui_medium).color(ink(severity, k)))
}

fn main_thread(key: &str, m: &MainThread, k: &Kit) -> El {
    let severity = if m.stuck() { Severity::Warning } else { Severity::Healthy };
    let mut body = col().w_full().gap(4.0).child(verdict(key, m.headline(), severity, k));
    if !m.reason.is_empty() {
        body = body.child(line(m.reason.clone(), k));
    }
    body
}

/// A column: fixed and right-aligned for figures, or flexible for names.
#[derive(Clone, Copy)]
enum Col {
    Flex,
    Fixed(f32),
}

/// A small table: a header row, then a row per entry, each cell under its column.
fn grid(key: &str, columns: &[(&str, Col)], rows: Vec<Vec<El>>, k: &Kit) -> El {
    let c = &k.c;
    let lay = |cells: Vec<El>| {
        cells.into_iter().zip(columns.iter()).fold(row().w_full().gap(12.0).center_y(), |r, (cell, (_, spec))| match spec {
            Col::Flex => r.child(col().grow(1.0).min_w(0.0).child(cell)),
            Col::Fixed(w) => r.child(col().w(*w).items_end().child(cell)),
        })
    };
    let header = lay(
        columns
            .iter()
            .map(|(title, _)| text((*title).to_owned()).font(k.ty.text_xsmall_medium).color(c.text_faint))
            .collect(),
    );
    let mut body = col()
        .key(key.to_owned())
        .w_full()
        .radius(k.radius.md)
        .border(1.0, c.border_weak)
        .fill(c.bg_base)
        .py(4.0)
        .child(header.px(12.0).py(4.0));
    for (i, cells) in rows.into_iter().enumerate() {
        body = body.child(lay(cells).key(format!("{key}-{i}")).px(12.0).py(4.0).min_h(26.0));
    }
    body
}

fn figure(v: String, k: &Kit) -> El {
    text(v).align(Align::End).font(k.ty.small.features("'tnum' 1")).color(k.c.text_base)
}

fn name(v: String, k: &Kit) -> El {
    text(v.clone()).font(k.ty.small).color(k.c.text_base).truncate().min_w(0.0).help_when_truncated(v)
}

fn sample(r: &SampleReport, k: &Kit) -> El {
    let c = &k.c;
    let secs = r.duration_ms as f64 / 1000.0;
    let mut body = col().w_full().gap(10.0);
    if let Some(m) = &r.main_thread {
        body = body.child(main_thread("sample-verdict", m, k));
    }
    let rows: Vec<Vec<El>> = r
        .stacks
        .iter()
        .take(ROWS)
        .map(|st| {
            let callers = st.callers(4).join(" › ");
            let mut where_ = col().w_full().min_w(0.0).gap(1.0).child(
                text(st.leaf().to_owned())
                    .font(k.ty.mono)
                    .color(c.text_base)
                    .truncate()
                    .min_w(0.0)
                    .help_when_truncated(st.leaf().to_owned()),
            );
            if !callers.is_empty() {
                where_ = where_.child(
                    text(format!("from {callers}"))
                        .font(k.ty.text_xsmall)
                        .color(c.text_faint)
                        .truncate()
                        .min_w(0.0)
                        .help_when_truncated(st.frames.join(" › ")),
                );
            }
            vec![where_, figure(format!("{:.1}%", st.percent), k), figure(st.count.to_string(), k)]
        })
        .collect();
    body.child(line(
        format!(
            "{} samples over {secs:.1} s · {} distinct stacks · busiest first",
            r.total_samples, r.stack_count
        ),
        k,
    ))
    .child(grid(
        "sample-stacks",
        &[("Where the time went", Col::Flex), ("Share", Col::Fixed(56.0)), ("Samples", Col::Fixed(60.0))],
        rows,
        k,
    ))
    .child(
        row()
            .gap(6.0)
            .center_y()
            .flex_wrap()
            .child(text("For a flame graph, run").font(k.ty.small).color(c.text_muted))
            .child(mono(format!("spectra flamegraph {}", r.pid), k).key("sample-flamegraph")),
    )
}

fn hang(r: &HangReport, k: &Kit) -> El {
    let m = &r.main_thread;
    let mut body = col().w_full().gap(8.0).child(main_thread("hang-verdict", m, k));
    if !m.top_frames.is_empty() {
        let frames = m
            .top_frames
            .iter()
            .filter(|f| !f.starts_with("Thread_"))
            .enumerate()
            .fold(col().key("hang-frames").w_full().gap(2.0).pl(4.0), |l, (i, f)| {
                l.child(mono(f.clone(), k).pl(i.min(8) as f32 * 10.0))
            });
        body = body.child(line("Where the main thread is, outermost first:", k)).child(frames);
    }
    body
}

/// Labelled figures in a row, wrapping.
fn figures(key: &str, items: &[(&str, String)], k: &Kit) -> El {
    let c = &k.c;
    items.iter().fold(row().key(key.to_owned()).w_full().flex_wrap().gaps(24.0, 8.0), |r, (label, v)| {
        r.child(
            col()
                .min_w(110.0)
                .gap(2.0)
                .child(text((*label).to_owned()).font(k.ty.small).color(c.text_muted))
                .child(text(v.clone()).font(k.ty.ui_medium.features("'tnum' 1")).color(c.text_base)),
        )
    })
}

fn vmmap(r: &VmmapReport, k: &Kit) -> El {
    let rows: Vec<Vec<El>> = r
        .regions
        .iter()
        .take(ROWS)
        .map(|g| {
            vec![
                name(g.kind.clone(), k),
                figure(fmt_bytes(g.dirty_bytes), k),
                figure(fmt_bytes(g.resident_bytes), k),
                figure(fmt_bytes(g.virtual_bytes), k),
                figure(fmt_bytes(g.swapped_bytes), k),
            ]
        })
        .collect();
    col()
        .w_full()
        .gap(10.0)
        .child(figures(
            "vmmap-totals",
            &[
                ("Footprint", fmt_bytes(r.footprint_bytes)),
                ("Peak", fmt_bytes(r.footprint_peak_bytes)),
                ("Dirty", fmt_bytes(r.total_dirty_bytes)),
                ("Swapped", fmt_bytes(r.total_swapped_bytes)),
            ],
            k,
        ))
        .child(grid(
            "vmmap-regions",
            &[
                ("Region type", Col::Flex),
                ("Dirty", Col::Fixed(68.0)),
                ("Resident", Col::Fixed(68.0)),
                ("Virtual", Col::Fixed(68.0)),
                ("Swapped", Col::Fixed(68.0)),
            ],
            rows,
            k,
        ))
}

fn regions(r: &RegionsReport, k: &Kit) -> El {
    let c = &k.c;
    let rows: Vec<Vec<El>> = r
        .top_dirty
        .iter()
        .take(ROWS)
        .map(|g| {
            let mut what = col().w_full().min_w(0.0).gap(1.0).child(name(g.kind.clone(), k));
            if !g.detail.is_empty() {
                what = what.child(
                    text(g.detail.clone())
                        .font(k.ty.text_xsmall)
                        .color(c.text_faint)
                        .truncate()
                        .min_w(0.0)
                        .help_when_truncated(g.detail.clone()),
                );
            }
            vec![
                what,
                text(format!("{} {}", g.prot, g.share)).font(k.ty.mono).color(c.text_muted),
                figure(fmt_bytes(g.dirty_bytes), k),
                figure(fmt_bytes(g.resident_bytes), k),
            ]
        })
        .collect();
    col()
        .w_full()
        .gap(10.0)
        .child(figures(
            "vmregions-totals",
            &[
                ("Dirty", fmt_bytes(r.total_dirty_bytes)),
                ("Anonymous", fmt_bytes(r.anonymous_dirty_bytes)),
                ("File-backed", fmt_bytes(r.file_backed_dirty_bytes)),
                ("Shared, resident", fmt_bytes(r.shared_resident_bytes)),
            ],
            k,
        ))
        .child(grid(
            "vmregions-top",
            &[
                ("Region", Col::Flex),
                ("Access", Col::Fixed(64.0)),
                ("Dirty", Col::Fixed(68.0)),
                ("Resident", Col::Fixed(68.0)),
            ],
            rows,
            k,
        ))
}

fn memtrace(r: &MemtraceReport, k: &Kit) -> El {
    let s = &r.summary;
    let severity = if r.growing() { Severity::Warning } else { Severity::Healthy };
    let secs = r.duration_ms as f64 / 1000.0;
    let mut body = col()
        .w_full()
        .gap(10.0)
        .child(verdict("memtrace-verdict", r.headline(), severity, k))
        .child(line(
            format!(
                "{} over {secs:.0} s ({} to {}) · {} readings, every {:.0} s",
                super::card::delta(s.last_kib, s.first_kib, fmt_mem),
                fmt_mem(s.first_kib),
                fmt_mem(s.last_kib),
                r.samples.len(),
                r.interval_ms as f64 / 1000.0
            ),
            k,
        ));
    if r.polls_failed > 0 {
        body = body.child(line(format!("{} readings failed and were left out.", r.polls_failed), k));
    }
    let lane = [Lane {
        key: "memtrace-lane",
        label: "Resident memory",
        values: r.samples.iter().map(|x| x.rss_kib).collect(),
        unit: fmt_mem,
        mark: Mark::Line,
    }];
    body.child(lanes("memtrace", r.samples.len(), &lane, None, k.c.text_accent, k))
}

/// What a lineage flag means.
fn flag_line(flag: &str) -> String {
    match flag {
        "reparented" => "Its parent has exited; launchd took it over.".into(),
        "orphan" => "Its parent is gone.".into(),
        "zombie" => "It has exited and waits for its parent to collect it.".into(),
        f => format!("Spectra notes: {f}"),
    }
}

/// A process in a lineage, as a row: how deep, which, and what's said on its right.
struct Kin<'a> {
    key: String,
    depth: usize,
    pid: u32,
    command: &'a str,
    extra: String,
}

/// A process in a lineage: its name and pid, a link when it has a page. `here` is the page's.
fn lineage_row(s: &App, kin: Kin<'_>, here: u32, k: &Kit) -> El {
    let c = &k.c;
    let Kin { key, depth, pid, command, extra } = kin;
    let linked = pid != here && s.process(pid).is_some();
    let mut label = text(command.to_owned())
        .font(if pid == here { k.ty.small_medium } else { k.ty.small })
        .color(if linked { c.text_accent } else { c.text_base })
        .truncate()
        .min_w(0.0)
        .shrink();
    if linked {
        label = label.help_when_truncated(command.to_owned());
    }
    let mut r = row().key(key).w_full().h(24.0).gap(10.0).center_y().pl(depth.min(12) as f32 * 16.0);
    if depth > 0 {
        r = r.child(text("└").font(k.ty.small).color(c.text_faint));
    }
    r = r
        .child(label)
        .child(text(pid.to_string()).font(k.ty.small.features("'tnum' 1")).color(c.text_muted))
        .child(el().grow(1.0))
        .child(text(extra).font(k.ty.small.features("'tnum' 1")).color(c.text_muted));
    if linked {
        let name = command.to_owned();
        r = r
            .role("link")
            .label(format!("Open {name}"))
            .cursor(Cursor::Pointer)
            .on_press(move |s: &mut App| s.open(pid));
    }
    r
}

fn lineage(s: &App, r: &LineageReport, k: &Kit) -> El {
    let mut body = col().w_full().gap(10.0);
    for f in &r.flags {
        body = body.child(line(flag_line(f), k));
    }
    // Ancestors come nearest first; show them from launchd down.
    let up: Vec<_> = r.ancestors.iter().rev().collect();
    let chain = up.iter().enumerate().fold(col().key("lineage-ancestors").w_full(), |l, (i, a)| {
        let kin = Kin { key: format!("lineage-up-{}", a.pid), depth: i, pid: a.pid, command: &a.command, extra: a.user.clone() };
        l.child(lineage_row(s, kin, r.pid, k))
    });
    body = body.child(line("Started by, from launchd down:", k)).child(chain);
    let tree = r.descendants();
    if tree.len() > 1 {
        let shown = tree.iter().take(60).fold(col().key("lineage-tree").w_full(), |l, (d, n)| {
            let extra = format!("{} · {}", fmt_cpu(n.cpu_pct), fmt_mem(n.rss_kib));
            let kin = Kin { key: format!("lineage-down-{}", n.pid), depth: *d, pid: n.pid, command: &n.command, extra };
            l.child(lineage_row(s, kin, r.pid, k))
        });
        let total = r.tree.as_ref().map_or(0, |t| t.count);
        let all = r.tree.as_ref().map_or(0.0, |t| t.subtree_rss_kib);
        body = body
            .child(line(format!("It and what it started: {total} processes, {} in all", fmt_mem(all)), k))
            .child(shown);
        if tree.len() > 60 {
            body = body.child(line(format!("and {} more", tree.len() - 60), k));
        }
    } else {
        body = body.child(line("It has started nothing that's still running.", k).key("lineage-none"));
    }
    body
}

fn owner(r: &OwnerReport, k: &Kit) -> El {
    let c = &k.c;
    let mut body = col().w_full().gap(10.0);
    if r.holders.is_empty() {
        body = body.child(line("Spectra found no owner.", k));
    }
    for (i, h) in r.holders.iter().enumerate() {
        let held = [h.hold.as_str(), h.mode.as_str(), h.detail.as_str()]
            .into_iter()
            .filter(|p| !p.is_empty())
            .collect::<Vec<_>>()
            .join(" · ");
        let chain = h.chain.iter().map(|l| format!("{} ({})", l.command, l.pid)).collect::<Vec<_>>().join(" ‹ ");
        let fact = |label: &str, v: String| {
            row()
                .w_full()
                .gap(12.0)
                .child(text(label.to_owned()).w(96.0).font(k.ty.small).color(c.text_muted))
                .child(
                    text(if v.is_empty() { "Unknown".into() } else { v })
                        .font(k.ty.small)
                        .color(c.text_base)
                        .selectable(true)
                        .selection_color(k.selection())
                        .wrap_anywhere()
                        .grow(1.0)
                        .min_w(0.0),
                )
        };
        body = body.child(
            col()
                .key(format!("owner-{i}"))
                .w_full()
                .gap(4.0)
                .child(fact("Process", format!("{} · {}", h.command, h.pid)))
                .child(fact("User", h.user.clone()))
                .child(fact("Executable", h.executable.clone()))
                .child(fact("Started", crate::procs::fmt_start(&h.started)))
                .child(fact("Held as", held))
                .child(fact("Parents", chain)),
        );
    }
    if r.incomplete || !r.hint.is_empty() {
        let hint = if r.hint.is_empty() { "Without root, only your own processes are visible.".to_owned() } else { r.hint.clone() };
        body = body.child(line(hint, k).key("owner-hint"));
    }
    body
}

fn runtime(r: &RuntimeReport, k: &Kit) -> El {
    let c = &k.c;
    let kind = match r.runtime.as_str() {
        "" => "Unknown".to_owned(),
        rt => crate::procs::Runtime::from(rt.to_owned()).label().to_owned(),
    };
    let mut body = col()
        .w_full()
        .gap(8.0)
        .child(
            row()
                .gap(8.0)
                .center_y()
                .child(text("Runs on").font(k.ty.small).color(c.text_muted))
                .child(text(kind).key("runtime-kind").font(k.ty.ui_medium).color(c.text_base)),
        );
    if !r.evidence.is_empty() {
        body = body.child(line(format!("Because: {}", r.evidence), k));
    }
    if r.capabilities.is_empty() {
        return body.child(line("Spectra suggests nothing more for it.", k));
    }
    let steps = r.capabilities.iter().enumerate().fold(col().key("runtime-steps").w_full().gap(8.0), |l, (i, cap)| {
        l.child(
            col()
                .key(format!("runtime-step-{i}"))
                .w_full()
                .gap(2.0)
                .child(text(capitalised(&cap.name)).font(k.ty.small_medium).color(c.text_base))
                .child(mono(cap.how.clone(), k)),
        )
    });
    body.child(line("Next steps:", k)).child(steps)
}

fn capitalised(s: &str) -> String {
    let mut ch = s.chars();
    match ch.next() {
        Some(f) => f.to_uppercase().chain(ch).collect(),
        None => String::new(),
    }
}
