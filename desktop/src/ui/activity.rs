//! The Activity page: processes starting and exiting, newest first, with a summary of the last
//! minute above (starts, exits, and the parents starting the most). Recording runs while this
//! page is open, or everywhere while "Keep recording" is on.

use rune::{Cursor, El, col, el, row, text};
use rune_kit::{Kit, Switch, TextInput};

use super::card::notice;
use super::detail::section;
use super::home::{callout, tile};
use crate::app::{App, fmt_ago};
use crate::procs::fmt_count;
use crate::spawns::{
    Event, Fail, INTERVAL, Kind, ROUND_SECS, SHORT_LIVED_MS, WINDOW, fmt_lifetime,
};

/// Rows the table lists before "and N more".
pub const ROWS: usize = 300;

pub fn page(s: &App, k: &Kit) -> El {
    let a = &s.activity;
    let mut page = col()
        .key("act-page")
        .w_full()
        .padding(24.0)
        .gap(20.0)
        .child(controls(s, k));
    match &a.error {
        Some(Fail::Unsupported(why)) => {
            page = page.child(callout(
                "act-unsupported",
                "This Spectra can't trace processes starting and exiting",
                why,
                k,
            ));
            if a.events.is_empty() {
                return page;
            }
        }
        Some(Fail::Failed(why)) => {
            let body = format!("{} It tries again in a few seconds.", ensure_stop(why));
            page = page.child(callout(
                "act-error",
                "The last recording round failed",
                &body,
                k,
            ));
        }
        _ => {}
    }
    page.child(section("Last minute", last_minute(s, k), k))
        .child(section("Starts and exits", events(s, k), k))
}

fn ensure_stop(why: &str) -> String {
    let why = why.trim();
    if why.ends_with('.') {
        why.to_owned()
    } else {
        format!("{why}.")
    }
}

/// Keep recording, and what recording is doing now.
fn controls(s: &App, k: &Kit) -> El {
    let a = &s.activity;
    let note = if a.keep_recording {
        "On: keeps recording on other pages, checking once a second; uses some CPU."
    } else {
        "Off: recording runs only while this page is open."
    };
    let keep = Switch::new(a.keep_recording)
        .label("Keep recording")
        .description(note)
        .on_toggle(|s: &mut App| s.toggle_keep_recording())
        .build(k)
        .key("act-keep");
    let status = if a.recording() {
        let every = a.interval().unwrap_or(INTERVAL);
        let mut line = format!("Recording in rounds of {ROUND_SECS} s, looking every {every}.");
        if let Some(at) = a.updated {
            line.push_str(&format!(" Last round landed {} ago.", fmt_ago(s.now - at)));
        }
        line
    } else if a.events.is_empty() {
        "Not recording.".to_owned()
    } else {
        format!(
            "Not recording. {} events kept.",
            fmt_count(a.events.len() as u64)
        )
    };
    col().w_full().gap(8.0).child(keep).child(
        text(status)
            .key("act-status")
            .role("status")
            .font(k.ty.small)
            .color(k.c.text_muted)
            .wrap(),
    )
}

