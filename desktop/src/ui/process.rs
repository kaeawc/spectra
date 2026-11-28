//! One process's page: what's wrong with it, what it is, how busy it has been, what it started
//! and how it was started. A JVM gets its JVM numbers below.

use rune::{Cursor, El, col, el, row, text};
use rune_kit::{Button, ButtonVariant, Kit};

use super::card::{card, dot, ink};
use super::chart::{Lane, Mark, count, lanes};
use super::detail::{jvm_part, section};
use super::list::sign_badge;
use crate::app::App;
use crate::findings::{Finding, Severity};
use crate::procs::{Process, fmt_count, fmt_cpu, fmt_mem};

const LABEL_W: f32 = 120.0;
/// Command-line lines shown before "Show all".
const CMD_LINES: usize = 12;
/// Children listed before "and N more".
const CHILDREN: usize = 40;

pub fn page(s: &App, p: &Process, k: &Kit) -> El {
    let findings = s.findings_for(p.pid);
    let mut page = col()
        .key("pd-page")
        .w_full()
        .padding(24.0)
        .gap(24.0)
        .child(header(s, p, findings.first().copied(), k));
    if let Some(o) = super::quit::outcome(s, Some(p.pid), k) {
        page = page.child(o);
    }
    if !findings.is_empty() {
        let list = findings
            .iter()
            .fold(col().key("pd-findings").w_full().gap(10.0), |l, f| {
                l.child(card(s, f, k))
            });
        page = page.child(section("Findings", list, k));
    }
    page = page
        .child(section("About", facts(s, p, k), k))
        .child(section("Activity", activity(s, p, k), k));
    if s.ended_page().is_none() {
        page = page
            .child(section(
                "Network and energy",
                super::app_page::live(s, "pd", k),
                k,
            ))
            .child(super::tools::panel(s, p.pid, k))
            .child(super::logs::panel(s, k));
    }
    if let Some(j) = s.jvm(p.pid) {
        page = page.child(jvm_part(s, j, k));
    }
    let kids = s.children(p.pid);
    if !kids.is_empty() {
        page = page.child(section(
            &format!("Started by it ({})", fmt_count(kids.len() as u64)),
            children("pd", &kids, k),
            k,
        ));
    }
    page.child(section("Command line", command_line(s, p, k), k))
        .child(section("How Spectra recognised it", evidence(p, k), k))
}

fn header(s: &App, p: &Process, worst: Option<&Finding>, k: &Kit) -> El {
    let c = &k.c;
    let severity = worst.map_or(Severity::Healthy, |f| f.severity);
    let verdict = worst.map_or_else(|| "Nothing unusual".to_owned(), |f| f.title.clone());
    // A JVM is better known by what it runs than by `java`.
    let (name, runs) = match s.jvm(p.pid) {
        Some(j) => (j.name(), format!("{} · ", p.name())),
        None => (p.name().into_owned(), String::new()),
    };
    let meta = format!(
        "{runs}pid {} · {} CPU · {} · {} threads",
        p.pid,
        fmt_cpu(p.cpu_pct),
        fmt_mem(p.rss_kib),
        fmt_count(p.thread_count)
    );
    let about = col()
        .grow(1.0)
        .min_w(0.0)
        .gap(6.0)
        .child(
            row()
                .gap(10.0)
                .center_y()
                .flex_wrap()
                .child(
                    text(name)
                        .key("pd-name")
                        .font(k.ty.body_medium)
                        .color(c.text_base),
                )
                .child(
                    text(meta)
                        .key("pd-meta")
                        .font(k.ty.small.features("'tnum' 1"))
                        .color(c.text_muted),
                ),
        )
        .child(
            row().gap(8.0).center_y().child(dot(severity, k)).child(
                text(verdict)
                    .key("pd-verdict")
                    .font(k.ty.ui_medium)
                    .color(ink(severity, k)),
            ),
        );
    row()
        .key("pd-header")
        .w_full()
        .gap(16.0)
        .items_start()
        .child(about)
        .child(super::quit::actions(s, p, "pd", k))
}

