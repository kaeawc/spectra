//! The Compare page: what's different from when it was fine. The snapshots Spectra has stored,
//! a choice of an earlier one and a later one (or now), and the differences in plain words, area
//! by area. A snapshot is taken only on a press, and the page says where it went.

use rune::{El, col, el, row, text};
use rune_kit::{Button, ButtonVariant, Kit};

use super::detail::section;
use super::home::callout;
use crate::app::{App, fmt_ago};
use crate::compare::{Compare, Group, Kind, SHOWN, STORE, Stored, groups};
use crate::procs::fmt_count;

pub fn page(s: &App, k: &Kit) -> El {
    let intro = text(
        "What's different from when it was fine? Pick an earlier snapshot and Spectra shows what changed since: apps, Java and Homebrew, listening ports and more. Comparing with now takes a minute, because Spectra looks over the whole Mac first.",
    )
    .font(k.ty.small)
    .color(k.c.text_muted)
    .wrap();
    let c = &s.compare;
    let mut page = col()
        .key("cmp-page")
        .w_full()
        .padding(24.0)
        .gap(24.0)
        .child(intro)
        .child(section("Take a snapshot", take(s, k), k))
        .child(section("Stored snapshots", stored(s, k), k));
    if c.diffing.is_some() || c.diff_error.is_some() || c.shown.is_some() {
        page = page.child(section("What changed", result(s, k), k));
    }
    page
}

fn panel(key: &str, k: &Kit) -> El {
    col()
        .key(key.to_owned())
        .w_full()
        .gap(10.0)
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(k.c.layer_01)
        .border(1.0, k.c.border_weak)
}

fn small(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}

/// Take a snapshot, or a baseline that is kept; what it's doing, and where the last one went.
fn take(s: &App, k: &Kit) -> El {
    let c = &s.compare;
    let busy = c.taking.is_some();
    let now = Button::new("Take a snapshot now")
        .variant(ButtonVariant::Outline)
        .disabled(busy)
        .help("Spectra looks over this Mac and stores what it finds, to compare with later")
        .on_press(|s: &mut App| s.take_snapshot(false))
        .build(k)
        .key("cmp-take");
    let baseline = Button::new("Take a baseline")
        .variant(ButtonVariant::Ghost)
        .disabled(busy)
        .help("Like a snapshot, but kept for good: Spectra never prunes a baseline")
        .on_press(|s: &mut App| s.take_snapshot(true))
        .build(k)
        .key("cmp-take-baseline");
    let mut body = col()
        .key("cmp-take-box")
        .w_full()
        .gap(10.0)
        .child(
            row()
                .w_full()
                .gap(12.0)
                .center_y()
                .child(now)
                .child(baseline)
                .child(el().grow(1.0)),
        )
        .child(
            small(
                format!(
                    "Spectra writes it to its own store, {STORE}. Nothing is written until you press. Spectra keeps the newest 100 snapshots; a baseline stays."
                ),
                k,
            )
            .key("cmp-take-note"),
        );
    if let Some(g) = &c.taking {
        body = body.child(
            text(format!(
                "Taking {}… started {} ago. Spectra inspects every app, which can take a minute or more.",
                if g.baseline { "a baseline" } else { "a snapshot" },
                fmt_ago(s.now - g.started)
            ))
            .key("cmp-taking")
            .role("status")
            .font(k.ty.small)
            .color(k.c.text_muted)
            .wrap(),
        );
    }
    match &c.taken {
        Some(Ok(t)) => {
            body = body.child(
                text(format!(
                    "Saved {} {} in Spectra's store, {STORE}. It's in the list below.",
                    if t.baseline { "baseline" } else { "snapshot" },
                    t.id
                ))
                .key("cmp-taken")
                .role("status")
                .font(k.ty.small)
                .color(k.c.success.fg)
                .wrap(),
            );
        }
        Some(Err(why)) => {
            body = body.child(callout(
                "cmp-take-error",
                "Couldn't take a snapshot",
                &format!("{} Nothing was changed on your Mac.", sentence(why)),
                k,
            ));
        }
        None => {}
    }
    body
}

/// `why` ending in a full stop.
fn sentence(why: &str) -> String {
    let w = why.trim();
    if w.ends_with('.') {
        w.to_owned()
    } else {
        format!("{w}.")
    }
}

