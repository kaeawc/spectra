//! A JVM's tools on its page: the thread dump (deadlocks first, alike stacks together, filtered
//! by name and state), the heap histogram and what grew since the last one, GC, native memory,
//! Spectra's diagnosis, a heap dump with its analyses, and a flight recording.

use rune::{Cursor, El, col, el, icon, row, text};
use rune_kit::{Kit, SegmentedControl, TextInput};

use super::{Col, failed, figure, figures, grid, line, mono, name, status, tool_row, verdict};
use crate::app::App;
use crate::findings::Severity;
use crate::jvm_tools::{
    DominatorReport, ExplainReport, GcReport, HIGHLIGHTS, HeapDumpReport, HistogramReport,
    JfrReport, LeakReport, NativeMemoryReport, ThreadDumpReport, ThreadState, class_label,
    collector_flag, growth, severity,
};
use crate::procs::{fmt_count, fmt_mem};
use crate::tools::{Tool, fmt_bytes};

/// Thread groups listed before "and N more".
const GROUPS: usize = 40;
/// Rows in a JVM result's tables.
const ROWS: usize = 12;

/// Whether the page's recording is running, or being stopped.
pub(super) fn stopping_or_recording(s: &App) -> bool {
    s.inspect.jvm.recording.is_some()
        || s.inspect.runs.get(&Tool::JfrStop).is_some_and(|r| r.busy())
}

/// What a JVM tool's row shows under its own status: the heap dump's analyses, the recording.
pub(super) fn below(s: &App, tool: Tool, pid: u32, k: &Kit) -> Option<El> {
    match tool {
        Tool::HeapDump => {
            s.heap_dump_path()?;
            let c = &k.c;
            let list = col()
                .key("heapdump-analyses")
                .w_full()
                .radius(k.radius.md)
                .border(1.0, c.border_weak)
                .child(tool_row(s, Tool::LeakSuspects, pid, k))
                .child(el().w_full().h(1.0).fill(c.border_weak))
                .child(tool_row(s, Tool::Dominators, pid, k));
            Some(list)
        }
        Tool::JfrStart => {
            let mut body = col().w_full().gap(10.0);
            let mut any = false;
            if let Some(rec) = &s.inspect.jvm.recording {
                any = true;
                let secs = (s.now - rec.since).max(0.0);
                let took = if secs < 60.0 {
                    format!("{secs:.0} s")
                } else {
                    format!("{:.0} min", secs / 60.0)
                };
                body = body.child(
                    col()
                        .key("jfr-recording")
                        .w_full()
                        .gap(4.0)
                        .child(verdict(
                            "jfr-recording-verdict",
                            format!("Recording for {took}"),
                            Severity::Critical,
                            k,
                        ))
                        .child(line(
                            "Leaving this page stops the recording and discards it.",
                            k,
                        )),
                );
            }
            if let Some(state) = s.inspect.state(Tool::JfrStop) {
                any = true;
                body = body.child(status(s, Tool::JfrStop, state, k));
            }
            any.then_some(body)
        }
        _ => None,
    }
}

// Thread dump.

