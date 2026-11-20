//! The detail page's timeline: stacked lanes on one x-axis, each scaled to its own range, and the
//! maths behind them, kept pure so it can be tested without a window.

use rune::{Color, El, Shape, canvas, col, el, row, text};
use rune_kit::Kit;

use crate::data::{Sample, fmt_kib};

/// Lanes are drawn in a view box this wide; it stretches to the lane's width.
pub const SPAN: f32 = 100.0;
pub const LANE_H: f32 = 44.0;
/// Space kept above and below a line so its stroke isn't clipped.
const PAD: f32 = 4.0;
const LABEL_W: f32 = 132.0;
const RANGE_W: f32 = 72.0;

/// The x of reading `i` of `n`, spread across `SPAN`.
pub fn x_at(i: usize, n: usize) -> f32 {
    if n <= 1 {
        SPAN / 2.0
    } else {
        i as f32 / (n - 1) as f32 * SPAN
    }
}

/// Where each value sits vertically in a lane `height` tall, scaled to the series' own range
/// (the lowest value on the bottom pad, the highest on the top one). A flat series sits mid-lane.
pub fn scale_y(values: &[f64], height: f32) -> Vec<f32> {
    let Some((lo, hi)) = range(values) else {
        return Vec::new();
    };
    let span = hi - lo;
    if span <= 1e-9 * hi.abs().max(1.0) {
        return vec![height / 2.0; values.len()];
    }
    let usable = height - 2.0 * PAD;
    values
        .iter()
        .map(|v| height - PAD - ((v - lo) / span) as f32 * usable)
        .collect()
}

pub fn range(values: &[f64]) -> Option<(f64, f64)> {
    let first = *values.first()?;
    Some(
        values
            .iter()
            .fold((first, first), |(lo, hi), v| (lo.min(*v), hi.max(*v))),
    )
}

/// GC time spent between consecutive readings, in seconds. A counter that goes backwards (the
/// JVM restarted under the same pid) counts as no time rather than negative time.
pub fn gc_deltas(history: &[Sample]) -> Vec<f64> {
    history
        .windows(2)
        .map(|w| (w[1].gc_seconds - w[0].gc_seconds).max(0.0))
        .collect()
}

/// Bars for `deltas` over `n` readings: bar `i` spans readings `i` to `i + 1`, rising from the
/// lane's floor in proportion to the largest delta. Returns `[x0, x1, top]` per bar; an all-zero
/// series has no bars.
pub fn bars(deltas: &[f64], height: f32) -> Vec<[f32; 3]> {
    let n = deltas.len() + 1;
    let max = deltas.iter().copied().fold(0.0, f64::max);
    if max <= 0.0 {
        return Vec::new();
    }
    let floor = height - 1.0;
    deltas
        .iter()
        .enumerate()
        .filter(|(_, d)| **d > 0.0)
        .map(|(i, d)| {
            let (a, b) = (x_at(i, n), x_at(i + 1, n));
            let inset = (b - a) * 0.15;
            [
                a + inset,
                b - inset,
                floor - (d / max) as f32 * (floor - PAD),
            ]
        })
        .collect()
}

pub fn fmt_secs(s: f64) -> String {
    if s >= 10.0 {
        format!("{s:.0} s")
    } else if s >= 1.0 {
        format!("{s:.1} s")
    } else {
        format!("{:.0} ms", s * 1000.0)
    }
}

fn count(n: f64) -> String {
    format!("{n:.0}")
}

pub enum Mark {
    Line,
    Bars,
}

pub struct Lane {
    pub key: &'static str,
    pub label: &'static str,
    pub values: Vec<f64>,
    pub unit: fn(f64) -> String,
    pub mark: Mark,
}

/// Lanes over `n` readings on one shared axis, keyed `{prefix}-timeline`, under a caption, or a
/// line saying readings are still coming in. `baseline` marks a reading.
pub fn lanes(
    prefix: &str,
    n: usize,
    lanes: &[Lane],
    baseline: Option<usize>,
    color: Color,
    k: &Kit,
) -> El {
    let c = &k.c;
    let body = col().key(format!("{prefix}-timeline")).w_full().gap(8.0);
    if n < 2 {
        return body.child(
            text("Collecting readings — the timeline starts after the next one.")
                .key(format!("{prefix}-timeline-collecting"))
                .font(k.ty.small)
                .color(c.text_muted),
        );
    }
    let caption = match baseline {
        Some(_) => format!("Last {n} readings · the rule marks the baseline"),
        None => format!("Last {n} readings"),
    };
    let mut rows = col()
        .w_full()
        .radius(k.radius.md)
        .border(1.0, c.border_weak)
        .fill(c.layer_01);
    for (i, lane) in lanes.iter().enumerate() {
        let mut r = lane_row(lane, n, baseline, color, k);
        if i > 0 {
            r = col()
                .w_full()
                .child(el().w_full().h(1.0).fill(c.border_weak))
                .child(r);
        }
        rows = rows.child(r);
    }
    body.child(
        text(caption)
            .key(format!("{prefix}-timeline-caption"))
            .font(k.ty.small)
            .color(c.text_faint),
    )
    .child(rows)
}

/// The timeline for one JVM's `history`, with a marker at `baseline` (an index into it).
pub fn timeline(history: &[Sample], baseline: Option<usize>, color: Color, k: &Kit) -> El {
    let all = [
        Lane {
            key: "jd-lane-heap",
            label: "Heap used",
            values: history.iter().map(|x| x.heap_used_kib).collect(),
            unit: fmt_kib,
            mark: Mark::Line,
        },
        Lane {
            key: "jd-lane-old",
            label: "Old generation",
            values: history.iter().map(|x| x.old_used_kib).collect(),
            unit: fmt_kib,
            mark: Mark::Line,
        },
        Lane {
            key: "jd-lane-gc",
            label: "GC time per reading",
            values: gc_deltas(history),
            unit: fmt_secs,
            mark: Mark::Bars,
        },
        Lane {
            key: "jd-lane-threads",
            label: "Threads",
            values: history.iter().map(|x| x.threads as f64).collect(),
            unit: count,
            mark: Mark::Line,
        },
    ];
    lanes("jd", history.len(), &all, baseline, color, k)
}

