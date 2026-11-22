//! The overview: the machine in three numbers, then every finding, worst first.

use std::collections::HashSet;

use rune::{Cursor, El, col, el, row, text};
use rune_kit::Kit;

use super::card::{card, dot, notice};
use crate::app::App;
use crate::findings::Finding;
use crate::groups::{Category, Nav};
use crate::procs::{ProcError, fmt_count, fmt_mem};
use crate::whatswrong::{Action, Item};

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

pub(super) fn tile(key: &str, label: &str, value: String, note: String, k: &Kit) -> El {
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
                    .child(el().w_full().h(4.0).radius(2.0).fill(c.layer_03).child(bar)),
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

/// Where a "Right now" line's link goes, and what it says.
fn action_link(s: &App, action: &Action) -> (String, String) {
    match action {
        Action::Process(pid) => {
            let name = s
                .process(*pid)
                .map_or_else(|| format!("pid {pid}"), |p| p.name().into_owned());
            (format!("{name} · {pid}"), format!("Open {name}"))
        }
        Action::App(path) => {
            let name = s.app_name(path);
            (name.clone(), format!("Open {name}"))
        }
        Action::Machine => ("Machine".into(), "Show the Machine page".into()),
    }
}

fn right_now_item(s: &App, i: usize, item: &Item, k: &Kit) -> El {
    let c = &k.c;
    let key = format!("ww-{i}");
    let (subject, label) = action_link(s, &item.action);
    let action = item.action.clone();
    let link = text(subject)
        .key(format!("{key}-open"))
        .role("link")
        .label(label)
        .cursor(Cursor::Pointer)
        .font(k.ty.small)
        .color(c.text_accent)
        .on_press(move |s: &mut App| match &action {
            Action::Process(pid) => s.open(*pid),
            Action::App(path) => s.open_app(path),
            Action::Machine => s.show(Nav::System),
        });
    let mut words = col().grow(1.0).shrink().min_w(0.0).gap(2.0).child(
        text(item.title.clone())
            .key(format!("{key}-title"))
            .font(k.ty.ui_medium)
            .color(c.text_base),
    );
    if !item.evidence.is_empty() {
        words = words.child(
            text(item.evidence.clone())
                .key(format!("{key}-evidence"))
                .font(k.ty.small)
                .color(c.text_muted)
                .wrap(),
        );
    }
    if !item.fix.is_empty() {
        words = words.child(
            text(item.fix.clone())
                .key(format!("{key}-fix"))
                .font(k.ty.small)
                .color(c.text_base)
                .wrap(),
        );
    }
    row()
        .key(key)
        .w_full()
        .gap(10.0)
        .center_y()
        .py(10.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .child(dot(item.severity, k))
        .child(words)
        .child(link)
}

/// "Right now": why this Mac may be slow at this moment, from Spectra's last look, with how old
/// that look is. Nothing until the first look starts.
fn right_now(s: &App, items: &[Item], k: &Kit) -> Option<El> {
    let c = &k.c;
    let w = &s.whatswrong;
    if w.report.is_none() && !w.running() && w.error.is_none() {
        return None;
    }
    let mut head = row().w_full().gap(8.0).center_y().child(
        text("Right now")
            .key("right-now-title")
            .font(k.ty.small_medium)
            .color(c.text_muted),
    );
    if let Some(age) = s.right_now_age() {
        head = head.child(el().grow(1.0)).child(
            text(age.clone())
                .key("right-now-age")
                .role("status")
                .font(k.ty.small)
                .color(c.text_faint)
                .truncate()
                .shrink()
                .min_w(0.0)
                .help_when_truncated(age),
        );
    }
    let mut section = col().key("right-now").w_full().gap(8.0).child(head);
    let line = |key: &str, words: String| {
        text(words)
            .key(key.to_owned())
            .font(k.ty.small)
            .color(c.text_muted)
            .wrap()
    };
    match (&w.report, &w.error) {
        (None, Some(e)) => {
            section = section.child(line(
                "right-now-error",
                format!("Couldn't check what's slowing this Mac: {e}"),
            ));
        }
        (None, None) => {
            section = section.child(line(
                "right-now-checking",
                "Asking Spectra what's slowing this Mac down…".into(),
            ));
        }
        (Some(_), _) if items.is_empty() => {
            section = section.child(line(
                "right-now-clear",
                "Nothing is slowing this Mac down right now.".into(),
            ));
        }
        (Some(_), _) => {
            section = items.iter().enumerate().fold(section, |sec, (i, item)| {
                sec.child(right_now_item(s, i, item, k))
            });
        }
    }
    Some(section)
}

pub fn home(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let now = s.right_now();
    // A finding "Right now" already says is shown there, once.
    let covered: HashSet<&str> = now
        .iter()
        .flat_map(|i| i.covers.iter().map(String::as_str))
        .collect();
    let findings: Vec<&Finding> = s
        .findings
        .iter()
        .filter(|f| !covered.contains(f.id().as_str()))
        .collect();
    let mut list = col().key("findings").w_full().padding(24.0).gap(10.0);
    if let Some(n) = feed_notice(s, k) {
        list = list.child(n);
    }
    if let Some(section) = right_now(s, &now, k) {
        list = list.child(section);
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
    // With lines in "Right now", "nothing needs attention" isn't true: only the rest is fine.
    let settled = s.loaded && s.proc_loaded;
    if s.findings.is_empty() && now.is_empty() && settled {
        list = list.child(notice(
            "Nothing needs attention",
            "Every process looks as it should.",
            k,
        ));
    } else if findings.is_empty() && (!s.findings.is_empty() || (!now.is_empty() && settled)) {
        list = list.child(
            text("Nothing else needs attention.")
                .key("findings-rest")
                .font(k.ty.small)
                .color(c.text_faint),
        );
    }
    findings
        .iter()
        .fold(list, |list, f| list.child(card(s, f, k)))
}
