//! Quit and Force quit: the buttons on a process page and over the list, the confirmation
//! before anything is sent, what happened after, and the page of a process that has ended.

use rune::{Chord, El, col, icon, row, text};
use rune_kit::{Button, ButtonVariant, Dialog, IconButton, IconButtonVariant, Kit};

use crate::app::{App, Status};
use crate::procs::Process;
use crate::signal::{self, Signal, Target};

/// ⌥⌘Q: opens the Quit confirmation, never sends on its own.
pub fn shortcut() -> Chord {
    Chord::parse("mod+alt+q").expect("a valid chord")
}

/// The two buttons for `p`, or the reason they're off. `key` prefixes their keys.
pub fn actions(s: &App, p: &Process, key: &str, k: &Kit) -> El {
    let c = &k.c;
    let pid = p.pid;
    let refusal = s.refusal(pid);
    let checking = s.checking();
    let off = refusal.is_some() || checking;
    let name = signal::display_name(p);
    let pending = s.quit_pending(pid);
    let quit = Button::new("Quit")
        .variant(ButtonVariant::Outline)
        .disabled(off || pending)
        .help(if checking {
            "Checking…".to_owned()
        } else if pending {
            format!("Already asked {name} to quit")
        } else {
            format!("Quit {name} (pid {pid})")
        })
        .shortcut("⌥⌘Q")
        .on_press(move |s: &mut App| s.ask(pid, Signal::Term))
        .build(k)
        .key(format!("{key}-quit"));
    // Destructive: red, and marked with a cross so it reads without colour.
    let force = Button::new("Force quit")
        .variant(ButtonVariant::Danger)
        .icon(k.glyph.circle_x)
        .disabled(off)
        .help(if checking { "Checking…".to_owned() } else { format!("Stop {name} (pid {pid}) immediately") })
        .on_press(move |s: &mut App| s.ask(pid, Signal::Kill))
        .build(k)
        .key(format!("{key}-force"));
    let mut r = row().key(format!("{key}-actions")).gap(8.0).center_y().min_w(0.0).shrink();
    if let Some(why) = refusal {
        r = r.child(
            row()
                .gap(4.0)
                .center_y()
                .min_w(0.0)
                .shrink()
                .child(icon(k.glyph.lock, 12.0).color(c.icon_muted))
                .child(
                    text(why.clone())
                        .key(format!("{key}-refusal"))
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .truncate()
                        .shrink()
                        .min_w(0.0)
                        .help_when_truncated(why),
                ),
        );
    }
    r.child(quit).child(force)
}

/// Over the list: what the cursor's row would quit, and its buttons.
pub fn list_actions(s: &App, k: &Kit) -> Option<El> {
    let c = &k.c;
    let cursor = s.table.cursor()?;
    let Some(p) = s.cursor_target().and_then(|pid| s.process(pid)) else {
        // A group of several that isn't one app: no single process to act on.
        return cursor.starts_with("g:").then(|| {
            text("Open the group to quit one of its processes")
                .key("list-no-target")
                .font(k.ty.small)
                .color(c.text_muted)
                .truncate()
                .shrink()
                .min_w(0.0)
        });
    };
    let what = format!("{} · {}", signal::display_name(p), p.pid);
    Some(
        row()
            .gap(10.0)
            .center_y()
            .min_w(0.0)
            .shrink()
            .child(
                text(what.clone())
                    .key("list-target")
                    .font(k.ty.small_medium)
                    .color(c.text_base)
                    .truncate()
                    .shrink()
                    .min_w(0.0)
                    .max_w(220.0)
                    .help_when_truncated(what),
            )
            .child(actions(s, p, "list", k)),
    )
}

/// The confirmation, while one is open: named, with its pid and what will happen. Cancel has
/// focus; Esc, the close button and a click outside cancel.
pub fn dialog(s: &App, k: &Kit, layer: &mut Vec<El>) {
    let Some(confirm) = &s.confirm else { return };
    let t: &Target = &confirm.target;
    let signal = confirm.signal;
    let lines = signal::consequence(t, signal);
    // The words go in the body, where they wrap; the dialog's description line doesn't.
    let mut d = Dialog::new("confirm", format!("{} {}?", signal.verb(), t.name))
        .width(440.0)
        .fit()
        .open(true)
        .on_close(|s: &mut App| s.cancel());
    let mut body = col().w_full().gap(12.0).px(16.0).pb(16.0).child(
        text(lines[0].clone())
            .key("confirm-line-0")
            .font(k.ty.ui)
            .color(k.c.text_base)
            .wrap()
            .w_full(),
    );
    for (i, line) in lines.iter().enumerate().skip(1) {
        body = body.child(
            row()
                .key(format!("confirm-note-{i}"))
                .w_full()
                .gap(8.0)
                .py(8.0)
                .px(10.0)
                .radius(k.radius.md)
                .border(1.0, k.c.warning.fg)
                .child(text("Careful:").font(k.ty.small_medium).color(k.c.text_base))
                .child(text(line.clone()).key(format!("confirm-line-{i}")).font(k.ty.small).color(k.c.text_base).wrap().grow(1.0).min_w(0.0)),
        );
    }
    let send = match signal {
        Signal::Term => Button::new("Quit").variant(ButtonVariant::Contrast),
        Signal::Kill => Button::new("Force quit").variant(ButtonVariant::Danger).icon(k.glyph.circle_x),
    };
    let buttons = row()
        .w_full()
        .gap(8.0)
        .justify_end()
        .child(
            Button::new("Cancel")
                .variant(ButtonVariant::Outline)
                .on_press(|s: &mut App| s.cancel())
                .build(k)
                .key("confirm-cancel")
                .autofocus(),
        )
        .child(send.on_press(|s: &mut App| s.confirm()).build(k).key("confirm-send"));
    d = d.child(body.child(buttons));
    d.build(k, layer);
}

