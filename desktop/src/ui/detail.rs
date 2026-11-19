//! One JVM's page: what's wrong with it in a line, its numbers now, how they moved, its findings
//! and the arguments it was started with.

use rune::{El, col, el, row, text};
use rune_kit::{Collapsible, Kit};

use super::card::{card, delta, dot, ink};
use super::chart::{fmt_secs, timeline};
use crate::app::App;
use crate::data::{Jvm, Sample, fmt_kib};
use crate::findings::Severity;

const TILE_W: f32 = 160.0;

pub fn detail(s: &App, j: &Jvm, k: &Kit) -> El {
    let history = s.history(j.pid);
    let findings = s.findings_for(j.pid);
    let mut list = col().key("jd-findings").w_full().gap(10.0);
    list = findings
        .iter()
        .fold(list, |list, f| list.child(card(s, f, k)));
    col()
        .key("jd-detail")
        .w_full()
        .padding(24.0)
        .gap(24.0)
        .child(header(s, j, k))
        .child(tiles(s, j, k))
        .child(section(
            "Timeline",
            timeline(&history, s.baseline_index(j.pid), k.c.text_accent, k),
            k,
        ))
        .child(section("Findings", list, k))
        .child(args(s, j, k))
}

fn section(title: &str, body: El, k: &Kit) -> El {
    col()
        .w_full()
        .gap(10.0)
        .child(
            text(title.to_owned())
                .font(k.ty.small_medium)
                .color(k.c.text_muted),
        )
        .child(body)
}

fn header(s: &App, j: &Jvm, k: &Kit) -> El {
    let c = &k.c;
    let worst = s.findings_for(j.pid).into_iter().next();
    let severity = worst.as_ref().map_or(Severity::Healthy, |f| f.severity);
    let verdict = worst.map_or_else(|| "Healthy".to_owned(), |f| f.title);
    let jdk = match (j.jdk_vendor.is_empty(), j.jdk_version.is_empty()) {
        (_, true) => "JDK unknown".to_owned(),
        (true, false) => format!("JDK {}", j.jdk_version),
        (false, false) => format!("{} {}", j.jdk_vendor, j.jdk_version),
    };
    col()
        .key("jd-header")
        .w_full()
        .gap(6.0)
        .child(
            row()
                .gap(10.0)
                .center_y()
                .child(
                    text(j.name())
                        .key("jd-name")
                        .font(k.ty.body_medium)
                        .color(c.text_base),
                )
                .child(
                    text(format!("pid {} · {jdk}", j.pid))
                        .key("jd-meta")
                        .font(k.ty.small)
                        .color(c.text_muted),
                ),
        )
        .child(
            row().gap(8.0).center_y().child(dot(severity, k)).child(
                text(verdict)
                    .key("jd-verdict")
                    .font(k.ty.ui_medium)
                    .color(ink(severity, k)),
            ),
        )
}

fn fmt_count(n: f64) -> String {
    let digits = format!("{:.0}", n.abs());
    let mut out = String::new();
    for (i, ch) in digits.chars().enumerate() {
        if i > 0 && (digits.len() - i) % 3 == 0 {
            out.push(',');
        }
        out.push(ch);
    }
    if n < 0.0 { format!("-{out}") } else { out }
}

struct Tile {
    key: &'static str,
    label: &'static str,
    value: String,
    since: Option<String>,
    bar: Option<(f64, Severity)>,
}

