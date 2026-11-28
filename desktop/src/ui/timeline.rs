//! The Timeline page: what changed before it got slow. Processes that started, bursts of errors
//! in the system log, and installs, newest first over a chosen window, each with when (how long
//! ago and the clock), a glyph for its kind, one sentence and, where there is one, a way to its
//! process or app. Rows from just before the load climbed are marked.

use rune::{El, col, el, icon, row, text};
use rune_kit::{Button, ButtonVariant, Kit, SegmentedControl, Spinner};

use super::home::callout;
use super::tools::failed;
use crate::app::App;
use crate::procs::fmt_count;
use crate::timeline::{
    DEADLINE, Filter, Go, Kind, Row, SLOW_BEFORE, Slow, Timeline, Window, ago, around, clock,
};

/// Rows drawn at once; a kind or a shorter window narrows the rest.
pub const SHOWN: usize = 200;

pub fn page(s: &App, k: &Kit) -> El {
    let intro = small(
        "What changed before it got slow: processes that started and are still running, bursts of errors in the system log, and installs and updates, newest first. Read when this page opens, when you pick a window, and on Refresh.",
        k,
    );
    let mut page = col()
        .key("tl-page")
        .w_full()
        .padding(24.0)
        .gap(16.0)
        .child(intro)
        .child(controls(s, k));
    let onset = s.slow_since();
    let wall = (s.wall)();
    if let Some(slow) = onset {
        let at = slow.at;
        let since = if slow.fresh {
            " and is still there".to_owned()
        } else {
            let last = s
                .system
                .load
                .updated
                .map_or(0, |u| (s.now - u).round() as i64);
            format!(
                "; the last load reading was {}, so it may have eased since",
                ago(last)
            )
        };
        let body = format!(
            "The load climbed past one per core {} ({}){since}. Rows from the {} minutes before are marked.",
            ago(wall - at),
            clock(at, wall, s.timeline_offset(at)),
            SLOW_BEFORE / 60
        );
        page = page.child(callout("tl-slow", "Things got slow", &body, k));
    }
    if let Some(at) = s.timeline.anchor {
        let body = format!(
            "Opened from the Crashes page for a crash {} ({}). Rows from the {} minutes before it are marked.",
            ago(wall - at),
            clock(at, wall, s.timeline_offset(at)),
            SLOW_BEFORE / 60
        );
        page = page.child(callout("tl-crash", "Around a crash", &body, k));
    }
    let t = &s.timeline;
    if let Some(w) = t.reading() {
        page = page.child(
            row()
                .key("tl-reading")
                .gap(8.0)
                .center_y()
                .child(Spinner::new().size(12.0).build(k))
                .child(small(
                    format!(
                        "Reading what happened {}… Stopped after {} s if it hasn't finished.",
                        w.words(),
                        DEADLINE.as_secs()
                    ),
                    k,
                )),
        );
    }
    if let Some(why) = &t.error {
        page = page
            .child(failed(
                "tl-error",
                &format!("Couldn't read the timeline: {why}"),
                k,
            ))
            .child(small("Refresh to try again.", k).key("tl-error-hint"));
    }
    match &t.data {
        Some((tl, w, at)) => page.child(rows(s, tl, *w, *at, onset, k)),
        None => page,
    }
}

fn controls(s: &App, k: &Kit) -> El {
    let t = &s.timeline;
    let window = SegmentedControl::new(
        &Window::ALL.map(Window::label),
        Window::ALL.iter().position(|w| *w == t.window),
    )
    .label("How far back")
    .fit()
    .on_pick(|s: &mut App, i: usize, _| s.set_timeline_window(Window::ALL[i]))
    .build(k)
    .key("tl-window");
    let kind = SegmentedControl::new(
        &Filter::ALL.map(Filter::label),
        Filter::ALL.iter().position(|f| *f == t.filter),
    )
    .label("Show")
    .fit()
    .on_pick(|s: &mut App, i: usize, _| s.set_timeline_filter(Filter::ALL[i]))
    .build(k)
    .key("tl-kind");
    let refresh = Button::new("Refresh")
        .variant(ButtonVariant::Outline)
        .disabled(t.reading().is_some())
        .help(format!(
            "Runs spectra timeline and spectra updates for {}",
            t.window.label().to_lowercase()
        ))
        .on_press(|s: &mut App| s.read_timeline())
        .build(k)
        .key("tl-refresh");
    row()
        .w_full()
        .gaps(12.0, 8.0)
        .center_y()
        .flex_wrap()
        .child(window)
        .child(kind)
        .child(refresh)
}