fn last_minute(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let sum = s.activity.summary((s.wall)() as f64);
    let mut body = col().key("act-summary").w_full().gap(12.0);
    body = body.child(
        row()
            .w_full()
            .flex_wrap()
            .gaps(8.0, 8.0)
            .child(tile(
                "act-starts",
                "Started",
                fmt_count(sum.starts as u64),
                format!("in the last {WINDOW:.0} s"),
                k,
            ))
            .child(tile(
                "act-exits",
                "Exited",
                fmt_count(sum.exits as u64),
                format!("in the last {WINDOW:.0} s"),
                k,
            )),
    );
    if sum.parents.is_empty() {
        return body;
    }
    let mut list = col()
        .key("act-parents")
        .w_full()
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .py(4.0);
    for p in &sum.parents {
        let ppid = p.ppid;
        let name = if p.name.is_empty() {
            format!("pid {ppid}")
        } else {
            p.name.clone()
        };
        let what = if p.count == 1 { "process" } else { "processes" };
        let line = format!(
            "{name} · {ppid} started {} {what}",
            fmt_count(p.count as u64)
        );
        let key = format!("act-parent-{ppid}");
        let who = if lives(s, ppid, &p.name) {
            text(line)
                .key(key.clone())
                .role("link")
                .label(format!("Open {name}"))
                .cursor(Cursor::Pointer)
                .font(k.ty.small)
                .color(c.text_accent)
                .on_press(move |s: &mut App| s.open(ppid))
        } else {
            text(line)
                .key(key.clone())
                .font(k.ty.small)
                .color(c.text_base)
        };
        let filter = text("Show only these")
            .key(format!("{key}-filter"))
            .role("link")
            .label(format!("Show only what {name} started"))
            .cursor(Cursor::Pointer)
            .font(k.ty.small)
            .color(c.text_accent)
            .on_press(move |s: &mut App| {
                s.activity.parent = Some(ppid);
                s.activity.expanded = None;
            });
        list = list.child(
            row()
                .w_full()
                .h(28.0)
                .px(12.0)
                .gap(12.0)
                .center_y()
                .child(
                    row()
                        .grow(1.0)
                        .min_w(0.0)
                        .child(who.truncate().shrink().min_w(0.0)),
                )
                .child(filter),
        );
    }
    body.child(list)
}

fn events(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let a = &s.activity;
    let shown = a.shown();
    let search = TextInput::new(&a.query)
        .placeholder("Filter by name, pid or parent")
        .label("Filter events")
        .leading_icon(k.glyph.search)
        .clear_button()
        .width(Some(260.0))
        .on_edit(|s: &mut App| &mut s.activity.query)
        .build(k)
        .key("act-filter");
    let short = Switch::new(a.short_only)
        .label("Short-lived only")
        .description(format!("Lived under {} s", SHORT_LIVED_MS / 1000))
        .on_toggle(|s: &mut App| s.activity.short_only = !s.activity.short_only)
        .build(k)
        .key("act-short");
    let own = Switch::new(a.show_own)
        .label("Show Spectra Desktop's own readings")
        .description("What this app starts to look at your machine")
        .on_toggle(|s: &mut App| s.toggle_show_own())
        .build(k)
        .key("act-own");
    let count = format!(
        "{} of {} events",
        fmt_count(shown.len() as u64),
        fmt_count(a.events.iter().filter(|e| a.counts(e)).count() as u64)
    );
    let mut bar = row()
        .w_full()
        .gap(16.0)
        .center_y()
        .flex_wrap()
        .child(search)
        .child(short)
        .child(own);
    if let Some(ppid) = a.parent {
        let name = a
            .events
            .iter()
            .rev()
            .find(|e| e.ppid == ppid)
            .map(|e| e.parent_name().to_owned())
            .filter(|n| !n.is_empty())
            .or_else(|| s.process(ppid).map(|p| p.name().into_owned()))
            .unwrap_or_else(|| "pid".to_owned());
        bar = bar.child(
            row()
                .key("act-parent-chip")
                .gap(8.0)
                .center_y()
                .py(4.0)
                .px(10.0)
                .radius(k.radius.md)
                .fill(c.layer_02)
                .child(
                    text(format!("Started by {name} · {ppid}"))
                        .font(k.ty.small)
                        .color(c.text_base),
                )
                .child(
                    text("✕")
                        .key("act-parent-clear")
                        .role("button")
                        .label("Show every parent")
                        .cursor(Cursor::Pointer)
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .on_press(|s: &mut App| s.activity.parent = None),
                ),
        );
    }
    let bar = bar.child(el().grow(1.0)).child(
        text(count)
            .key("act-count")
            .font(k.ty.small)
            .color(c.text_muted),
    );
    let mut body = col()
        .key("act-events")
        .w_full()
        .gap(12.0)
        .child(
            text("Checks every 250 ms (every second while recording in the background); a process that starts and ends between checks, or between rounds, can be missed.")
            .key("act-blindspot")
            .font(k.ty.small)
            .color(c.text_muted)
            .wrap(),
        )
        .child(bar);
    if a.events.is_empty() {
        let (title, line) = if a.recording() {
            (
                "Watching",
                format!(
                    "Processes that start or exit show here as each {ROUND_SECS} s round lands."
                ),
            )
        } else {
            (
                "Nothing recorded yet",
                "Open this page or turn on Keep recording to watch processes start and exit."
                    .to_owned(),
            )
        };
        return body.child(notice(title, &line, k).key("act-empty"));
    }
    if shown.is_empty() {
        return body.child(
            notice(
                "Nothing matches",
                "Try another word, or clear the filters.",
                k,
            )
            .key("act-empty"),
        );
    }
    body = body.child(table(s, &shown, k));
    body
}

