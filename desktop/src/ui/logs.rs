//! A page's Logs section: how far back and the lowest level, Show logs, Follow, and the entries
//! newest first with errors and faults standing out, under a filter.

use rune::{El, col, el, icon, row, text};
use rune_kit::{Button, ButtonVariant, Kit, SegmentedControl, Spinner, Switch, TextInput};

use super::detail::section;
use super::tools::failed;
use crate::app::App;
use crate::inspect::RunState;
use crate::logs::{
    Entry, FOLLOW_REST, Kind, Level, LogsReport, MAX_ROWS, QUERY_TOP, Target, Window,
};
use crate::procs::fmt_count;
use crate::tools::Tool;

/// Entries drawn at once; the filter narrows the rest.
const SHOWN: usize = 200;

pub fn panel(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let page = &s.inspect.logs;
    let target = s.logs_target();
    let mut body = col().key("logs").w_full().gap(10.0);
    match &target {
        Ok(t) => body = body.child(line(about(t), k).key("logs-about")),
        Err(why) => body = body.child(line(format!("{why}."), k).key("logs-off")),
    }
    body = body.child(controls(s, target.is_ok(), k));
    if let Some(why) = &page.follow_error {
        body = body.child(failed(
            "logs-follow-error",
            &format!("Follow stopped: {why}"),
            k,
        ));
    }
    match &page.state {
        None => {}
        Some(RunState::Running { .. }) => {
            body = body.child(
                row()
                    .key("logs-running")
                    .gap(8.0)
                    .center_y()
                    .child(Spinner::new().size(12.0).build(k))
                    .child(
                        text(format!(
                            "{} Stopped after {} s if it hasn't finished.",
                            Tool::Logs.running(),
                            Tool::Logs.deadline().as_secs()
                        ))
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .wrap(),
                    ),
            )
        }
        Some(RunState::Cancelling | RunState::Cancelled) => {
            body = body.child(line("Cancelled.", k).key("logs-cancelled"))
        }
        Some(RunState::Failed(why)) => body = body.child(failed("logs-error", why, k)),
        Some(RunState::Done(_)) => {
            if let (Some(r), Ok(t)) = (page.report(), &target) {
                body = body.child(entries(s, r, t, k));
            }
        }
    }
    section("Logs", body, k)
}

fn about(t: &Target) -> String {
    let mut s = format!(
        "Recent entries {} (pid {}) wrote to the system log.",
        t.name, t.pid
    );
    if t.namesakes > 0 {
        let n = t.namesakes + 1;
        s.push_str(&format!(
            " {n} running processes are named {}, and the log tells them apart only by pid: entries from the others are left out.",
            t.name
        ));
    }
    s
}

fn controls(s: &App, can: bool, k: &Kit) -> El {
    let page = &s.inspect.logs;
    let running = matches!(page.state, Some(RunState::Running { .. }));
    let window = SegmentedControl::new(
        &Window::ALL.map(Window::label),
        Window::ALL.iter().position(|w| *w == page.window),
    )
    .label("How far back")
    .fit()
    .on_pick(|s: &mut App, i: usize, _| s.set_logs_window(Window::ALL[i]))
    .build(k)
    .key("logs-window");
    let level = SegmentedControl::new(
        &Level::ALL.map(Level::label),
        Level::ALL.iter().position(|l| *l == page.level),
    )
    .label("Lowest level")
    .fit()
    .on_pick(|s: &mut App, i: usize, _| s.set_logs_level(Level::ALL[i]))
    .build(k)
    .key("logs-level");
    let button = if running {
        Button::new("Cancel")
            .variant(ButtonVariant::Outline)
            .help("Stop reading the log")
            .on_press(|s: &mut App| s.cancel_logs())
            .build(k)
            .key("logs-cancel")
    } else {
        Button::new(if page.state.is_some() {
            "Refresh"
        } else {
            "Show logs"
        })
        .variant(ButtonVariant::Outline)
        .disabled(!can)
        .help(format!("Runs spectra logs for {}", page.window.words()))
        .on_press(|s: &mut App| s.run_logs())
        .build(k)
        .key("logs-run")
    };
    let follow = Switch::new(page.follow)
        .label("Follow")
        .description(format!(
            "Looks again every {FOLLOW_REST:.0} s while this page is open"
        ))
        .on_toggle(|s: &mut App| s.toggle_logs_follow())
        .build(k)
        .key("logs-follow");
    row()
        .w_full()
        .gaps(12.0, 8.0)
        .center_y()
        .flex_wrap()
        .child(window)
        .child(level)
        .child(button)
        .child(follow)
}

