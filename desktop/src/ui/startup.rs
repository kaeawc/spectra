//! The Startup page: everything that launches at login or boot and the background services,
//! grouped by the app they belong to, with Disable / Enable for the person's own launch agents
//! behind a confirmation, and why it's off for everything else.

use std::collections::HashMap;

use rune::{El, col, el, icon, row, text};
use rune_kit::{
    Button, ButtonVariant, Checkbox, Dialog, IconButton, IconButtonVariant, Kit, TextInput,
};

use super::home::callout;
use crate::app::App;
use crate::procs::fmt_count;
use crate::startup::{Action, ChangeStatus, Group, Heading, Item, Kind, MAX_ROWS, group};

pub fn page(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let st = &s.startup;
    let refresh = Button::new(if st.reading { "Reading…" } else { "Refresh" })
        .variant(ButtonVariant::Outline)
        .disabled(st.reading)
        .help("Read again what starts at login or boot")
        .on_press(|s: &mut App| s.read_startup())
        .build(k)
        .key("su-refresh");
    let intro = row()
        .w_full()
        .gap(12.0)
        .center_y()
        .child(
            text("What launches at login or boot, and the background services launchd runs. You can turn off your own launch agents here.")
                .font(k.ty.small)
                .color(c.text_muted)
                .wrap()
                .grow(1.0)
                .min_w(0.0),
        )
        .child(refresh);
    let mut page = col()
        .key("su-page")
        .w_full()
        .padding(24.0)
        .gap(16.0)
        .child(intro);
    if let Some(o) = outcome(s, k) {
        page = page.child(o);
    }
    let Some(listing) = &st.listing else {
        let body = match &st.error {
            Some(why) => failed("su-error", "Couldn't read what starts at login", why, k),
            None => text("Reading what starts at login…")
                .key("su-reading")
                .font(k.ty.small)
                .color(c.text_faint),
        };
        return page.child(body);
    };
    if let Some(why) = &st.error {
        page = page.child(failed(
            "su-error",
            "The last refresh failed; this is the list from before",
            why,
            k,
        ));
    }
    page = page.child(filters(s, k));
    for (i, note) in listing.notes.iter().enumerate() {
        page = page.child(
            row()
                .key(format!("su-note-{i}"))
                .gap(6.0)
                .center_y()
                .child(icon(k.glyph.dash, 12.0).color(c.icon_muted))
                .child(
                    text(note.clone())
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .wrap()
                        .grow(1.0)
                        .min_w(0.0),
                ),
        );
    }
    let shown = st.shown();
    if shown.is_empty() {
        let why = if listing.items.is_empty() {
            "Nothing starts at login or boot."
        } else {
            "Nothing matches the filters."
        };
        return page.child(
            text(why)
                .key("su-empty")
                .font(k.ty.small)
                .color(c.text_muted),
        );
    }
    let mut left = MAX_ROWS;
    let mut keys = Keys::default();
    for g in group(&shown) {
        if left == 0 {
            break;
        }
        let take = g.items.len().min(left);
        left -= take;
        page = page.child(section(s, &g, take, &mut keys, k));
    }
    if shown.len() > MAX_ROWS {
        page = page.child(
            text(format!(
                "And {} more. Search to narrow the list.",
                fmt_count((shown.len() - MAX_ROWS) as u64)
            ))
            .key("su-more")
            .font(k.ty.small)
            .color(c.text_muted),
        );
    }
    page
}

/// The search, the two toggles and how many are shown.
fn filters(s: &App, k: &Kit) -> El {
    let st = &s.startup;
    let total = st.listing.as_ref().map_or(0, |l| l.items.len());
    let search = TextInput::new(&st.query)
        .placeholder("Search name, label, app or path")
        .label("Search startup items")
        .leading_icon(k.glyph.search)
        .clear_button()
        .width(Some(280.0))
        .on_edit(|s: &mut App| &mut s.startup.query)
        .build(k)
        .key("su-search");
    let running = Checkbox::new(st.running_only)
        .label("Running now")
        .on_toggle(|s: &mut App| s.startup_toggle_running())
        .build(k)
        .key("su-running");
    let not_apple = Checkbox::new(st.not_apple)
        .label("Not from Apple")
        .on_toggle(|s: &mut App| s.startup_toggle_apple())
        .build(k)
        .key("su-not-apple");
    let count = format!(
        "{} of {}",
        fmt_count(st.shown().len() as u64),
        fmt_count(total as u64)
    );
    row()
        .key("su-filters")
        .w_full()
        .gap(16.0)
        .center_y()
        .child(search)
        .child(running)
        .child(not_apple)
        .child(el().grow(1.0))
        .child(
            text(count)
                .key("su-count")
                .font(k.ty.small)
                .color(k.c.text_muted),
        )
}