fn head(t: &str, w: Option<f32>, k: &Kit) -> El {
    let t = text(t.to_owned())
        .font(k.ty.text_xsmall)
        .color(k.c.text_faint);
    match w {
        Some(w) => t.w(w),
        None => t.grow(1.0).min_w(0.0),
    }
}

const TIME_W: f32 = 64.0;
const KIND_W: f32 = 56.0;
const NAME_W: f32 = 170.0;
const PARENT_W: f32 = 160.0;
const LIFE_W: f32 = 60.0;

fn table(s: &App, shown: &[&Event], k: &Kit) -> El {
    let c = &k.c;
    let mut table = col()
        .key("act-table")
        .w_full()
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .py(4.0)
        .child(
            row()
                .w_full()
                .h(24.0)
                .px(12.0)
                .gap(12.0)
                .center_y()
                .child(head("Time", Some(TIME_W), k))
                .child(head("Event", Some(KIND_W), k))
                .child(head("Process", Some(NAME_W), k))
                .child(head("Parent", Some(PARENT_W), k))
                .child(head("Lived", Some(LIFE_W), k))
                .child(head("Command line", None, k)),
        );
    for e in shown.iter().take(ROWS) {
        table = table.child(event_row(s, e, k));
    }
    if shown.len() > ROWS {
        table = table.child(
            text(format!(
                "and {} older; filter to narrow them down",
                fmt_count((shown.len() - ROWS) as u64)
            ))
            .key("act-more")
            .px(12.0)
            .py(6.0)
            .font(k.ty.small)
            .color(c.text_faint),
        );
    }
    table
}

/// Whether the process feed lists `pid` under `name`: a reused pid is some other process, and
/// its page isn't this event's.
fn lives(s: &App, pid: u32, name: &str) -> bool {
    !name.is_empty() && s.process(pid).is_some_and(|p| p.name() == name)
}

/// A process as `name · pid`: a link to its page while the process feed lists it.
fn who(s: &App, key: String, name: &str, pid: u32, link: bool, w: f32, k: &Kit) -> El {
    let c = &k.c;
    let name = if name.is_empty() { "?" } else { name };
    let t = text(format!("{name} · {pid}"))
        .key(key)
        .w(w)
        .truncate()
        .font(k.ty.small);
    if link && lives(s, pid, name) {
        let label = format!("Open {name}");
        t.role("link")
            .label(label)
            .cursor(Cursor::Pointer)
            .color(c.text_accent)
            .on_press(move |s: &mut App| s.open(pid))
    } else {
        t.color(c.text_base)
    }
}

/// The command line the process feed gave for it, else what started it.
fn command_line(s: &App, e: &Event) -> String {
    if !e.command_line.is_empty() {
        return e.command_line.clone();
    }
    match s.process(e.pid).filter(|p| p.name() == e.name.as_str()) {
        Some(p) if !p.full_command_line.is_empty() => p.full_command_line.clone(),
        _ => String::new(),
    }
}

