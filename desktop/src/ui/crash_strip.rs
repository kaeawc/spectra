//! The Crashes page's strip of crashes over time: one bar per slice of the span, as tall as the
//! reports in it. Hovering a bar says when and how many; dragging across bars (or pressing one)
//! keeps the lists below to that window, and Clear shows everything again.

use rune::{Cursor, El, col, el, row, text};
use rune_kit::{Button, ButtonVariant, Kit};

use crate::app::App;
use crate::crash_strip::BUCKETS;
use crate::timeline::clock;

/// How tall the bars may get.
const BARS_H: f32 = 40.0;

fn crashes(n: u32) -> String {
    match n {
        0 => "no crashes".into(),
        1 => "1 crash".into(),
        n => format!("{n} crashes"),
    }
}

/// `Nov 19, 21:00 to 00:00`.
fn between(s: &App, from: i64, to: i64) -> String {
    let wall = (s.wall)();
    format!(
        "{} to {}",
        clock(from, wall, s.timeline_offset(from)),
        clock(to, wall, s.timeline_offset(to))
    )
}

pub fn strip(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let Some(st) = s.crash_strip() else {
        return el();
    };
    let p = &s.crashes;
    // The bars a drag covers, else the ones the window holds.
    let picked = p.drag.or_else(|| {
        p.range
            .map(|(from, to)| (st.bucket(from), st.bucket((to - 1).max(from))))
    });
    let shown = |i: usize| picked.is_some_and(|(a, b)| i >= a.min(b) && i <= a.max(b));
    let max = st.max().max(1) as f32;
    let mut bars = row()
        .key("cr-strip-bars")
        .w_full()
        .h(BARS_H + 8.0)
        .items_end()
        .px(4.0)
        .pt(4.0)
        .radius(k.radius.md)
        .border(1.0, c.border_weak)
        .fill(c.bg_base)
        .cursor(Cursor::Crosshair)
        .role("slider")
        .label("Crashes over time: drag across to show only those hours")
        .on_hover_change(|s: &mut App, inside, _| {
            if !inside {
                s.crashes.hover = None;
            }
        });
    for (i, n) in st.counts.iter().enumerate() {
        let ink = if picked.is_some() && !shown(i) {
            c.text_faint
        } else {
            c.text_accent
        };
        let mut slot = col()
            .key(format!("cr-strip-b{i}"))
            .grow(1.0)
            .min_w(0.0)
            .h_full()
            .justify_end()
            .cursor(Cursor::Crosshair)
            // Every bar is as wide as the others, so a drag from this one reads where it went.
            .on_drag(move |s: &mut App, d, _| s.drag_crash_strip(i, d))
            .on_hover(move |s: &mut App| s.crashes.hover = Some(i));
        if shown(i) {
            slot = slot.fill(c.layer_02);
        }
        if *n > 0 {
            let h = (*n as f32 / max * BARS_H).max(3.0);
            slot = slot.child(el().w_full().h(h).mx(1.0).radius(1.0).fill(ink));
        }
        bars = bars.child(slot);
    }
    let status = if let Some((a, b)) = p.drag {
        let (from, to) = st.window(a, b);
        format!("{}: {}", between(s, from, to), crashes(st.count(a, b)))
    } else if let Some(i) = p.hover.filter(|i| *i < BUCKETS) {
        let (from, to) = st.window(i, i);
        format!("{}: {}", between(s, from, to), crashes(st.counts[i]))
    } else if let Some((from, to)) = p.range {
        let n = picked.map_or(0, |(a, b)| st.count(a, b));
        format!("Showing {}: {}", between(s, from, to), crashes(n))
    } else {
        "Drag across the strip to show only those hours.".into()
    };
    let mut head = row()
        .w_full()
        .gap(12.0)
        .center_y()
        .child(
            text("Crashes over time")
                .key("cr-strip-title")
                .font(k.ty.ui_medium)
                .color(c.text_base),
        )
        .child(
            text(status)
                .key("cr-strip-status")
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_muted)
                .truncate()
                .grow(1.0)
                .min_w(0.0),
        );
    if p.range.is_some() {
        head = head.child(
            Button::new("Clear")
                .variant(ButtonVariant::Outline)
                .help("Show every crash again")
                .on_press(|s: &mut App| s.clear_crash_window())
                .build(k)
                .key("cr-strip-clear"),
        );
    }
    let wall = (s.wall)();
    let axis = row()
        .w_full()
        .justify_between()
        .child(
            text(clock(st.start, wall, s.timeline_offset(st.start)))
                .key("cr-strip-from")
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_faint),
        )
        .child(text("now").font(k.ty.small).color(c.text_faint));
    col()
        .key("cr-strip")
        .w_full()
        .gap(6.0)
        .child(head)
        .child(bars)
        .child(axis)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn counts_read_in_words() {
        assert_eq!(crashes(0), "no crashes");
        assert_eq!(crashes(1), "1 crash");
        assert_eq!(crashes(12), "12 crashes");
    }

    #[test]
    fn a_window_reads_as_two_clock_times() {
        let mut s = crate::app::init();
        s.wall = || 1_795_176_000;
        s.timeline.local = |_| Some(-5 * 3600);
        // 03:00 to 06:00 New York, the same day as the wall clock (07:00 there).
        let from = 1_795_176_000 - 4 * 3600;
        assert_eq!(between(&s, from, from + 3 * 3600), "03:00 to 06:00");
    }
}
