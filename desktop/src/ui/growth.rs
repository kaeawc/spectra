//! The Memory and alerts page: processes whose memory jumped past its usual level, and the
//! alert history Spectra's daemon keeps, with an Acknowledge button for each alert still going.

use rune::{Cursor, El, col, row, text};
use rune_kit::{Button, ButtonVariant, Kit};

use super::card::dot;
use super::home::callout;
use crate::app::{App, fmt_ago};
use crate::findings::Severity;
use crate::growth::{Alert, MAX_JUMPS, Note, how_far};
use crate::procs::{fmt_count, fmt_mem};

pub fn page(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let g = &s.growth;
    let refresh = Button::new(if g.running() {
        "Checking…"
    } else {
        "Check now"
    })
    .variant(ButtonVariant::Outline)
    .disabled(g.running())
    .help("Ask Spectra again")
    .on_press(|s: &mut App| s.growth_refresh())
    .build(k)
    .key("gr-refresh");
    let mut head = row().w_full().gap(12.0).center_y().child(
        text(
            "Memory that jumped past its usual level, and the alerts Spectra's daemon has raised.",
        )
        .font(k.ty.small)
        .color(c.text_muted)
        .wrap()
        .grow(1.0)
        .min_w(0.0),
    );
    if let Some(age) = s.growth_age() {
        head = head.child(
            text(age)
                .key("gr-age")
                .role("status")
                .font(k.ty.small)
                .color(c.text_faint),
        );
    }
    col()
        .key("gr-page")
        .w_full()
        .padding(24.0)
        .gap(20.0)
        .child(head.child(refresh))
        .child(jumps(s, k))
        .child(alerts(s, k))
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
        .child(
            text(about.to_owned())
                .font(k.ty.small)
                .color(k.c.text_muted)
                .wrap(),
        )
}

fn say(key: &str, words: String, k: &Kit) -> El {
    text(words)
        .key(key.to_owned())
        .font(k.ty.small)
        .color(k.c.text_muted)
        .wrap()
}

/// "Memory jumps": a row for each process well above its usual memory.
fn jumps(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let g = &s.growth;
    let mut sec = col().key("gr-jumps").w_full().gap(8.0).child(heading(
        "gr-jumps-title",
        "Memory jumps",
        "Processes using well more memory than usual, against Spectra's recent baseline for each. It catches sudden jumps; a slow climb isn't flagged.",
        k,
    ));
    let rows = s.jump_rows();
    if let Some(Note::Failed(why)) = &g.jumps_note {
        sec = sec.child(callout(
            "gr-jumps-error",
            if g.jumps.is_some() {
                "The last memory check failed; these rows are from before"
            } else {
                "Couldn't check memory"
            },
            why,
            k,
        ));
    }
    if g.jumps.is_none() && g.jumps_note.is_none() {
        return sec.child(say(
            "gr-jumps-checking",
            "Checking memory against its usual levels…".into(),
            k,
        ));
    }
    if g.jumps_note == Some(Note::NotEnough) {
        return sec.child(say(
            "gr-jumps-history",
            "Spectra hasn't stored enough history yet. Jumps show once it has watched processes for a few minutes.".into(),
            k,
        ));
    }
    if g.jumps.is_none() {
        return sec;
    }
    if rows.is_empty() {
        return sec.child(say(
            "gr-jumps-empty",
            "Nothing is jumping past its usual memory.".into(),
            k,
        ));
    }
    for (i, j) in rows.iter().take(MAX_JUMPS).enumerate() {
        let key = format!("gr-jump-{i}");
        let pid = j.pid;
        let link = text(format!("{} · {}", j.name, j.pid))
            .key(format!("{key}-open"))
            .role("link")
            .label(format!("Open {}", j.name))
            .cursor(Cursor::Pointer)
            .font(k.ty.ui_medium)
            .color(c.text_accent)
            .on_press(move |s: &mut App| s.open(pid));
        sec = sec.child(
            row()
                .key(key.clone())
                .w_full()
                .gap(10.0)
                .center_y()
                .py(10.0)
                .px(16.0)
                .radius(k.radius.md)
                .fill(c.layer_01)
                .border(1.0, c.border_weak)
                .child(
                    col()
                        .grow(1.0)
                        .shrink()
                        .min_w(0.0)
                        .gap(2.0)
                        .child(link)
                        .child(
                            text(format!(
                                "now {}, usually {}",
                                fmt_mem(j.now_kib),
                                fmt_mem(j.usual_kib)
                            ))
                            .key(format!("{key}-memory"))
                            .font(k.ty.small)
                            .color(c.text_base),
                        ),
                )
                .child(
                    text(how_far(j.z))
                        .key(format!("{key}-how"))
                        .font(k.ty.small)
                        .color(c.text_muted),
                ),
        );
    }
    if rows.len() > MAX_JUMPS {
        sec = sec.child(say(
            "gr-jumps-more",
            format!("And {} more.", fmt_count((rows.len() - MAX_JUMPS) as u64)),
            k,
        ));
    }
    sec
}