pub(super) fn threads(s: &App, r: &ThreadDumpReport, k: &Kit) -> El {
    let c = &k.c;
    let mut body = col().w_full().gap(10.0);
    // Deadlocks first, and loudly: which threads, which locks.
    for (i, d) in r.dump.deadlocks.iter().enumerate() {
        let ink = c.danger.fg;
        let mut card = col()
            .key(format!("threads-deadlock-{i}"))
            .w_full()
            .gap(4.0)
            .padding(10.0)
            .radius(k.radius.md)
            .fill(c.danger.bg)
            .border(1.0, c.danger.border)
            .child(
                row()
                    .gap(6.0)
                    .center_y()
                    .child(icon(k.glyph.circle_x, 13.0).color(ink))
                    .child(
                        text(format!("Deadlock: {}", d.threads.join(", ")))
                            .key(format!("threads-deadlock-{i}-title"))
                            .font(k.ty.ui_medium)
                            .color(ink),
                    ),
            );
        for (j, l) in d.lines().into_iter().enumerate() {
            card = card.child(mono(l, k).key(format!("threads-deadlock-{i}-wait-{j}")));
        }
        body = body.child(card.child(line(
            "These threads wait for each other and will never continue on their own.",
            k,
        )));
    }
    if r.dump.deadlocks.is_empty() && r.summary.deadlocks > 0 {
        body = body.child(verdict(
            "threads-deadlock-0-title",
            format!("Spectra counted {} deadlocks", r.summary.deadlocks),
            Severity::Critical,
            k,
        ));
    }
    let mut counts = vec![("Threads", fmt_count(r.total()))];
    counts.extend(ThreadState::SHOWN.map(|st| (st.label(), fmt_count(r.count(st)))));
    body = body.child(figures("threads-counts", &counts, k));
    let page = &s.inspect.jvm;
    let picked = page
        .threads_state
        .and_then(|st| ThreadState::SHOWN.iter().position(|x| *x == st))
        .map_or(0, |i| i + 1);
    let mut labels = vec!["All"];
    labels.extend(ThreadState::SHOWN.map(ThreadState::label));
    let filter = row()
        .w_full()
        .gap(12.0)
        .center_y()
        .flex_wrap()
        .child(
            TextInput::new(&page.threads_query)
                .placeholder("Filter by thread name")
                .label("Filter threads")
                .leading_icon(k.glyph.search)
                .clear_button()
                .width(Some(220.0))
                .on_edit(|s: &mut App| &mut s.inspect.jvm.threads_query)
                .build(k)
                .key("threads-filter"),
        )
        .child(
            SegmentedControl::new(&labels, Some(picked))
                .label("Thread state")
                .fit()
                .on_pick(|s: &mut App, i: usize, _| {
                    s.inspect.jvm.threads_state = i.checked_sub(1).map(|i| ThreadState::SHOWN[i])
                })
                .build(k)
                .key("threads-state"),
        );
    body = body.child(filter);
    let groups = r.groups(&page.threads_query.text, page.threads_state);
    if groups.is_empty() {
        return body.child(line("No threads match.", k).key("threads-none"));
    }
    body = body.child(line(
        format!(
            "{} threads in {} groups with the same stack · biggest first · press one for its frames",
            fmt_count(groups.iter().map(|g| g.names.len() as u64).sum()),
            fmt_count(groups.len() as u64)
        ),
        k,
    ));
    let mut list = col()
        .key("threads-groups")
        .w_full()
        .radius(k.radius.md)
        .border(1.0, c.border_weak)
        .fill(c.bg_base);
    for (i, g) in groups.iter().take(GROUPS).enumerate() {
        if i > 0 {
            list = list.child(el().w_full().h(1.0).fill(c.border_weak));
        }
        let open = page.threads_open.contains(&g.id);
        let states = g
            .states
            .iter()
            .map(|st| st.label())
            .collect::<Vec<_>>()
            .join(", ");
        let ink = if g.deadlocked {
            c.danger.fg
        } else {
            c.text_base
        };
        let head = row()
            .w_full()
            .gap(10.0)
            .center_y()
            .child(
                icon(
                    if open {
                        k.glyph.triangle_down
                    } else {
                        k.glyph.triangle_right
                    },
                    10.0,
                )
                .color(c.icon_muted),
            )
            .child(
                text(format!("×{}", g.names.len()))
                    .w(36.0)
                    .font(k.ty.small_medium.features("'tnum' 1"))
                    .color(ink),
            )
            .child(
                text(states)
                    .w(110.0)
                    .font(k.ty.small)
                    .color(if g.deadlocked { ink } else { c.text_muted }),
            )
            .child(
                text(g.top().to_owned())
                    .font(k.ty.mono)
                    .color(ink)
                    .truncate()
                    .min_w(0.0)
                    .grow(1.0)
                    .help_when_truncated(g.top().to_owned()),
            );
        let names = g.names.join(", ");
        let id = g.id.clone();
        let mut entry = col()
            .key(format!("threads-group-{i}"))
            .w_full()
            .px(12.0)
            .py(6.0)
            .gap(2.0)
            .role("button")
            .label(format!("{} threads: {}", g.names.len(), g.top()))
            .cursor(Cursor::Pointer)
            .on_press(move |s: &mut App| {
                let open = &mut s.inspect.jvm.threads_open;
                if !open.remove(&id) {
                    open.insert(id.clone());
                }
            })
            .child(head)
            .child(
                text(names.clone())
                    .pl(66.0)
                    .font(k.ty.text_xsmall)
                    .color(c.text_faint)
                    .truncate()
                    .min_w(0.0)
                    .help_when_truncated(names),
            );
        if open {
            let frames = g.frames.iter().fold(
                col()
                    .key(format!("threads-frames-{i}"))
                    .w_full()
                    .gap(1.0)
                    .pl(66.0),
                |l, f| l.child(mono(f.clone(), k)),
            );
            entry = entry.child(if g.frames.is_empty() {
                frames.child(line(
                    "No Java frames: it's running native code or just started.",
                    k,
                ))
            } else {
                frames
            });
        }
        list = list.child(entry);
    }
    body = body.child(list);
    if groups.len() > GROUPS {
        body = body.child(line(
            format!("and {} more groups", groups.len() - GROUPS),
            k,
        ));
    }
    body
}

