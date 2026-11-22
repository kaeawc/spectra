//! The Storage page: how much room each disk has left, the biggest folders in ~/Library with
//! "Look inside" to go deeper, and the Time Machine snapshots kept on this Mac. Sizing runs only
//! on a press, shows how long it has been going, and can be stopped. Nothing here deletes.

use rune::{Color, El, col, el, icon, row, text};
use rune_kit::{Button, ButtonVariant, Kit};

use super::detail::section;
use super::home::callout;
use crate::app::{App, fmt_ago};
use crate::groups::Nav;
use crate::procs::fmt_count;
use crate::storage::{Entry, LOW_FREE, Storage, low_free};
use crate::system::fmt_size;

pub fn page(s: &App, k: &Kit) -> El {
    let intro = text(
        "Where disk space went. Sizing a folder walks every file in it, so it runs only when you ask and can take minutes. Nothing here deletes anything.",
    )
    .font(k.ty.small)
    .color(k.c.text_muted)
    .wrap();
    let mut page = col()
        .key("st-page")
        .w_full()
        .padding(24.0)
        .gap(24.0)
        .child(intro)
        .child(section("Free space", disks(s, k), k));
    if let Some(path) = s.storage.inside() {
        let title = format!("Inside {}", s.storage.short(path));
        page = page.child(section(&title, inside(s, path, k), k));
    }
    page.child(section("Biggest folders in ~/Library", library(s, k), k))
        .child(section("Time Machine snapshots", time_machine(s, k), k))
}

fn panel(key: &str, k: &Kit) -> El {
    col()
        .key(key.to_owned())
        .w_full()
        .gap(10.0)
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(k.c.layer_01)
        .border(1.0, k.c.border_weak)
}

/// A bar filled to `frac` in `color`.
fn bar(key: &str, frac: f64, color: Color, k: &Kit) -> El {
    el().key(key.to_owned())
        .w_full()
        .h(6.0)
        .radius(3.0)
        .fill(k.c.layer_03)
        .child(
            el().w_frac(frac.clamp(0.0, 1.0) as f32)
                .h_full()
                .radius(3.0)
                .fill(color),
        )
}

fn small(t: impl Into<String>, k: &Kit) -> El {
    text(t.into()).font(k.ty.small).color(k.c.text_muted).wrap()
}

/// Free room on each disk, from the Machine page's own reading, and a warning for a low one.
fn disks(s: &App, k: &Kit) -> El {
    let c = &k.c;
    let part = &s.system.storage;
    let mut body = col().key("st-disks").w_full().gap(10.0);
    if part.last.is_none() {
        return body.child(match &part.error {
            Some(e) => callout("st-disks-error", "Couldn't read the disks", e.why(), k),
            None => text("Reading the disks…")
                .key("st-disks-reading")
                .font(k.ty.small)
                .color(c.text_faint),
        });
    }
    let vols = s.system.volumes();
    if vols.is_empty() {
        return body.child(small("Spectra listed no disks to show.", k).key("st-disks-none"));
    }
    for v in vols.iter().filter(|v| low_free(v)) {
        let key = format!("st-low-{}", v.mount.replace('/', ":"));
        let body_text = format!(
            "Only {} free ({:.0}% used). macOS needs room for updates, swap and caches. The biggest folders below show where the space went.",
            fmt_size(v.avail),
            v.used_pct()
        );
        body = body.child(callout(
            &key,
            &format!("{} is low on space", v.label()),
            &body_text,
            k,
        ));
    }
    let mut list = panel("st-volumes", k);
    for v in &vols {
        let key = format!("st-vol-{}", v.mount.replace('/', ":"));
        let low = low_free(v);
        let ink = if low { c.warning.fg } else { c.text_accent };
        let figures = format!(
            "{} free of {}",
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
                        .child(
                            text(v.label())
                                .font(k.ty.small_medium)
                                .color(c.text_base)
                                .truncate()
                                .shrink()
                                .min_w(0.0),
                        )
                        .child(el().grow(1.0))
                        .child(
                            text(figures)
                                .key(format!("{key}-free"))
                                .font(k.ty.small.features("'tnum' 1"))
                                .color(if low { c.warning.fg } else { c.text_muted }),
                        ),
                )
                .child(bar(&format!("{key}-bar"), v.used_pct() / 100.0, ink, k)),
        );
    }
    let more = Button::new("More on the Machine page")
        .variant(ButtonVariant::Ghost)
        .help("Every disk's used and free space, and how full it is")
        .on_press(|s: &mut App| s.show(Nav::System))
        .build(k)
        .key("st-machine");
    let floor = format!(
        "A disk is low under {} free or once it's as full as the Machine page warns about.",
        fmt_size(LOW_FREE)
    );
    body.child(list).child(
        row()
            .w_full()
            .gap(12.0)
            .center_y()
            .child(small(floor, k).grow(1.0).min_w(0.0))
            .child(more),
    )
}

