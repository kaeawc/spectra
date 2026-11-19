//! Spectra Desktop: a diagnosis-first view of the JVMs on this machine.
//!
//! `cargo run` opens the window. `cargo run -- --snap out.png --scene sample [--theme light]`
//! renders it headless with sample data.

mod data;
mod findings;

use std::collections::{HashMap, VecDeque};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::time::Duration;

use data::{Jvm, Sample, fmt_kib};
use findings::{Chart, Finding, Severity, diagnose};
use rune::{AppSpec, Chrome, Color, Cursor, Cx, El, KeyEvent, ScrollSpec, Shape, Tx, canvas, col, el, row, text};
use rune_kit::shell::{self, PaneState, Sidebar, SidebarRow, SplitView, Toolbar};
use rune_kit::{Button, ButtonVariant, Kit};

const HISTORY: usize = 120;
const REFRESH: Duration = Duration::from_secs(2);

type Feed = Result<Vec<Jvm>, String>;

struct App {
    sidebar: PaneState,
    jvms: Vec<Jvm>,
    history: HashMap<u32, VecDeque<Sample>>,
    /// The readings pinned by "Pin baseline", which the evidence compares against.
    baseline: HashMap<u32, Sample>,
    /// `None` shows every JVM's findings.
    selected: Option<u32>,
    expanded: Option<String>,
    loaded: bool,
    error: Option<String>,
    feed: Option<Receiver<Feed>>,
    feed_tx: Option<Sender<Feed>>,
}

fn init() -> App {
    let (tx, rx) = channel();
    App {
        sidebar: PaneState::new(240.0).range(160.0, 420.0),
        jvms: Vec::new(),
        history: HashMap::new(),
        baseline: HashMap::new(),
        selected: None,
        expanded: None,
        loaded: false,
        error: None,
        feed: Some(rx),
        feed_tx: Some(tx),
    }
}

impl App {
    fn land(&mut self, feed: Feed) {
        self.loaded = true;
        match feed {
            Ok(jvms) => {
                self.error = None;
                self.history.retain(|pid, _| jvms.iter().any(|j| j.pid == *pid));
                for j in &jvms {
                    let h = self.history.entry(j.pid).or_default();
                    h.push_back(j.sample());
                    if h.len() > HISTORY {
                        h.pop_front();
                    }
                }
                if self.selected.is_some_and(|pid| jvms.iter().all(|j| j.pid != pid)) {
                    self.selected = None;
                }
                self.jvms = jvms;
            }
            Err(e) => self.error = Some(e),
        }
    }

    fn history(&self, pid: u32) -> Vec<Sample> {
        self.history.get(&pid).map(|h| h.iter().copied().collect()).unwrap_or_default()
    }

    /// The findings on screen, worst first.
    fn findings(&self) -> Vec<Finding> {
        let mut all: Vec<Finding> = self
            .jvms
            .iter()
            .filter(|j| self.selected.is_none_or(|pid| pid == j.pid))
            .flat_map(|j| diagnose(j, &self.history(j.pid)))
            .collect();
        all.sort_by_key(|f| (f.severity, f.pid));
        all
    }

    fn worst(&self, pid: u32) -> Severity {
        let jvm = self.jvms.iter().find(|j| j.pid == pid);
        jvm.and_then(|j| diagnose(j, &self.history(pid)).iter().map(|f| f.severity).min()).unwrap_or(Severity::Healthy)
    }
}

/// Sample JVMs for headless renders: one leaking, one tight on heap, one healthy.
fn sample_scene(s: &mut App) {
    let jvm = |pid, main: &str, args: &str, ou: f64, fgc, fgct| Jvm {
        pid,
        main_class: main.into(),
        jdk_vendor: "Temurin".into(),
        jdk_version: "21.0.5".into(),
        vm_args: args.into(),
        thread_count: 48,
        gc: Some(data::Gc { ou, eu: 80_000.0, mu: 60_000.0, ygc: 40, fgc, fgct, gct: fgct + 0.4, ..data::Gc::default() }),
        classes: Some(data::Classes { loaded: 15_000 }),
    };
    s.baseline.clear();
    for i in 0..8 {
        let step = i as f64;
        s.land(Ok(vec![
            jvm(4101, "com.acme.checkout.Server", "-Xmx2g", 900_000.0 + 110_000.0 * step, 6, 4.2),
            jvm(4102, "org.gradle.launcher.daemon.bootstrap.GradleDaemon", "-Xmx4g -XX:+HeapDumpOnOutOfMemoryError", 3_150_000.0, 0, 0.0),
            jvm(4103, "com.acme.search.Indexer", "-Xmx1g -XX:+HeapDumpOnOutOfMemoryError", 210_000.0 + 15_000.0 * (step % 3.0), 0, 0.0),
        ]));
        if i == 2 {
            s.baseline = s.jvms.iter().map(|j| (j.pid, j.sample())).collect();
        }
    }
    s.expanded = s.findings().first().map(Finding::id);
}

