//! The code view's panel: the frame, which tool made what's shown, and the code in a scrolling
//! monospace box with the frame's instruction or line highlighted, opening a little above it.
//! Copy puts every line on the clipboard. Closing it (or Esc) stops the tools.

use rune::{El, ScrollSpec, col, el, row, text};
use rune_kit::{Button, ButtonVariant, ControlSize, Dialog, Kit, Spinner};

use super::tools::failed;
use crate::app::App;
use crate::codeview::{DEADLINE, Panel, Shown, State};

/// Lines shown above the mark when the panel opens.
const ABOVE: usize = 12;
/// Lines drawn at once; Copy takes them all.
const SHOWN: usize = 1500;
const SHOWN_ALL: usize = 6000;

/// A title that fits the panel's header.
fn short(t: &str) -> String {
    const MAX: usize = 90;
    if t.chars().count() <= MAX {
        return t.to_owned();
    }
    let mut s: String = t.chars().take(MAX - 1).collect();
    s.push('…');
    s
}

/// Every line of the code on show, for Copy.
pub fn all_text(s: &App) -> Option<String> {
    match &s.codeview.open.as_ref()?.state {
        State::Shown(sh) => Some(sh.lines.join("\n")),
        _ => None,
    }
}

fn small(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}

fn block(key: &str, lines: &[String], k: &Kit) -> El {
    text(lines.join("\n"))
        .key(key.to_owned())
        .font(k.ty.mono)
        .color(k.c.text_base)
        .selectable(true)
        .selection_color(k.selection())
        .wrap_anywhere()
        .w_full()
}

/// The code box: a little above the mark unless everything was asked for.
fn code(p: &Panel, sh: &Shown, k: &Kit) -> El {
    let n = sh.lines.len();
    let start = match (p.show_all, sh.mark) {
        (false, Some(m)) if m > ABOVE => m - ABOVE,
        _ => 0,
    };
    let end = n.min(start + if p.show_all { SHOWN_ALL } else { SHOWN });
    let mut b = col()
        .key("cv-code")
        .w_full()
        .h(420.0)
        .padding(8.0)
        .radius(k.radius.sm)
        .border(1.0, k.c.border_weak)
        .fill(k.c.bg_base)
        .scroll(ScrollSpec {
            stick_bottom: false,
        });
    if start > 0 {
        b = b.child(
            Button::new(format!("Show the {start} lines above"))
                .variant(ButtonVariant::GhostMuted)
                .control_size(ControlSize::Small)
                .on_press(|s: &mut App| s.show_all_code())
                .build(k)
                .key("cv-above"),
        );
    }
    match sh.mark.filter(|m| (start..end).contains(m)) {
        Some(m) => {
            if m > start {
                b = b.child(block("cv-code-before", &sh.lines[start..m], k));
            }
            b = b.child(
                el().key("cv-mark")
                    .w_full()
                    .radius(2.0)
                    .fill(k.c.warning.bg)
                    .border(1.0, k.c.warning.border)
                    .child(
                        text(sh.lines[m].clone())
                            .key("cv-mark-line")
                            .font(k.ty.mono)
                            .color(k.c.text_strong)
                            .selectable(true)
                            .selection_color(k.selection())
                            .wrap_anywhere()
                            .w_full(),
                    ),
            );
            if m + 1 < end {
                b = b.child(block("cv-code-after", &sh.lines[m + 1..end], k));
            }
        }
        None => b = b.child(block("cv-code-text", &sh.lines[start..end], k)),
    }
    if end < n {
        b = b.child(
            small(
                format!("And {} more lines; Copy takes all of them.", n - end),
                k,
            )
            .key("cv-more"),
        );
    }
    b
}

/// The panel, when one is open.
pub fn dialog(s: &App, k: &Kit, layer: &mut Vec<El>) {
    let Some(p) = &s.codeview.open else {
        return;
    };
    let mut body = col().key("cv-body").w_full().gap(10.0).px(16.0).pb(16.0);
    match &p.state {
        State::Running => {
            body = body.child(
                row()
                    .key("cv-running")
                    .gap(8.0)
                    .center_y()
                    .child(Spinner::new().size(12.0).build(k))
                    .child(small(
                        format!(
                            "Finding the code… Stopped after {} s if it hasn't finished; closing stops it.",
                            DEADLINE.as_secs()
                        ),
                        k,
                    )),
            );
        }
        State::Nothing(why) => {
            body = body.child(failed(
                "cv-none",
                &format!("No code view for this frame. {why}"),
                k,
            ));
        }
        State::Shown(sh) => {
            body = body.child(
                row()
                    .w_full()
                    .gap(12.0)
                    .center_y()
                    .child(
                        text(sh.what.clone())
                            .key("cv-tool")
                            .font(k.ty.small_medium)
                            .color(k.c.text_base)
                            .grow(1.0)
                            .min_w(0.0)
                            .truncate(),
                    )
                    .child(
                        Button::new("Copy")
                            .variant(ButtonVariant::Outline)
                            .help("Copy every line to the clipboard")
                            .on_press_tx(|s: &mut App, tx| {
                                if let Some(t) = all_text(s) {
                                    tx.copy(t);
                                }
                            })
                            .build(k)
                            .key("cv-copy"),
                    ),
            );
            if !sh.note.is_empty() {
                body = body.child(small(sh.note.clone(), k).key("cv-note"));
            }
            if !sh.tried.is_empty() {
                body = body.child(
                    text(format!("Tried first: {}.", sh.tried.join("; ")))
                        .key("cv-tried")
                        .font(k.ty.small)
                        .color(k.c.text_faint)
                        .wrap(),
                );
            }
            body = body.child(code(p, sh, k));
        }
    }
    Dialog::new("cv", short(&p.title))
        .width(900.0)
        .fit()
        .open(true)
        .on_close(|s: &mut App| s.close_code())
        .child(body)
        .build(k, layer);
}