/// A failure, with Try again.
fn failed(key: &str, title: &str, why: &str, k: &Kit) -> El {
    col()
        .w_full()
        .gap(8.0)
        .child(callout(key, title, why, k))
        .child(
            row().child(
                Button::new("Try again")
                    .variant(ButtonVariant::Outline)
                    .on_press(|s: &mut App| s.read_startup())
                    .build(k)
                    .key("su-retry"),
            ),
        )
}

/// Element keys for rows, unique even when two rows share a label.
#[derive(Default)]
struct Keys(HashMap<String, usize>);

impl Keys {
    fn of(&mut self, label: &str) -> String {
        let base = format!("su-{}", label.replace('/', "_"));
        let n = self.0.entry(base.clone()).or_default();
        *n += 1;
        if *n == 1 { base } else { format!("{base}~{n}") }
    }
}

/// One app (or the unowned items): a heading, then its first `take` rows.
fn section(s: &App, g: &Group<'_>, take: usize, keys: &mut Keys, k: &Kit) -> El {
    let c = &k.c;
    let mut head = row().gap(8.0).center_y().child(
        text(g.title())
            .key(format!("su-group-{}", g.id()))
            .font(k.ty.ui_medium)
            .color(c.text_base),
    );
    if let Heading::App(o) = &g.heading
        && !o.developer.is_empty()
    {
        head = head.child(
            text(o.developer.clone())
                .font(k.ty.small)
                .color(c.text_muted),
        );
    }
    head = head.child(
        text(fmt_count(g.items.len() as u64))
            .font(k.ty.small)
            .color(c.text_faint),
    );
    let mut list = col()
        .w_full()
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak);
    for (i, item) in g.items.iter().take(take).enumerate() {
        list = list.child(item_row(s, item, keys.of(&item.label), i > 0, k));
    }
    col()
        .key(format!("su-section-{}", g.id()))
        .w_full()
        .gap(8.0)
        .child(head)
        .child(list)
}

/// A small outlined word, so a state reads without colour.
fn pill(label: &str, key: String, k: &Kit) -> El {
    row()
        .key(key)
        .px(6.0)
        .h(18.0)
        .center_y()
        .radius(9.0)
        .border(1.0, k.c.warning.fg)
        .child(
            text(label.to_owned())
                .font(k.ty.text_xsmall_medium)
                .color(k.c.text_base),
        )
}

fn item_row(s: &App, item: &Item, key: String, rule: bool, k: &Kit) -> El {
    let c = &k.c;
    let name = item.display_name().to_owned();
    let mut title = row().gap(8.0).center_y().min_w(0.0).child(
        text(name.clone())
            .key(format!("{key}-name"))
            .font(k.ty.small_medium)
            .color(c.text_base)
            .truncate()
            .shrink()
            .min_w(0.0)
            .help_when_truncated(format!("{name} ({})", item.label)),
    );
    if item.disabled {
        title = title.child(pill("Disabled", format!("{key}-disabled"), k));
    }
    let what = format!("{} · {}", item.kind.label(), item.trigger_text());
    let mut left = col().grow(1.0).min_w(0.0).gap(2.0).child(title).child(
        text(what)
            .key(format!("{key}-what"))
            .font(k.ty.text_xsmall)
            .color(c.text_muted)
            .truncate()
            .min_w(0.0),
    );
    let path = if item.program.is_empty() {
        item.plist_path.clone()
    } else {
        item.program.clone()
    };
    if !path.is_empty() {
        left = left.child(
            text(path.clone())
                .key(format!("{key}-path"))
                .font(k.ty.text_xsmall)
                .color(c.text_faint)
                .truncate()
                .min_w(0.0)
                .help_when_truncated(path),
        );
    }
    let refusal = item.change_refusal();
    if let (Some(why), false) = (refusal, item.kind == Kind::Service) {
        left = left.child(
            row()
                .gap(4.0)
                .center_y()
                .min_w(0.0)
                .child(icon(k.glyph.lock, 11.0).color(c.icon_muted))
                .child(
                    text(why)
                        .key(format!("{key}-why"))
                        .font(k.ty.text_xsmall)
                        .color(c.text_muted)
                        .truncate()
                        .shrink()
                        .min_w(0.0),
                ),
        );
    }
    let mut r = row()
        .key(key.clone())
        .w_full()
        .gap(12.0)
        .center_y()
        .py(8.0)
        .px(12.0)
        .child(left)
        .child(running(s, item, &key, k));
    if rule {
        r = r.border_sides([1.0, 0.0, 0.0, 0.0], c.border_weak);
    }
    if item.kind != Kind::Service {
        r = r.child(action(s, item, refusal, &key, k));
    }
    r
}

