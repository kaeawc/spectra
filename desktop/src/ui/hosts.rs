//! Other Macs: the Hosts page (the list, each host's health, add and remove) and one host's
//! page (its processes grouped as on this Mac, and its JVMs), read-only, through Spectra Remote.

use rune::{Cursor, El, col, el, row, text};
use rune_kit::{Button, ButtonVariant, Kit, SidebarRow, TextInput};

use super::home::callout;
use crate::app::{App, fmt_ago};
use crate::data::{Jvm, fmt_kib};
use crate::groups::{Group, Nav, Sort, SortKey, group, matches_query};
use crate::procs::{Process, fmt_count, fmt_cpu, fmt_mem};
use crate::remote::{HostState, Kind, OP_JVMS, OP_PROCESSES};

/// Group rows drawn before "narrow it with the search".
const SHOWN: usize = 150;

fn small(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}

fn faint(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_faint)
}

fn sentence(why: &str) -> String {
    let w = why.trim();
    if w.ends_with('.') {
        w.to_owned()
    } else {
        format!("{w}.")
    }
}

/// One line on how a host is doing, for the sidebar, the list and its page.
pub fn status_line(s: &App, h: &HostState) -> String {
    if s.hosts.busy(h.id, Kind::Health) && h.health.get().is_none() {
        return "Connecting…".into();
    }
    match (h.health.get(), &h.health.error) {
        (_, Some(e)) if h.health.get().is_none() => format!("Unreachable · {}", sentence(e)),
        (Some(health), err) => {
            let mut line = format!(
                "Connected · Spectra {}",
                or_unknown(&health.spectra_version)
            );
            if let Some(at) = h.health.at() {
                line.push_str(&format!(" · checked {} ago", fmt_ago(s.now - at)));
            }
            if let Some(e) = err {
                line.push_str(&format!(" · last check failed: {}", sentence(e)));
            }
            line
        }
        (None, _) => "Not checked yet".into(),
    }
}

fn or_unknown(v: &str) -> &str {
    if v.is_empty() { "version unknown" } else { v }
}

pub fn sidebar_rows(s: &App, k: &Kit) -> Vec<El> {
    s.hosts
        .list
        .iter()
        .map(|h| {
            let id = h.id;
            let mut r = SidebarRow::new(format!("   {}", h.host.name))
                .height(28.0)
                .selected(s.nav == Nav::Host(id) && !s.on_page())
                .on_select(move |s: &mut App, _| s.show(Nav::Host(id)));
            if let Some((procs, _)) = &h.procs.value {
                r = r.detail(fmt_count(procs.len() as u64));
            }
            r.build(k).key(format!("host-row-{id}"))
        })
        .collect()
}

fn missing_notice(s: &App, k: &Kit) -> Option<El> {
    let why = s.hosts.missing.as_ref()?;
    Some(callout(
        "hosts-missing",
        "Spectra Remote isn't installed on this Mac",
        &format!(
            "{} Other Macs are reached through Spectra Remote: its controller here, and spectra-remote-agent serve-tsnet on each Mac you want to see, on the same tailnet.",
            sentence(why)
        ),
        k,
    ))
}

/// The Hosts page: what this is, the list with each host's health, and a form to add one.
pub fn page(s: &App, k: &Kit) -> El {
    let intro = small(
        "Processes and JVMs on your other Macs, read-only. Each Mac runs Spectra Remote's agent (spectra-remote-agent serve-tsnet) on your tailnet; this Mac asks it through the spectra-remote controller, and the agent decides who may ask. Nothing here changes anything on another Mac. Health is checked every 30 s while this page is open.",
        k,
    );
    let mut page = col()
        .key("hosts-page")
        .w_full()
        .padding(24.0)
        .gap(16.0)
        .child(intro);
    if let Some(n) = missing_notice(s, k) {
        page = page.child(n);
    }
    if let Some(e) = &s.hosts.file_error {
        page = page.child(callout("hosts-file-error", "The list of Macs", e, k));
    }
    if s.hosts.list.is_empty() {
        page = page.child(
            small(
                "No other Macs yet. Add one by the name it has on your tailnet, like work-mac (port 7878 unless its agent listens elsewhere).",
                k,
            )
            .key("hosts-empty"),
        );
    } else {
        let mut list = col().key("hosts-list").w_full().gap(8.0);
        for h in &s.hosts.list {
            list = list.child(host_card(s, h, k));
        }
        page = page.child(list);
    }
    page.child(add_form(s, k))
}

