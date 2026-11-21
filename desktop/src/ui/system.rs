//! The System page: the whole machine at a glance. CPU and load, memory pressure and where the
//! memory is, thermal state and battery, disks, network and listening ports, and the processes
//! using the most. Each part says when its numbers are from once that matters, and a part
//! Spectra doesn't read on this system is left out rather than shown blank.

use rune::{Color, Cursor, El, col, el, row, text};
use rune_kit::Kit;

use super::card::dot;
use super::chart::{Lane, Mark, lanes};
use super::detail::section;
use super::home::{callout, tile};
use crate::app::App;
use crate::findings::Severity;
use crate::procs::{Process, fmt_count, fmt_cpu, fmt_mem};
use crate::system::{Fail, Pressure, Section, ThermalState, counter_deltas, fmt_size};

/// Listening ports listed before "and N more".
const PORTS: usize = 60;
/// Processes in each top-consumer list.
const TOP: usize = 5;
const LABEL_W: f32 = 150.0;

pub fn page(s: &App, k: &Kit) -> El {
    let sys = &s.system;
    let mut page = col()
        .key("sys-page")
        .w_full()
        .padding(24.0)
        .gap(24.0)
        .child(section("CPU", cpu(s, k), k));
    if !sys.memory.unsupported() {
        page = page.child(section("Memory", memory(s, k), k));
    }
    if let Some(body) = thermal_power(s, k) {
        page = page.child(section("Thermal and power", body, k));
    }
    if !sys.storage.unsupported() {
        page = page.child(section("Disks", disks(s, k), k));
    }
    if !sys.network.unsupported() {
        page = page.child(section("Network", network(s, k), k));
    }
    if s.has_processes() {
        page = page.child(section("Top consumers", top(s, k), k));
    }
    page
}

/// What stands in for a part's numbers, or sits above them: still reading, why the first
/// reading failed, or when the numbers are from.
fn state<T>(s: &App, part: &Section<T>, key: &str, what: &str, k: &Kit) -> Option<El> {
    let c = &k.c;
    if part.waiting() {
        return Some(
            text(format!("Reading {what}…"))
                .key(format!("{key}-reading"))
                .font(k.ty.small)
                .color(c.text_faint),
        );
    }
    if part.last.is_none() {
        let why = part.error.as_ref().map_or("", Fail::why);
        return Some(callout(
            &format!("{key}-error"),
            &format!("Couldn't read {what}"),
            why,
            k,
        ));
    }
    let status = part.status(s.now)?;
    Some(
        text(status.clone())
            .key(format!("{key}-status"))
            .role("status")
            .font(k.ty.small)
            .color(c.text_muted)
            .truncate()
            .shrink()
            .min_w(0.0)
            .help_when_truncated(status),
    )
}

fn panel(key: &str, k: &Kit) -> El {
    col()
        .key(key.to_owned())
        .w_full()
        .gap(8.0)
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(k.c.layer_01)
        .border(1.0, k.c.border_weak)
}

fn fact(key: &str, label: &str, value: El, k: &Kit) -> El {
    row()
        .key(format!("sys-fact-{key}"))
        .w_full()
        .gap(12.0)
        .child(
            text(label.to_owned())
                .w(LABEL_W)
                .font(k.ty.small)
                .color(k.c.text_muted),
        )
        .child(col().grow(1.0).min_w(0.0).child(value))
}

fn value(v: impl Into<String>, k: &Kit) -> El {
    text(v.into())
        .font(k.ty.small.features("'tnum' 1"))
        .color(k.c.text_base)
        .wrap_anywhere()
}

/// A bar filled to `frac` in `color`.
fn gauge(key: &str, frac: f64, color: Color, k: &Kit) -> El {
    el().key(key.to_owned())
        .w_full()
        .h(8.0)
        .radius(4.0)
        .fill(k.c.layer_03)
        .child(
            el().w_frac(frac.clamp(0.0, 1.0) as f32)
                .h_full()
                .radius(4.0)
                .fill(color),
        )
}

fn pct(v: f64) -> String {
    format!("{v:.0}%")
}

fn counted(v: f64) -> String {
    fmt_count(v.max(0.0).round() as u64)
}

fn load_fmt(v: f64) -> String {
    format!("{v:.2}")
}