/// Running with its pid (a button to its page when the process list has it), or not running.
fn running(s: &App, item: &Item, key: &str, k: &Kit) -> El {
    let c = &k.c;
    let key = format!("{key}-run");
    match item.pid {
        Some(pid) if s.process(pid).is_some() => Button::new(format!("Running · pid {pid}"))
            .variant(ButtonVariant::Ghost)
            .help(format!("Open pid {pid}'s page"))
            .on_press(move |s: &mut App| s.open(pid))
            .build(k)
            .key(key),
        Some(pid) => text(format!("Running · pid {pid}"))
            .key(key)
            .font(k.ty.small)
            .color(c.text_base),
        None if item.running => text("Running").key(key).font(k.ty.small).color(c.text_base),
        None => text("Not running")
            .key(key)
            .font(k.ty.small)
            .color(c.text_faint),
    }
}

/// Disable or Enable, off with its reason for anything but the person's own launch agents.
fn action(s: &App, item: &Item, refusal: Option<&'static str>, key: &str, k: &Kit) -> El {
    let st = &s.startup;
    let act = if item.disabled {
        Action::Enable
    } else {
        Action::Disable
    };
    let busy = st.busy() || st.confirm.is_some();
    let help = match refusal {
        Some(why) => why.to_owned(),
        None if st.busy() => "Another change is on its way".to_owned(),
        None => format!("{} {}", act.verb(), item.display_name()),
    };
    let (label, plist) = (item.label.clone(), item.plist_path.clone());
    Button::new(act.verb())
        .variant(ButtonVariant::Outline)
        .disabled(refusal.is_some() || busy)
        .help(help)
        .on_press(move |s: &mut App| s.ask_startup(&label, &plist, act))
        .build(k)
        .key(format!("{key}-act"))
}

/// What the last change came to.
fn outcome(s: &App, k: &Kit) -> Option<El> {
    let c = &k.c;
    let o = s.startup.outcome.as_ref()?;
    let (mark, border) = match &o.status {
        ChangeStatus::Sending => (None, c.border_base),
        ChangeStatus::Done(_) => (Some(k.glyph.check_small), c.border_base),
        ChangeStatus::Refused(_) => (Some(k.glyph.dash), c.warning.fg),
        ChangeStatus::Failed(_) => (Some(k.glyph.circle_x), c.danger.fg),
    };
    let mut r = row()
        .key("su-outcome")
        .role("status")
        .w_full()
        .gap(8.0)
        .center_y()
        .py(6.0)
        .px(12.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, border);
    if let Some(m) = mark {
        r = r.child(icon(m, 12.0).color(c.icon_base));
    }
    r = r.child(
        text(o.line())
            .key("su-outcome-text")
            .font(k.ty.small)
            .color(c.text_base)
            .wrap()
            .grow(1.0)
            .min_w(0.0),
    );
    if s.startup.busy() {
        return Some(r);
    }
    Some(
        r.child(
            IconButton::new(k.glyph.close)
                .variant(IconButtonVariant::Ghost)
                .label("Dismiss")
                .on_press(|s: &mut App| s.dismiss_startup_outcome())
                .build(k)
                .key("su-outcome-dismiss"),
        ),
    )
}

/// The confirmation, while one is open: the item by name and label, and what will change.
/// Cancel has focus; Esc, the close button and a click outside cancel.
pub fn dialog(s: &App, k: &Kit, layer: &mut Vec<El>) {
    let Some(ask) = &s.startup.confirm else {
        return;
    };
    let lines = ask.consequence();
    let mut d = Dialog::new(
        "su-confirm",
        format!("{} {}?", ask.action.verb(), ask.item.display_name()),
    )
    .width(460.0)
    .fit()
    .open(true)
    .on_close(|s: &mut App| s.cancel_startup());
    let mut body = col().w_full().gap(12.0).px(16.0).pb(16.0).child(
        text(lines[0].clone())
            .key("su-confirm-line-0")
            .font(k.ty.ui)
            .color(k.c.text_base)
            .wrap()
            .w_full(),
    );
    for (i, line) in lines.iter().enumerate().skip(1) {
        body = body.child(
            text(line.clone())
                .key(format!("su-confirm-line-{i}"))
                .font(k.ty.small)
                .color(k.c.text_muted)
                .wrap()
                .w_full(),
        );
    }
    let buttons = row()
        .w_full()
        .gap(8.0)
        .justify_end()
        .child(
            Button::new("Cancel")
                .variant(ButtonVariant::Outline)
                .on_press(|s: &mut App| s.cancel_startup())
                .build(k)
                .key("su-confirm-cancel")
                .autofocus(),
        )
        .child(
            Button::new(ask.action.verb())
                .variant(ButtonVariant::Contrast)
                .on_press(|s: &mut App| s.confirm_startup())
                .build(k)
                .key("su-confirm-send"),
        );
    d = d.child(body.child(buttons));
    d.build(k, layer);
}