fn host_card(s: &App, h: &HostState, k: &Kit) -> El {
    let id = h.id;
    let key = format!("host-{id}");
    let status = status_line(s, h);
    let healthy = h.health.get().is_some() && h.health.error.is_none();
    let failed = h.health.get().is_none() && h.health.error.is_some();
    let ink = if healthy {
        k.c.success.fg
    } else if failed {
        k.c.danger.fg
    } else {
        k.c.text_muted
    };
    let open = Button::new("Open")
        .variant(ButtonVariant::Outline)
        .help("Shows this Mac's processes and JVMs")
        .on_press(move |s: &mut App| s.show(Nav::Host(id)))
        .build(k)
        .key(format!("{key}-open"));
    let remove = Button::new("Remove")
        .variant(ButtonVariant::GhostMuted)
        .help("Takes it off this list; nothing changes on that Mac")
        .on_press(move |s: &mut App| s.remove_host(id))
        .build(k)
        .key(format!("{key}-remove"));
    let mut lines = col()
        .gap(2.0)
        .grow(1.0)
        .min_w(0.0)
        .child(
            row()
                .gap(8.0)
                .center_y()
                .child(
                    text(h.host.name.clone())
                        .key(format!("{key}-name"))
                        .font(k.ty.ui_medium)
                        .color(k.c.text_base),
                )
                .child(faint(h.host.target.clone(), k)),
        )
        .child(
            text(status)
                .key(format!("{key}-status"))
                .font(k.ty.small)
                .color(ink)
                .wrap(),
        );
    if let Some(health) = h.health.get() {
        let mut offers = Vec::new();
        if !health.agent_version.is_empty() {
            offers.push(format!("agent {}", health.agent_version));
        }
        offers.push(
            if health.offers(OP_PROCESSES) {
                "processes"
            } else {
                "no process list"
            }
            .to_owned(),
        );
        if health.offers(OP_JVMS) {
            offers.push("JVMs".into());
        }
        lines = lines.child(faint(offers.join(" · "), k).key(format!("{key}-offers")));
    }
    row()
        .key(key)
        .w_full()
        .gap(12.0)
        .center_y()
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(k.c.layer_01)
        .border(1.0, k.c.border_weak)
        .child(lines)
        .child(open)
        .child(remove)
}

fn add_form(s: &App, k: &Kit) -> El {
    let h = &s.hosts;
    let name = TextInput::new(&h.name)
        .placeholder("Name (optional)")
        .label("Name")
        .width(Some(180.0))
        .on_edit(|s: &mut App| &mut s.hosts.name)
        .build(k)
        .key("hosts-add-name");
    let target = TextInput::new(&h.target)
        .placeholder("work-mac or work-mac:7878")
        .label("Tailnet name")
        .width(Some(260.0))
        .on_edit(|s: &mut App| &mut s.hosts.target)
        .build(k)
        .key("hosts-add-target");
    let add = Button::new("Add")
        .variant(ButtonVariant::Outline)
        .disabled(h.target.text.trim().is_empty())
        .on_press(|s: &mut App| s.add_host())
        .build(k)
        .key("hosts-add");
    let mut form = col()
        .key("hosts-add-form")
        .w_full()
        .gap(8.0)
        .child(text("Add a Mac").font(k.ty.ui_medium).color(k.c.text_base));
    form = form.child(
        row()
            .gap(12.0)
            .center_y()
            .child(name)
            .child(target)
            .child(add),
    );
    if let Some(e) = &h.add_error {
        form = form.child(
            text(e.clone())
                .key("hosts-add-error")
                .role("alert")
                .font(k.ty.small)
                .color(k.c.danger.fg)
                .wrap(),
        );
    }
    form
}