/// What happened to the last Quit or Force quit, when it's about `pid` (or any, for `None`).
pub fn outcome(s: &App, pid: Option<u32>, k: &Kit) -> Option<El> {
    let c = &k.c;
    let o = s.outcome.as_ref()?;
    if pid.is_some_and(|pid| pid != o.target.pid) {
        return None;
    }
    let (mark, ink, border) = match &o.status {
        Status::Checking | Status::Asked { .. } => (None, c.text_base, c.border_base),
        Status::Ended => (Some(k.glyph.check_small), c.text_base, c.border_base),
        Status::StillRunning | Status::MaybeSent { .. } | Status::Waiting { .. } => {
            (Some(k.glyph.dash), c.text_base, c.warning.fg)
        }
        Status::Refused(_) | Status::Failed(_) => (Some(k.glyph.circle_x), c.text_base, c.danger.fg),
    };
    let line = o.line();
    let mut r = row()
        .key("quit-outcome")
        .role("status")
        .w_full()
        .gap(8.0)
        .center_y()
        .py(6.0)
        .px(12.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, border);
    if let Some(m) = mark {
        r = r.child(icon(m, 12.0).color(c.icon_base));
    }
    r = r.child(text(line).key("quit-outcome-text").font(k.ty.small).color(ink).wrap().grow(1.0).min_w(0.0));
    let alive = s.process(o.target.pid).is_some_and(|p| o.target.is(p));
    if o.status == Status::StillRunning && o.signal == Signal::Term && alive && s.refusal(o.target.pid).is_none() {
        // The process as drawn: if its pid changes hands before the press, the press opens nothing.
        let target = o.target.clone();
        r = r.child(
            Button::new("Force quit")
                .variant(ButtonVariant::Danger)
                .icon(k.glyph.circle_x)
                .on_press(move |s: &mut App| s.ask_target(target.clone(), Signal::Kill))
                .build(k)
                .key("quit-outcome-force"),
        );
    }
    if o.status == Status::Checking {
        // Its answer has to land somewhere.
        return Some(r);
    }
    Some(
        r.child(
            IconButton::new(k.glyph.close)
                .variant(IconButtonVariant::Ghost)
                .label("Dismiss")
                .on_press(|s: &mut App| s.dismiss_outcome())
                .build(k)
                .key("quit-outcome-dismiss"),
        ),
    )
}

/// The page of a process the app stopped, once it's gone: what it was, that it ended, and
/// the way back.
pub fn ended(s: &App, t: &Target, k: &Kit) -> El {
    let c = &k.c;
    let p = &t.process;
    let said = match &s.outcome {
        Some(o) if o.status == Status::Ended => o.line(),
        _ => format!("{} has ended", t.name),
    };
    let to = s.back_to().map_or_else(|| s.nav.title().to_owned(), |pid| super::page_name(s, pid));
    col()
        .key("ended-page")
        .w_full()
        .padding(24.0)
        .gap(12.0)
        .child(
            row()
                .gap(10.0)
                .center_y()
                .child(text(t.name.clone()).key("ended-name").font(k.ty.body_medium).color(c.text_base))
                .child(text(format!("pid {} · started {}", t.pid, p.started())).font(k.ty.small).color(c.text_muted)),
        )
        .child(
            row()
                .gap(8.0)
                .center_y()
                .child(icon(k.glyph.check_small, 14.0).color(c.icon_base))
                .child(text(said).key("ended-said").font(k.ty.ui_medium).color(c.text_base)),
        )
        .child(
            text("It's no longer running, so there's nothing more to show.")
                .font(k.ty.small)
                .color(c.text_muted),
        )
        .child(
            row().pt(4.0).child(
                Button::new(format!("Back to {to}"))
                    .variant(ButtonVariant::Outline)
                    .on_press(|s: &mut App| s.back())
                    .build(k)
                    .key("ended-back"),
            ),
        )
}