/// How long ago `at` (an RFC 3339 time) was, as of the page's wall clock.
fn ago(s: &App, at: Option<&str>) -> Option<String> {
    let t = crate::procs::parse_rfc3339(at?)?;
    Some(fmt_ago(((s.wall)() - t) as f64))
}

fn alert_row(s: &App, i: usize, a: &Alert, k: &Kit) -> El {
    let c = &k.c;
    let key = format!("gr-alert-{i}");
    let active = a.active();
    let mut when = Vec::new();
    if let Some(t) = ago(s, Some(&a.fired_at)) {
        when.push(format!("fired {t} ago"));
    }
    let status = if !active {
        if let Some(t) = ago(s, a.resolved_at.as_deref()) {
            when.push(format!("cleared {t} ago"));
        }
        "Cleared"
    } else if a.acknowledged() {
        "Acknowledged"
    } else {
        "Active"
    };
    let mut words = col().grow(1.0).shrink().min_w(0.0).gap(2.0).child(
        text(a.title.clone())
            .key(format!("{key}-title"))
            .font(k.ty.ui_medium)
            .color(c.text_base),
    );
    let detail = s.alert_detail(a);
    if !detail.is_empty() {
        words = words.child(
            text(detail)
                .key(format!("{key}-detail"))
                .font(k.ty.small)
                .color(c.text_muted)
                .wrap(),
        );
    }
    if !when.is_empty() {
        words = words.child(
            text(when.join(" · "))
                .key(format!("{key}-when"))
                .font(k.ty.small)
                .color(c.text_faint),
        );
    }
    // A cleared alert is dimmed to a plain dot; one still going keeps its severity.
    let mark = dot(
        if active {
            a.severity()
        } else {
            Severity::Healthy
        },
        k,
    );
    let mut r = row()
        .key(key.clone())
        .w_full()
        .gap(10.0)
        .center_y()
        .py(10.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .child(mark)
        .child(words)
        .child(
            text(status)
                .key(format!("{key}-status"))
                .font(k.ty.small)
                .color(if active { c.text_base } else { c.text_muted }),
        );
    if active && !a.acknowledged() && !a.id.is_empty() {
        let id = a.id.clone();
        r = r.child(
            Button::new("Acknowledge")
                .variant(ButtonVariant::Outline)
                .disabled(s.growth.acking.is_some())
                .help("Tell Spectra's daemon you've seen this alert")
                .on_press(move |s: &mut App| s.ack_alert(&id))
                .build(k)
                .key(format!("{key}-ack")),
        );
    }
    r
}

/// "Alerts": the daemon's history, newest first.
fn alerts(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let g = &s.growth;
    let mut sec = col().key("gr-alerts").w_full().gap(8.0).child(heading(
        "gr-alerts-title",
        "Alerts",
        "What Spectra's daemon has flagged, newest first. Acknowledging tells it you've seen an alert; it doesn't clear it.",
        k,
    ));
    if let Some(o) = &g.outcome {
        sec = sec.child(
            row()
                .key("gr-outcome")
                .role("status")
                .w_full()
                .py(6.0)
                .px(12.0)
                .radius(k.radius.md)
                .fill(c.layer_01)
                .border(1.0, c.border_base)
                .child(
                    text(o.line())
                        .key("gr-outcome-text")
                        .font(k.ty.small)
                        .color(c.text_base)
                        .wrap()
                        .grow(1.0)
                        .min_w(0.0),
                ),
        );
    }
    if let Some(why) = &g.alerts_error {
        sec = sec.child(callout(
            "gr-alerts-error",
            if g.alerts.is_some() {
                "The last read of the alerts failed; this is the list from before"
            } else {
                "Spectra's daemon isn't available, so alerts can't be read"
            },
            why,
            k,
        ));
    }
    let Some((list, _)) = &g.alerts else {
        if g.alerts_error.is_none() {
            sec = sec.child(say("gr-alerts-checking", "Reading the alerts…".into(), k));
        }
        return sec;
    };
    if list.is_empty() {
        return sec.child(say(
            "gr-alerts-empty",
            "Spectra hasn't raised any alerts.".into(),
            k,
        ));
    }
    for (i, a) in list.iter().enumerate() {
        sec = sec.child(alert_row(s, i, a, k));
    }
    sec
}