/// One other Mac: its health, then its processes and JVMs, read-only.
pub fn host_page(s: &App, h: &HostState, k: &Kit) -> El {
    let id = h.id;
    let refresh = Button::new("Refresh")
        .variant(ButtonVariant::Outline)
        .disabled(s.hosts.calling())
        .help("Asks this Mac again now")
        .on_press(move |s: &mut App| s.refresh_host(id))
        .build(k)
        .key("host-refresh");
    let header = row()
        .key("host-header")
        .w_full()
        .gap(12.0)
        .center_y()
        .child(
            col()
                .gap(2.0)
                .grow(1.0)
                .min_w(0.0)
                .child(
                    text(status_line(s, h))
                        .key("host-status")
                        .role("status")
                        .font(k.ty.small)
                        .color(k.c.text_base)
                        .wrap(),
                )
                .child(faint(
                    format!(
                        "{} · through Spectra Remote · read-only: no Quit, tools or logs on another Mac",
                        h.host.target
                    ),
                    k,
                )
                .key("host-readonly")),
        )
        .child(refresh);
    let mut page = col()
        .key("host-page")
        .w_full()
        .padding(24.0)
        .gap(16.0)
        .child(header);
    if let Some(n) = missing_notice(s, k) {
        page = page.child(n);
    }
    match h.offers(OP_PROCESSES) {
        None if h.health.error.is_some() => {
            return page.child(small(
                "Check that the Mac is on, on your tailnet, runs spectra-remote-agent serve-tsnet, and that its allowlist lets you in. Refresh tries again.",
                k,
            ).key("host-unreachable-hint"));
        }
        None => {
            return page
                .child(faint("Asking what this Mac's agent offers…", k).key("host-connecting"));
        }
        Some(false) => {
            page = page.child(callout(
                "host-unsupported",
                "This Mac's agent can't list processes yet",
                "Its Spectra Remote agent doesn't offer process.list. On that Mac, run a spectra-remote-agent that has it and start it with --allow-processes (process lists show every command line, so the agent keeps them off unless asked); its health is shown above.",
                k,
            ));
        }
        Some(true) => page = page.child(processes(s, h, k)),
    }
    if h.offers(OP_JVMS) == Some(true) {
        page = page.child(jvms(s, h, k));
    }
    page
}

fn stale(s: &App, at: Option<f64>, error: &Option<String>, every: f64) -> Option<String> {
    let at = at?;
    let age = s.now - at;
    match error {
        Some(e) => Some(format!(
            "Updated {} ago · last reading failed: {}",
            fmt_ago(age),
            sentence(e)
        )),
        None if age > every * 3.0 => Some(format!("Updated {} ago", fmt_ago(age))),
        None => None,
    }
}