fn cpu(s: &App, k: &Kit) -> El {
    let sys = &s.system;
    let mut body = col().key("sys-cpu").w_full().gap(10.0);
    if let Some(n) = state(s, &sys.load, "sys-load", "the load", k) {
        body = body.child(n);
    }
    let cores = sys.load.last.as_ref().map_or(0, |l| l.cpus as usize);
    let cores = if cores > 0 { cores } else { s.cores };
    let mut tiles = row().w_full().flex_wrap().gaps(8.0, 8.0);
    if s.has_processes() {
        let cpu = s.stats.cpu;
        tiles = tiles.child(tile(
            "sys-cpu-use",
            "CPU in use",
            format!("{cpu:.0}%"),
            format!("about {:.1} of {cores} cores", cpu / 100.0),
            k,
        ));
    }
    if let Some(l) = &sys.load.last {
        tiles = tiles.child(tile(
            "sys-load1",
            "Load, last minute",
            format!("{:.2}", l.load1),
            format!("{:.1} per core", l.load1 / cores.max(1) as f64),
            k,
        ));
    }
    tiles = tiles.child(tile(
        "sys-cores",
        "Cores",
        cores.to_string(),
        "logical".into(),
        k,
    ));
    body = body.child(tiles);
    if !sys.load.unsupported() {
        let h = &sys.cpu_history;
        let mut all = Vec::new();
        // The CPU lane needs the process feed; without it there is no number to draw.
        if s.has_processes() {
            all.push(Lane {
                key: "sys-lane-cpu",
                label: "CPU in use",
                values: h.iter().map(|p| p.cpu.unwrap_or(0.0)).collect(),
                unit: pct,
                mark: Mark::Line,
            });
        }
        all.push(Lane {
            key: "sys-lane-load",
            label: "Load, last minute",
            values: h.iter().map(|p| p.load1).collect(),
            unit: load_fmt,
            mark: Mark::Line,
        });
        body = body.child(lanes("sys-cpu", h.len(), &all, None, k.c.text_accent, k));
    }
    body
}

fn pressure_ink(p: Pressure, k: &Kit) -> Color {
    match p {
        Pressure::Normal => k.c.success.fg,
        Pressure::Warning => k.c.warning.fg,
        Pressure::Critical => k.c.danger.fg,
        Pressure::Unknown => k.c.text_faint,
    }
}

fn memory(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let sys = &s.system;
    let mut body = col().key("sys-memory").w_full().gap(10.0);
    if let Some(n) = state(s, &sys.memory, "sys-mem", "memory", k) {
        body = body.child(n);
    }
    let Some(m) = &sys.memory.last else {
        return body;
    };
    let ink = pressure_ink(m.pressure, k);
    let in_use = match m.free_pct {
        Some(free) => 1.0 - free / 100.0,
        None if m.physical > 0.0 => m.used() / m.physical,
        None => 0.0,
    };
    let headline = match m.free_pct {
        Some(free) => format!("{} · {free:.0}% free for new work", m.pressure.label()),
        None => m.pressure.label().to_owned(),
    };
    let gauge_row = col()
        .w_full()
        .gap(6.0)
        .child(
            row()
                .w_full()
                .gap(8.0)
                .center_y()
                .child(text("Pressure").font(k.ty.small).color(c.text_muted))
                .child(el().size(8.0, 8.0).radius(4.0).fill(ink))
                .child(
                    text(headline)
                        .key("sys-pressure")
                        .font(k.ty.ui_medium)
                        .color(ink),
                ),
        )
        .child(gauge("sys-pressure-gauge", in_use, ink, k));
    let of = |part: f64| format!("{} of {}", fmt_size(part), fmt_size(m.physical));
    let mut facts = panel("sys-mem-facts", k)
        .child(gauge_row)
        .child(fact("used", "Used", value(of(m.used()), k), k))
        .child(fact("app", "App memory", value(fmt_size(m.app), k), k))
        .child(fact("wired", "Wired", value(fmt_size(m.wired), k), k))
        .child(fact(
            "file",
            "File-backed (cached files)",
            value(fmt_size(m.file_backed), k),
            k,
        ));
    let compressed = if m.compressed_from > 0.0 && m.ratio > 0.0 {
        format!(
            "{} holding {} ({:.1}× smaller)",
            fmt_size(m.compressed),
            fmt_size(m.compressed_from),
            m.ratio
        )
    } else {
        fmt_size(m.compressed)
    };
    facts = facts.child(fact("compressed", "Compressed", value(compressed, k), k));
    let swap = if m.swap_total > 0.0 {
        format!("{} of {}", fmt_size(m.swap_used), fmt_size(m.swap_total))
    } else if m.swap_used > 0.0 {
        fmt_size(m.swap_used)
    } else {
        "None".to_owned()
    };
    facts = facts.child(fact("swap", "Swap used", value(swap, k), k));
    if let Some((ins, outs, comp)) = sys.per_reading() {
        let line = format!(
            "{} swap-ins · {} swap-outs · {} compressions",
            fmt_count(ins as u64),
            fmt_count(outs as u64),
            fmt_count(comp as u64)
        );
        facts = facts.child(fact(
            "per-reading",
            "Since the last reading",
            value(line, k).key("sys-mem-per-reading"),
            k,
        ));
    }
    body = body.child(facts);
    let h = &sys.mem_history;
    let compressions: Vec<f64> = h.iter().map(|p| p.compressions).collect();
    let mut all = Vec::new();
    if m.free_pct.is_some() {
        all.push(Lane {
            key: "sys-lane-free",
            label: "Free for new work",
            values: h.iter().map(|p| p.free_pct).collect(),
            unit: pct,
            mark: Mark::Line,
        });
    }
    all.push(Lane {
        key: "sys-lane-compressed",
        label: "Compressed",
        values: h.iter().map(|p| p.compressed).collect(),
        unit: fmt_size,
        mark: Mark::Line,
    });
    all.push(Lane {
        key: "sys-lane-compressions",
        label: "Compressions per reading",
        values: counter_deltas(&compressions),
        unit: counted,
        mark: Mark::Bars,
    });
    all.push(Lane {
        key: "sys-lane-swap",
        label: "Swap used",
        values: h.iter().map(|p| p.swap_used).collect(),
        unit: fmt_size,
        mark: Mark::Line,
    });
    body.child(lanes("sys-mem", h.len(), &all, None, ink, k))
}

