//! A finding's card: a dot, its title and detail, and its subject; pressed, it opens on its
//! evidence and fix.

use rune::{Color, Cursor, El, Shape, canvas, col, el, row, text};
use rune_kit::Kit;

use crate::app::App;
use crate::data::fmt_kib;
use crate::findings::{Chart, Finding, Link, Severity};

pub fn ink(severity: Severity, k: &Kit) -> Color {
    match severity {
        Severity::Critical => k.c.danger.fg,
        Severity::Warning => k.c.warning.fg,
        Severity::Info => k.c.info.fg,
        Severity::Healthy => k.c.success.fg,
    }
}

pub fn dot(severity: Severity, k: &Kit) -> El {
    el().size(8.0, 8.0).radius(4.0).fill(ink(severity, k))
}

/// A line through `values`, scaled to its own range, filling the element.
fn sparkline(values: &[f64], color: Color) -> El {
    let (lo, hi) = values
        .iter()
        .fold((f64::MAX, f64::MIN), |(lo, hi), v| (lo.min(*v), hi.max(*v)));
    let span = (hi - lo).max(1.0);
    let last = values.len().saturating_sub(1).max(1) as f32;
    let points: Vec<[f32; 2]> = values
        .iter()
        .enumerate()
        .map(|(i, v)| {
            [
                i as f32 / last * 100.0,
                36.0 - ((v - lo) / span) as f32 * 32.0,
            ]
        })
        .collect();
    canvas(
        Shape::new()
            .stroke(points, 1.5, color)
            .view_box(100.0, 40.0),
    )
    .w_full()
    .h(56.0)
}

/// `now − then` with its sign, in `unit`.
pub fn delta(now: f64, then: f64, unit: fn(f64) -> String) -> String {
    let d = now - then;
    format!("{}{}", if d >= 0.0 { "+" } else { "−" }, unit(d.abs()))
}

fn evidence(s: &App, f: &Finding, k: &Kit) -> El {
    let c = &k.c;
    let history = s.history(f.pid);
    let mut body = col().w_full().gap(10.0).pt(12.0);
    let readings = s.proc_history(f.pid);
    let series: Vec<f64> = match f.chart {
        Chart::HeapUsed => history.iter().map(|x| x.heap_used_kib).collect(),
        Chart::OldGen => history.iter().map(|x| x.old_used_kib).collect(),
        Chart::Cpu => readings.iter().map(|x| x.cpu).collect(),
        Chart::Memory => readings.iter().map(|x| x.rss_kib).collect(),
        Chart::None => Vec::new(),
    };
    if series.len() >= 2 {
        let label = match f.chart {
            Chart::OldGen => "Old generation",
            Chart::Cpu => "CPU",
            Chart::Memory => "Memory",
            _ => "Heap used",
        };
        body = body
            .child(
                text(format!("{label}, last {} readings", series.len()))
                    .font(k.ty.small)
                    .color(c.text_faint),
            )
            .child(sparkline(&series, ink(f.severity, k)));
    }
    let jvm_rule = f.rule.starts_with("jvm-");
    if let (true, Some(base), Some(now)) = (
        jvm_rule,
        s.baseline.get(&f.pid).map(|b| b.sample),
        history.last(),
    ) {
        let count = |n: f64| format!("{n:.0}");
        let line = format!(
            "Since baseline: heap {}, old gen {}, classes {}, threads {}, GCs {}",
            delta(now.heap_used_kib, base.heap_used_kib, fmt_kib),
            delta(now.old_used_kib, base.old_used_kib, fmt_kib),
            delta(now.classes as f64, base.classes as f64, count),
            delta(now.threads as f64, base.threads as f64, count),
            delta(now.gc_count as f64, base.gc_count as f64, count),
        );
        body = body.child(
            text(line)
                .key("compare")
                .font(k.ty.small)
                .color(c.text_muted)
                .wrap(),
        );
    }
    if !f.fix.is_empty() {
        body = body.child(
            text(f.fix.clone())
                .key("fix")
                .font(k.ty.ui)
                .color(c.text_base)
                .wrap(),
        );
    }
    // A broken signature may be worth stopping now; that's for its page, behind a confirmation.
    if f.rule == "proc-invalid-signature"
        && let Link::Process(pid) = f.link
    {
        body = body.child(
            text(format!("To stop it now, open {} and choose Force quit.", f.subject))
                .key(format!("fix-open-{pid}"))
                .role("link")
                .label(format!("Open {}", f.subject))
                .cursor(Cursor::Pointer)
                .font(k.ty.small)
                .color(c.text_accent)
                .on_press(move |s: &mut App| s.open(pid)),
        );
    }
    body
}

pub fn card(s: &App, f: &Finding, k: &Kit) -> El {
    let c = &k.c;
    let id = f.id();
    let open = s.expanded.as_deref() == Some(id.as_str());
    let (pid, link) = (f.pid, f.link);
    let (subject, label) = match link {
        Link::Process(pid) => (format!("{} · {pid}", f.subject), format!("Open {}", f.subject)),
        Link::Unidentified => (f.subject.clone(), "Show unsigned and ad hoc processes".to_owned()),
        Link::System => (f.subject.clone(), "Show the Machine page".to_owned()),
    };
    // Its own handler, so pressing it opens the process rather than the card.
    let subject = text(subject)
        .key(format!("subject-{pid}-{}", f.rule))
        .role("link")
        .label(label)
        .cursor(Cursor::Pointer)
        .font(k.ty.small)
        .color(c.text_faint)
        .on_press(move |s: &mut App| s.follow(link));
    let head = row()
        .w_full()
        .gap(10.0)
        .center_y()
        .child(dot(f.severity, k))
        .child(
            col()
                .grow(1.0)
                .gap(2.0)
                .child(
                    text(f.title.clone())
                        .font(k.ty.ui_medium)
                        .color(c.text_base),
                )
                .child(
                    text(f.detail.clone())
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .wrap(),
                ),
        )
        .child(subject);
    let mut card = col()
        .key(format!("finding-{id}"))
        .role("button")
        .label(f.title.clone())
        .focusable(true)
        .cursor(Cursor::Pointer)
        .w_full()
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .on_press(move |s: &mut App| {
            s.expanded = if s.expanded.as_deref() == Some(id.as_str()) {
                None
            } else {
                Some(id.clone())
            }
        })
        .child(head);
    if open {
        card = card.child(evidence(s, f, k));
    }
    card
}

pub fn notice(title: &str, body: &str, k: &Kit) -> El {
    col()
        .w_full()
        .gap(4.0)
        .py(48.0)
        .items_center()
        .child(
            text(title.to_owned())
                .font(k.ty.body_medium)
                .color(k.c.text_base),
        )
        .child(
            text(body.to_owned())
                .font(k.ty.small)
                .color(k.c.text_muted)
                .wrap(),
        )
}