fn processes(s: &App, h: &HostState, k: &Kit) -> El {
    let mut sec = col()
        .key("host-procs")
        .w_full()
        .gap(8.0)
        .child(text("Processes").font(k.ty.ui_medium).color(k.c.text_base));
    let Some((procs, at)) = &h.procs.value else {
        return sec.child(match &h.procs.error {
            Some(e) => small(format!("Couldn't read its processes: {}", sentence(e)), k)
                .key("host-procs-error"),
            None => faint("Reading its processes…", k).key("host-procs-reading"),
        });
    };
    if let Some(line) = stale(s, Some(*at), &h.procs.error, Kind::Procs.every()) {
        sec = sec.child(
            text(line)
                .key("host-procs-stale")
                .font(k.ty.small)
                .color(k.c.warning.fg)
                .wrap(),
        );
    }
    let cpu: f64 = procs.iter().map(|p| p.cpu_pct).sum();
    let mem: f64 = procs.iter().map(|p| p.rss_kib).sum();
    let q = s.hosts.search();
    let kept: Vec<&Process> = procs.iter().filter(|p| matches_query(p, &q)).collect();
    let groups = group(
        &kept,
        Sort {
            key: SortKey::Cpu,
            descending: true,
        },
    );
    let search = TextInput::new(&s.hosts.query)
        .placeholder("Search by name, pid, app or path")
        .label("Search this Mac's processes")
        .leading_icon(k.glyph.search)
        .clear_button()
        .width(Some(300.0))
        .on_edit(|s: &mut App| &mut s.hosts.query)
        .build(k)
        .key("host-search");
    let summary = format!(
        "{} processes · {} CPU · {} memory · read {} ago",
        fmt_count(procs.len() as u64),
        fmt_cpu(cpu),
        fmt_mem(mem),
        fmt_ago(s.now - at)
    );
    sec = sec.child(
        row()
            .w_full()
            .gap(12.0)
            .center_y()
            .child(search)
            .child(el().grow(1.0))
            .child(
                text(summary)
                    .key("host-summary")
                    .font(k.ty.small)
                    .color(k.c.text_muted),
            ),
    );
    if groups.is_empty() {
        return sec.child(
            small(
                format!(
                    "No process matches \"{}\". Clear the search to see everything.",
                    s.hosts.query.text.trim()
                ),
                k,
            )
            .key("host-no-match"),
        );
    }
    let by_pid = |pid: u32| procs.iter().find(|p| p.pid == pid);
    let mut table = col()
        .key("host-table")
        .w_full()
        .gap(2.0)
        .child(header_row(k));
    for g in groups.iter().take(SHOWN) {
        table = table.child(group_row(s, g, k));
        if g.pids.len() > 1 && s.hosts.open.contains(&g.id) {
            for p in g.pids.iter().filter_map(|pid| by_pid(*pid)) {
                table = table.child(member_row(p, k));
            }
        }
    }
    if groups.len() > SHOWN {
        table = table.child(
            small(
                format!(
                    "Showing the busiest {} of {} rows. Search to narrow the list.",
                    SHOWN,
                    fmt_count(groups.len() as u64)
                ),
                k,
            )
            .key("host-more"),
        );
    }
    sec.child(table)
}

fn cells(name: El, kind: String, runtime: String, cpu: String, mem: String, k: &Kit) -> El {
    let num = |t: String| {
        text(t)
            .font(k.ty.small.features("'tnum' 1"))
            .color(k.c.text_base)
            .w(90.0)
    };
    row()
        .w_full()
        .gap(12.0)
        .center_y()
        .child(name.grow(1.0).min_w(0.0))
        .child(
            text(kind)
                .font(k.ty.small)
                .color(k.c.text_muted)
                .w(110.0)
                .truncate(),
        )
        .child(
            text(runtime)
                .font(k.ty.small)
                .color(k.c.text_muted)
                .w(80.0)
                .truncate(),
        )
        .child(num(cpu))
        .child(num(mem))
}

fn header_row(k: &Kit) -> El {
    let h = |t: &str| t.to_owned();
    cells(
        text("Name").font(k.ty.small_medium).color(k.c.text_muted),
        h("Kind"),
        h("Runtime"),
        h("CPU"),
        h("Memory"),
        k,
    )
    .key("host-table-header")
    .pb(4.0)
}

fn group_row(s: &App, g: &Group, k: &Kit) -> El {
    let many = g.pids.len() > 1;
    let open = many && s.hosts.open.contains(&g.id);
    let mut label = String::new();
    if many {
        label.push_str(if open { "▾ " } else { "▸ " });
    }
    label.push_str(&g.name);
    if many {
        label.push_str(&format!(" ({})", g.pids.len()));
    }
    let mut name = row().gap(8.0).center_y().child(
        text(label)
            .font(k.ty.small_medium)
            .color(k.c.text_base)
            .truncate(),
    );
    if let Some(hint) = &g.hint {
        name = name.child(faint(hint.clone(), k).truncate());
    }
    let key = format!("host-group-{}", g.id);
    let r = cells(
        name,
        g.role.label().to_owned(),
        g.runtime.label().to_owned(),
        fmt_cpu(g.cpu),
        fmt_mem(g.rss_kib),
        k,
    )
    .key(key)
    .py(3.0);
    if !many {
        return r;
    }
    let id = g.id.clone();
    r.role("button")
        .label(format!(
            "{} {}",
            if open { "Close" } else { "Open" },
            g.name
        ))
        .cursor(Cursor::Pointer)
        .on_press(move |s: &mut App| s.toggle_host_group(&id))
}