fn setup(s: &mut App, wake: rune::Waker) {
    let Some(tx) = s.feed_tx.take() else { return };
    std::thread::spawn(move || {
        loop {
            if tx.send(data::fetch()).is_err() {
                return;
            }
            wake();
            std::thread::sleep(REFRESH);
        }
    });
}

fn poll(s: &mut App, _: f64, _: &mut Tx) -> bool {
    let mut landed = false;
    while let Some(feed) = s.feed.as_ref().and_then(|rx| rx.try_recv().ok()) {
        s.land(feed);
        landed = true;
    }
    landed
}

fn ink(severity: Severity, k: &Kit) -> Color {
    match severity {
        Severity::Critical => k.c.danger.fg,
        Severity::Warning => k.c.warning.fg,
        Severity::Info => k.c.info.fg,
        Severity::Healthy => k.c.success.fg,
    }
}

fn dot(severity: Severity, k: &Kit) -> El {
    el().size(8.0, 8.0).radius(4.0).fill(ink(severity, k))
}

/// A line through `values`, scaled to its own range, filling the element.
fn sparkline(values: &[f64], color: Color) -> El {
    let (lo, hi) = values.iter().fold((f64::MAX, f64::MIN), |(lo, hi), v| (lo.min(*v), hi.max(*v)));
    let span = (hi - lo).max(1.0);
    let last = values.len().saturating_sub(1).max(1) as f32;
    let points: Vec<[f32; 2]> =
        values.iter().enumerate().map(|(i, v)| [i as f32 / last * 100.0, 36.0 - ((v - lo) / span) as f32 * 32.0]).collect();
    canvas(Shape::new().stroke(points, 1.5, color).view_box(100.0, 40.0)).w_full().h(56.0)
}

fn delta(now: f64, then: f64, unit: fn(f64) -> String) -> String {
    let d = now - then;
    format!("{}{}", if d >= 0.0 { "+" } else { "−" }, unit(d.abs()))
}

fn evidence(s: &App, f: &Finding, k: &Kit) -> El {
    let c = &k.c;
    let history = s.history(f.pid);
    let mut body = col().w_full().gap(10.0).pt(12.0);
    let series: Vec<f64> = match f.chart {
        Chart::HeapUsed => history.iter().map(|x| x.heap_used_kib).collect(),
        Chart::OldGen => history.iter().map(|x| x.old_used_kib).collect(),
        Chart::None => Vec::new(),
    };
    if series.len() >= 2 {
        let label = if f.chart == Chart::OldGen { "Old generation" } else { "Heap used" };
        body = body
            .child(text(format!("{label}, last {} readings", series.len())).font(k.ty.small).color(c.text_faint))
            .child(sparkline(&series, ink(f.severity, k)));
    }
    if let (Some(base), Some(now)) = (s.baseline.get(&f.pid), history.last()) {
        let count = |n: f64| format!("{n:.0}");
        let line = format!(
            "Since baseline: heap {}, old gen {}, classes {}, threads {}, GCs {}",
            delta(now.heap_used_kib, base.heap_used_kib, fmt_kib),
            delta(now.old_used_kib, base.old_used_kib, fmt_kib),
            delta(now.classes as f64, base.classes as f64, count),
            delta(now.threads as f64, base.threads as f64, count),
            delta(now.gc_count as f64, base.gc_count as f64, count),
        );
        body = body.child(text(line).key("compare").font(k.ty.small).color(c.text_muted).wrap());
    }
    if !f.fix.is_empty() {
        body = body.child(text(f.fix.clone()).key("fix").font(k.ty.ui).color(c.text_base).wrap());
    }
    body
}