fn thermal_ink(t: ThermalState) -> Severity {
    match t {
        ThermalState::Nominal => Severity::Healthy,
        ThermalState::Warm => Severity::Info,
        ThermalState::Throttled => Severity::Warning,
        ThermalState::Unknown => Severity::Info,
    }
}

/// Thermal state, CPU speed limit and the battery: whichever of them Spectra reads here.
fn thermal_power(s: &App, k: &Kit) -> Option<El> {
    let sys = &s.system;
    let mut body = col().key("sys-thermal-power").w_full().gap(10.0);
    let mut any = false;
    if !sys.thermal.unsupported()
        && let Some(n) = state(s, &sys.thermal, "sys-thermal", "the thermal state", k)
    {
        body = body.child(n);
        any = true;
    }
    if !sys.power.unsupported()
        && let Some(n) = state(s, &sys.power, "sys-power", "the battery", k)
    {
        body = body.child(n);
        any = true;
    }
    let mut facts = panel("sys-thermal-facts", k);
    let mut rows = 0;
    if let Some(t) = sys.thermal_state() {
        let mut v = row()
            .gap(8.0)
            .center_y()
            .child(dot(thermal_ink(t), k))
            .child(value(t.label(), k).key("sys-thermal-state"));
        if let Some(e) = sys
            .thermal
            .last
            .as_ref()
            .map(|t| t.evidence.clone())
            .filter(|e| !e.is_empty())
        {
            v = v.child(
                text(e)
                    .font(k.ty.small)
                    .color(k.c.text_muted)
                    .wrap()
                    .shrink()
                    .min_w(0.0),
            );
        }
        facts = facts.child(fact("thermal", "Thermal state", v, k));
        rows += 1;
    }
    if let Some(limit) = sys.speed_limit() {
        facts = facts.child(fact(
            "speed-limit",
            "CPU speed limit",
            value(format!("{limit:.0}%"), k),
            k,
        ));
        rows += 1;
    }
    if let Some(b) = sys.power.last.as_ref().and_then(|p| p.battery.as_ref()) {
        let source = if b.on_battery {
            "on battery"
        } else {
            "on power adapter"
        };
        let line = match b.pct {
            Some(p) => format!("{p:.0}% · {source}"),
            None => source[..1].to_uppercase() + &source[1..],
        };
        facts = facts.child(fact(
            "battery",
            "Battery",
            value(line, k).key("sys-battery"),
            k,
        ));
        rows += 1;
    }
    if rows > 0 {
        body = body.child(facts);
        any = true;
    }
    any.then_some(body)
}