// Heap histogram.

/// A signed size: `+4 KB`, `−1.2 MB`.
fn signed_bytes(d: i64) -> String {
    format!(
        "{}{}",
        if d >= 0 { "+" } else { "−" },
        fmt_bytes(d.unsigned_abs())
    )
}

fn signed_count(d: i64) -> String {
    format!(
        "{}{}",
        if d >= 0 { "+" } else { "−" },
        fmt_count(d.unsigned_abs())
    )
}

pub(super) fn histogram(s: &App, r: &HistogramReport, k: &Kit) -> El {
    let total = r.total_bytes().max(1);
    let mut body = col().w_full().gap(10.0).child(figures(
        "histogram-totals",
        &[
            ("Live objects", fmt_bytes(r.total_bytes())),
            ("Objects", fmt_count(r.total_instances())),
            ("Classes", fmt_count(r.histogram.entries.len() as u64)),
        ],
        k,
    ));
    match &s.inspect.jvm.prev_histogram {
        Some(before) => {
            let g = growth(before, r);
            body = body.child(line(
                "Since the last histogram on this page, most grown first:",
                k,
            ));
            if g.is_empty() {
                body = body.child(line("Nothing changed.", k).key("histogram-growth-none"));
            } else {
                let rows = g
                    .iter()
                    .take(ROWS)
                    .map(|x| {
                        vec![
                            name(class_label(&x.class_name), k),
                            figure(signed_bytes(x.bytes), k),
                            figure(signed_count(x.instances), k),
                            figure(fmt_bytes(x.bytes_now), k),
                        ]
                    })
                    .collect();
                body = body.child(grid(
                    "histogram-growth",
                    &[
                        ("Class", Col::Flex),
                        ("Grew by", Col::Fixed(80.0)),
                        ("Objects", Col::Fixed(72.0)),
                        ("Now", Col::Fixed(72.0)),
                    ],
                    rows,
                    k,
                ));
            }
        }
        None => {
            body = body.child(
                line(
                    "Run it again later to see which classes grew in between.",
                    k,
                )
                .key("histogram-hint"),
            )
        }
    }
    if !r.suspects.is_empty() {
        let list = r.suspects.iter().take(5).enumerate().fold(
            col().key("histogram-suspects").w_full().gap(2.0),
            |l, (i, x)| {
                l.child(
                    line(
                        format!(
                            "{} · {} in {} objects: {}",
                            class_label(&x.class_name),
                            fmt_bytes(x.bytes),
                            fmt_count(x.instances),
                            x.reason
                        ),
                        k,
                    )
                    .key(format!("histogram-suspect-{i}")),
                )
            },
        );
        body = body.child(line("Spectra's leak suspects:", k)).child(list);
    }
    let rows = r
        .by_bytes()
        .into_iter()
        .take(ROWS)
        .map(|e| {
            vec![
                name(class_label(&e.class_name), k),
                figure(fmt_bytes(e.bytes), k),
                figure(fmt_count(e.instances), k),
                figure(format!("{:.1}%", 100.0 * e.bytes as f64 / total as f64), k),
            ]
        })
        .collect();
    let most = r
        .by_instances()
        .into_iter()
        .take(5)
        .map(|e| {
            vec![
                name(class_label(&e.class_name), k),
                figure(fmt_count(e.instances), k),
                figure(fmt_bytes(e.bytes), k),
            ]
        })
        .collect();
    body.child(grid(
        "histogram-classes",
        &[
            ("Class, most bytes first", Col::Flex),
            ("Bytes", Col::Fixed(72.0)),
            ("Objects", Col::Fixed(72.0)),
            ("Share", Col::Fixed(52.0)),
        ],
        rows,
        k,
    ))
    .child(grid(
        "histogram-instances",
        &[
            ("Class, most objects first", Col::Flex),
            ("Objects", Col::Fixed(72.0)),
            ("Bytes", Col::Fixed(72.0)),
        ],
        most,
        k,
    ))
}