/// ~/Library: Measure (or Measure again) and Look inside the home folder, then what it found.
fn library(s: &App, k: &Kit) -> El {
    let st = &s.storage;
    let lib = st.library();
    let measured = st.results.get(&lib);
    let label = if measured.is_some() {
        "Measure again"
    } else {
        "Measure ~/Library"
    };
    let lib_press = lib.clone();
    let measure = Button::new(label)
        .variant(ButtonVariant::Outline)
        .disabled(st.busy())
        .help("Size every folder in ~/Library. This can take several minutes.")
        .on_press(move |s: &mut App| s.measure_storage(&lib_press))
        .build(k)
        .key("st-measure");
    let home = st.home.clone();
    let look_home = Button::new("Look inside your home folder")
        .variant(ButtonVariant::Ghost)
        .disabled(st.busy() || home.is_empty())
        .help("Size the folders in your home folder, ~/Library included")
        .on_press(move |s: &mut App| s.look_inside(&home, false))
        .build(k)
        .key("st-look-home");
    let status = match measured {
        Some(m) => format!(
            "{} in {} files · measured {} ago",
            fmt_size(m.du.total),
            fmt_count(m.du.files),
            fmt_ago(s.now - m.at)
        ),
        None => "Not measured yet.".to_owned(),
    };
    let head = row()
        .w_full()
        .gap(12.0)
        .center_y()
        .child(
            text(status)
                .key("st-library-status")
                .font(k.ty.small.features("'tnum' 1"))
                .color(k.c.text_muted)
                .truncate()
                .shrink()
                .min_w(0.0),
        )
        .child(el().grow(1.0))
        .child(look_home)
        .child(measure);
    let mut body = col().key("st-library").w_full().gap(10.0).child(head);
    body = folder(s, st, &lib, "st-lib", false, body, k);
    if measured.is_none() && !st.errors.contains_key(&lib) && st.measuring_path() != Some(&lib) {
        body = body.child(
            small(
                "Measure ~/Library to see which folders in it take the most space: caches, app data, developer tools and mail live here.",
                k,
            )
            .key("st-library-empty"),
        );
    }
    body
}

/// The folder looked inside: Back, Measure again, then its folders and its biggest files.
fn inside(s: &App, path: &str, k: &Kit) -> El {
    let st = &s.storage;
    let back_to = match st.trail.len() {
        0 | 1 => "Back".to_owned(),
        n => format!("Back to {}", st.short(&st.trail[n - 2])),
    };
    let back = Button::new(format!("‹ {back_to}"))
        .variant(ButtonVariant::Ghost)
        .on_press(|s: &mut App| s.storage_back())
        .build(k)
        .key("st-back");
    let p = path.to_owned();
    let again = Button::new(if st.results.contains_key(path) {
        "Measure again"
    } else {
        "Measure"
    })
    .variant(ButtonVariant::Outline)
    .disabled(st.busy())
    .on_press(move |s: &mut App| s.measure_storage(&p))
    .build(k)
    .key("st-inside-measure");
    let status = st.results.get(path).map_or_else(
        || {
            if st.errors.contains_key(path) || st.measuring_path() == Some(path) {
                String::new()
            } else {
                "Not measured yet.".to_owned()
            }
        },
        |m| {
            format!(
                "{} in {} files · measured {} ago",
                fmt_size(m.du.total),
                fmt_count(m.du.files),
                fmt_ago(s.now - m.at)
            )
        },
    );
    let head = row()
        .w_full()
        .gap(12.0)
        .center_y()
        .child(back)
        .child(
            text(status)
                .key("st-inside-status")
                .font(k.ty.small.features("'tnum' 1"))
                .color(k.c.text_muted)
                .truncate()
                .shrink()
                .min_w(0.0),
        )
        .child(el().grow(1.0))
        .child(again);
    let body = col().key("st-inside").w_full().gap(10.0).child(head);
    let mut body = folder(s, st, path, "st-in", true, body, k);
    if let Some(m) = st.results.get(path)
        && !m.du.files_top.is_empty()
    {
        let mut files = panel("st-in-files", k).child(
            text("Biggest files")
                .font(k.ty.small_medium)
                .color(k.c.text_muted),
        );
        for (i, e) in m.du.files_top.iter().enumerate() {
            files = files.child(entry(
                st,
                e,
                path,
                m.du.total,
                &format!("st-in-file-{i}"),
                None,
                k,
            ));
        }
        body = body.child(files);
    }
    body
}