fn disks(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let sys = &s.system;
    let mut body = col().key("sys-disks").w_full().gap(10.0);
    if let Some(n) = state(s, &sys.storage, "sys-storage", "the disks", k) {
        body = body.child(n);
    }
    if sys.storage.last.is_none() {
        return body;
    }
    let vols = sys.volumes();
    if vols.is_empty() {
        return body.child(
            text("Spectra listed no disks to show.")
                .key("sys-disks-none")
                .font(k.ty.small)
                .color(c.text_muted),
        );
    }
    let mut list = panel("sys-volumes", k).gap(12.0);
    for v in vols {
        let used = v.used_pct();
        let ink = if used >= crate::system_findings::VOLUME_CRITICAL {
            c.danger.fg
        } else if used >= crate::system_findings::VOLUME_WARN {
            c.warning.fg
        } else {
            c.text_accent
        };
        let key = format!("sys-vol-{}", v.mount.replace('/', ":"));
        let label = v.label();
        let mut name = row().gap(8.0).center_y().min_w(0.0).shrink().child(
            text(label.clone())
                .font(k.ty.small_medium)
                .color(c.text_base),
        );
        if label != v.mount {
            name = name.child(
                text(v.mount.clone())
                    .font(k.ty.text_xsmall)
                    .color(c.text_faint)
                    .truncate()
                    .shrink()
                    .min_w(0.0),
            );
        }
        let figures = format!(
            "{} used · {} free of {} · {used:.0}%",
            fmt_size(v.used),
            fmt_size(v.avail),
            fmt_size(v.used + v.avail)
        );
        list = list.child(
            col()
                .key(key.clone())
                .w_full()
                .gap(6.0)
                .child(
                    row()
                        .w_full()
                        .gap(12.0)
                        .center_y()
                        .child(name)
                        .child(el().grow(1.0))
                        .child(
                            text(figures)
                                .key(format!("{key}-figures"))
                                .font(k.ty.small.features("'tnum' 1"))
                                .color(c.text_muted),
                        ),
                )
                .child(gauge(&format!("{key}-bar"), used / 100.0, ink, k)),
        );
    }
    body.child(list)
}

fn network(s: &App, k: &Kit) -> El {
    let sys = &s.system;
    let mut body = col().key("sys-network").w_full().gap(10.0);
    if let Some(n) = state(s, &sys.network, "sys-net", "the network", k) {
        body = body.child(n);
    }
    let Some(n) = &sys.network.last else {
        return body;
    };
    let mut facts = panel("sys-net-facts", k);
    if !n.iface.is_empty() {
        let route = if n.gateway.is_empty() {
            n.iface.clone()
        } else {
            format!("{} via {}", n.iface, n.gateway)
        };
        facts = facts.child(fact("route", "Default route", value(route, k), k));
    }
    if !n.dns.is_empty() {
        facts = facts.child(fact("dns", "DNS servers", value(n.dns.join(", "), k), k));
    }
    let vpn = match (n.vpn, n.vpn_ifaces.is_empty()) {
        (true, false) => format!("On ({})", n.vpn_ifaces.join(", ")),
        (true, true) => "On".to_owned(),
        (false, _) => "Off".to_owned(),
    };
    facts = facts
        .child(fact("vpn", "VPN", value(vpn, k), k))
        .child(fact(
            "connections",
            "Connections",
            value(format!("{} established", fmt_count(n.established)), k),
            k,
        ))
        .child(fact(
            "listening",
            "Listening",
            value(format!("{} ports", fmt_count(n.ports.len() as u64)), k)
                .key("sys-listening-count"),
            k,
        ));
    body = body.child(facts);
    if !n.ports.is_empty() {
        body = body.child(ports(s, k));
    }
    body
}

