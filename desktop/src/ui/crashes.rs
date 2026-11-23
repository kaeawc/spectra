//! The Crashes page: what has been crashing on this Mac, and why. Repeated crashes first (most
//! often, then most recent), then the recent reports one by one, then processes ended for using
//! too much memory. Picking a row and pressing "Open report" decodes that report and shows its
//! crashed thread's top frames, the exception and the reason right under the row.

use rune::{Cursor, El, col, el, row, text};
use rune_kit::{Button, ButtonVariant, Kit, Spinner};

use super::tools::failed;
use crate::app::App;
use crate::crashes::{
    DEADLINE, Inspected, MemoryKill, Report, STALE, Section, Signature, TOP_FRAMES, what,
};
use crate::procs::fmt_count;
use crate::timeline::ago;

pub fn page(s: &App, k: &Kit) -> El {
    let c = &s.crashes;
    let wall = (s.wall)();
    let mut head = row().w_full().gap(12.0).center_y().child(
        small(
            format!(
                "What has been crashing on this Mac, and why, from the reports macOS keeps. Read when this page opens (again once the last reading is {} minutes old) and on Refresh.",
                STALE / 60
            ),
            k,
        )
        .grow(1.0)
        .min_w(0.0),
    );
    if let Some((_, at)) = &c.data {
        head = head.child(
            text(format!("Read {}", ago(wall - at)))
                .key("cr-age")
                .role("status")
                .font(k.ty.small)
                .color(k.c.text_faint),
        );
    }
    let refresh = Button::new("Refresh")
        .variant(ButtonVariant::Outline)
        .disabled(c.reading())
        .help("Runs spectra crash signatures, list and oom")
        .on_press(|s: &mut App| s.read_crashes())
        .build(k)
        .key("cr-refresh");
    let mut page = col()
        .key("cr-page")
        .w_full()
        .padding(24.0)
        .gap(20.0)
        .child(head.child(refresh));
    if c.reading() {
        page = page.child(
            row()
                .key("cr-reading")
                .gap(8.0)
                .center_y()
                .child(Spinner::new().size(12.0).build(k))
                .child(small(
                    format!(
                        "Reading the crash reports… Stopped after {} s if it hasn't finished.",
                        DEADLINE.as_secs()
                    ),
                    k,
                )),
        );
    }
    if let Some(why) = &c.error {
        page = page
            .child(failed(
                "cr-error",
                &format!("Couldn't read the crash reports: {why}"),
                k,
            ))
            .child(small("Refresh to try again.", k).key("cr-error-hint"));
    }
    let Some((data, _)) = &c.data else {
        return page;
    };
    if data.is_empty() && data.reports_error.is_none() && data.kills_error.is_none() {
        return page.child(
            small(
                "No crashes in the reports folder. When a process crashes, hangs or is ended for using too much memory, it shows here.",
                k,
            )
            .key("cr-none"),
        );
    }
    page.child(signatures(s, &data.signatures, wall, k))
        .child(reports(
            s,
            &data.reports,
            data.reports_error.as_deref(),
            wall,
            k,
        ))
        .child(kills(s, &data.kills, data.kills_error.as_deref(), wall, k))
}

fn heading(key: &str, title: &str, about: &str, k: &Kit) -> El {
    col()
        .gap(2.0)
        .child(
            text(title.to_owned())
                .key(key.to_owned())
                .font(k.ty.ui_medium)
                .color(k.c.text_base),
        )
        .child(small(about, k))
}

fn frame(k: &Kit) -> El {
    col()
        .w_full()
        .radius(k.radius.md)
        .border(1.0, k.c.border_weak)
        .fill(k.c.bg_base)
        .py(4.0)
}

fn signatures(s: &App, sigs: &[Signature], wall: i64, k: &Kit) -> El {
    let body = col().key("cr-sigs").w_full().gap(10.0).child(heading(
        "cr-sigs-title",
        "Repeated crashes",
        "Crashes of one process with the same exception and top frame, most often first.",
        k,
    ));
    if sigs.is_empty() {
        return body.child(small("No crashes to group.", k).key("cr-sigs-none"));
    }
    let mut list = frame(k);
    for (i, x) in sigs.iter().enumerate() {
        let times = if x.count == 1 {
            "once".to_owned()
        } else {
            format!("{} times", fmt_count(x.count))
        };
        let mut summary = format!("{} · {times}", what(&x.exception, "crash"));
        if let Some(last) = x.last {
            summary.push_str(&format!(" · last {}", ago(wall - last)));
        }
        let detail = [x.exception.as_str(), x.top_frame.as_str()]
            .into_iter()
            .filter(|t| !t.is_empty())
            .collect::<Vec<_>>()
            .join(" · ");
        list = list.child(item(
            s,
            Section::Signatures,
            i,
            &x.file,
            &x.process,
            &summary,
            &detail,
            k,
        ));
    }
    body.child(list)
}