fn card(s: &App, f: &Finding, k: &Kit) -> El {
    let c = &k.c;
    let id = f.id();
    let open = s.expanded.as_deref() == Some(id.as_str());
    let head = row()
        .w_full()
        .gap(10.0)
        .center_y()
        .child(dot(f.severity, k))
        .child(
            col()
                .grow(1.0)
                .gap(2.0)
                .child(text(f.title.clone()).font(k.ty.ui_medium).color(c.text_base))
                .child(text(f.detail.clone()).font(k.ty.small).color(c.text_muted).wrap()),
        )
        .child(text(format!("{} · {}", f.subject, f.pid)).font(k.ty.small).color(c.text_faint));
    let mut card = col()
        .key(format!("finding-{id}"))
        .role("button")
        .label(f.title.clone())
        .focusable(true)
        .cursor(Cursor::Pointer)
        .w_full()
        .py(12.0)
        .px(16.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .on_press(move |s: &mut App| s.expanded = if s.expanded.as_deref() == Some(id.as_str()) { None } else { Some(id.clone()) })
        .child(head);
    if open {
        card = card.child(evidence(s, f, k));
    }
    card
}

fn notice(title: &str, body: &str, k: &Kit) -> El {
    col()
        .w_full()
        .gap(4.0)
        .py(48.0)
        .items_center()
        .child(text(title.to_owned()).font(k.ty.body_medium).color(k.c.text_base))
        .child(text(body.to_owned()).font(k.ty.small).color(k.c.text_muted).wrap())
}

fn sidebar(s: &App, k: &Kit) -> El {
    let overview = SidebarRow::new("All JVMs")
        .detail(s.jvms.len().to_string())
        .height(32.0)
        .selected(s.selected.is_none())
        .on_select(|s: &mut App, _| s.selected = None)
        .build(k)
        .key("all");
    let sb = Sidebar::new().header(overview);
    s.jvms
        .iter()
        .fold(sb, |sb, j| {
            let pid = j.pid;
            let r = SidebarRow::new(j.name())
                .height(44.0)
                .avatar(dot(s.worst(pid), k))
                .subtitle(format!("pid {pid} · JDK {}", j.jdk_version))
                .selected(s.selected == Some(pid))
                .on_select(move |s: &mut App, _| s.selected = Some(pid))
                .build(k)
                .key(format!("jvm-{pid}"));
            sb.child(r)
        })
        .build(k)
}

fn view(s: &App, cx: &mut Cx<App>) -> El {
    let k = Kit::neutral(cx.system_dark());
    let findings = s.findings();
    let mut list = col().key("findings").w_full().padding(24.0).gap(10.0);
    if let Some(e) = &s.error {
        list = list.child(notice("Couldn't reach Spectra", e, &k));
    } else if !s.loaded {
        list = list.child(notice("Looking for JVMs", "Spectra is reading every running JVM.", &k));
    } else if s.jvms.is_empty() {
        list = list.child(notice("No JVMs running", "Start one and it shows up here, with nothing to set up.", &k));
    }
    list = findings.iter().fold(list, |list, f| list.child(card(s, f, &k)));
    let main = col().w_full().h_full().min_h(0.0).scroll(ScrollSpec { stick_bottom: false }).child(list);

    let problems = findings.iter().filter(|f| f.severity < Severity::Info).count();
    let title = match s.selected.and_then(|pid| s.jvms.iter().find(|j| j.pid == pid)) {
        Some(j) => j.name(),
        None if problems == 0 => "Nothing needs attention".to_owned(),
        None => format!("{problems} to look at"),
    };
    let pin = Button::new(if s.baseline.is_empty() { "Pin baseline" } else { "Clear baseline" })
        .variant(ButtonVariant::Ghost)
        .on_press(|s: &mut App| {
            s.baseline = if s.baseline.is_empty() { s.jvms.iter().map(|j| (j.pid, j.sample())).collect() } else { HashMap::new() };
        })
        .build(&k)
        .key("pin-baseline");
    SplitView::new(main)
        .sidebar(&s.sidebar, |s: &mut App| &mut s.sidebar, sidebar(s, &k))
        .toolbar(Toolbar::new().title(title).trailing(pin))
        .titlebar_inset(cx.titlebar_inset())
        .reduced_motion(cx.is_reduced_motion())
        .build(&k)
}

fn on_key(s: &mut App, ev: &KeyEvent, _: &mut Tx) -> bool {
    if shell::is_sidebar_toggle(ev) {
        s.sidebar.toggle();
        return true;
    }
    false
}

fn main() {
    let chrome = Chrome { full_size_content: true, traffic_lights: Some((16.0, 17.0)), drag_regions: true };
    let spec = AppSpec {
        min_size: Some((640.0, 420.0)),
        scenes: &[("sample", sample_scene)],
        setup: Some(setup),
        poll: Some(poll),
        on_key: Some(on_key),
        ..AppSpec::new("Spectra", (980.0, 640.0), init, view)
    };
    rune::App::new(spec).chrome(chrome).run();
}