fn rows(s: &App, tl: &Timeline, w: Window, read_at: i64, onset: Option<Slow>, k: &Kit) -> El {
    let c = &k.c;
    let filter = s.timeline.filter;
    let mut body = col().key("tl-body").w_full().gap(10.0);
    if let Some(why) = &tl.updates_error {
        body = body.child(
            small(
                format!(
                    "Couldn't read the install history ({why}), so installs come from the timeline alone and don't say who made them."
                ),
                k,
            )
            .key("tl-updates-error"),
        );
    }
    if tl.log_capped() {
        body = body.child(
            small(
                format!(
                    "Spectra keeps the first {} error lines of a window, so later errors {} may be missing. A shorter window shows them.",
                    fmt_count(tl.log_lines as u64),
                    w.words()
                ),
                k,
            )
            .key("tl-log-capped"),
        );
    }
    let shown = tl.shown(filter);
    if tl.rows.is_empty() {
        return body.child(
            small(
                format!(
                    "Nothing changed {}: no process that is still running started, no errors were logged and nothing was installed.",
                    w.words()
                ),
                k,
            )
            .key("tl-none"),
        );
    }
    let mut count = format!(
        "{} of {} · newest first · read {}",
        fmt_count(shown.len() as u64),
        fmt_count(tl.rows.len() as u64),
        ago((s.wall)() - read_at)
    );
    if w != s.timeline.window {
        count.push_str(&format!(" · showing {}", w.label().to_lowercase()));
    }
    body = body.child(
        text(count)
            .key("tl-count")
            .font(k.ty.small)
            .color(c.text_muted),
    );
    if shown.is_empty() {
        return body.child(
            small(
                format!("No {} {}.", filter.label().to_lowercase(), w.words()),
                k,
            )
            .key("tl-no-match"),
        );
    }
    let now = (s.wall)();
    let mut list = col()
        .key("tl-rows")
        .w_full()
        .radius(k.radius.md)
        .border(1.0, c.border_weak)
        .fill(c.bg_base)
        .py(4.0);
    for (i, r) in shown.iter().take(SHOWN).enumerate() {
        let mark = if s.timeline.anchor.is_some_and(|a| around(r, a)) {
            Some("Around the crash")
        } else if onset.is_some_and(|o| around(r, o.at)) {
            Some("Around when things got slow")
        } else {
            None
        };
        list = list.child(line(s, i, r, now, s.timeline_offset(r.at), mark, k));
    }
    body = body.child(list);
    if shown.len() > SHOWN {
        body = body.child(
            small(
                format!(
                    "And {} more. Pick a kind or a shorter window to see them.",
                    fmt_count((shown.len() - SHOWN) as u64)
                ),
                k,
            )
            .key("tl-more"),
        );
    }
    body
}

fn line(s: &App, i: usize, r: &Row, now: i64, offset: i64, mark: Option<&str>, k: &Kit) -> El {
    let slow = mark.is_some();
    let c = &k.c;
    let id = r.id();
    let (glyph, ink) = match r.kind {
        Kind::Start => (k.glyph.caret_right, c.text_accent),
        Kind::Errors if r.loud => (k.glyph.circle_x, c.danger.fg),
        Kind::Errors => (k.glyph.dash, c.warning.fg),
        Kind::Install => (k.glyph.check, c.success.fg),
    };
    let when = col()
        .w(118.0)
        .gap(2.0)
        .child(
            text(ago(now - r.at))
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_base),
        )
        .child(
            text(clock(r.at, now, offset))
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_faint),
        );
    let mut what = col().grow(1.0).min_w(0.0).gap(2.0).child(
        text(r.sentence())
            .key(format!("tl-text-{id}"))
            .font(k.ty.small)
            .color(c.text_base)
            .selectable(true)
            .selection_color(k.selection())
            .wrap_anywhere(),
    );
    if let Some(mark) = mark {
        what = what.child(
            text(mark.to_owned())
                .key(format!("tl-mark-{id}"))
                .font(k.ty.small_medium)
                .color(c.warning.fg),
        );
    }
    let mut r_el = row()
        .key(format!("tl-row-{id}"))
        .w_full()
        .gap(12.0)
        .items_start()
        .px(12.0)
        .py(6.0)
        .child(when)
        .child(el().pt(2.0).child(icon(glyph, 13.0).color(ink)))
        .child(what);
    if let Some(go) = s.timeline_go(r) {
        let (label, help) = match &go {
            Go::Process(_) => ("Open process", "Open this process's page"),
            Go::App(_) => ("Open app", "Open this app's page"),
        };
        r_el = r_el.child(
            Button::new(label)
                .variant(ButtonVariant::Ghost)
                .help(help)
                .on_press(move |s: &mut App| s.follow_timeline(go.clone()))
                .build(k)
                .key(format!("tl-go-{id}")),
        );
    }
    if slow {
        r_el = r_el.fill(c.layer_01);
    }
    col()
        .w_full()
        .child(if i > 0 {
            el().w_full().h(1.0).fill(c.border_weak)
        } else {
            el()
        })
        .child(r_el)
}

fn small(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}