fn event_row(s: &App, e: &Event, k: &Kit) -> El {
    let c = &k.c;
    let id = e.id();
    let key = format!("act-ev-{id}");
    let open = s.activity.expanded.as_deref() == Some(id.as_str());
    let (kind_ink, kind) = match e.kind {
        Kind::Start => (c.success.fg, Kind::Start.label()),
        Kind::Exit => (c.text_muted, Kind::Exit.label()),
    };
    let life = match (e.kind, e.lifetime_ms) {
        (Kind::Exit, Some(ms)) => fmt_lifetime(ms),
        _ => String::new(),
    };
    let life_ink = if e.short_lived() {
        c.warning.fg
    } else {
        c.text_muted
    };
    let cmd = command_line(s, e);
    let cmd_el = if cmd.is_empty() {
        let chain = if e.chain.is_empty() {
            "—".to_owned()
        } else {
            format!("under {}", e.chain.join(" < "))
        };
        text(chain).font(k.ty.small).color(c.text_faint)
    } else {
        text(cmd.clone()).font(k.ty.small).color(c.text_base)
    };
    let line = row()
        .w_full()
        .h(26.0)
        .px(12.0)
        .gap(12.0)
        .center_y()
        .child(
            text(e.clock.clone())
                .key(format!("{key}-time"))
                .w(TIME_W)
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_muted),
        )
        .child(text(kind).w(KIND_W).font(k.ty.small).color(kind_ink))
        .child(who(
            s,
            format!("{key}-open"),
            &e.name,
            e.pid,
            e.kind == Kind::Start,
            NAME_W,
            k,
        ))
        .child(who(
            s,
            format!("{key}-parent"),
            e.parent_name(),
            e.ppid,
            true,
            PARENT_W,
            k,
        ))
        .child(
            text(life)
                .w(LIFE_W)
                .font(k.ty.small.features("'tnum' 1"))
                .color(life_ink),
        )
        .child(
            row()
                .grow(1.0)
                .min_w(0.0)
                .child(cmd_el.truncate().shrink().min_w(0.0)),
        );
    let toggle = id.clone();
    let mut item = col()
        .key(key.clone())
        .role("button")
        .label(format!("{} {} {}", kind, e.name, e.pid))
        .cursor(Cursor::Pointer)
        .w_full()
        .on_press(move |s: &mut App| s.toggle_event(&toggle))
        .child(line);
    if open {
        item = item.child(detail(e, &cmd, &key, k));
    }
    item
}

/// An open row: the whole command line, every parent up the chain, and how sure the lifetime is.
fn detail(e: &Event, cmd: &str, key: &str, k: &Kit) -> El {
    let c = &k.c;
    let muted = |t: String| text(t).font(k.ty.small).color(c.text_muted).wrap_anywhere();
    let mut body = col()
        .key(format!("{key}-detail"))
        .w_full()
        .gap(4.0)
        .px(12.0)
        .pb(8.0)
        .pl(12.0 + TIME_W + 12.0);
    body = body.child(if cmd.is_empty() {
        muted("Command line: not known. Spectra names short-lived processes but the process list never saw this one's arguments.".into())
    } else {
        text(cmd.to_owned())
            .key(format!("{key}-cmd"))
            .font(k.ty.small.features("'tnum' 1"))
            .color(c.text_base)
            .wrap_anywhere()
    });
    if !e.chain.is_empty() {
        body = body.child(muted(format!("Started by {}", e.chain.join(" < "))));
    }
    if let (Kind::Exit, Some(ms)) = (e.kind, e.lifetime_ms) {
        let within = e
            .lifetime_err_ms
            .filter(|err| *err > 0)
            .map_or_else(String::new, |err| {
                format!(", give or take {}", fmt_lifetime(err))
            });
        body = body.child(muted(format!("Lived about {}{within}", fmt_lifetime(ms))));
    }
    body
}
