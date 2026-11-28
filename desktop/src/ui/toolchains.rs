//! The Toolchains page: the developer tooling installed on this Mac, in collapsible groups with
//! Java and the language runtimes first and Homebrew last. It is read when first opened and on
//! Refresh; a search narrows the rows by name.

use rune::{El, col, el, row, text};
use rune_kit::{Button, ButtonVariant, Collapsible, Kit, TextInput};

use super::home::callout;
use crate::app::{App, fmt_ago};
use crate::procs::fmt_count;
use crate::toolchains::{Inventory, Row, SHOWN, Section};

pub fn page(s: &App, k: &Kit) -> El {
    let intro = text(
        "What's installed on this Mac for building and running code: Java installs, language runtimes, build tools and Homebrew. Read when this page first opens and when you press Refresh, never in the background.",
    )
    .font(k.ty.small)
    .color(k.c.text_muted)
    .wrap();
    let t = &s.toolchains;
    let mut page = col()
        .key("tc-page")
        .w_full()
        .padding(24.0)
        .gap(16.0)
        .child(intro)
        .child(controls(s, k));
    if let Some(why) = &t.error {
        page = page.child(callout(
            "tc-error",
            if t.data.is_some() {
                "The last read failed; this is from before"
            } else {
                "Couldn't read the toolchains"
            },
            &format!(
                "{} Nothing was changed on your Mac. Press Refresh to try again.",
                sentence(why)
            ),
            k,
        ));
    }
    match &t.data {
        Some((inv, _)) => page.child(body(s, inv, k)),
        None if t.reading() => page.child(
            text("Reading the toolchains… Spectra asks Homebrew and each version manager, which can take several seconds.")
                .key("tc-reading")
                .role("status")
                .font(k.ty.small)
                .color(k.c.text_faint)
                .wrap(),
        ),
        None => page,
    }
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

fn small(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}

fn controls(s: &App, k: &Kit) -> El {
    let t = &s.toolchains;
    let search = TextInput::new(&t.query)
        .placeholder("Search by name, version or source")
        .label("Search toolchains")
        .leading_icon(k.glyph.search)
        .clear_button()
        .width(Some(300.0))
        .on_edit(|s: &mut App| &mut s.toolchains.query)
        .build(k)
        .key("tc-search");
    let refresh = Button::new("Refresh")
        .variant(ButtonVariant::Outline)
        .disabled(t.reading())
        .help("Runs spectra toolchain --json again")
        .on_press(|s: &mut App| s.refresh_toolchains())
        .build(k)
        .key("tc-refresh");
    let mut r = row()
        .key("tc-controls")
        .w_full()
        .gap(12.0)
        .center_y()
        .child(search)
        .child(refresh);
    if t.reading() && t.data.is_some() {
        r = r.child(
            text("Reading…")
                .key("tc-busy")
                .role("status")
                .font(k.ty.small)
                .color(k.c.text_muted),
        );
    }
    r = r.child(el().grow(1.0));
    if let Some((inv, at)) = &t.data {
        let q = t.search();
        let shown: usize = inv.sections.iter().map(|x| x.matching(&q).len()).sum();
        let count = if q.is_empty() {
            format!("{} installed", fmt_count(inv.total() as u64))
        } else {
            format!(
                "{} of {}",
                fmt_count(shown as u64),
                fmt_count(inv.total() as u64)
            )
        };
        r = r
            .child(
                text(count)
                    .key("tc-count")
                    .font(k.ty.small)
                    .color(k.c.text_muted),
            )
            .child(
                text(format!("Read {} ago", fmt_ago(s.now - at)))
                    .key("tc-read-at")
                    .font(k.ty.small)
                    .color(k.c.text_faint),
            );
    }
    r
}

fn body(s: &App, inv: &Inventory, k: &Kit) -> El {
    let t = &s.toolchains;
    let q = t.search();
    let mut b = col().key("tc-body").w_full().gap(12.0);
    if inv.sections.is_empty() {
        return b.child(
            small(
                "Spectra found no Java installs, language runtimes, build tools or Homebrew packages on this Mac.",
                k,
            )
            .key("tc-empty"),
        );
    }
    let flags = inv.flags();
    if !flags.is_empty() {
        b = b.child(flags_box(&flags, k));
    }
    let mut any = false;
    for sec in &inv.sections {
        let rows = sec.matching(&q);
        if rows.is_empty() {
            continue;
        }
        any = true;
        b = b.child(group(s, sec, &rows, &q, k));
    }
    if !any {
        b = b.child(
            small(
                format!(
                    "Nothing installed matches \"{}\". Clear the search to see everything.",
                    t.query.text.trim()
                ),
                k,
            )
            .key("tc-no-match"),
        );
    }
    b
}

fn panel(key: &str, k: &Kit) -> El {
    col()
        .key(key.to_owned())
        .w_full()
        .gap(6.0)
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(k.c.layer_01)
        .border(1.0, k.c.border_weak)
}

fn flags_box(flags: &[String], k: &Kit) -> El {
    let mut p = panel("tc-flags", k).child(
        text("Worth a look")
            .font(k.ty.ui_medium)
            .color(k.c.warning.fg),
    );
    for (i, f) in flags.iter().enumerate() {
        p = p.child(
            text(f.clone())
                .key(format!("tc-flag-{i}"))
                .font(k.ty.small)
                .color(k.c.text_base)
                .wrap(),
        );
    }
    p
}

fn group(s: &App, sec: &Section, rows: &[&Row], q: &str, k: &Kit) -> El {
    let t = &s.toolchains;
    let title = if q.is_empty() {
        format!("{} ({})", sec.title, fmt_count(sec.rows.len() as u64))
    } else {
        format!(
            "{} ({} of {})",
            sec.title,
            fmt_count(rows.len() as u64),
            fmt_count(sec.rows.len() as u64)
        )
    };
    let id = sec.id.clone();
    let mut list = col().w_full().gap(2.0).pb(8.0);
    for (i, r) in rows.iter().take(SHOWN).enumerate() {
        list = list.child(entry(&sec.id, i, r, k));
    }
    if rows.len() > SHOWN {
        list = list.child(
            small(
                format!(
                    "Showing the first {} of {}. Search to narrow the list.",
                    fmt_count(SHOWN as u64),
                    fmt_count(rows.len() as u64)
                ),
                k,
            )
            .key(format!("tc-more-{}", sec.id)),
        );
    }
    col().key(format!("tc-sec-{}", sec.id)).w_full().child(
        Collapsible::new(title, t.is_open(sec))
            .on_toggle(move |s: &mut App| s.toggle_toolchain_section(&id))
            .child(list)
            .build(k),
    )
}

fn entry(sec: &str, i: usize, r: &Row, k: &Kit) -> El {
    let key = format!("tc-row-{sec}-{i}");
    let mut line = row()
        .key(key.clone())
        .w_full()
        .gap(12.0)
        .pl(32.0)
        .center_y()
        .child(
            text(r.name.clone())
                .key(format!("{key}-name"))
                .font(k.ty.small_medium)
                .color(k.c.text_base)
                .w(230.0)
                .truncate(),
        )
        .child(
            text(r.version.clone())
                .font(k.ty.small.features("'tnum' 1"))
                .color(k.c.text_base)
                .w(120.0)
                .truncate(),
        )
        .child(
            text(r.source.clone())
                .font(k.ty.small)
                .color(k.c.text_muted)
                .w(70.0)
                .truncate(),
        );
    if !r.marks.is_empty() {
        line = line.child(
            text(r.marks.join(", "))
                .key(format!("{key}-marks"))
                .font(k.ty.small_medium)
                .color(if r.marks.iter().any(|m| m == "deprecated") {
                    k.c.warning.fg
                } else {
                    k.c.success.fg
                }),
        );
    }
    line.child(
        text(r.path.clone())
            .font(k.ty.small)
            .color(k.c.text_faint)
            .truncate()
            .grow(1.0)
            .min_w(0.0),
    )
}
