//! An app's page: its processes together, how busy they've been, its network and energy while
//! the page is open, and its bundle as Spectra reads it: what it's built with, how it's signed,
//! what it may do and has been allowed to, what it installs and what it keeps on disk.

use rune::{El, col, row, text};
use rune_kit::{Button, ButtonVariant, Kit, Spinner};

use super::chart::{Lane, Mark, count, lanes};
use super::detail::section;
use super::process::{children, fact, value};
use super::tools::failed;
use crate::app::App;
use crate::bundle::{Bundle, permission_label};
use crate::inspect::BundleState;
use crate::live::{fmt_rate, fmt_score};
use crate::procs::{fmt_count, fmt_cpu, fmt_mem};
use crate::tools::fmt_bytes;

pub fn page(s: &App, path: &str, k: &Kit) -> El {
    let procs = s.app_procs(path);
    let mut page = col().key("ap-page").w_full().padding(24.0).gap(24.0).child(header(s, path, k));
    if procs.is_empty() {
        page = page.child(
            text("Not running now.")
                .key("ap-not-running")
                .font(k.ty.small)
                .color(k.c.text_muted),
        );
    } else {
        page = page
            .child(section("Activity, all its processes together", activity(s, path, k), k))
            .child(section("Network and energy", live(s, "ap", k), k))
            .child(super::logs::panel(s, k))
            .child(section(
                &format!("Processes ({})", fmt_count(procs.len() as u64)),
                children("ap", &procs, k),
                k,
            ));
    }
    match &s.inspect.bundle {
        Some(BundleState::Loaded(b)) => about(page, b, k),
        Some(BundleState::Failed(why)) => page.child(section(
            "Bundle",
            col()
                .w_full()
                .gap(8.0)
                .child(failed("ap-bundle-error", &format!("Couldn't read the bundle: {why}"), k))
                .child(
                    row().child(
                        Button::new("Try again")
                            .variant(ButtonVariant::Outline)
                            .on_press(|s: &mut App| s.read_bundle())
                            .build(k)
                            .key("ap-bundle-retry"),
                    ),
                ),
            k,
        )),
        Some(BundleState::Loading) | None => page.child(section(
            "Bundle",
            row()
                .key("ap-bundle-loading")
                .gap(8.0)
                .center_y()
                .child(Spinner::new().size(12.0).build(k))
                .child(text(format!("Reading {}…", file_name(path))).font(k.ty.small).color(k.c.text_muted)),
            k,
        )),
    }
}

fn file_name(path: &str) -> &str {
    path.trim_end_matches('/').rsplit('/').next().unwrap_or(path)
}

fn header(s: &App, path: &str, k: &Kit) -> El {
    let c = &k.c;
    let procs = s.app_procs(path);
    let (cpu, mem, threads) = procs
        .iter()
        .fold((0.0, 0.0, 0), |(c, m, t), p| (c + p.cpu_pct, m + p.rss_kib, t + p.thread_count));
    let meta = match procs.len() {
        0 => "not running".to_owned(),
        n => format!(
            "{} {} · {} CPU · {} · {} threads",
            fmt_count(n as u64),
            if n == 1 { "process" } else { "processes" },
            fmt_cpu(cpu),
            fmt_mem(mem),
            fmt_count(threads)
        ),
    };
    col()
        .key("ap-header")
        .w_full()
        .gap(6.0)
        .child(
            row()
                .gap(10.0)
                .center_y()
                .flex_wrap()
                .child(text(s.app_name(path)).key("ap-name").font(k.ty.body_medium).color(c.text_base))
                .child(text(meta).key("ap-meta").font(k.ty.small.features("'tnum' 1")).color(c.text_muted)),
        )
        .child(
            text(path.to_owned())
                .key("ap-path")
                .font(k.ty.small)
                .color(c.text_faint)
                .selectable(true)
                .selection_color(k.selection())
                .wrap_anywhere(),
        )
}

