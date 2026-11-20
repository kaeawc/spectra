//! The process list: a search field over a virtualised table of groups (an app with its
//! helpers, several of one program) that open onto their processes.

use std::borrow::Cow;

use rune::{Align, Cursor, El, col, el, icon, row, text};
use rune_kit::{Cell, Kit, Table, TableColumn, TextInput};

use super::card::notice;
use crate::app::App;
use crate::groups::{GroupKind, Nav, RowRef};
use crate::procs::{Trust, fmt_count, fmt_cpu, fmt_mem};

/// A signature as a word and a shape, so it reads without colour: a checked one plain with a
/// check, one only as displayed plain with a dash and "not verified", unidentified ones
/// outlined, an invalid one outlined with a cross.
pub fn sign_badge(trust: Trust, k: &Kit) -> El {
    let c = &k.c;
    let font = k.ty.text_xsmall_medium;
    let label = trust.label();
    let pill = |ink, mark: Option<&'static rune::Icon>| {
        let mut r = row()
            .gap(3.0)
            .center_y()
            .px(6.0)
            .h(18.0)
            .radius(9.0)
            .border(1.0, ink);
        if let Some(m) = mark {
            r = r.child(icon(m, 11.0).color(ink));
        }
        r.child(text(label.clone()).font(font).color(ink))
    };
    let plain = |mark: &'static rune::Icon| {
        row()
            .gap(3.0)
            .center_y()
            .min_w(0.0)
            .shrink()
            .child(icon(mark, 12.0).color(c.icon_faint))
            .child(
                text(label.clone())
                    .font(k.ty.text_xsmall)
                    .color(c.text_muted)
                    .truncate()
                    .shrink()
                    .min_w(0.0)
                    .help_when_truncated(label.clone()),
            )
    };
    let badge = match trust {
        Trust::Verified(_) => plain(k.glyph.check_small),
        Trust::Unverified(_) => plain(k.glyph.dash),
        Trust::Adhoc | Trust::Unsigned => pill(c.warning.fg, None),
        Trust::Invalid => pill(c.danger.fg, Some(k.glyph.circle_x)),
        Trust::Unknown => row().child(text("Unknown").font(k.ty.text_xsmall).color(c.text_faint)),
    };
    badge.label(format!("Signature: {label}"))
}

/// A number, right-aligned in its column with figures of one width.
fn number(v: String, k: &Kit) -> Cell {
    Cell::el(
        text(v)
            .font(k.ty.text_small.features("'tnum' 1"))
            .color(k.c.text_base),
    )
}

/// What a name cell shows beside the name.
struct Name {
    name: String,
    /// The folder that tells it from a same-named row.
    hint: Option<String>,
    kind: String,
    depth: u8,
    /// A group's id and whether it's open; pressing the triangle opens or closes it.
    group: Option<(String, bool)>,
}

/// The name column: an open/closed triangle for a group, the name, where it runs from when
/// another row shares the name, and a faint runtime and role.
fn name_cell(n: Name, k: &Kit) -> Cell {
    let c = &k.c;
    let mark = match n.group {
        Some((id, open)) => {
            let glyph = if open { k.glyph.triangle_down } else { k.glyph.triangle_right };
            row()
                .key(format!("twisty-{id}"))
                .role("button")
                .label(format!("{} {}", if open { "Close" } else { "Open" }, n.name))
                .cursor(Cursor::Pointer)
                .size(18.0, 18.0)
                .center()
                .child(icon(glyph, 10.0).color(c.icon_muted))
                .on_press(move |s: &mut App| s.toggle_group(&id))
        }
        None => el().size(18.0, 18.0),
    };
    let mut r = row()
        .gap(6.0)
        .center_y()
        .min_w(0.0)
        .shrink()
        .pl(f32::from(n.depth) * 18.0)
        .child(mark)
        .child(
            text(n.name.clone())
                .font(k.ty.text_small)
                .color(c.text_base)
                .truncate()
                .shrink()
                .min_w(0.0)
                .help_when_truncated(n.name),
        );
    if let Some(hint) = n.hint {
        r = r.child(
            text(hint.clone())
                .font(k.ty.text_xsmall)
                .color(c.text_muted)
                .truncate()
                .shrink()
                .min_w(0.0)
                .max_w(150.0)
                .help_when_truncated(hint),
        );
    }
    // Capped, so a long name keeps most of the room.
    Cell::el(
        r.child(
            text(n.kind)
                .font(k.ty.text_xsmall)
                .color(c.text_faint)
                .truncate()
                .shrink()
                .min_w(0.0)
                .max_w(110.0),
        ),
    )
}