fn tiles(s: &App, j: &Jvm, k: &Kit) -> El {
    let now = j.sample();
    let base = s.baseline.get(&j.pid).map(|b| b.sample);
    let since = |f: fn(&Sample) -> f64, unit: fn(f64) -> String| {
        base.map(|b| format!("{} since baseline", delta(f(&now), f(&b), unit)))
    };
    let heap_pct = if now.heap_max_kib > 0.0 {
        now.heap_used_kib / now.heap_max_kib
    } else {
        0.0
    };
    let heap_severity = match heap_pct {
        p if p >= 0.9 => Severity::Critical,
        p if p >= 0.75 => Severity::Warning,
        _ => Severity::Healthy,
    };
    let all = [
        Tile {
            key: "tile-heap",
            label: "Heap",
            value: format!(
                "{} / {}",
                fmt_kib(now.heap_used_kib),
                fmt_kib(now.heap_max_kib)
            ),
            since: since(|x| x.heap_used_kib, fmt_kib),
            bar: Some((heap_pct, heap_severity)),
        },
        Tile {
            key: "tile-old",
            label: "Old gen",
            value: fmt_kib(now.old_used_kib),
            since: since(|x| x.old_used_kib, fmt_kib),
            bar: None,
        },
        Tile {
            key: "tile-metaspace",
            label: "Metaspace",
            value: fmt_kib(now.metaspace_kib),
            since: since(|x| x.metaspace_kib, fmt_kib),
            bar: None,
        },
        Tile {
            key: "tile-threads",
            label: "Threads",
            value: fmt_count(now.threads as f64),
            since: since(|x| x.threads as f64, fmt_count),
            bar: None,
        },
        Tile {
            key: "tile-classes",
            label: "Loaded classes",
            value: fmt_count(now.classes as f64),
            since: since(|x| x.classes as f64, fmt_count),
            bar: None,
        },
        Tile {
            key: "tile-gcs",
            label: "GCs",
            value: fmt_count(now.gc_count as f64),
            since: since(|x| x.gc_count as f64, fmt_count),
            bar: None,
        },
        Tile {
            key: "tile-gc-time",
            label: "GC time",
            value: fmt_secs(now.gc_seconds),
            since: since(|x| x.gc_seconds, fmt_secs),
            bar: None,
        },
    ];
    all.into_iter().fold(
        row().key("jd-tiles").w_full().flex_wrap().gaps(8.0, 8.0),
        |r, t| r.child(tile(t, k)),
    )
}

fn tile(t: Tile, k: &Kit) -> El {
    let c = &k.c;
    let key = format!("jd-{}", t.key);
    let mut body = col()
        .key(key.clone())
        .w(TILE_W)
        .gap(4.0)
        .py(10.0)
        .px(12.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .child(text(t.label).font(k.ty.small).color(c.text_muted))
        .child(
            text(t.value)
                .key(format!("{key}-value"))
                .font(k.ty.ui_medium)
                .color(c.text_base),
        );
    if let Some((frac, severity)) = t.bar {
        let fill = el()
            .w_frac(frac.clamp(0.0, 1.0) as f32)
            .h_full()
            .radius(2.0)
            .fill(ink(severity, k));
        body = body.child(
            el().w_full()
                .h(4.0)
                .radius(2.0)
                .fill(c.layer_03)
                .child(fill),
        );
    }
    if let Some(since) = t.since {
        body = body.child(
            text(since)
                .key(format!("{key}-since"))
                .font(k.ty.small)
                .color(c.text_muted),
        );
    }
    body
}

fn args(s: &App, j: &Jvm, k: &Kit) -> El {
    let c = &k.c;
    let flags: Vec<El> = j
        .vm_args
        .split_whitespace()
        .enumerate()
        .map(|(i, a)| {
            text(a.to_owned())
                .key(format!("jd-arg-{i}"))
                .font(k.ty.mono)
                .color(c.text_base)
                .selectable(true)
                .selection_color(k.selection())
        })
        .collect();
    let body = if flags.is_empty() {
        col().child(
            text("Started with no arguments.")
                .font(k.ty.small)
                .color(c.text_muted),
        )
    } else {
        col()
            .key("jd-arg-lines")
            .w_full()
            .gap(2.0)
            .pl(4.0)
            .pb(8.0)
            .selection_group(true)
            .children(flags)
    };
    let title = format!("JVM arguments ({})", j.vm_args.split_whitespace().count());
    col().key("jd-args").w_full().child(
        Collapsible::new(title, s.args_open)
            .on_toggle(|s: &mut App| s.args_open = !s.args_open)
            .child(body)
            .build(k),
    )
}