fn member_row(p: &Process, k: &Kit) -> El {
    let name = row()
        .gap(8.0)
        .pl(24.0)
        .center_y()
        .child(
            text(p.name().into_owned())
                .font(k.ty.small)
                .color(k.c.text_base)
                .truncate(),
        )
        .child(faint(format!("pid {} · {}", p.pid, p.user), k).truncate());
    cells(
        name,
        p.role.label().to_owned(),
        p.runtime.label().to_owned(),
        fmt_cpu(p.cpu_pct),
        fmt_mem(p.rss_kib),
        k,
    )
    .key(format!("host-proc-{}", p.pid))
    .py(2.0)
}

fn jvms(s: &App, h: &HostState, k: &Kit) -> El {
    let mut sec = col()
        .key("host-jvms")
        .w_full()
        .gap(8.0)
        .child(text("JVMs").font(k.ty.ui_medium).color(k.c.text_base));
    let Some((jvms, at)) = &h.jvms.value else {
        return sec.child(match &h.jvms.error {
            Some(e) => {
                small(format!("Couldn't read its JVMs: {}", sentence(e)), k).key("host-jvms-error")
            }
            None if s.hosts.busy(h.id, Kind::Jvms) => {
                faint("Reading its JVMs… a reading can take about 20 s.", k)
                    .key("host-jvms-reading")
            }
            None => faint("JVMs are read after the processes.", k).key("host-jvms-waiting"),
        });
    };
    if let Some(line) = stale(s, Some(*at), &h.jvms.error, Kind::Jvms.every()) {
        sec = sec.child(
            text(line)
                .key("host-jvms-stale")
                .font(k.ty.small)
                .color(k.c.warning.fg)
                .wrap(),
        );
    }
    if jvms.is_empty() {
        return sec.child(small("No JVMs are running on this Mac.", k).key("host-jvms-none"));
    }
    for j in jvms {
        sec = sec.child(jvm_row(j, k));
    }
    sec
}

fn jvm_row(j: &Jvm, k: &Kit) -> El {
    let (used, max) = (j.heap_used_kib(), j.heap_max_kib());
    let share = if max > 0.0 { used / max } else { 0.0 };
    let heap = if max > 0.0 {
        format!(
            "heap {} of {} ({:.0}%)",
            fmt_kib(used),
            fmt_kib(max),
            share * 100.0
        )
    } else {
        format!("heap {}", fmt_kib(used))
    };
    let g = j.gc.clone().unwrap_or_default();
    let detail = format!(
        "pid {} · JDK {} {} · {} threads · {} young and {} full GCs, {:.1} s in GC",
        j.pid, j.jdk_vendor, j.jdk_version, j.thread_count, g.ygc, g.fgc, g.gct
    );
    row()
        .key(format!("host-jvm-{}", j.pid))
        .w_full()
        .gap(12.0)
        .center_y()
        .py(8.0)
        .px(12.0)
        .radius(k.radius.md)
        .fill(k.c.layer_01)
        .border(1.0, k.c.border_weak)
        .child(
            col()
                .gap(2.0)
                .grow(1.0)
                .min_w(0.0)
                .child(
                    text(j.name())
                        .font(k.ty.small_medium)
                        .color(k.c.text_base)
                        .truncate(),
                )
                .child(faint(detail, k).truncate()),
        )
        .child(
            text(heap)
                .key(format!("host-jvm-{}-heap", j.pid))
                .font(k.ty.small.features("'tnum' 1"))
                .color(if share >= 0.85 {
                    k.c.warning.fg
                } else {
                    k.c.text_base
                }),
        )
}