/// The listening ports, each with the process holding it: a link to its page when the process
/// feed lists that pid.
fn ports(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let Some(n) = &s.system.network.last else {
        return col();
    };
    let figures = k.ty.small.features("'tnum' 1");
    let head = |t: &str, w: Option<f32>| {
        let t = text(t.to_owned())
            .font(k.ty.text_xsmall)
            .color(c.text_faint);
        match w {
            Some(w) => t.w(w),
            None => t.grow(1.0).min_w(0.0),
        }
    };
    let mut table = col()
        .key("sys-ports")
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
                .child(head("Port", Some(56.0)))
                .child(head("Protocol", Some(56.0)))
                .child(head("Address", Some(150.0)))
                .child(head("Process", None)),
        );
    for p in n.ports.iter().take(PORTS) {
        let key = format!("sys-port-{}-{}-{}", p.port, p.proto, p.pid.unwrap_or(0));
        let listed = p.pid.and_then(|pid| s.process(pid));
        let who = match (listed, p.pid) {
            (Some(proc), Some(pid)) => text(format!("{} · {pid}", proc.name()))
                .key(format!("{key}-open"))
                .role("link")
                .label(format!("Open {}", proc.name()))
                .cursor(Cursor::Pointer)
                .font(k.ty.small)
                .color(c.text_accent)
                .truncate()
                .shrink()
                .min_w(0.0)
                .on_press(move |s: &mut App| s.open(pid)),
            (None, Some(pid)) => text(if p.command.is_empty() {
                format!("pid {pid}")
            } else {
                format!("{} · {pid}", p.command)
            })
            .font(k.ty.small)
            .color(c.text_muted)
            .truncate()
            .shrink()
            .min_w(0.0),
            (_, None) => text(if p.command.is_empty() {
                "Unknown".to_owned()
            } else {
                p.command.clone()
            })
            .font(k.ty.small)
            .color(c.text_muted)
            .truncate()
            .shrink()
            .min_w(0.0),
        };
        table = table.child(
            row()
                .key(key)
                .w_full()
                .h(26.0)
                .px(12.0)
                .gap(12.0)
                .center_y()
                .child(
                    text(p.port.to_string())
                        .w(56.0)
                        .font(figures)
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
                        .w(150.0)
                        .font(k.ty.small)
                        .color(c.text_muted)
                        .truncate(),
                )
                .child(row().grow(1.0).min_w(0.0).child(who)),
        );
    }
    if n.ports.len() > PORTS {
        table = table.child(
            text(format!(
                "and {} more",
                fmt_count((n.ports.len() - PORTS) as u64)
            ))
            .key("sys-ports-more")
            .px(12.0)
            .py(6.0)
            .font(k.ty.small)
            .color(c.text_faint),
        );
    }
    table
}

/// The busiest processes by CPU and the largest by memory, each opening its page.
fn top(s: &App, k: &Kit) -> El {
    let mut by_cpu: Vec<&Process> = s.procs.iter().collect();
    by_cpu.sort_by(|a, b| b.cpu_pct.total_cmp(&a.cpu_pct).then(a.pid.cmp(&b.pid)));
    let mut by_mem: Vec<&Process> = s.procs.iter().collect();
    by_mem.sort_by(|a, b| b.rss_kib.total_cmp(&a.rss_kib).then(a.pid.cmp(&b.pid)));
    row()
        .key("sys-top")
        .w_full()
        .flex_wrap()
        .gaps(16.0, 16.0)
        .items_start()
        .child(top_list(
            "cpu",
            "By CPU",
            &by_cpu[..by_cpu.len().min(TOP)],
            |p| fmt_cpu(p.cpu_pct),
            k,
        ))
        .child(top_list(
            "mem",
            "By memory",
            &by_mem[..by_mem.len().min(TOP)],
            |p| fmt_mem(p.rss_kib),
            k,
        ))
}

fn top_list(
    id: &str,
    title: &str,
    procs: &[&Process],
    figure: fn(&Process) -> String,
    k: &Kit,
) -> El {
    let c = &k.c;
    let figures = k.ty.small.features("'tnum' 1");
    let mut list = col()
        .key(format!("sys-top-{id}"))
        .grow(1.0)
        .min_w(280.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .py(4.0)
        .child(
            text(title.to_owned())
                .px(12.0)
                .py(4.0)
                .font(k.ty.text_xsmall)
                .color(c.text_faint),
        );
    for p in procs {
        let pid = p.pid;
        let r = row()
            .key(format!("sys-top-{id}-{pid}"))
            .role("link")
            .label(format!("Open {}", p.name()))
            .cursor(Cursor::Pointer)
            .w_full()
            .h(28.0)
            .px(12.0)
            .gap(12.0)
            .center_y()
            .on_press(move |s: &mut App| s.open(pid))
            .child(
                text(p.name().into_owned())
                    .font(k.ty.small)
                    .color(c.text_base)
                    .truncate()
                    .shrink()
                    .min_w(0.0),
            )
            .child(el().grow(1.0))
            .child(
                text(figure(p))
                    .w(72.0)
                    .align(rune::Align::End)
                    .font(figures)
                    .color(c.text_base),
            )
            .child(
                text(pid.to_string())
                    .w(56.0)
                    .align(rune::Align::End)
                    .font(figures)
                    .color(c.text_muted),
            );
        list = list.child(k.interactive(r));
    }
    list
}