fn activity(s: &App, path: &str, k: &Kit) -> El {
    let h = s.app_history(path);
    let all = [
        Lane { key: "ap-lane-cpu", label: "CPU", values: h.iter().map(|x| x.cpu).collect(), unit: fmt_cpu, mark: Mark::Line },
        Lane { key: "ap-lane-mem", label: "Memory", values: h.iter().map(|x| x.rss_kib).collect(), unit: fmt_mem, mark: Mark::Line },
        Lane {
            key: "ap-lane-threads",
            label: "Threads",
            values: h.iter().map(|x| x.threads as f64).collect(),
            unit: count,
            mark: Mark::Line,
        },
    ];
    lanes("ap", h.len(), &all, None, k.c.text_accent, k)
}

/// The open page's network in and out and its energy impact, read every few seconds while
/// it's open; each says why when Spectra couldn't read it.
pub fn live(s: &App, prefix: &str, k: &Kit) -> El {
    let c = &k.c;
    let i = &s.inspect;
    let mut body = col().key(format!("{prefix}-live")).w_full().gap(12.0);
    let note = |key: String, words: String| text(words).key(key).font(k.ty.small).color(c.text_muted).wrap();
    match &i.net.off {
        Some(why) => body = body.child(note(format!("{prefix}-net-off"), format!("Network: {why}"))),
        None if i.net.points.len() < 2 => {
            body = body.child(note(format!("{prefix}-net-waiting"), "Measuring network traffic…".into()));
        }
        None => {
            let net = [
                Lane {
                    key: "live-lane-in",
                    label: "Network in",
                    values: i.net.points.iter().map(|p| p.in_bps).collect(),
                    unit: fmt_rate,
                    mark: Mark::Line,
                },
                Lane {
                    key: "live-lane-out",
                    label: "Network out",
                    values: i.net.points.iter().map(|p| p.out_bps).collect(),
                    unit: fmt_rate,
                    mark: Mark::Line,
                },
            ];
            body = body.child(lanes(&format!("{prefix}-net"), i.net.points.len(), &net, None, c.text_accent, k));
        }
    }
    match &i.energy.off {
        Some(why) => body = body.child(note(format!("{prefix}-energy-off"), format!("Energy impact: {why}"))),
        None if i.energy.points.len() < 2 => {
            body = body.child(note(format!("{prefix}-energy-waiting"), "Measuring energy impact…".into()));
        }
        None => {
            let energy = [Lane {
                key: "live-lane-energy",
                label: "Energy impact",
                values: i.energy.points.iter().copied().collect(),
                unit: fmt_score,
                mark: Mark::Line,
            }];
            body = body.child(lanes(&format!("{prefix}-energy"), i.energy.points.len(), &energy, None, c.text_accent, k));
        }
    }
    body.child(
        text("Read only while this page is open, one reading at a time.")
            .font(k.ty.text_xsmall)
            .color(c.text_faint),
    )
}

fn facts(key: &str, k: &Kit) -> El {
    let c = &k.c;
    col()
        .key(key.to_owned())
        .w_full()
        .gap(8.0)
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
}

fn none(key: &str, words: &str, k: &Kit) -> El {
    text(words.to_owned()).key(key.to_owned()).font(k.ty.small).color(k.c.text_muted)
}

/// A list of short names, wrapping, one pill each.
fn pills(key: &str, items: &[String], k: &Kit) -> El {
    let c = &k.c;
    items.iter().fold(row().key(key.to_owned()).w_full().flex_wrap().gaps(6.0, 6.0), |r, item| {
        r.child(
            row()
                .px(8.0)
                .h(22.0)
                .center_y()
                .radius(11.0)
                .border(1.0, c.border_base)
                .child(text(item.clone()).font(k.ty.text_xsmall).color(c.text_base)),
        )
    })
}

