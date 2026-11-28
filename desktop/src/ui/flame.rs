//! The CPU sample's flame chart: where the process's time went, as an icicle (the whole process
//! on top, callees below, each frame as wide as its share of the samples), and the functions that
//! ran the most themselves. Hovering a frame names it in full with its share and self time;
//! pressing one zooms into it, and Back or Whole process zooms out. The layout comes from
//! `flame.rs`, built when the result was read; drawing only turns its blocks into elements.

use rune::{Color, Cursor, El, col, el, row, text};
use rune_kit::{Button, ButtonVariant, ControlSize, Kit};

use super::{Col, figure, grid, line, name};
use crate::app::App;
use crate::flame::{Cell, Flame, Kind, What};
use crate::inspect::RunState;
use crate::tools::{Report, SampleReport, Tool};

const ROW_H: f32 = 20.0;
/// Blocks narrower than this share of the chart (about 24 px at its usual width) have no label.
const LABEL_MIN: f64 = 0.03;

/// Runs `f` on the flame of the page's sample result, if there is one.
fn with_flame(s: &mut App, f: impl FnOnce(&mut Flame)) {
    if let Some(run) = s.inspect.runs.get_mut(&Tool::Sample)
        && let RunState::Done(report) = &mut run.state
        && let Report::Sample(r) = report.as_mut()
    {
        f(&mut r.flame);
    }
}

fn pct(v: f64) -> String {
    if v > 0.0 && v < 0.1 {
        "<0.1%".into()
    } else {
        format!("{v:.1}%")
    }
}

/// What hovering a frame says: its full name, its share and its self time.
fn about(f: &Flame, cell: &Cell) -> String {
    match cell.what {
        What::Node(n) => {
            let node = &f.nodes[n];
            let mut s = match node.kind {
                Kind::Root => format!("Whole process: {} samples", node.total),
                Kind::Unlisted => format!(
                    "{}: {} samples ({}). Spectra lists only the busiest stacks.",
                    node.name,
                    node.total,
                    pct(f.share(node.total))
                ),
                _ => format!(
                    "{}\n{} of samples ({}) · self {} ({})",
                    node.name,
                    pct(f.share(node.total)),
                    node.total,
                    pct(f.share(node.self_samples)),
                    node.self_samples
                ),
            };
            if node.kind == Kind::Unnamed {
                s.push_str("\nNo symbol name in the sample.");
            }
            s
        }
        What::More { samples, frames } => format!(
            "{frames} frames too narrow to draw: {samples} samples ({}) together",
            pct(f.share(samples))
        ),
    }
}

/// A stable warm colour per name, so a function keeps its colour across zooms.
fn warm(name: &str, k: &Kit) -> Color {
    let h = name
        .bytes()
        .fold(2166136261u32, |h, b| (h ^ b as u32).wrapping_mul(16777619));
    let t = (h % 1000) as f32 / 1000.0;
    k.c.warning.bg.mix(k.c.danger.bg, t)
}

fn block(f: &Flame, cell: &Cell, k: &Kit) -> El {
    let c = &k.c;
    let (label, fill, key, press) = match cell.what {
        What::Node(n) => {
            let node = &f.nodes[n];
            let label = match node.kind {
                Kind::Root => format!("Whole process · {} samples", node.total),
                _ => node.name.clone(),
            };
            let fill = match node.kind {
                Kind::Root => c.layer_03,
                Kind::Thread => c.info.bg,
                Kind::Unnamed | Kind::Unlisted => c.layer_02,
                Kind::Frame => warm(&node.name, k),
            };
            (label, fill, format!("flame-cell-{n}"), Some(n))
        }
        What::More { .. } => (
            "…".to_owned(),
            c.layer_02,
            format!("flame-more-{}-{:.0}", cell.row, cell.x * 10_000.0),
            None,
        ),
    };
    let mut face = row()
        .w_full()
        .h_full()
        .min_w(0.0)
        .center_y()
        .radius(2.0)
        .fill(fill)
        .help(about(f, cell));
    // A block too narrow for a letter goes unlabelled, so its padding can't push it wider.
    if cell.w >= LABEL_MIN {
        face = face.px(4.0).child(
            text(label.clone())
                .font(k.ty.text_xsmall)
                .color(c.text_base)
                .truncate()
                .shrink()
                .min_w(0.0),
        );
    }
    if let Some(n) = press {
        let top = cell.row == 0;
        face = face
            .role("button")
            .label(if top && f.zoomed() {
                format!("{label}: zoom out")
            } else {
                format!("Zoom into {label}")
            })
            .cursor(Cursor::Pointer)
            .hover(c.hover)
            .on_press(move |s: &mut App| with_flame(s, |f| f.press(n)));
    }
    row()
        .key(key)
        .w_frac(cell.w as f32)
        .h_full()
        .min_w(0.0)
        .pr(1.0)
        .child(face)
}