pub(super) fn fact(key: &str, label: &str, value: El, k: &Kit) -> El {
    row()
        .key(format!("pd-fact-{key}"))
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

pub(super) fn value(v: impl Into<String>, k: &Kit) -> El {
    text(v.into())
        .font(k.ty.small)
        .color(k.c.text_base)
        .selectable(true)
        .selection_color(k.selection())
        .wrap_anywhere()
}

fn facts(s: &App, p: &Process, k: &Kit) -> El {
    let c = &k.c;
    let ppid = p.ppid;
    let parent = match s.process(ppid).filter(|_| ppid != p.pid) {
        Some(parent) => text(format!("{} · {ppid}", parent.name()))
            .key("pd-parent")
            .role("link")
            .label(format!("Open {}", parent.name()))
            .cursor(Cursor::Pointer)
            .font(k.ty.small)
            .color(c.text_accent)
            .on_press(move |s: &mut App| s.open(ppid)),
        None if ppid == 0 => value("None: started by the kernel", k),
        None => value(format!("pid {ppid}, no longer running"), k),
    };
    let user = if p.user.is_empty() {
        format!("uid {}", p.uid)
    } else {
        format!("{} (uid {})", p.user, p.uid)
    };
    let or_unknown = |v: &str| {
        if v.is_empty() {
            "Unknown".to_owned()
        } else {
            v.to_owned()
        }
    };
    let mut list = col()
        .key("pd-facts")
        .w_full()
        .gap(8.0)
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .child(fact("parent", "Parent", parent, k))
        .child(fact("user", "User", value(user, k), k))
        .child(fact(
            "started",
            "Started",
            value(or_unknown(&p.started()), k),
            k,
        ))
        .child(fact(
            "path",
            "Executable",
            value(or_unknown(&p.executable_path), k),
            k,
        ))
        .child(fact("role", "Role", value(p.role.label(), k), k))
        .child(fact("runtime", "Runtime", value(p.runtime.label(), k), k));
    if let Some(app) = &p.app {
        let name = if app.name.is_empty() {
            app.bundle_path.clone()
        } else {
            app.name.clone()
        };
        list = list
            .child(fact("app", "App", value(name, k), k))
            .child(fact(
                "bundle-id",
                "Bundle id",
                value(or_unknown(&app.bundle_id), k),
                k,
            ))
            .child(fact(
                "bundle",
                "Bundle",
                value(or_unknown(&app.bundle_path), k),
                k,
            ));
    }
    list.child(fact("signing", "Signature", signing(p, k), k))
}

/// The signature's claim, its team and hardened runtime, and Spectra's own words on how it
/// knows. Unless Spectra checked it, the team is only what `codesign` displayed.
fn signing(p: &Process, k: &Kit) -> El {
    let c = &k.c;
    let Some(sg) = &p.signing else {
        return value("Not read", k);
    };
    let checked = p.validated();
    let team = match (sg.team_id.is_empty(), checked) {
        (true, _) => "no team".to_owned(),
        (false, true) => format!("team {}", sg.team_id),
        (false, false) => format!("team {} as displayed, not verified", sg.team_id),
    };
    let hardened = if sg.hardened_runtime {
        "hardened runtime on"
    } else {
        "hardened runtime off"
    };
    let mut body = col().w_full().gap(4.0).child(
        row()
            .gap(8.0)
            .center_y()
            .flex_wrap()
            .child(sign_badge(p.trust(), k))
            .child(
                text(format!("{team} · {hardened}"))
                    .key("pd-signing")
                    .font(k.ty.small)
                    .color(c.text_base)
                    .wrap(),
            ),
    );
    if let Some(line) = p.signing_evidence() {
        body = body.child(
            text(line.to_owned())
                .key("pd-signing-evidence")
                .font(k.ty.small)
                .color(c.text_muted)
                .selectable(true)
                .selection_color(k.selection())
                .wrap_anywhere(),
        );
    }
    body
}

fn activity(s: &App, p: &Process, k: &Kit) -> El {
    let h = s.proc_history(p.pid);
    let all = [
        Lane {
            key: "pd-lane-cpu",
            label: "CPU",
            values: h.iter().map(|x| x.cpu).collect(),
            unit: fmt_cpu,
            mark: Mark::Line,
        },
        Lane {
            key: "pd-lane-mem",
            label: "Memory",
            values: h.iter().map(|x| x.rss_kib).collect(),
            unit: fmt_mem,
            mark: Mark::Line,
        },
        Lane {
            key: "pd-lane-threads",
            label: "Threads",
            values: h.iter().map(|x| x.threads as f64).collect(),
            unit: count,
            mark: Mark::Line,
        },
    ];
    lanes("pd", h.len(), &all, None, k.c.text_accent, k)
}

/// Processes a row each, busiest first; each opens its page. `prefix` keys them.
pub(super) fn children(prefix: &str, kids: &[&Process], k: &Kit) -> El {
    let c = &k.c;
    let mut kids: Vec<&&Process> = kids.iter().collect();
    kids.sort_by(|a, b| crate::groups::Sort::default().processes(a, b));
    let figures = k.ty.small.features("'tnum' 1");
    let mut list = col()
        .key(format!("{prefix}-children"))
        .w_full()
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .py(4.0);
    for p in kids.iter().take(CHILDREN) {
        let pid = p.pid;
        let r = row()
            .key(format!("{prefix}-child-{pid}"))
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
            .child(
                text(p.runtime.label())
                    .font(k.ty.text_xsmall)
                    .color(c.text_faint),
            )
            .child(el().grow(1.0))
            .child(
                text(fmt_cpu(p.cpu_pct))
                    .w(64.0)
                    .align(rune::Align::End)
                    .font(figures)
                    .color(c.text_base),
            )
            .child(
                text(fmt_mem(p.rss_kib))
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
    if kids.len() > CHILDREN {
        list = list.child(
            text(format!("and {} more", kids.len() - CHILDREN))
                .key(format!("{prefix}-children-more"))
                .px(12.0)
                .py(6.0)
                .font(k.ty.small)
                .color(c.text_muted),
        );
    }
    list
}

/// The command line a line at a time: the program and its plain arguments, then each flag
/// with its value, so a path with spaces stays whole.
pub fn command_lines(cmd: &str) -> Vec<String> {
    let mut lines: Vec<String> = Vec::new();
    for word in cmd.split_whitespace() {
        match lines.last_mut() {
            Some(line) if !word.starts_with('-') => {
                line.push(' ');
                line.push_str(word);
            }
            _ => lines.push(word.to_owned()),
        }
    }
    lines
}

fn command_line(s: &App, p: &Process, k: &Kit) -> El {
    let c = &k.c;
    let all = command_lines(&p.full_command_line);
    if all.is_empty() {
        return text("Spectra couldn't read how it was started.")
            .key("pd-cmd-none")
            .font(k.ty.small)
            .color(c.text_muted);
    }
    let shown = if s.cmd_all {
        all.len()
    } else {
        all.len().min(CMD_LINES)
    };
    let lines: Vec<El> = all
        .iter()
        .take(shown)
        .enumerate()
        .map(|(i, line)| {
            text(line.clone())
                .key(format!("pd-cmd-{i}"))
                .font(k.ty.mono)
                .color(c.text_base)
                .selectable(true)
                .selection_color(k.selection())
                .wrap_anywhere()
                .pl(if i == 0 { 0.0 } else { 16.0 })
        })
        .collect();
    let mut body = col()
        .key("pd-cmd")
        .w_full()
        .gap(2.0)
        .py(10.0)
        .px(12.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .selection_group(true)
        .children(lines);
    if all.len() > CMD_LINES {
        let label = if s.cmd_all {
            "Show less".to_owned()
        } else {
            format!("Show all {} lines", all.len())
        };
        body = body.child(
            row().pt(4.0).child(
                Button::new(label)
                    .variant(ButtonVariant::Ghost)
                    .on_press(|s: &mut App| s.cmd_all = !s.cmd_all)
                    .build(k)
                    .key("pd-cmd-more"),
            ),
        );
    }
    body
}

fn evidence(p: &Process, k: &Kit) -> El {
    if p.evidence.is_empty() {
        return text("Spectra gave no reasons.")
            .font(k.ty.small)
            .color(k.c.text_muted);
    }
    p.evidence.iter().enumerate().fold(
        col().key("pd-evidence").w_full().gap(4.0),
        |l, (i, line)| {
            l.child(
                text(line.clone())
                    .key(format!("pd-evidence-{i}"))
                    .font(k.ty.small)
                    .color(k.c.text_muted)
                    .wrap(),
            )
        },
    )
}

#[cfg(test)]
mod tests {
    use super::command_lines;

    #[test]
    fn a_command_line_breaks_before_each_flag_and_keeps_paths_with_spaces() {
        let cmd = "/Applications/Slack.app/Contents/MacOS/Slack Helper (Renderer) --type=renderer \
                   --user-data-dir=/Users/me/Library/Application Support/Slack -avd fold";
        assert_eq!(
            command_lines(cmd),
            [
                "/Applications/Slack.app/Contents/MacOS/Slack Helper (Renderer)",
                "--type=renderer",
                "--user-data-dir=/Users/me/Library/Application Support/Slack",
                "-avd fold",
            ]
        );
        assert!(command_lines("  ").is_empty());
        assert_eq!(command_lines("-zsh"), ["-zsh"]);
    }
}