// GC stats.

fn fmt_secs(secs: f64) -> String {
    if secs < 1.0 {
        format!("{:.0} ms", secs * 1000.0)
    } else {
        format!("{secs:.2} s")
    }
}

pub(super) fn gc(s: &App, r: &GcReport, k: &Kit) -> El {
    let mut body = col().w_full().gap(10.0);
    let pid = match &s.inspect.subject {
        Some(crate::inspect::Subject::Process { pid, .. }) => Some(*pid),
        _ => None,
    };
    if let Some(name) = pid
        .and_then(|p| s.jvm(p))
        .and_then(|j| collector_flag(&j.vm_args))
    {
        body = body
            .child(line(format!("Collector: {name}, as its arguments ask"), k).key("gc-collector"));
    }
    let rows = r
        .collectors()
        .into_iter()
        .map(|x| {
            vec![
                name(x.name.to_owned(), k),
                figure(fmt_count(x.count), k),
                figure(fmt_secs(x.seconds), k),
                figure(
                    x.avg_ms()
                        .map_or_else(|| "—".to_owned(), |ms| format!("{ms:.1} ms")),
                    k,
                ),
            ]
        })
        .collect();
    let areas = r
        .areas()
        .into_iter()
        .filter(|a| a.capacity_kib > 0.0 || a.used_kib > 0.0)
        .map(|a| {
            vec![
                name(a.name.to_owned(), k),
                figure(fmt_mem(a.used_kib), k),
                figure(fmt_mem(a.capacity_kib), k),
                figure(
                    a.percent()
                        .map_or_else(|| "—".to_owned(), |p| format!("{p:.0}%")),
                    k,
                ),
            ]
        })
        .collect();
    body.child(
        line(
            format!(
                "{} paused for collection in all",
                fmt_secs(r.total_seconds())
            ),
            k,
        )
        .key("gc-total"),
    )
    .child(grid(
        "gc-collectors",
        &[
            ("Collections", Col::Flex),
            ("Count", Col::Fixed(64.0)),
            ("Total pause", Col::Fixed(84.0)),
            ("Average", Col::Fixed(72.0)),
        ],
        rows,
        k,
    ))
    .child(grid(
        "gc-areas",
        &[
            ("Area", Col::Flex),
            ("Used", Col::Fixed(72.0)),
            ("Capacity", Col::Fixed(72.0)),
            ("Full", Col::Fixed(52.0)),
        ],
        areas,
        k,
    ))
}

// Native memory.