fn reports(s: &App, reports: &[Report], error: Option<&str>, wall: i64, k: &Kit) -> El {
    let body = col().key("cr-reps").w_full().gap(10.0).child(heading(
        "cr-reps-title",
        "Recent reports",
        "Each crash or hang report, newest first.",
        k,
    ));
    if let Some(why) = error {
        return body.child(failed(
            "cr-reps-error",
            &format!("Couldn't list the recent reports: {why}"),
            k,
        ));
    }
    if reports.is_empty() {
        return body.child(small("No crash or hang reports.", k).key("cr-reps-none"));
    }
    let mut list = frame(k);
    for (i, r) in reports.iter().enumerate() {
        let mut summary = what(&r.exception, &r.kind);
        if let Some(at) = r.at {
            summary.push_str(&format!(" · {}", ago(wall - at)));
        }
        list = list.child(item(
            s,
            Section::Reports,
            i,
            &r.file,
            &r.process,
            &summary,
            &r.exception,
            k,
        ));
    }
    body.child(list)
}

fn kills(s: &App, kills: &[MemoryKill], error: Option<&str>, wall: i64, k: &Kit) -> El {
    let body = col().key("cr-ooms").w_full().gap(10.0).child(heading(
        "cr-ooms-title",
        "Ended for using too much memory",
        "Processes that went over their memory limit, or were ended when the Mac ran low.",
        k,
    ));
    if let Some(why) = error {
        return body.child(failed(
            "cr-ooms-error",
            &format!("Couldn't list the memory kills: {why}"),
            k,
        ));
    }
    if kills.is_empty() {
        return body.child(small("None.", k).key("cr-ooms-none"));
    }
    let mut list = frame(k);
    for (i, m) in kills.iter().enumerate() {
        let mut summary = if m.reason.is_empty() {
            "used too much memory".to_owned()
        } else {
            m.reason.clone()
        };
        if let Some(at) = m.at {
            summary.push_str(&format!(" · {}", ago(wall - at)));
        }
        let detail = match (m.observed.as_str(), m.limit.as_str()) {
            ("", "") => String::new(),
            ("", l) => format!("limit {l}"),
            (o, "") => format!("used {o}"),
            (o, l) => format!("used {o}, limit {l}"),
        };
        list = list.child(item(
            s,
            Section::Kills,
            i,
            &m.file,
            &m.process,
            &summary,
            &detail,
            k,
        ));
    }
    body.child(list)
}

/// One row: the process, what happened and, faint, the detail. Pressing it picks it; a picked
/// row offers "Open report" and shows the report once opened.
#[allow(clippy::too_many_arguments)]
fn item(
    s: &App,
    section: Section,
    i: usize,
    file: &str,
    process: &str,
    summary: &str,
    detail: &str,
    k: &Kit,
) -> El {
    let c = &k.c;
    let key = format!("cr-{}-{i}", section.id());
    let picked = s
        .crashes
        .picked
        .as_ref()
        .is_some_and(|(sec, f)| *sec == section && f == file);
    let mut words = col().grow(1.0).min_w(0.0).gap(2.0).child(
        row()
            .gap(8.0)
            .child(
                text(process.to_owned())
                    .key(format!("{key}-name"))
                    .font(k.ty.small_medium)
                    .color(c.text_base)
                    .truncate()
                    .shrink()
                    .min_w(0.0),
            )
            .child(
                text(summary.to_owned())
                    .key(format!("{key}-what"))
                    .font(k.ty.small)
                    .color(c.text_muted)
                    .truncate()
                    .shrink()
                    .min_w(0.0),
            ),
    );
    if !detail.is_empty() {
        words = words.child(
            text(detail.to_owned())
                .font(k.ty.small)
                .color(c.text_faint)
                .truncate(),
        );
    }
    let mut line = row()
        .w_full()
        .gap(12.0)
        .center_y()
        .px(12.0)
        .py(6.0)
        .child(words);
    if picked {
        let opening = s.crashes.opening() == Some(file);
        line = line.child(if file.is_empty() {
            small("No report file to open.", k).key(format!("{key}-nofile"))
        } else {
            Button::new(if opening { "Opening…" } else { "Open report" })
                .variant(ButtonVariant::Outline)
                .disabled(opening)
                .help("Runs spectra crash inspect on this report")
                .on_press(|s: &mut App| s.open_crash_report())
                .build(k)
                .key(format!("{key}-open"))
        });
    }
    let pick = file.to_owned();
    let mut out = col()
        .key(key.clone())
        .w_full()
        .role("button")
        .label(format!("{process} {summary}"))
        .cursor(Cursor::Pointer)
        .on_press(move |s: &mut App| s.pick_crash(section, &pick))
        .child(if i > 0 {
            el().w_full().h(1.0).fill(c.border_weak)
        } else {
            el()
        })
        .child(line);
    if picked {
        out = out.fill(c.layer_01);
        match s.crashes.opened.get(file) {
            _ if s.crashes.opening() == Some(file) => {
                out = out.child(
                    row()
                        .key(format!("{key}-opening"))
                        .gap(8.0)
                        .center_y()
                        .px(12.0)
                        .pb(8.0)
                        .child(Spinner::new().size(12.0).build(k))
                        .child(small("Reading the report…", k)),
                );
            }
            Some(Ok(r)) => out = out.child(opened(&key, r, (s.wall)(), k)),
            Some(Err(why)) => {
                out = out.child(el().px(12.0).pb(8.0).child(failed(
                    &format!("{key}-failed"),
                    &format!("Couldn't open the report: {why}"),
                    k,
                )));
            }
            None => {}
        }
    }
    out
}