fn cell(s: &App, i: usize, column: &str, k: &Kit) -> Cell {
    let Some(r) = s.rows.get(i) else {
        return Cell::text("");
    };
    match r.item {
        RowRef::Group(g) => {
            let g = &s.groups[g];
            match column {
                "name" => {
                    let kind = match g.kind {
                        GroupKind::App => format!("{} · {}", g.runtime.label(), g.role.label()),
                        _ => g.runtime.label().to_owned(),
                    };
                    let open = s.open_groups.contains(&g.id);
                    name_cell(
                        Name { name: g.name.clone(), hint: g.hint.clone(), kind, depth: 0, group: Some((g.id.clone(), open)) },
                        k,
                    )
                }
                "sign" => Cell::el(sign_badge(g.signing, k)),
                "cpu" => number(fmt_cpu(g.cpu), k),
                "mem" => number(fmt_mem(g.rss_kib), k),
                "threads" => number(fmt_count(g.threads), k),
                "count" => number(fmt_count(g.pids.len() as u64), k),
                _ => Cell::text(""),
            }
        }
        RowRef::Process(pid) => {
            let Some(p) = s.process(pid) else {
                return Cell::text("");
            };
            match column {
                "name" => {
                    // Under its group the runtime is the group's; the role tells helpers apart.
                    let kind = if r.depth > 0 {
                        p.role.label().to_owned()
                    } else {
                        format!("{} · {}", p.runtime.label(), p.role.label())
                    };
                    // A process on its own row speaks for its group, hint and all.
                    let hint = if r.depth == 0 { s.groups.get(r.group).and_then(|g| g.hint.clone()) } else { None };
                    name_cell(Name { name: p.name().into_owned(), hint, kind, depth: r.depth, group: None }, k)
                }
                "sign" => Cell::el(sign_badge(p.trust(), k)),
                "cpu" => number(fmt_cpu(p.cpu_pct), k),
                "mem" => number(fmt_mem(p.rss_kib), k),
                "threads" => number(fmt_count(p.thread_count), k),
                "pid" => Cell::text(pid.to_string()).muted(),
                _ => Cell::text(""),
            }
        }
    }
}

fn row_id(s: &App, i: usize) -> Cow<'_, str> {
    Cow::Borrowed(s.rows[i].id.as_str())
}

fn table(s: &App, k: &Kit) -> El {
    let ck = k.clone();
    let num = |id: &str, title: &str, w: f32| TableColumn::new(id, title).width(w).min(w - 12.0).max(w + 60.0).align(Align::End);
    let mut t = Table::new(|s: &App| &s.table, |s: &mut App| &mut s.table, |s: &App| s.rows.len(), row_id)
        .name("Processes")
        .column(TableColumn::new("name", "Name").flex(1.0).min(180.0).sortable());
    // Linux has no code signatures to show.
    if s.stats.signed {
        t = t.column(TableColumn::new("sign", "Signature").width(184.0).min(110.0).max(260.0));
    }
    // Moving the cursor (a click, an arrow key) only moves it; Return or a double click opens.
    t.column(num("cpu", "CPU", 72.0).sortable())
        .column(num("mem", "Memory", 80.0).sortable())
        .column(num("threads", "Threads", 74.0))
        .column(num("count", "Processes", 90.0))
        .column(num("pid", "PID", 64.0))
        .cell(move |s: &App, i, column| cell(s, i, column, &ck))
        .on_sort(|s: &mut App, column, dir, _| s.sort_by(column, dir))
        .on_open(|s: &mut App, id, _| s.open_row(id))
        .build(s, k)
        .grow(1.0)
        .min_h(0.0)
}

pub fn list(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let page = col().key("list").w_full().h_full().min_h(0.0).padding(16.0).gap(12.0);
    if let Some(n) = super::home::feed_notice(s, k) {
        return page.child(n);
    }
    let shown: usize = s.groups.iter().map(|g| g.pids.len()).sum();
    let what = if s.nav == Nav::All { "processes".to_owned() } else { format!("of {} processes", fmt_count(s.procs.len() as u64)) };
    let count = format!("{} {what} · {} rows", fmt_count(shown as u64), fmt_count(s.groups.len() as u64));
    let search = TextInput::new(&s.query)
        .placeholder("Search name, pid, bundle id or path")
        .label("Search processes")
        .leading_icon(k.glyph.search)
        .clear_button()
        .width(Some(300.0))
        .on_edit(|s: &mut App| &mut s.query)
        .on_change(|s: &mut App, _| s.rebuild())
        .build(k)
        .key("search");
    let mut bar = row().w_full().gap(12.0).center_y().child(search).child(el().grow(1.0));
    if let Some(actions) = super::quit::list_actions(s, k) {
        bar = bar.child(actions);
    }
    let bar = bar.child(text(count).key("list-count").font(k.ty.small).color(c.text_muted));
    let mut page = page.child(bar);
    if let Some(o) = super::quit::outcome(s, None, k) {
        page = page.child(o);
    }
    if let Some(status) = super::home::feed_status(s, k) {
        page = page.child(status);
    }
    let body = if s.rows.is_empty() {
        col().w_full().child(notice("Nothing matches", "Try another word, or clear the search.", k))
    } else {
        col()
            .key("frame")
            .w_full()
            .grow(1.0)
            .min_h(0.0)
            .radius(k.radius.md)
            .fill(c.bg_base)
            .border(1.0, c.border_weak)
            .clip_rounded(k.radius.md)
            .child(table(s, k))
    };
    page.child(body)
}
