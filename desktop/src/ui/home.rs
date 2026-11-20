//! The overview: the machine in three numbers, then every finding, worst first.

use rune::{El, col, el, row, text};
use rune_kit::Kit;

use super::card::{card, notice};
use crate::app::App;
use crate::groups::Category;
use crate::procs::{ProcError, fmt_count, fmt_mem};

/// A boxed message that sits above other content.
pub fn callout(key: &str, title: &str, body: &str, k: &Kit) -> El {
    let c = &k.c;
    col()
        .key(key.to_owned())
        .w_full()
        .gap(4.0)
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_base)
        .child(
            text(title.to_owned())
                .key(format!("{key}-title"))
                .font(k.ty.ui_medium)
                .color(c.text_base),
        )
        .child(
            text(body.to_owned())
                .font(k.ty.small)
                .color(c.text_muted)
                .wrap(),
        )
}

/// What's wrong with the process feed, when something keeps the list from showing.
pub fn feed_notice(s: &App, k: &Kit) -> Option<El> {
    match &s.proc_error {
        Some(ProcError::TooOld(why)) => Some(callout(
            "too-old",
            "Spectra is too old to list every process",
            &format!(
                "{why} Install a newer Spectra, or point SPECTRA_BIN at one before opening the \
                 app (SPECTRA_BIN=/path/to/spectra). JVMs still show."
            ),
            k,
        )),
        Some(ProcError::Failed(e)) if s.procs.is_empty() => {
            Some(callout("proc-error", "Couldn't list processes", e, k))
        }
        None if !s.proc_loaded => Some(notice(
            "Reading processes",
            "Spectra is looking at everything running. The first look can take a few seconds.",
            k,
        )),
        _ => None,
    }
}

fn tile(key: &str, label: &str, value: String, note: String, k: &Kit) -> El {
    let c = &k.c;
    col()
        .key(key.to_owned())
        .w(180.0)
        .gap(4.0)
        .py(10.0)
        .px(12.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .child(text(label.to_owned()).font(k.ty.small).color(c.text_muted))
        .child(
            text(value)
                .key(format!("{key}-value"))
                .font(k.ty.ui_medium)
                .color(c.text_base),
        )
        .child(text(note).font(k.ty.small).color(c.text_muted))
}

/// The categories holding the most memory, each with a bar against the largest.
fn memory_by_kind(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let by_kind: &[(Category, f64)] = &s.stats.memory;
    let top = by_kind.first().map_or(1.0, |t| t.1).max(1.0);
    let figures = k.ty.small.features("'tnum' 1");
    by_kind.iter().take(3).fold(
        col()
            .key("summary-memory")
            .w(260.0)
            .gap(6.0)
            .py(10.0)
            .px(12.0)
            .radius(k.radius.md)
            .fill(c.layer_01)
            .border(1.0, c.border_weak)
            .child(text("Memory by kind").font(k.ty.small).color(c.text_muted)),
        |list, (cat, kib)| {
            let bar = el()
                .w_frac((kib / top) as f32)
                .h_full()
                .radius(2.0)
                .fill(c.text_accent);
            list.child(
                col()
                    .key(format!("summary-memory-{}", cat.id()))
                    .w_full()
                    .gap(3.0)
                    .child(
                        row()
                            .w_full()
                            .child(text(cat.label()).font(k.ty.small).color(c.text_base))
                            .child(el().grow(1.0))
                            .child(text(fmt_mem(*kib)).font(figures).color(c.text_base)),
                    )
                    .child(
                        el().w_full()
                            .h(4.0)
                            .radius(2.0)
                            .fill(c.layer_03)
                            .child(bar),
                    ),
            )
        },
    )
}

/// Process count, CPU in use, and where the memory goes.
fn summary(s: &App, k: &Kit) -> El {
    let (cpu, apps) = (s.stats.cpu, s.stats.apps);
    row()
        .key("summary")
        .w_full()
        .flex_wrap()
        .gaps(8.0, 8.0)
        .child(tile(
            "summary-procs",
            "Processes",
            fmt_count(s.procs.len() as u64),
            format!("{} apps open", fmt_count(apps as u64)),
            k,
        ))
        .child(tile(
            "summary-cpu",
            "CPU in use",
            format!("{cpu:.0}%"),
            format!("about {:.1} of {} cores", cpu / 100.0, s.cores),
            k,
        ))
        .child(memory_by_kind(s, k))
}

/// When the numbers are from, in a plain line, once they're old or the last reading failed.
pub fn feed_status(s: &App, k: &Kit) -> Option<El> {
    let status = s.feed_status()?;
    Some(
        text(status.clone())
            .key("feed-status")
            .role("status")
            .font(k.ty.small)
            .color(k.c.text_muted)
            .truncate()
            .shrink()
            .min_w(0.0)
            .help_when_truncated(status),
    )
}

pub fn home(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let findings = &s.findings;
    let mut list = col().key("findings").w_full().padding(24.0).gap(10.0);
    if let Some(n) = feed_notice(s, k) {
        list = list.child(n);
    }
    if s.has_processes() {
        if let Some(status) = feed_status(s, k) {
            list = list.child(status);
        }
        list = list.child(summary(s, k)).child(
            text("Findings")
                .key("findings-title")
                .font(k.ty.small_medium)
                .color(c.text_muted)
                .pt(14.0),
        );
    }
    if let Some(e) = &s.error {
        list = list.child(callout("jvm-error", "Couldn't read JVMs", e, k));
    } else if !s.loaded {
        list = list.child(
            text("Still reading JVMs: Spectra looks inside each one, which takes a while.")
                .key("jvm-loading")
                .font(k.ty.small)
                .color(c.text_faint),
        );
    }
    if findings.is_empty() && s.loaded && s.proc_loaded {
        list = list.child(notice(
            "Nothing needs attention",
            "Every process looks as it should.",
            k,
        ));
    }
    findings
        .iter()
        .fold(list, |list, f| list.child(card(s, f, k)))
}