pub(super) fn native_memory(r: &NativeMemoryReport, k: &Kit) -> El {
    let mut body = col().w_full().gap(10.0);
    if let Some(why) = r.off() {
        body = body
            .child(verdict(
                "nmt-off",
                "Native memory tracking is off".into(),
                Severity::Warning,
                k,
            ))
            .child(line(why, k).key("nmt-off-why"));
    } else if let Err(why) = &r.nmt {
        body = body.child(failed(
            "nmt-error",
            &format!("Couldn't read native memory tracking: {why}"),
            k,
        ));
    } else if let Ok(n) = &r.nmt {
        let rows = r
            .categories()
            .into_iter()
            .take(ROWS)
            .map(|x| {
                vec![
                    name(x.name.clone(), k),
                    figure(fmt_mem(x.committed_kib), k),
                    figure(fmt_mem(x.reserved_kib), k),
                ]
            })
            .collect();
        body = body
            .child(figures(
                "nmt-totals",
                &[
                    ("Committed", fmt_mem(n.total_committed_kib)),
                    ("Reserved", fmt_mem(n.total_reserved_kib)),
                ],
                k,
            ))
            .child(grid(
                "nmt-categories",
                &[
                    ("Used for, most committed first", Col::Flex),
                    ("Committed", Col::Fixed(80.0)),
                    ("Reserved", Col::Fixed(80.0)),
                ],
                rows,
                k,
            ));
    }
    if let Some(vm) = &r.vm {
        let heap = vm.heap();
        if !heap.is_empty() {
            let lines = heap
                .into_iter()
                .fold(col().key("nmt-heap").w_full().gap(1.0), |l, h| {
                    l.child(mono(h, k))
                });
            body = body
                .child(line("The heap, as the JVM describes it:", k))
                .child(lines);
        }
        if let Some(cc) = vm.code_cache() {
            body = body.child(mono(cc, k).key("nmt-code-cache"));
        }
    }
    body
}

// Explain.

pub(super) fn explain(r: &ExplainReport, k: &Kit) -> El {
    let c = &k.c;
    let mut body = col().w_full().gap(10.0);
    let jdk = [r.jdk_vendor.as_str(), r.jdk_version.as_str()]
        .into_iter()
        .filter(|p| !p.is_empty())
        .collect::<Vec<_>>()
        .join(" ");
    if !jdk.is_empty() {
        body = body.child(line(format!("JDK {jdk}"), k).key("explain-jdk"));
    }
    let found = r.findings();
    if found.is_empty() {
        return body.child(line("Spectra found nothing to say about it.", k).key("explain-none"));
    }
    let list = found.into_iter().enumerate().fold(
        col().key("explain-findings").w_full().gap(10.0),
        |l, (i, o)| {
            let sev = severity(&o.severity);
            let mut f = col()
                .key(format!("explain-{i}"))
                .w_full()
                .gap(2.0)
                .child(verdict(
                    &format!("explain-{i}-summary"),
                    o.summary.clone(),
                    sev,
                    k,
                ));
            if !o.evidence.is_empty() {
                f = f.child(line(format!("Evidence: {}", o.evidence), k).pl(16.0));
            }
            if !o.recommendation.is_empty() {
                f = f.child(
                    text(o.recommendation.clone())
                        .pl(16.0)
                        .font(k.ty.small)
                        .color(c.text_base)
                        .wrap(),
                );
            }
            l.child(f)
        },
    );
    body.child(list)
}

// Heap dump and its analyses.

pub(super) fn heap_dump(r: &HeapDumpReport, k: &Kit) -> El {
    col()
        .w_full()
        .gap(4.0)
        .child(line("Saved to:", k))
        .child(mono(r.path.clone(), k).key("heapdump-path"))
        .child(line(
            "It holds everything in the heap, passwords and tokens included: share it with care.",
            k,
        ))
}

pub(super) fn leaks(r: &LeakReport, k: &Kit) -> El {
    let l = &r.report;
    let mut body = col().w_full().gap(10.0).child(figures(
        "suspects-totals",
        &[
            ("Reachable heap", fmt_bytes(l.reachable_bytes)),
            ("Objects", fmt_count(l.reachable_objects)),
        ],
        k,
    ));
    if l.suspects.is_empty() {
        let at = if l.threshold_percent > 0.0 {
            format!("{:.0}%", l.threshold_percent)
        } else {
            "a large share".into()
        };
        return body.child(
            line(
                format!("Nothing keeps more than {at} of the heap alive on its own."),
                k,
            )
            .key("suspects-none"),
        );
    }
    for (i, x) in l.suspects.iter().enumerate().take(5) {
        let mut f = col()
            .key(format!("suspects-{i}"))
            .w_full()
            .gap(2.0)
            .child(verdict(
                &format!("suspects-{i}-title"),
                format!(
                    "{} keeps {} alive ({:.0}% of the heap)",
                    class_label(&x.class_name),
                    fmt_bytes(x.retained_bytes),
                    x.percent_of_heap
                ),
                Severity::Warning,
                k,
            ));
        if let Some(a) = &x.accumulation_point {
            f = f.child(
                line(
                    format!(
                        "It piles up in {}: {} across {} objects directly under it",
                        class_label(&a.class_name),
                        fmt_bytes(a.retained_bytes),
                        fmt_count(a.direct_children)
                    ),
                    k,
                )
                .pl(16.0),
            );
        }
        if let Some(p) = x.path_to_root.as_ref().filter(|p| !p.steps.is_empty()) {
            f = f
                .child(line("Held from a GC root by:", k).pl(16.0))
                .child(mono(p.line(), k).pl(16.0));
        }
        body = body.child(f);
    }
    body
}