/// The list, with From and To on each row and the Compare button.
fn stored(s: &App, k: &Kit) -> El {
    let c = &s.compare;
    let mut body = col().key("cmp-stored").w_full().gap(10.0);
    if let Some(why) = &c.list_error {
        body = body.child(callout(
            "cmp-list-error",
            if c.list.is_some() {
                "The last read failed; this is from before"
            } else {
                "Couldn't read the stored snapshots"
            },
            &sentence(why),
            k,
        ));
    }
    let Some(list) = &c.list else {
        return match (&c.list_error, c.reading) {
            (None, true) => body.child(
                text("Reading the stored snapshots…")
                    .key("cmp-reading")
                    .font(k.ty.small)
                    .color(k.c.text_faint),
            ),
            (None, false) => body.child(small("The snapshots haven't been read yet.", k)),
            _ => body,
        };
    };
    if list.is_empty() {
        return body.child(
            small(
                "No snapshots are stored yet. Take one now while things are fine, and come back to compare it with later.",
                k,
            )
            .key("cmp-empty"),
        );
    }
    body = body.child(chosen(c, k));
    let mut rows = panel("cmp-list", k);
    for st in list.iter().take(crate::compare::LISTED) {
        rows = rows.child(entry(c, st, k));
    }
    body = body.child(rows);
    if list.len() > crate::compare::LISTED {
        body = body.child(
            small(
                format!(
                    "Showing the newest {} of {} stored snapshots.",
                    crate::compare::LISTED,
                    fmt_count(list.len() as u64)
                ),
                k,
            )
            .key("cmp-more"),
        );
    }
    body
}

/// "From … to …" and the Compare button.
fn chosen(c: &Compare, k: &Kit) -> El {
    let to = c.to.as_deref().map_or("now".to_owned(), |id| c.name_of(id));
    let summary = match c.from.as_deref() {
        Some(id) => format!("From {} to {to}", c.name_of(id)),
        None => "Pick an earlier snapshot below with From, then Compare.".to_owned(),
    };
    let go = Button::new("Compare")
        .variant(ButtonVariant::Submit)
        .disabled(!c.can_compare())
        .help("Ask Spectra what differs between the two")
        .on_press(|s: &mut App| s.run_compare())
        .build(k)
        .key("cmp-go");
    let now = Button::new("Now")
        .variant(if c.to.is_none() {
            ButtonVariant::Neutral
        } else {
            ButtonVariant::Ghost
        })
        .help("Compare against the Mac as it is now")
        .on_press(|s: &mut App| s.pick_to(None))
        .build(k)
        .key("cmp-to-now");
    row()
        .key("cmp-chosen")
        .w_full()
        .gap(12.0)
        .center_y()
        .child(
            text(summary)
                .key("cmp-chosen-text")
                .font(k.ty.small_medium)
                .color(k.c.text_base)
                .truncate()
                .shrink()
                .min_w(0.0),
        )
        .child(el().grow(1.0))
        .child(now)
        .child(go)
}

/// One stored snapshot: when, what it holds, and From and To buttons.
fn entry(c: &Compare, st: &Stored, k: &Kit) -> El {
    let id = st.id.clone();
    let is_from = c.from.as_deref() == Some(&st.id);
    let is_to = c.to.as_deref() == Some(&st.id);
    let kind = if st.baseline { "Baseline" } else { "Snapshot" };
    let detail = format!(
        "{kind} · {} app{}{}",
        fmt_count(st.apps),
        if st.apps == 1 { "" } else { "s" },
        if st.name.is_empty() {
            String::new()
        } else {
            format!(" · {}", st.name)
        }
    );
    let pick = |label: &str, on: bool, key: String, help: &str, f: Box<dyn Fn(&mut App)>| {
        Button::new(label)
            .variant(if on {
                ButtonVariant::Neutral
            } else {
                ButtonVariant::Ghost
            })
            .help(help.to_owned())
            .on_press(move |s: &mut App| f(s))
            .build(k)
            .key(key)
    };
    let (from_id, to_id) = (id.clone(), id.clone());
    let from = pick(
        "From",
        is_from,
        format!("cmp-from-{id}"),
        "Use this as the earlier snapshot",
        Box::new(move |s| s.pick_from(&from_id)),
    );
    let to = pick(
        "To",
        is_to,
        format!("cmp-to-{id}"),
        "Compare against this one instead of now",
        Box::new(move |s| s.pick_to(Some(&to_id))),
    );
    row()
        .key(format!("cmp-row-{id}"))
        .w_full()
        .gap(12.0)
        .center_y()
        .child(
            col()
                .gap(2.0)
                .shrink()
                .min_w(0.0)
                .child(
                    text(st.when.clone())
                        .font(k.ty.small.features("'tnum' 1"))
                        .color(k.c.text_base)
                        .truncate(),
                )
                .child(
                    text(detail)
                        .font(k.ty.small)
                        .color(k.c.text_muted)
                        .truncate(),
                ),
        )
        .child(el().grow(1.0))
        .child(from)
        .child(to)
}

