//! The Network page: a compact line of the network's state (interface, VPN, proxy, DNS), the
//! processes moving the most bytes over the last window with their remote endpoints, and what is
//! listening, with anything open on every interface flagged. A window measures only while this
//! page is open.

use rune::{Cursor, El, col, row, text};
use rune_kit::Kit;

use super::detail::section;
use super::home::callout;
use crate::app::{App, fmt_ago};
use crate::live::fmt_rate;
use crate::network::{
    Fail, REMOTES, RETRY_FAILED_SECS, User, WINDOW_SECS, exposed, fmt_amount, listening,
};
use crate::procs::fmt_count;
use crate::system::Port;

/// Listening ports listed before "and N more".
pub const PORTS: usize = 60;
const IN_W: f32 = 150.0;
const REMOTES_W: f32 = 230.0;

pub fn page(s: &App, k: &Kit) -> El {
    let n = &s.network;
    let mut page = col()
        .key("net-page")
        .w_full()
        .padding(24.0)
        .gap(20.0)
        .child(header(s, k))
        .child(
            text(status(s))
                .key("net-status")
                .role("status")
                .font(k.ty.small)
                .color(k.c.text_muted)
                .wrap(),
        );
    match &n.error {
        Some(Fail::Unsupported(why)) => {
            page = page.child(callout(
                "net-unsupported",
                "This Spectra can't measure which processes use the network",
                why,
                k,
            ));
        }
        Some(Fail::Failed(why)) => {
            let why = why.trim().trim_end_matches('.');
            let body = format!("{why}. It tries again in {RETRY_FAILED_SECS:.0} s.");
            page = page.child(callout(
                "net-error",
                "The last measurement failed",
                &body,
                k,
            ));
        }
        _ => {}
    }
    if !matches!(n.error, Some(Fail::Unsupported(_))) || n.top.is_some() {
        page = page.child(section("Using the network", users(s, k), k));
    }
    page.child(section("Listening", ports(s, k), k))
}

/// What the measuring is doing, in a line.
fn status(s: &App) -> String {
    let n = &s.network;
    let last = n.top.as_ref().zip(n.updated).map(|(t, at)| {
        format!(
            "Measured over {:.0} s, {} ago.",
            (t.duration_ms as f64 / 1000.0).max(1.0),
            fmt_ago((s.now - at).max(0.0))
        )
    });
    match (n.measuring(), last) {
        (true, None) => format!("Measuring for {WINDOW_SECS} s…"),
        (true, Some(l)) => format!("{l} Measuring again…"),
        (false, Some(l)) if s.network_wanted() && n.next > s.now => {
            format!("{l} Next in {:.0} s.", (n.next - s.now).ceil())
        }
        (false, Some(l)) => l,
        (false, None) => "Not measuring.".to_owned(),
    }
}