fn entries(s: &App, r: &LogsReport, t: &Target, k: &Kit) -> El {
    let c = &k.c;
    let page = &s.inspect.logs;
    let mut body = col().w_full().gap(10.0);
    if r.others > 0 {
        body = body.child(
            line(
                format!(
                    "Left out {} entries from other processes named {}.",
                    fmt_count(r.others as u64),
                    t.name
                ),
                k,
            )
            .key("logs-others"),
        );
    }
    if r.truncated {
        body = body.child(line(
            format!(
                "There are more than {} entries in {}; these are the newest of the earliest {}. Pick a shorter window or Error to see the latest.",
                fmt_count(QUERY_TOP as u64),
                page.window.words(),
                fmt_count(QUERY_TOP as u64)
            ),
            k,
        )
        .key("logs-truncated"));
    }
    if r.follow_gaps {
        body = body.child(line(
            format!(
                "Following can't keep up: this process wrote more than {} entries in a minute, so some are missing. Pick Error or Fault to follow fewer.",
                fmt_count(QUERY_TOP as u64)
            ),
            k,
        )
        .key("logs-follow-gaps"));
    }
    if r.entries.is_empty() {
        return body.child(
            line(
                format!(
                    "No entries from {} in {}{}.",
                    t.name,
                    page.window.words(),
                    page.level.words()
                ),
                k,
            )
            .key("logs-none"),
        );
    }
    let filter = TextInput::new(&page.filter)
        .placeholder("Filter by message, subsystem or level")
        .label("Filter entries")
        .leading_icon(k.glyph.search)
        .clear_button()
        .width(Some(280.0))
        .on_edit(|s: &mut App| &mut s.inspect.logs.filter)
        .build(k)
        .key("logs-filter");
    let shown = r.shown(&page.filter.text);
    let mut count = format!(
        "{} of {} entries · newest first",
        fmt_count(shown.len() as u64),
        fmt_count(r.entries.len() as u64)
    );
    if r.entries.len() >= MAX_ROWS {
        count.push_str(&format!(
            " · keeps the newest {}",
            fmt_count(MAX_ROWS as u64)
        ));
    }
    if page.follow {
        count.push_str(" · following");
    }
    body = body.child(
        row()
            .w_full()
            .gaps(12.0, 8.0)
            .center_y()
            .flex_wrap()
            .child(filter)
            .child(
                text(count)
                    .key("logs-count")
                    .font(k.ty.small)
                    .color(c.text_muted),
            ),
    );
    if shown.is_empty() {
        return body.child(line("No entries match the filter.", k).key("logs-no-match"));
    }
    let mut list = col()
        .key("logs-rows")
        .w_full()
        .radius(k.radius.md)
        .border(1.0, c.border_weak)
        .fill(c.bg_base)
        .py(4.0);
    for (i, e) in shown.iter().take(SHOWN).enumerate() {
        list = list.child(entry_row(i, e, k));
    }
    body = body.child(list);
    if shown.len() > SHOWN {
        body = body.child(line(
            format!(
                "And {} more. Filter to narrow them.",
                fmt_count((shown.len() - SHOWN) as u64)
            ),
            k,
        ));
    }
    body
}

fn entry_row(i: usize, e: &Entry, k: &Kit) -> El {
    let c = &k.c;
    let ink = match e.kind {
        Kind::Fault => c.danger.fg,
        Kind::Error => c.warning.fg,
        _ => c.text_muted,
    };
    let mut level = row().w(76.0).gap(4.0).center_y();
    if e.kind.loud() {
        level = level.child(
            icon(
                if e.kind == Kind::Fault {
                    k.glyph.circle_x
                } else {
                    k.glyph.dash
                },
                12.0,
            )
            .color(ink),
        );
    }
    level = level.child(
        text(e.kind.label())
            .font(if e.kind.loud() {
                k.ty.small_medium
            } else {
                k.ty.small
            })
            .color(ink),
    );
    let source = e.source();
    let mut r = row()
        .key(format!("logs-row-{i}"))
        .w_full()
        .gap(12.0)
        .items_start()
        .px(12.0)
        .py(4.0)
        .child(
            text(e.clock().to_owned())
                .w(92.0)
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_muted),
        )
        .child(level)
        .child(
            text(source.clone())
                .w(200.0)
                .font(k.ty.small)
                .color(c.text_muted)
                .truncate()
                .min_w(0.0)
                .help_when_truncated(source),
        )
        .child(
            text(e.message.clone())
                .font(k.ty.mono)
                .color(if e.kind.loud() { ink } else { c.text_base })
                .selectable(true)
                .selection_color(k.selection())
                .wrap_anywhere()
                .grow(1.0)
                .min_w(0.0),
        );
    if e.kind.loud() {
        r = r.fill(c.layer_01);
    }
    col()
        .w_full()
        .child(if i > 0 {
            el().w_full().h(1.0).fill(c.border_weak)
        } else {
            el()
        })
        .child(r)
}

fn line(v: impl Into<String>, k: &Kit) -> El {
    text(v.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}