/// A comparison on its way, why one failed, and the differences it found.
fn result(s: &App, k: &Kit) -> El {
    let c = &s.compare;
    let mut body = col().key("cmp-result").w_full().gap(12.0);
    if let Some(g) = &c.diffing {
        let stop = Button::new("Stop")
            .variant(ButtonVariant::Outline)
            .on_press(|s: &mut App| s.stop_compare())
            .build(k)
            .key("cmp-stop");
        body = body.child(
            row()
                .key("cmp-diffing")
                .w_full()
                .gap(12.0)
                .center_y()
                .child(
                    text(format!(
                        "Comparing… started {} ago. {}",
                        fmt_ago(s.now - g.started),
                        if c.to.is_none() {
                            "Against now, Spectra inspects every app first."
                        } else {
                            ""
                        }
                    ))
                    .key("cmp-progress")
                    .role("status")
                    .font(k.ty.small)
                    .color(k.c.text_muted)
                    .wrap()
                    .grow(1.0)
                    .min_w(0.0),
                )
                .child(stop),
        );
    }
    if let Some(why) = &c.diff_error {
        let title = if c.shown.is_some() {
            "Couldn't compare; this is the comparison from before"
        } else {
            "Couldn't compare"
        };
        body = body.child(callout("cmp-diff-error", title, &sentence(why), k));
    }
    let Some(shown) = &c.shown else { return body };
    let diff = &shown.diff;
    let gs = groups(diff);
    let to = shown
        .to
        .as_deref()
        .map_or("now".to_owned(), |id| c.name_of(id));
    let from = c.name_of(&shown.from);
    let headline = if gs.is_empty() {
        format!("Nothing differs between {from} and {to}.")
    } else {
        format!(
            "{} difference{} in {} area{}, {from} to {to}.",
            fmt_count(diff.total() as u64),
            if diff.total() == 1 { "" } else { "s" },
            gs.len(),
            if gs.len() == 1 { "" } else { "s" }
        )
    };
    body = body.child(
        text(headline)
            .key("cmp-headline")
            .font(k.ty.ui_medium)
            .color(k.c.text_base)
            .wrap(),
    );
    for g in &gs {
        body = body.child(group(c, g, k));
    }
    body.child(
        small(
            format!(
                "Spectra's comparison covers the Mac itself, apps, Java, Homebrew, language versions, listening ports, VPN, PATH, resource limits, kernel settings and install history. It doesn't include login items or background services; the Startup page shows those as they are now. Compared {} ago.",
                fmt_ago(s.now - shown.at)
            ),
            k,
        )
        .key("cmp-scope"),
    )
}

fn group(c: &Compare, g: &Group, k: &Kit) -> El {
    let open = c.expanded.contains(&g.id);
    let key = format!("cmp-g-{}", g.id);
    let mut p = panel(&key, k).child(
        row()
            .w_full()
            .gap(12.0)
            .center_y()
            .child(
                text(g.title.clone())
                    .key(format!("{key}-title"))
                    .font(k.ty.ui_medium)
                    .color(k.c.text_base),
            )
            .child(el().grow(1.0))
            .child(
                text(g.summary.clone())
                    .key(format!("{key}-summary"))
                    .font(k.ty.small)
                    .color(k.c.text_muted),
            ),
    );
    let shown = if open { g.lines.len() } else { SHOWN };
    for (i, l) in g.lines.iter().take(shown).enumerate() {
        let (mark, ink) = match l.kind {
            Kind::Added => ("+", k.c.success.fg),
            Kind::Removed => ("−", k.c.danger.fg),
            Kind::Changed => ("~", k.c.warning.fg),
        };
        p = p.child(
            row()
                .key(format!("{key}-{i}"))
                .w_full()
                .gap(10.0)
                .child(text(mark).font(k.ty.small_medium).color(ink).w(12.0))
                .child(
                    text(l.text.clone())
                        .key(format!("{key}-{i}-text"))
                        .font(k.ty.small)
                        .color(k.c.text_base)
                        .wrap()
                        .grow(1.0)
                        .min_w(0.0),
                ),
        );
    }
    if g.lines.len() > SHOWN {
        let id = g.id.clone();
        let label = if open {
            "Show fewer".to_owned()
        } else {
            format!("Show all {}", fmt_count(g.lines.len() as u64))
        };
        p = p.child(
            Button::new(label)
                .variant(ButtonVariant::Ghost)
                .on_press(move |s: &mut App| s.toggle_compare_group(&id))
                .build(k)
                .key(format!("{key}-toggle")),
        );
    }
    p
}