/// What's known about `path`: a measurement on its way, why the last one failed, its folders.
fn folder(s: &App, st: &Storage, path: &str, key: &str, deeper: bool, mut body: El, k: &Kit) -> El {
    if st.measuring_path() == Some(path) {
        let started = st.measuring.as_ref().map_or(s.now, |m| m.started);
        let stop = Button::new("Stop")
            .variant(ButtonVariant::Outline)
            .on_press(|s: &mut App| s.stop_measuring())
            .build(k)
            .key(format!("{key}-stop"));
        body = body.child(
            row()
                .key(format!("{key}-measuring"))
                .w_full()
                .gap(12.0)
                .center_y()
                .child(icon(k.glyph.dash, 12.0).color(k.c.icon_muted))
                .child(
                    text(format!(
                        "Measuring {}… started {} ago. A big folder can take several minutes.",
                        st.short(path),
                        fmt_ago(s.now - started)
                    ))
                    .key(format!("{key}-progress"))
                    .role("status")
                    .font(k.ty.small)
                    .color(k.c.text_muted)
                    .wrap()
                    .grow(1.0)
                    .min_w(0.0),
                )
                .child(stop),
        );
    }
    if let Some(why) = st.errors.get(path) {
        let hint = if why.contains("longer than") {
            " Try a smaller folder: look inside one of its folders instead."
        } else {
            ""
        };
        let title = if st.results.contains_key(path) {
            format!(
                "Couldn't measure {} again; these are the sizes from before",
                st.short(path)
            )
        } else {
            format!("Couldn't measure {}", st.short(path))
        };
        body = body.child(callout(
            &format!("{key}-error"),
            &title,
            &format!("{}.{hint}", why.trim_end_matches('.')),
            k,
        ));
    }
    let Some(m) = st.results.get(path) else {
        return body;
    };
    let du = &m.du;
    if du.dirs_top.is_empty() {
        return body.child(small("No folders in it take any space.", k).key(format!("{key}-none")));
    }
    let mut list = panel(&format!("{key}-dirs"), k);
    for (i, e) in du.dirs_top.iter().enumerate() {
        list = list.child(entry(
            st,
            e,
            path,
            du.total,
            &format!("{key}-dir-{i}"),
            Some(deeper),
            k,
        ));
    }
    body = body.child(list);
    let mut notes = Vec::new();
    if du.partial {
        notes.push("Spectra stopped before the end: the sizes are at least this much.".to_owned());
    }
    if du.unreadable > 0 {
        notes.push(format!(
            "{} folder{} couldn't be opened (macOS privacy protection), so the total leaves {} out.",
            fmt_count(du.unreadable as u64),
            if du.unreadable == 1 { "" } else { "s" },
            if du.unreadable == 1 { "it" } else { "them" }
        ));
    }
    if du.skipped > 0 {
        notes.push(format!(
            "{} other disk{} mounted inside {} not counted.",
            fmt_count(du.skipped as u64),
            if du.skipped == 1 { "" } else { "s" },
            if du.skipped == 1 { "is" } else { "are" }
        ));
    }
    for (i, n) in notes.into_iter().enumerate() {
        body = body.child(small(n, k).key(format!("{key}-note-{i}")));
    }
    body
}