fn about(page: El, b: &Bundle, k: &Kit) -> El {
    let or = |v: &str, d: &str| if v.is_empty() { d.to_owned() } else { v.to_owned() };
    let mut signed = Vec::new();
    signed.push(if b.team_id.is_empty() { "no team".to_owned() } else { format!("team {}", b.team_id) });
    signed.push(if b.hardened_runtime { "hardened runtime on".into() } else { "hardened runtime off".into() });
    signed.push(if b.sandboxed { "sandboxed".into() } else { "not sandboxed".into() });
    if b.mas_receipt {
        signed.push("App Store receipt".into());
    }
    let gatekeeper = match (b.gatekeeper_status.is_empty(), b.gatekeeper_source.is_empty()) {
        (true, _) => "Not checked".to_owned(),
        (false, true) => b.gatekeeper_status.clone(),
        (false, false) => format!("{} · {}", b.gatekeeper_status, b.gatekeeper_source),
    };
    let mut list = facts("ap-facts", k)
        .child(fact("bundle-id", "Bundle id", value(or(&b.bundle_id, "Unknown"), k), k))
        .child(fact("version", "Version", value(b.version(), k), k))
        .child(fact("built", "Built with", value(b.built_with(), k), k));
    if !b.framework_versions.is_empty() {
        let fw = b.framework_versions.iter().map(|(n, v)| format!("{n} {v}")).collect::<Vec<_>>().join(", ");
        list = list.child(fact("frameworks", "Frameworks", value(fw, k), k));
    }
    if !b.architectures.is_empty() {
        list = list.child(fact("arch", "Architectures", value(b.architectures.join(", "), k), k));
    }
    list = list
        .child(fact("size", "Size on disk", value(fmt_bytes(b.bundle_size_bytes), k), k))
        .child(fact("signing", "Signature", value(signed.join(" · "), k), k))
        .child(fact("gatekeeper", "Gatekeeper", value(gatekeeper, k), k));
    let granted: Vec<String> = b.granted_permissions.iter().map(|p| permission_label(p)).collect();
    let granted = if granted.is_empty() { none("ap-granted-none", "None granted.", k) } else { pills("ap-granted", &granted, k) };
    let entitled = if b.entitlements.is_empty() {
        none("ap-entitlements-none", "None.", k)
    } else {
        pills("ap-entitlements", &b.entitlements, k)
    };
    let helpers = if b.helpers.is_empty() {
        none("ap-helpers-none", "None.", k)
    } else {
        let mut l = facts("ap-helpers", k);
        for (key, label, items) in [
            ("helper-apps", "Helper apps", &b.helpers.helper_apps),
            ("xpc", "XPC services", &b.helpers.xpc_services),
            ("plugins", "Plug-ins", &b.helpers.plugins),
        ] {
            if !items.is_empty() {
                l = l.child(fact(key, label, value(items.join(", "), k), k));
            }
        }
        l
    };
    let login = if b.login_items.is_empty() {
        none("ap-login-none", "None installed.", k)
    } else {
        b.login_items.iter().enumerate().fold(facts("ap-login", k).gap(10.0), |l, (i, item)| {
            let mut how = vec![if item.daemon { "daemon".to_owned() } else { "agent".to_owned() }];
            if !item.scope.is_empty() {
                how.push(format!("{} scope", item.scope));
            }
            if item.run_at_load {
                how.push("starts at login".into());
            }
            if item.keep_alive {
                how.push("restarted if it quits".into());
            }
            l.child(
                col()
                    .key(format!("ap-login-{i}"))
                    .w_full()
                    .gap(2.0)
                    .child(text(or(&item.label, "Unlabelled")).font(k.ty.small_medium).color(k.c.text_base))
                    .child(text(how.join(" · ")).font(k.ty.small).color(k.c.text_muted))
                    .child(value(item.path.clone(), k).color(k.c.text_faint)),
            )
        })
    };
    let storage = if b.storage.total == 0 {
        none("ap-storage-none", "Nothing found in its usual places.", k)
    } else {
        let mut l = facts("ap-storage", k).child(fact("storage-total", "In all", value(fmt_bytes(b.storage.total), k), k));
        for (i, (label, bytes)) in b.storage.parts().into_iter().enumerate() {
            l = l.child(fact(&format!("storage-{i}"), label, value(fmt_bytes(bytes), k), k));
        }
        if !b.storage.paths.is_empty() {
            l = l.child(fact("storage-paths", "Where", value(b.storage.paths.join("\n"), k), k));
        }
        l
    };
    page.child(section("Bundle", list, k))
        .child(section("Permissions granted", granted, k))
        .child(section(&format!("Entitlements ({})", b.entitlements.len()), entitled, k))
        .child(section("Helpers it carries", helpers, k))
        .child(section("Login items", login, k))
        .child(section("Data it keeps", storage, k))
}