fn lane_row(lane: &Lane, n: usize, baseline: Option<usize>, color: Color, k: &Kit) -> El {
    let c = &k.c;
    let (lo, hi) = match lane.mark {
        Mark::Line => range(&lane.values).unwrap_or((0.0, 0.0)),
        Mark::Bars => (0.0, range(&lane.values).map_or(0.0, |r| r.1)),
    };
    let now = lane.values.last().copied().unwrap_or(0.0);
    let mut shape = Shape::new();
    if let Some(b) = baseline {
        let x = x_at(b, n);
        shape = shape.line(
            [x, 0.0],
            [x, LANE_H],
            1.0,
            rune::LineCap::Butt,
            c.text_faint,
        );
    }
    shape = match lane.mark {
        Mark::Line => {
            let ys = scale_y(&lane.values, LANE_H);
            let points: Vec<[f32; 2]> = ys
                .iter()
                .enumerate()
                .map(|(i, y)| [x_at(i, n), *y])
                .collect();
            shape.stroke(points, 1.5, color)
        }
        Mark::Bars => {
            let floor = LANE_H - 1.0;
            let mut s = shape.line(
                [0.0, floor],
                [SPAN, floor],
                1.0,
                rune::LineCap::Butt,
                c.border_base,
            );
            for [a, b, top] in bars(&lane.values, LANE_H) {
                s = s.fill(vec![[a, top], [b, top], [b, floor], [a, floor]], color);
            }
            s
        }
    };
    let label = col()
        .w(LABEL_W)
        .gap(2.0)
        .child(text(lane.label).font(k.ty.small).color(c.text_muted))
        .child(
            text((lane.unit)(now))
                .key(format!("{}-now", lane.key))
                .font(k.ty.ui_medium)
                .color(c.text_base),
        );
    let span = col()
        .w(RANGE_W)
        .h(LANE_H)
        .justify_between()
        .items_end()
        .child(
            text(format!("max {}", (lane.unit)(hi)))
                .font(k.ty.small)
                .color(c.text_faint),
        )
        .child(
            text(format!("min {}", (lane.unit)(lo)))
                .font(k.ty.small)
                .color(c.text_faint),
        );
    row()
        .key(lane.key)
        .w_full()
        .px(12.0)
        .py(8.0)
        .gap(12.0)
        .center_y()
        .child(label)
        .child(canvas(shape.view_box(SPAN, LANE_H)).grow(1.0).h(LANE_H))
        .child(span)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_lane_scales_to_its_own_range() {
        let ys = scale_y(&[10.0, 20.0, 15.0], 44.0);
        assert_eq!(ys[0], 44.0 - PAD, "the lowest value sits on the floor");
        assert_eq!(ys[1], PAD, "the highest at the top");
        assert!(
            (ys[2] - 22.0).abs() < 1e-4,
            "halfway between is mid-lane: {}",
            ys[2]
        );
        // Shifting and stretching the values doesn't change the shape.
        assert_eq!(
            scale_y(&[1010.0, 1020.0, 1015.0], 44.0),
            scale_y(&[0.0, 1e6, 5e5], 44.0)
        );
    }

    #[test]
    fn a_flat_series_draws_a_flat_line_mid_lane() {
        assert_eq!(scale_y(&[48.0; 5], 44.0), vec![22.0; 5]);
        assert_eq!(scale_y(&[0.0, 0.0], 44.0), vec![22.0; 2]);
        // Float noise on a large flat value is still flat.
        assert_eq!(
            scale_y(&[3_150_000.0, 3_150_000.0 + 1e-9], 44.0),
            vec![22.0; 2]
        );
        assert!(scale_y(&[], 44.0).is_empty());
    }

    #[test]
    fn readings_spread_across_the_shared_axis() {
        assert_eq!(x_at(0, 8), 0.0);
        assert_eq!(x_at(7, 8), SPAN);
        assert_eq!(x_at(0, 1), SPAN / 2.0);
    }

    #[test]
    fn gc_time_per_reading_is_the_delta_between_readings() {
        let s = |gc_seconds| Sample {
            gc_seconds,
            ..Sample::default()
        };
        let d = gc_deltas(&[s(1.0), s(1.5), s(1.5), s(2.25), s(0.1)]);
        assert_eq!(
            d,
            [0.5, 0.0, 0.75, 0.0],
            "a counter that resets counts as no time"
        );
        assert!(gc_deltas(&[s(1.0)]).is_empty());

        let b = bars(&d, 44.0);
        assert_eq!(b.len(), 2, "readings with no GC have no bar");
        let [a0, b0, top0] = b[0];
        assert!(
            a0 > 0.0 && b0 < x_at(1, 5),
            "the first bar sits between readings 0 and 1"
        );
        assert!(top0 > b[1][2], "a shorter delta draws a lower bar");
        assert_eq!(b[1][2], PAD, "the largest delta reaches the top");
        assert!(bars(&[0.0, 0.0], 44.0).is_empty());
    }

    #[test]
    fn formats_gc_seconds() {
        assert_eq!(fmt_secs(0.05), "50 ms");
        assert_eq!(fmt_secs(4.2), "4.2 s");
        assert_eq!(fmt_secs(42.0), "42 s");
    }
}