/// A decoded report: what happened, how it ended, and the crashed thread's top frames.
fn opened(key: &str, r: &Inspected, wall: i64, k: &Kit) -> El {
    let c = &k.c;
    let mut body = col()
        .key(format!("{key}-report"))
        .w_full()
        .gap(6.0)
        .px(12.0)
        .pb(10.0);
    let mut who = r.process.clone();
    if !r.version.is_empty() {
        who.push_str(&format!(" {}", r.version));
    }
    if let Some(pid) = r.pid {
        who.push_str(&format!(" (pid {pid})"));
    }
    let mut head = format!("{who} {}", what(&r.exception, &r.kind));
    if let Some(at) = r.at {
        head.push_str(&format!(" {}", ago(wall - at)));
    }
    if !r.os_version.is_empty() {
        head.push_str(&format!(", on {}", r.os_version));
    }
    head.push('.');
    body = body.child(
        text(head)
            .key(format!("{key}-report-head"))
            .font(k.ty.small_medium)
            .color(c.text_base)
            .wrap(),
    );
    let mut facts: Vec<(&str, String)> = Vec::new();
    if !r.exception.is_empty() {
        let mut e = r.exception.clone();
        if !r.detail.is_empty() {
            e.push_str(&format!(": {}", r.detail));
        }
        facts.push(("Exception", e));
    }
    if !r.termination.is_empty() {
        facts.push(("Reason", r.termination.clone()));
    }
    if let Some(res) = &r.resource {
        let mut l = res.explanation.clone();
        match (res.observed.as_str(), res.limit.as_str()) {
            ("", "") => {}
            (o, "") => l.push_str(&format!(" (used {o})")),
            ("", lim) => l.push_str(&format!(" (limit {lim})")),
            (o, lim) => l.push_str(&format!(" (used {o}, limit {lim})")),
        }
        facts.push(("Limit", l));
    }
    for (label, value) in facts {
        body = body.child(
            row()
                .w_full()
                .gap(8.0)
                .items_start()
                .child(
                    text(label.to_owned())
                        .w(72.0)
                        .font(k.ty.small)
                        .color(c.text_faint),
                )
                .child(
                    text(value)
                        .font(k.ty.small)
                        .color(c.text_base)
                        .selectable(true)
                        .selection_color(k.selection())
                        .wrap_anywhere()
                        .grow(1.0)
                        .min_w(0.0),
                ),
        );
    }
    let Some(t) = &r.crashed else {
        return body.child(
            small("The report doesn't say which thread crashed.", k)
                .key(format!("{key}-report-nothread")),
        );
    };
    let mut name = format!("Thread {}", t.index);
    for extra in [&t.name, &t.queue] {
        if !extra.is_empty() {
            name.push_str(&format!(" · {extra}"));
        }
    }
    body = body.child(
        text(format!("Crashed thread: {name}"))
            .key(format!("{key}-report-thread"))
            .font(k.ty.small_medium)
            .color(c.text_base),
    );
    let mut frames = col()
        .key(format!("{key}-report-frames"))
        .w_full()
        .gap(2.0)
        .padding(8.0)
        .radius(k.radius.sm)
        .fill(c.bg_base);
    for (n, f) in t.frames.iter().take(TOP_FRAMES).enumerate() {
        frames = frames.child(
            text(format!("{n:>2}  {f}"))
                .font(k.ty.mono)
                .color(c.text_base)
                .selectable(true)
                .selection_color(k.selection())
                .truncate(),
        );
    }
    if t.frames.is_empty() {
        frames = frames.child(small("No frames.", k));
    }
    body = body.child(frames);
    if t.frames.len() > TOP_FRAMES {
        body = body.child(small(
            format!(
                "And {} more frames; spectra crash inspect shows them all.",
                t.frames.len() - TOP_FRAMES
            ),
            k,
        ));
    }
    body
}

fn small(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}