/// One fact of the state line.
fn chip(key: &str, label: &str, value: String, k: &Kit) -> El {
    let c = &k.c;
    col()
        .key(format!("net-state-{key}"))
        .gap(2.0)
        .py(8.0)
        .px(12.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .max_w(360.0)
        .child(
            text(label.to_owned())
                .font(k.ty.text_xsmall)
                .color(c.text_faint),
        )
        .child(
            text(value)
                .key(format!("net-state-{key}-value"))
                .font(k.ty.small)
                .color(c.text_base)
                .wrap_anywhere(),
        )
}

/// The interface, VPN, proxy and DNS, from the Machine page's own network reading.
fn header(s: &App, k: &Kit) -> El {
    let part = &s.system.network;
    let Some(n) = &part.last else {
        if part.waiting() {
            return text("Reading the network…")
                .key("net-state-reading")
                .font(k.ty.small)
                .color(k.c.text_faint);
        }
        let why = part.error.as_ref().map_or("", |e| e.why());
        return callout("net-state-error", "Couldn't read the network", why, k);
    };
    let none = || "None".to_owned();
    let iface = match (n.iface.is_empty(), n.gateway.is_empty()) {
        (true, _) => none(),
        (false, true) => n.iface.clone(),
        (false, false) => format!("{} via {}", n.iface, n.gateway),
    };
    let vpn = match (n.vpn, n.vpn_ifaces.is_empty()) {
        (true, false) => format!("On ({})", n.vpn_ifaces.join(", ")),
        (true, true) => "On".to_owned(),
        (false, _) => "Off".to_owned(),
    };
    let proxy = if n.proxy.is_empty() {
        none()
    } else {
        n.proxy.join(", ")
    };
    let dns = if n.dns.is_empty() {
        none()
    } else {
        n.dns.join(", ")
    };
    let mut out = col().key("net-state").w_full().gap(6.0).child(
        row()
            .w_full()
            .flex_wrap()
            .gaps(8.0, 8.0)
            .child(chip("iface", "Interface", iface, k))
            .child(chip("vpn", "VPN", vpn, k))
            .child(chip("proxy", "Proxy", proxy, k))
            .child(chip("dns", "DNS", dns, k)),
    );
    if let Some(when) = part.status(s.now) {
        out = out.child(
            text(when)
                .key("net-state-status")
                .font(k.ty.small)
                .color(k.c.text_muted),
        );
    }
    out
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

fn table(key: &str, k: &Kit) -> El {
    col()
        .key(key.to_owned())
        .w_full()
        .radius(k.radius.md)
        .fill(k.c.layer_01)
        .border(1.0, k.c.border_weak)
        .py(4.0)
}

/// A process as `name · pid`: a link to its page only when the process feed lists that pid
/// under the same name, so a reused pid opens nothing.
fn who(s: &App, key: String, seen: &str, pid: Option<u32>, k: &Kit) -> El {
    let c = &k.c;
    let name = crate::network::unescape(seen).trim().to_owned();
    let line = match (name.is_empty(), pid) {
        (true, Some(pid)) => format!("pid {pid}"),
        (false, Some(pid)) => format!("{name} · {pid}"),
        (true, None) => "Unknown".to_owned(),
        (false, None) => name.clone(),
    };
    let t = |line: String| text(line).font(k.ty.small).truncate().shrink().min_w(0.0);
    match pid.filter(|pid| s.same_process(*pid, seen)) {
        Some(pid) => {
            // The process list's whole name, not the one Spectra cut short.
            let label = s.process(pid).map_or(name, |p| p.name().into_owned());
            t(format!("{label} · {pid}"))
                .key(format!("{key}-open"))
                .role("link")
                .label(format!("Open {label}"))
                .cursor(Cursor::Pointer)
                .color(c.text_accent)
                .on_press(move |s: &mut App| s.open(pid))
        }
        None => t(line).key(key).color(c.text_base),
    }
}

/// `19 MB · 3.8 MB/s`.
fn moved(bytes: u64, rate: f64) -> String {
    format!("{} · {}", fmt_amount(bytes), fmt_rate(rate))
}

fn remotes(u: &User) -> String {
    if u.remotes.is_empty() {
        return "None open at the end".to_owned();
    }
    let mut line = u.remotes[..u.remotes.len().min(REMOTES)].join(", ");
    if u.remotes.len() > REMOTES {
        line.push_str(&format!(" +{}", u.remotes.len() - REMOTES));
    }
    line
}

/// The processes that moved the most bytes over the last window, most first.
fn users(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let Some(top) = &s.network.top else {
        let line = if s.network.measuring() {
            format!("The first measurement lands in about {WINDOW_SECS} s.")
        } else {
            "Nothing measured yet.".to_owned()
        };
        return text(line)
            .key("net-use-waiting")
            .font(k.ty.small)
            .color(c.text_faint);
    };
    if top.users.is_empty() {
        return text("No process moved bytes over the network in the last window.")
            .key("net-use-empty")
            .font(k.ty.small)
            .color(c.text_muted);
    }
    let figures = k.ty.small.features("'tnum' 1");
    let what = if top.active == 1 {
        "process"
    } else {
        "processes"
    };
    let total = text(format!(
        "{} {what} moved {} in and {} out ({} in, {} out).",
        fmt_count(top.active),
        fmt_amount(top.bytes_in),
        fmt_amount(top.bytes_out),
        fmt_rate(top.in_rate),
        fmt_rate(top.out_rate),
    ))
    .key("net-use-total")
    .font(k.ty.small)
    .color(c.text_muted)
    .wrap();
    let mut list = table("net-use", k).child(
        row()
            .w_full()
            .h(24.0)
            .px(12.0)
            .gap(12.0)
            .center_y()
            .child(head("Process", None, k))
            .child(head("In", Some(IN_W), k))
            .child(head("Out", Some(IN_W), k))
            .child(head("Remote endpoints", Some(REMOTES_W), k)),
    );
    for u in &top.users {
        let key = format!("net-use-{}", u.pid);
        let mut name = row().grow(1.0).min_w(0.0).gap(6.0).center_y().child(who(
            s,
            format!("{key}-who"),
            &u.command,
            Some(u.pid),
            k,
        ));
        if u.started {
            name = name.child(
                text("new")
                    .key(format!("{key}-new"))
                    .font(k.ty.text_xsmall)
                    .color(c.text_faint),
            );
        }
        let remote_line = remotes(u);
        list = list.child(
            row()
                .key(key.clone())
                .w_full()
                .h(28.0)
                .px(12.0)
                .gap(12.0)
                .center_y()
                .child(name)
                .child(
                    text(moved(u.bytes_in, u.in_rate))
                        .key(format!("{key}-in"))
                        .w(IN_W)
                        .font(figures)
                        .color(c.text_base)
                        .truncate(),
                )
                .child(
                    text(moved(u.bytes_out, u.out_rate))
                        .key(format!("{key}-out"))
                        .w(IN_W)
                        .font(figures)
                        .color(c.text_base)
                        .truncate(),
                )
                .child(
                    text(remote_line.clone())
                        .key(format!("{key}-remotes"))
                        .w(REMOTES_W)
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .truncate()
                        .help_when_truncated(u.remotes.join(", ")),
                ),
        );
    }
    col().w_full().gap(8.0).child(total).child(list)
}

fn port_row(s: &App, p: &Port, k: &Kit) -> El {
    let c = &k.c;
    let key = format!("net-port-{}-{}-{}", p.port, p.proto, p.pid.unwrap_or(0));
    let reach = if exposed(p) {
        text("All interfaces")
            .key(format!("{key}-exposed"))
            .w(110.0)
            .font(k.ty.small)
            .color(c.warning.fg)
    } else {
        text(String::new()).w(110.0)
    };
    row()
        .key(key.clone())
        .w_full()
        .h(26.0)
        .px(12.0)
        .gap(12.0)
        .center_y()
        .child(
            text(p.port.to_string())
                .w(56.0)
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_base),
        )
        .child(
            text(p.proto.clone())
                .w(56.0)
                .font(k.ty.small)
                .color(c.text_muted),
        )
        .child(
            text(p.addrs.join(", "))
                .w(130.0)
                .font(k.ty.small)
                .color(c.text_muted)
                .truncate(),
        )
        .child(reach)
        .child(
            row()
                .grow(1.0)
                .min_w(0.0)
                .child(who(s, format!("{key}-who"), &p.command, p.pid, k)),
        )
}

/// What is listening, anything open on every interface first and flagged.
fn ports(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let Some(n) = &s.system.network.last else {
        return text("Nothing read yet.")
            .key("net-listen-waiting")
            .font(k.ty.small)
            .color(c.text_faint);
    };
    if n.ports.is_empty() {
        return text("Nothing is listening.")
            .key("net-listen-empty")
            .font(k.ty.small)
            .color(c.text_muted);
    }
    let shown = listening(&n.ports);
    let open = shown.iter().filter(|p| exposed(p)).count();
    let what = if shown.len() == 1 { "port" } else { "ports" };
    let summary = match open {
        0 => format!(
            "{} {what} listening, each only on one address.",
            fmt_count(shown.len() as u64)
        ),
        _ => format!(
            "{} {what} listening; {} on all interfaces, so other devices on your network may reach them.",
            fmt_count(shown.len() as u64),
            fmt_count(open as u64)
        ),
    };
    let mut list = table("net-ports", k).child(
        row()
            .w_full()
            .h(24.0)
            .px(12.0)
            .gap(12.0)
            .center_y()
            .child(head("Port", Some(56.0), k))
            .child(head("Protocol", Some(56.0), k))
            .child(head("Address", Some(130.0), k))
            .child(head("Reach", Some(110.0), k))
            .child(head("Process", None, k)),
    );
    for p in shown.iter().take(PORTS) {
        list = list.child(port_row(s, p, k));
    }
    if shown.len() > PORTS {
        list = list.child(
            text(format!(
                "and {} more",
                fmt_count((shown.len() - PORTS) as u64)
            ))
            .key("net-ports-more")
            .px(12.0)
            .py(6.0)
            .font(k.ty.small)
            .color(c.text_faint),
        );
    }
    col()
        .w_full()
        .gap(8.0)
        .child(
            text(summary)
                .key("net-listen-summary")
                .font(k.ty.small)
                .color(c.text_muted)
                .wrap(),
        )
        .child(list)
}