pub(super) fn dominators(r: &DominatorReport, k: &Kit) -> El {
    let pct = |p: f64| format!("{p:.1}%");
    let objects = r
        .suspects
        .iter()
        .take(ROWS)
        .map(|x| {
            vec![
                name(class_label(&x.class_name), k),
                figure(fmt_bytes(x.retained_bytes), k),
                figure(pct(x.percent_of_heap), k),
            ]
        })
        .collect();
    let classes = r
        .classes
        .iter()
        .take(ROWS)
        .map(|x| {
            vec![
                name(class_label(&x.class_name), k),
                figure(fmt_count(x.instances), k),
                figure(fmt_bytes(x.retained_bytes), k),
                figure(pct(x.percent_of_heap), k),
            ]
        })
        .collect();
    col()
        .w_full()
        .gap(10.0)
        .child(figures(
            "dominators-totals",
            &[
                ("Reachable heap", fmt_bytes(r.reachable_bytes)),
                ("Objects", fmt_count(r.reachable_objects)),
            ],
            k,
        ))
        .child(grid(
            "dominators-objects",
            &[
                ("Object, most kept alive first", Col::Flex),
                ("Retained", Col::Fixed(76.0)),
                ("Share", Col::Fixed(52.0)),
            ],
            objects,
            k,
        ))
        .child(grid(
            "dominators-classes",
            &[
                ("Class", Col::Flex),
                ("Objects", Col::Fixed(68.0)),
                ("Retained", Col::Fixed(76.0)),
                ("Share", Col::Fixed(52.0)),
            ],
            classes,
            k,
        ))
}

// Flight recording.

pub(super) fn recording(r: &JfrReport, k: &Kit) -> El {
    let j = &r.summary;
    let mut facts = vec![("Events", fmt_count(j.total()))];
    if !j.duration.is_empty() {
        facts.insert(0, ("Recorded for", j.duration.clone()));
    }
    let highlights: Vec<(&str, String)> = HIGHLIGHTS
        .iter()
        .map(|(kind, label)| (*label, fmt_count(j.count(kind))))
        .collect();
    let rows = j
        .busiest()
        .into_iter()
        .take(ROWS)
        .map(|e| {
            vec![
                name(e.kind.strip_prefix("jdk.").unwrap_or(&e.kind).to_owned(), k),
                figure(fmt_count(e.count), k),
                figure(fmt_bytes(e.size_bytes), k),
            ]
        })
        .collect();
    col()
        .w_full()
        .gap(10.0)
        .child(figures("jfr-facts", &facts, k))
        .child(figures("jfr-highlights", &highlights, k))
        .child(grid(
            "jfr-events",
            &[
                ("What happened most", Col::Flex),
                ("Events", Col::Fixed(72.0)),
                ("Size", Col::Fixed(72.0)),
            ],
            rows,
            k,
        ))
        .child(line("Saved to:", k))
        .child(mono(r.path.clone(), k).key("jfr-path"))
        .child(
            row()
                .gap(6.0)
                .center_y()
                .flex_wrap()
                .child(
                    text("For the full analysis, run")
                        .font(k.ty.small)
                        .color(k.c.text_muted),
                )
                .child(mono(format!("spectra jvm jfr analyze {}", r.path), k).key("jfr-analyze")),
        )
}
