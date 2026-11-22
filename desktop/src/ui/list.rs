//! The process list: a search field over a virtualised table of groups (an app with its
//! helpers, several of one program) that open onto their processes, or of the processes as a
//! tree of parents and children.

use std::borrow::Cow;

use rune::{Align, Cursor, El, Key, KeyEvent, Tx, col, el, icon, row, text};
use rune_kit::{Cell, Kit, SegmentedControl, Table, TableColumn, TextInput};

use super::card::notice;
use crate::app::App;
use crate::groups::{GroupKind, Nav, RowRef};
use crate::procs::{Trust, fmt_count, fmt_cpu, fmt_mem};
use crate::tree::View;

#[cfg(test)]
#[path = "tests_tree.rs"]
mod tests_tree;

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
    /// What the triangle opens or closes, and whether it's open.
    twisty: Option<(Twisty, bool)>,
}

enum Twisty {
    /// A group, by its id.
    Group(String),
    /// A process's branch in the tree.
    Branch(u32),
}

/// The name column: an open/closed triangle for a group, the name, where it runs from when
/// another row shares the name, and a faint runtime and role.
fn name_cell(n: Name, k: &Kit) -> Cell {
    let c = &k.c;
    let mark = match n.twisty {
        Some((what, open)) => {
            let glyph = if open { k.glyph.triangle_down } else { k.glyph.triangle_right };
            let key = match &what {
                Twisty::Group(id) => format!("twisty-{id}"),
                Twisty::Branch(pid) => format!("twisty-p{pid}"),
            };
            row()
                .key(key)
                .role("button")
                .label(format!("{} {}", if open { "Close" } else { "Open" }, n.name))
                .cursor(Cursor::Pointer)
                .size(18.0, 18.0)
                .center()
                .child(icon(glyph, 10.0).color(c.icon_muted))
                .on_press(move |s: &mut App| match &what {
                    Twisty::Group(id) => s.toggle_group(id),
                    Twisty::Branch(pid) => s.toggle_tree(*pid),
                })
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

/// A tree row: the process, indented under its parent with a triangle when it has children,
/// and in "With children" what its whole branch uses.
fn tree_cell(s: &App, i: usize, column: &str, k: &Kit) -> Cell {
    let Some((r, node)) = s.rows.get(i).and_then(|r| Some((r, s.tree.nodes.get(r.group)?))) else {
        return Cell::text("");
    };
    let Some(p) = s.process(node.pid) else {
        return Cell::text("");
    };
    match column {
        "name" => {
            let twisty = s.tree_has_children(node.pid).then(|| (Twisty::Branch(node.pid), s.tree_is_open(node.pid)));
            let kind = format!("{} · {}", p.runtime.label(), p.role.label());
            name_cell(Name { name: p.name().into_owned(), hint: None, kind, depth: r.depth, twisty }, k)
        }
        "sign" => Cell::el(sign_badge(p.trust(), k)),
        "cpu" => number(fmt_cpu(p.cpu_pct), k),
        "mem" => number(fmt_mem(p.rss_kib), k),
        "sub" if node.children.is_empty() => Cell::text("—").muted(),
        "sub" => number(format!("{} · {}", fmt_cpu(node.sub_cpu), fmt_mem(node.sub_rss_kib)), k),
        "threads" => number(fmt_count(p.thread_count), k),
        "pid" => Cell::text(node.pid.to_string()).muted(),
        _ => Cell::text(""),
    }
}

fn cell(s: &App, i: usize, column: &str, k: &Kit) -> Cell {
    if s.view == View::Tree {
        return tree_cell(s, i, column, k);
    }
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
                        Name { name: g.name.clone(), hint: g.hint.clone(), kind, depth: 0, twisty: Some((Twisty::Group(g.id.clone()), open)) },
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
                    name_cell(Name { name: p.name().into_owned(), hint, kind, depth: r.depth, twisty: None }, k)
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
    t = t.column(num("cpu", "CPU", 72.0).sortable()).column(num("mem", "Memory", 80.0).sortable());
    t = match s.view {
        View::Grouped => t.column(num("threads", "Threads", 74.0)).column(num("count", "Processes", 90.0)),
        View::Tree => t.column(num("sub", "With children", 150.0)).column(num("threads", "Threads", 74.0)),
    };
    t.column(num("pid", "PID", 64.0))
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
    let (shown, rows) = match s.view {
        View::Grouped => (s.groups.iter().map(|g| g.pids.len()).sum(), s.groups.len()),
        View::Tree => (s.tree_matches, s.rows.len()),
    };
    let what = if s.nav == Nav::All { "processes".to_owned() } else { format!("of {} processes", fmt_count(s.procs.len() as u64)) };
    let count = format!("{} {what} · {} rows", fmt_count(shown as u64), fmt_count(rows as u64));
    let views = SegmentedControl::new(&["Grouped", "Tree"], Some(usize::from(s.view == View::Tree)))
        .label("Show processes")
        .fit()
        .on_pick(|s: &mut App, i: usize, _| s.set_view(if i == 1 { View::Tree } else { View::Grouped }))
        .build(k)
        .key("view-mode");
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
    let mut bar = row().w_full().gap(12.0).center_y().flex_wrap().child(search).child(views).child(el().grow(1.0));
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
            .on_key(tree_keys)
            .child(table(s, k))
    };
    page.child(body)
}

/// Left and Right in the tree, which the table leaves alone: they close and open the cursor
/// row's branch, then step to its parent or first child.
fn tree_keys(s: &mut App, ev: &KeyEvent, tx: &mut Tx) -> bool {
    let right = match ev.key {
        Key::Right => true,
        Key::Left => false,
        _ => return false,
    };
    if s.view != View::Tree || ev.mods.shift || ev.mods.alt || ev.mods.logo || ev.mods.ctrl {
        return false;
    }
    if let Some(id) = s.tree_step(right) {
        s.table.reveal(id, tx);
    }
    true
}