fn rows(f: &Flame, k: &Kit) -> El {
    let mut chart = col().key("flame").w_full().gap(1.0);
    for cells in f.view.cells.chunk_by(|a, b| a.row == b.row) {
        let mut line = row().w_full().h(ROW_H);
        let mut at = 0.0;
        for cell in cells {
            if cell.x > at + 1e-6 {
                line = line.child(el().w_frac((cell.x - at) as f32).h_full());
            }
            line = line.child(block(f, cell, k));
            at = cell.x + cell.w;
        }
        chart = chart.child(line);
    }
    chart
}

/// The functions that ran the most themselves; a named one can show its code.
fn heaviest(f: &Flame, pid: u32, k: &Kit) -> El {
    let rows = f
        .view
        .heaviest
        .iter()
        .enumerate()
        .map(|(i, h)| {
            let mut who = row()
                .w_full()
                .gap(8.0)
                .center_y()
                .child(col().grow(1.0).min_w(0.0).child(name(h.name.clone(), k)));
            if !h.unnamed {
                let symbol = h.name.clone();
                who = who.child(
                    Button::new("Show code")
                        .variant(ButtonVariant::GhostMuted)
                        .control_size(ControlSize::Small)
                        .help("Disassemble this function with the tools on this Mac")
                        .on_press(move |s: &mut App| s.show_sampled_code(pid, &symbol))
                        .build(k)
                        .key(format!("flame-heavy-{i}-code")),
                );
            }
            vec![
                who,
                figure(pct(f.share(h.self_samples)), k),
                figure(h.self_samples.to_string(), k),
            ]
        })
        .collect();
    grid(
        "flame-heaviest",
        &[
            ("Heaviest functions, by self time", Col::Flex),
            ("Self", Col::Fixed(56.0)),
            ("Samples", Col::Fixed(60.0)),
        ],
        rows,
        k,
    )
}

/// The flame chart and the heaviest functions, or why there's nothing to draw.
pub fn chart(r: &SampleReport, k: &Kit) -> El {
    let c = &k.c;
    let f = &r.flame;
    if f.is_empty() {
        return text(
            "No flame graph: the sample caught no stacks. Run it again while the process is busy.",
        )
        .key("flame-unavailable")
        .font(k.ty.small)
        .color(c.text_muted)
        .wrap();
    }
    let mut head = row()
        .w_full()
        .gap(8.0)
        .center_y()
        .child(
            text("Where the CPU time goes")
                .font(k.ty.text_xsmall_medium)
                .color(c.text_faint),
        )
        .child(el().grow(1.0));
    if f.zoomed() {
        head = head
            .child(
                Button::new("Back")
                    .variant(ButtonVariant::Outline)
                    .help("Zoom out one step")
                    .on_press(|s: &mut App| with_flame(s, Flame::back))
                    .build(k)
                    .key("flame-back"),
            )
            .child(
                Button::new("Whole process")
                    .variant(ButtonVariant::Outline)
                    .help("Show every thread again")
                    .on_press(|s: &mut App| with_flame(s, Flame::whole))
                    .build(k)
                    .key("flame-whole"),
            );
    }
    let mut body = col().key("flame-section").w_full().gap(8.0).child(head);
    if f.zoomed() {
        let n = f.view.root;
        let node = &f.nodes[n];
        body = body.child(
            col()
                .w_full()
                .gap(2.0)
                .child(
                    text(f.path(n).join(" › "))
                        .key("flame-focus")
                        .font(k.ty.mono)
                        .color(c.text_base)
                        .selectable(true)
                        .selection_color(k.selection())
                        .wrap_anywhere(),
                )
                .child(line(
                    format!(
                        "{} of samples ({}) · self {} ({})",
                        pct(f.share(node.total)),
                        node.total,
                        pct(f.share(node.self_samples)),
                        node.self_samples
                    ),
                    k,
                )),
        );
    }
    body = body.child(rows(f, k));
    let mut notes = vec![(
        "flame-hint",
        if f.zoomed() {
            "Press a frame to zoom in further; press the top bar or Back to zoom out.".to_owned()
        } else {
            "Hover a frame for its full name and self time; press it to zoom in.".to_owned()
        },
    )];
    if f.view.clipped {
        notes.push((
            "flame-clipped",
            "Some deeper frames aren't drawn; zoom in to see them.".into(),
        ));
    }
    if f.unlisted > 0 {
        notes.push((
            "flame-unlisted",
            format!(
                "Spectra lists only the busiest stacks: the other {} samples ({}) are one block.",
                f.unlisted,
                pct(f.share(f.unlisted))
            ),
        ));
    }
    if f.unnamed > 0 {
        let frames = if f.unnamed == 1 {
            "frame has"
        } else {
            "frames have"
        };
        notes.push((
            "flame-unnamed",
            format!(
                "{} {frames} no symbol name (shown as ??? or an address). They can't be \
                 resolved here: the sample doesn't say which binary they're in.",
                f.unnamed
            ),
        ));
    }
    for (key, n) in notes {
        body = body.child(line(n, k).key(key));
    }
    body.child(heaviest(f, r.pid, k))
}