/// One folder or file: its name under `root`, its size, a bar against the total, and Look
/// inside for a folder (`deeper` says whether it goes one deeper or starts over).
fn entry(
    st: &Storage,
    e: &Entry,
    root: &str,
    total: f64,
    key: &str,
    deeper: Option<bool>,
    k: &Kit,
) -> El {
    let c = &k.c;
    let name = e
        .path
        .strip_prefix(root)
        .and_then(|r| r.strip_prefix('/'))
        .filter(|r| !r.is_empty())
        .map_or_else(|| st.short(&e.path), str::to_owned);
    let mut top = row()
        .w_full()
        .gap(12.0)
        .center_y()
        .child(
            text(name.clone())
                .key(format!("{key}-name"))
                .font(k.ty.small)
                .color(c.text_base)
                .truncate()
                .shrink()
                .min_w(0.0)
                .help_when_truncated(e.path.clone()),
        )
        .child(el().grow(1.0))
        .child(
            text(fmt_size(e.bytes))
                .key(format!("{key}-size"))
                .font(k.ty.small.features("'tnum' 1"))
                .color(c.text_muted),
        );
    if let Some(deeper) = deeper {
        let p = e.path.clone();
        top = top.child(
            Button::new("Look inside")
                .variant(ButtonVariant::Ghost)
                .disabled(st.busy())
                .help(format!("Size the folders in {name}"))
                .on_press(move |s: &mut App| s.look_inside(&p, deeper))
                .build(k)
                .key(format!("{key}-look")),
        );
    }
    let frac = if total > 0.0 { e.bytes / total } else { 0.0 };
    col()
        .key(key.to_owned())
        .w_full()
        .gap(4.0)
        .child(top)
        .child(bar(&format!("{key}-bar"), frac, c.text_accent, k))
}

/// Time Machine's local snapshots, and a backup being written now.
fn time_machine(s: &App, k: &Kit) -> El {
    let st = &s.storage;
    let mut body = col().key("st-tm").w_full().gap(10.0);
    let Some(tm) = &st.tm else {
        return body.child(match &st.tm_error {
            Some(why) => callout("st-tm-error", "Couldn't read Time Machine", why, k),
            None if st.tm_reading => text("Reading Time Machine…")
                .key("st-tm-reading")
                .font(k.ty.small)
                .color(k.c.text_faint),
            None => small("Time Machine hasn't been read yet.", k).key("st-tm-none"),
        });
    };
    if let Some(why) = &st.tm_error {
        body = body.child(callout(
            "st-tm-error",
            "The last read failed; this is from before",
            why,
            k,
        ));
    }
    if tm.running {
        let how_far = tm.percent.map_or(String::new(), |p| format!(" ({p:.0}%)"));
        body = body.child(
            small(format!("A backup is being written now{how_far}."), k).key("st-tm-running"),
        );
    }
    if tm.snapshots.is_empty() {
        return body.child(small("No local snapshots on this Mac.", k).key("st-tm-empty"));
    }
    let mut list = panel("st-tm-list", k);
    for (i, snap) in tm.snapshots.iter().enumerate() {
        let when = if snap.when.is_empty() {
            snap.name.clone()
        } else {
            snap.when.clone()
        };
        let volume = match snap.volume.as_str() {
            "" => String::new(),
            "/" | "/System/Volumes/Data" => "Startup disk".to_owned(),
            v => v.to_owned(),
        };
        list = list.child(
            row()
                .key(format!("st-tm-{i}"))
                .w_full()
                .gap(12.0)
                .center_y()
                .child(
                    text(when)
                        .key(format!("st-tm-{i}-when"))
                        .font(k.ty.small.features("'tnum' 1"))
                        .color(k.c.text_base)
                        .truncate()
                        .shrink()
                        .min_w(0.0)
                        .help_when_truncated(snap.name.clone()),
                )
                .child(el().grow(1.0))
                .child(text(volume).font(k.ty.small).color(k.c.text_muted)),
        );
    }
    let n = tm.snapshots.len();
    body.child(list).child(
        small(
            format!(
                "{} local snapshot{}. Spectra doesn't report how much space each holds; macOS removes them on its own when a disk runs low.",
                fmt_count(n as u64),
                if n == 1 { "" } else { "s" }
            ),
            k,
        )
        .key("st-tm-note"),
    )
}
