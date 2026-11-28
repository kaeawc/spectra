//! The menu bar item on macOS: an icon that turns to its alert look while something needs a look,
//! and a small panel under it with the machine at a glance. Both show only what the window already
//! read: the panel and the icon start no Spectra call of their own.
//!
//! The panel is a second window with its own state ([`MenuBar`]). The main window's `poll` hands
//! it a fresh [`Snapshot`] when one differs from the last ([`refresh`]); a press in the panel opens
//! the main window on that page ([`go`]).

use std::collections::HashSet;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

use rune::{
    AppHandle, AppSpec, Display, KeyEvent, Tray, TrayEvent, TrayIcon, TrayItem, TraySpec, Tx,
    WindowHandle, WindowOptions, WindowSpec,
};

use crate::app::App;
use crate::findings::{Link, Severity};
use crate::groups::Nav;
use crate::procs::Process;
use crate::system::{Pressure, ThermalState};
use crate::whatswrong::Action;

/// The panel's window key: `--window menubar` renders it headless.
pub const PANEL: &str = "menubar";
/// The panel's width, in points.
pub const WIDTH: f32 = 340.0;
/// Readings each sparkline draws: four minutes at one every 5 s.
pub const SPARK: usize = 48;
/// Processes the panel lists.
pub const BUSIEST: usize = 3;
/// Points between the menu bar item and the panel's top.
const GAP: f64 = 6.0;
/// Points kept between the panel and the display's edge.
const MARGIN: f64 = 8.0;
/// A click on the menu bar item this soon after the panel lost focus is the click that took its
/// focus: it closes the panel rather than opening it again.
const REOPEN_AFTER: Duration = Duration::from_millis(400);

/// Where a press in the panel takes the main window.
#[derive(Clone, Debug, PartialEq)]
pub enum Go {
    Overview,
    Machine,
    Process(u32),
    App(String),
    /// Where a finding's subject link goes.
    Link(Link),
}

impl Go {
    /// Shows the page in the main window, the way the window's own links do.
    pub fn apply(&self, s: &mut App) {
        match self {
            Go::Overview => s.show(Nav::Overview),
            Go::Machine => s.show(Nav::System),
            Go::Process(pid) => s.open(*pid),
            Go::App(path) => s.open_app(path),
            Go::Link(link) => s.follow(*link),
        }
    }
}

/// The worst thing to look at: a "Right now" line or a finding.
#[derive(Clone, Debug, PartialEq)]
pub struct Top {
    pub severity: Severity,
    pub title: String,
    /// What it is about: a process, an app, or the machine.
    pub subject: String,
    pub go: Go,
}

/// One of the busiest processes.
#[derive(Clone, Debug, PartialEq)]
pub struct Busy {
    pub pid: u32,
    pub name: String,
    pub cpu: f64,
    pub rss_kib: f64,
}

/// The fullest disk a person keeps files on.
#[derive(Clone, Debug, PartialEq)]
pub struct Disk {
    pub label: String,
    /// Bytes still free.
    pub avail: f64,
    pub used_pct: f64,
}

/// Memory pressure as of the last reading.
#[derive(Clone, Debug, PartialEq)]
pub struct Mem {
    pub pressure: Pressure,
    pub free_pct: Option<f64>,
}

/// What the panel and the icon show, read from the main window's state.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Snapshot {
    /// Whether a process reading has landed yet.
    pub loaded: bool,
    /// Nothing needs a look: the icon's plain look.
    pub healthy: bool,
    /// How many things need a look, counted as the Overview's title counts them.
    pub needs_look: usize,
    /// CPU in use (percent of one core, summed), oldest first.
    pub cpu: Vec<f64>,
    pub cpu_now: Option<f64>,
    pub cores: usize,
    /// Memory in use (100 minus what's free for new work), oldest first.
    pub memory: Vec<f64>,
    pub mem: Option<Mem>,
    pub top: Option<Top>,
    pub busiest: Vec<Busy>,
    pub disk: Option<Disk>,
    pub thermal: Option<ThermalState>,
    /// When the process numbers are from, once that's worth saying.
    pub status: Option<String>,
}

/// The panel's own state.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct MenuBar {
    pub snap: Snapshot,
    /// The last press, kept for tests: the press itself goes to the main window.
    pub pressed: Option<Go>,
}

fn last<T: Copy>(values: impl DoubleEndedIterator<Item = T>, n: usize) -> Vec<T> {
    let mut out: Vec<T> = values.rev().take(n).collect();
    out.reverse();
    out
}

fn subject_of(s: &App, action: &Action) -> String {
    match action {
        Action::Process(pid) => s
            .process(*pid)
            .map_or_else(|| format!("pid {pid}"), |p| p.name().into_owned()),
        Action::App(path) => s.app_name(path),
        Action::Machine => "This Mac".to_owned(),
    }
}

fn go_of(action: &Action) -> Go {
    match action {
        Action::Process(pid) => Go::Process(*pid),
        Action::App(path) => Go::App(path.clone()),
        Action::Machine => Go::Machine,
    }
}

/// The panel's view of `s`, read-only.
pub fn snapshot(s: &App) -> Snapshot {
    let sys = &s.system;
    let now = s.right_now();
    // As the Overview's title counts: its findings, else its "Right now" lines.
    let needs_look = match s.problems() {
        0 => now.len(),
        n => n,
    };
    let top = if needs_look == 0 {
        None
    } else {
        // A finding "Right now" already says is the same news; on a tie "Right now" wins, since
        // the Overview shows it first.
        let covered: HashSet<&str> = now
            .iter()
            .flat_map(|i| i.covers.iter().map(String::as_str))
            .collect();
        let lines = now.iter().map(|i| Top {
            severity: i.severity,
            title: i.title.clone(),
            subject: subject_of(s, &i.action),
            go: go_of(&i.action),
        });
        let findings = s
            .findings
            .iter()
            .filter(|f| !covered.contains(f.id().as_str()))
            .map(|f| Top {
                severity: f.severity,
                title: f.title.clone(),
                subject: f.subject.clone(),
                go: Go::Link(f.link),
            });
        lines.chain(findings).min_by_key(|t| t.severity)
    };
    let procs = s.has_processes();
    let cpu = if procs {
        last(sys.cpu_history.iter().filter_map(|p| p.cpu), SPARK)
    } else {
        Vec::new()
    };
    let mem = sys.memory.last.as_ref().map(|m| Mem {
        pressure: m.pressure,
        free_pct: m.free_pct,
    });
    let memory = if mem.as_ref().is_some_and(|m| m.free_pct.is_some()) {
        last(
            sys.mem_history
                .iter()
                .map(|p| (100.0 - p.free_pct).max(0.0)),
            SPARK,
        )
    } else {
        Vec::new()
    };
    // The top few by reference; only those get an owned name.
    let mut leaders: Vec<&Process> = Vec::with_capacity(BUSIEST + 1);
    if procs {
        let before = |a: &Process, b: &Process| {
            b.cpu_pct
                .total_cmp(&a.cpu_pct)
                .then(a.pid.cmp(&b.pid))
                .is_lt()
        };
        for p in &s.procs {
            let at = leaders
                .iter()
                .position(|q| before(p, q))
                .unwrap_or(leaders.len());
            if at < BUSIEST {
                leaders.insert(at, p);
                leaders.truncate(BUSIEST);
            }
        }
    }
    let busiest: Vec<Busy> = leaders
        .into_iter()
        .map(|p| Busy {
            pid: p.pid,
            name: p.name().into_owned(),
            cpu: p.cpu_pct,
            rss_kib: p.rss_kib,
        })
        .collect();
    let disk = sys
        .volumes()
        .into_iter()
        .max_by(|a, b| a.used_pct().total_cmp(&b.used_pct()))
        .map(|v| Disk {
            label: v.label(),
            avail: v.avail,
            used_pct: v.used_pct(),
        });
    let cores = sys.load.last.as_ref().map_or(0, |l| l.cpus as usize);
    Snapshot {
        loaded: s.proc_loaded,
        healthy: needs_look == 0,
        needs_look,
        cpu,
        cpu_now: procs.then_some(s.stats.cpu),
        cores: if cores > 0 { cores } else { s.cores },
        memory,
        mem,
        top,
        busiest,
        disk,
        thermal: sys.thermal_state(),
        status: s.feed_status(),
    }
}

/// What the panel and the icon were last sent, so an unchanged reading sends nothing.
#[derive(Debug, Default)]
pub struct Pushed {
    snap: Option<Snapshot>,
    alert: Option<bool>,
}

impl Pushed {
    /// The snapshot to send when it differs from the last one sent, and the icon to show (alert
    /// or not) when that changed.
    pub fn next(&mut self, snap: Snapshot) -> (Option<Snapshot>, Option<bool>) {
        let alert = !snap.healthy;
        let icon = (self.alert != Some(alert)).then_some(alert);
        self.alert = Some(alert);
        if self.snap.as_ref() == Some(&snap) {
            return (None, icon);
        }
        self.snap = Some(snap.clone());
        (Some(snap), icon)
    }
}

/// What a primary click on the menu bar item does.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Click {
    Show,
    Hide,
    /// The click that took the panel's focus already closed it.
    Stay,
}

/// A click while the panel shows closes it; a click just after it lost focus was that same
/// click, so it stays closed; otherwise it opens.
pub fn on_click(shown: bool, since_hidden: Option<Duration>) -> Click {
    if shown {
        Click::Hide
    } else if since_hidden.is_some_and(|d| d < REOPEN_AFTER) {
        Click::Stay
    } else {
        Click::Show
    }
}

/// The display holding the middle of `icon` (`[x, y, w, h]`, physical pixels), else the primary.
pub fn display_of(icon: [i32; 4], displays: &[Display]) -> Option<&Display> {
    let (cx, cy) = (icon[0] + icon[2] / 2, icon[1] + icon[3] / 2);
    displays
        .iter()
        .find(|d| {
            let (x, y) = d.position;
            let (w, h) = (d.size.0 as i32, d.size.1 as i32);
            cx >= x && cx < x + w && cy >= y && cy < y + h
        })
        .or_else(|| displays.iter().find(|d| d.primary))
}

/// Where the panel's top-left goes, in physical pixels: centred under the menu bar item, just
/// below it, and kept inside `display` (`[x, y, w, h]`) when it's known. `scale` turns points
/// into pixels.
pub fn place(icon: [i32; 4], width: f32, scale: f64, display: Option<[i32; 4]>) -> (i32, i32) {
    let w = (f64::from(width) * scale).round() as i32;
    let margin = (MARGIN * scale).round() as i32;
    let mut x = icon[0] + icon[2] / 2 - w / 2;
    let y = icon[1] + icon[3] + (GAP * scale).round() as i32;
    if let Some([dx, _, dw, _]) = display {
        let (lo, hi) = (dx + margin, dx + dw - w - margin);
        x = if hi < lo { dx } else { x.clamp(lo, hi) };
    }
    (x, y)
}

/// The icon's side, in pixels: 22 points on a 2× menu bar, the height macOS draws a status
/// item's image at, so it isn't scaled up and blurred.
pub const ICON: u32 = 44;

/// The glyph is laid out on a 36-unit grid and drawn at `ICON` pixels.
const GRID: f32 = 36.0;

/// The menu bar glyph: three bars of a chart, black on transparent so macOS tints it, and with
/// `alert` a dot at the top right, cut clear of the bars.
pub fn glyph(alert: bool) -> Vec<u8> {
    let n = ICON as usize;
    let unit = GRID / ICON as f32;
    let mut px = vec![0u8; n * n * 4];
    // (left, top) of each 6-unit wide bar standing on y = 31, in grid units.
    let bars = [(5.0, 19.0), (15.0, 6.0), (25.0, 14.0)];
    let dot = (29.0_f32, 7.0_f32);
    for y in 0..n {
        for x in 0..n {
            // This pixel's centre on the grid.
            let (gx, gy) = ((x as f32 + 0.5) * unit, (y as f32 + 0.5) * unit);
            let on_bar = bars
                .iter()
                .any(|&(left, top)| gx >= left && gx < left + 6.0 && gy >= top && gy < 31.0);
            let mut a = if on_bar { 1.0 } else { 0.0 };
            if alert {
                // Distance in grid units; the dot's edge is softened over one unit.
                let d = ((gx - dot.0).powi(2) + (gy - dot.1).powi(2)).sqrt();
                // A gap around the dot keeps it apart from the bar under it.
                if d < 7.5 {
                    a = 0.0;
                }
                a = f32::max(a, (5.5 - d).clamp(0.0, 1.0));
            }
            px[(y * n + x) * 4 + 3] = (a * 255.0).round() as u8;
        }
    }
    px
}

fn icon(alert: bool) -> TrayIcon {
    TrayIcon::rgba(ICON, ICON, glyph(alert)).template(true)
}

fn menu() -> Vec<TrayItem> {
    vec![
        TrayItem::action("open", "Open Spectra Desktop"),
        TrayItem::Separator,
        TrayItem::action("quit", "Quit"),
    ]
}

/// Whether a headless run asks for the panel by its key (`--window menubar`) or for the list of
/// windows, so other headless runs draw the main window exactly as before.
pub fn asks_for_panel(args: impl IntoIterator<Item = String>) -> bool {
    let mut args = args.into_iter();
    while let Some(a) = args.next() {
        match a.as_str() {
            "--list-windows" => return true,
            "--window" if args.next().as_deref() == Some(PANEL) => return true,
            _ if a.strip_prefix("--window=") == Some(PANEL) => return true,
            _ => {}
        }
    }
    false
}

/// The handles the menu bar item works through, set once at startup on macOS.
struct Live {
    panel: AppHandle<MenuBar>,
    window: WindowHandle,
    main: AppHandle<App>,
    tray: Mutex<Option<Tray>>,
    pushed: Mutex<Pushed>,
    hidden_at: Mutex<Option<Instant>>,
}

static LIVE: OnceLock<Live> = OnceLock::new();

fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

fn on_key(_: &mut MenuBar, ev: &KeyEvent, tx: &mut Tx) -> bool {
    if ev.key == rune::Key::Escape {
        tx.close_window();
        return true;
    }
    false
}

/// Losing focus closes the panel, as a menu does.
fn on_focus(_: &mut MenuBar, focused: bool, tx: &mut Tx) {
    if !focused {
        tx.close_window();
    }
}

fn on_close(_: &mut MenuBar, _: &mut Tx) {
    hidden();
}

/// The panel went away: the menu bar item stops looking pressed.
fn hidden() {
    if let Some(live) = LIVE.get() {
        *lock(&live.hidden_at) = Some(Instant::now());
        if let Some(tray) = lock(&live.tray).as_ref() {
            tray.set_highlighted(false);
        }
    }
}

fn panel_spec() -> WindowSpec<MenuBar> {
    let spec = AppSpec {
        scenes: &[
            ("menubar", crate::sample_menubar::menubar_scene),
            ("menubar-alert", crate::sample_menubar::menubar_alert_scene),
            (
                "menubar-reading",
                crate::sample_menubar::menubar_reading_scene,
            ),
        ],
        on_key: Some(on_key),
        ..AppSpec::new(
            "Spectra Desktop",
            (WIDTH, 360.0),
            MenuBar::default,
            crate::ui::menubar::view,
        )
    };
    let app = rune::App::new(spec)
        .fit_content_height()
        .on_focus(on_focus)
        .on_close(on_close);
    WindowSpec::from_app(PANEL, app).configured_options(WindowOptions::launcher_panel())
}

/// Adds the panel window and the menu bar item: on macOS in a real window, and the panel alone in
/// a headless run that asks for it. Elsewhere it adds nothing.
pub fn install(app: &mut rune::App<App>) {
    let headless = rune::tooling::is_headless();
    let live = cfg!(target_os = "macos") && !headless;
    if !live && !(headless && asks_for_panel(std::env::args().skip(1))) {
        return;
    }
    let (panel, window) = app.window(panel_spec());
    if !live {
        return;
    }
    // The tray first: without it nothing can open the panel, so nothing is kept to update it.
    // Its callback finds `LIVE` empty until it's set just below, and a click can't come sooner
    // than the event loop runs.
    let spec = TraySpec::new("spectra-desktop", "Spectra Desktop", icon(false))
        .tooltip("Spectra Desktop")
        .menu(menu());
    let tray = match Tray::new(spec, on_tray) {
        Ok(tray) => tray,
        Err(e) => {
            eprintln!("spectra-desktop: no menu bar item: {e}");
            return;
        }
    };
    let _ = LIVE.set(Live {
        panel,
        window,
        main: app.handle(),
        tray: Mutex::new(Some(tray)),
        pushed: Mutex::new(Pushed::default()),
        hidden_at: Mutex::new(None),
    });
}

/// Runs on the main thread on macOS.
fn on_tray(ev: TrayEvent) {
    let Some(live) = LIVE.get() else { return };
    match ev {
        TrayEvent::Activate { .. } => toggle(live),
        TrayEvent::Menu(id) => match id.as_str() {
            "open" => live.main.open(),
            "quit" => live.main.quit(),
            _ => {}
        },
        _ => {}
    }
}

fn toggle(live: &Live) {
    let since = lock(&live.hidden_at).map(|t| t.elapsed());
    match on_click(live.panel.is_visible(), since) {
        Click::Stay => {}
        Click::Hide => {
            live.panel.hide();
            hidden();
        }
        Click::Show => {
            // The tray's lock is held only while asking it something, never across a request
            // to a window, whose handlers take it too.
            let rect = lock(&live.tray).as_ref().and_then(Tray::rect);
            if let Some(rect) = rect {
                let displays = rune::displays();
                let d = display_of(rect, &displays);
                let scale = d
                    .map(|d| d.scale)
                    .or_else(|| live.window.scale())
                    .unwrap_or(2.0);
                let bounds =
                    d.map(|d| [d.position.0, d.position.1, d.size.0 as i32, d.size.1 as i32]);
                live.panel.set_position(place(rect, WIDTH, scale, bounds));
            }
            // The main window's poll brings the snapshot's "Updated … ago" up to date; it starts
            // no reading while the window is hidden.
            live.main.wake();
            live.panel.open();
            if let Some(tray) = lock(&live.tray).as_ref() {
                tray.set_highlighted(true);
            }
        }
    }
}

/// Whether `refresh` has built a snapshot yet.
static LOOKED: AtomicBool = AtomicBool::new(false);

/// Sends the panel a fresh snapshot when it changed, and the icon its look when that changed.
/// Called from the main window's `poll`, which runs every frame: it builds a snapshot only when
/// the window says something `changed` (a reading landed, a status line aged), and once at the
/// start. Does nothing without a menu bar item.
pub fn refresh(s: &App, changed: bool) {
    let Some(live) = LIVE.get() else { return };
    if !changed && LOOKED.swap(true, Ordering::Relaxed) {
        return;
    }
    LOOKED.store(true, Ordering::Relaxed);
    let (snap, alert) = lock(&live.pushed).next(snapshot(s));
    if let Some(snap) = snap {
        live.panel.update_if(move |m, _| {
            let changed = m.snap != snap;
            m.snap = snap;
            changed
        });
    }
    if let Some(alert) = alert
        && let Some(tray) = lock(&live.tray).as_ref()
        && let Err(e) = tray.set_icon(icon(alert))
    {
        eprintln!("spectra-desktop: menu bar icon: {e}");
    }
}

/// A press in the panel: the main window comes forward on that page.
pub fn go(target: Go) {
    let Some(live) = LIVE.get() else { return };
    live.main.update(move |s, _| target.apply(s));
    live.main.open();
}

/// "Open Spectra Desktop": the main window comes forward as it was.
pub fn open_main() {
    if let Some(live) = LIVE.get() {
        live.main.open();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::app::init;
    use crate::sample_menubar::{alert_app, healthy_app};

    #[test]
    fn a_machine_with_findings_needs_a_look_and_names_the_worst() {
        let s = alert_app();
        let snap = snapshot(&s);
        assert!(snap.loaded);
        assert!(!snap.healthy);
        assert_eq!(snap.needs_look, s.problems());
        let top = snap.top.expect("something to look at");
        let worst = s
            .findings
            .iter()
            .map(|f| f.severity)
            .chain(s.right_now().iter().map(|i| i.severity))
            .min()
            .unwrap();
        assert_eq!(top.severity, worst, "the worst one shows");
        assert!(!top.title.is_empty());
    }

    #[test]
    fn a_calm_machine_is_healthy_with_nothing_on_top() {
        let snap = snapshot(&healthy_app());
        assert!(snap.loaded);
        assert!(snap.healthy);
        assert_eq!(snap.needs_look, 0);
        assert_eq!(snap.top, None);
        assert_eq!(snap.busiest.len(), BUSIEST, "the busiest still show");
        assert!(snap.cpu.len() >= 2 && snap.memory.len() >= 2);
        assert_eq!(snap.thermal, Some(ThermalState::Nominal));
        assert_eq!(
            snap.disk.as_ref().map(|d| d.label.as_str()),
            Some("Startup disk")
        );
    }

    #[test]
    fn nothing_read_yet_is_not_an_alert() {
        let snap = snapshot(&init());
        assert!(!snap.loaded);
        assert!(snap.healthy);
        assert!(snap.busiest.is_empty() && snap.cpu.is_empty() && snap.memory.is_empty());
        assert_eq!((snap.disk, snap.thermal, snap.top), (None, None, None));
    }

    #[test]
    fn the_busiest_three_go_by_cpu_then_pid() {
        let s = alert_app();
        let snap = snapshot(&s);
        assert_eq!(snap.busiest.len(), BUSIEST);
        let mut want: Vec<(f64, u32)> = s.procs.iter().map(|p| (p.cpu_pct, p.pid)).collect();
        want.sort_by(|a, b| b.0.total_cmp(&a.0).then(a.1.cmp(&b.1)));
        let got: Vec<(f64, u32)> = snap.busiest.iter().map(|b| (b.cpu, b.pid)).collect();
        assert_eq!(got, want[..BUSIEST].to_vec());
        let p = s.process(got[0].1).unwrap();
        assert_eq!(snap.busiest[0].name, p.name());
        assert_eq!(snap.busiest[0].rss_kib, p.rss_kib);
    }

    #[test]
    fn the_top_line_is_the_worst_finding_without_right_now() {
        let mut s = alert_app();
        s.whatswrong.report = None;
        let snap = snapshot(&s);
        let f = &s.findings[0];
        let top = snap.top.unwrap();
        assert_eq!(top.title, f.title);
        assert_eq!(top.subject, f.subject);
        assert_eq!(top.go, Go::Link(f.link));
    }

    #[test]
    fn right_now_wins_a_tie_with_a_finding() {
        let mut s = alert_app();
        for f in &mut s.findings {
            f.severity = Severity::Warning;
        }
        let first = s.right_now().into_iter().next().expect("a Right now line");
        assert_eq!(first.severity, Severity::Warning);
        let top = snapshot(&s).top.unwrap();
        assert_eq!(top.title, first.title);
        assert_eq!(top.go, go_of(&first.action));
    }

    #[test]
    fn right_now_alone_counts_as_something_to_look_at() {
        let mut s = alert_app();
        s.findings.clear();
        let n = s.right_now().len();
        assert!(n > 0);
        let snap = snapshot(&s);
        assert_eq!(snap.needs_look, n);
        assert!(!snap.healthy);
    }

    #[test]
    fn a_snapshot_is_sent_only_when_it_changed() {
        let mut p = Pushed::default();
        let calm = snapshot(&healthy_app());
        assert_eq!(p.next(calm.clone()), (Some(calm.clone()), Some(false)));
        assert_eq!(p.next(calm.clone()), (None, None), "nothing new");
        let mut busier = calm.clone();
        busier.cpu_now = Some(999.0);
        assert_eq!(
            p.next(busier.clone()),
            (Some(busier.clone()), None),
            "the icon stays"
        );
        let mut worse = busier.clone();
        worse.healthy = false;
        worse.needs_look = 1;
        assert_eq!(p.next(worse.clone()), (Some(worse), Some(true)));
        assert_eq!(p.next(busier.clone()), (Some(busier), Some(false)));
    }

    #[test]
    fn the_panel_sits_centred_under_the_item() {
        // A 44×48 px item at x 2000 on a 2× display 3024 px wide.
        let display = Some([0, 0, 3024, 1964]);
        let (x, y) = place([2000, 0, 44, 48], WIDTH, 2.0, display);
        assert_eq!(x, 2022 - 340);
        assert_eq!(y, 48 + 12);
        assert_eq!(place([2000, 0, 44, 48], WIDTH, 2.0, None), (x, y));
    }

    #[test]
    fn the_panel_stays_on_the_items_display() {
        let display = Some([0, 0, 3024, 1964]);
        // Near the right edge: pushed left, 16 px in.
        assert_eq!(
            place([2990, 0, 30, 48], WIDTH, 2.0, display).0,
            3024 - 680 - 16
        );
        // Near the left edge.
        assert_eq!(place([10, 0, 30, 48], WIDTH, 2.0, display).0, 16);
        // A display left of the main one, at a negative origin, 1× scale.
        let left = Some([-1920, 0, 1920, 1080]);
        assert_eq!(place([-30, 0, 24, 24], WIDTH, 1.0, left).0, -340 - 8);
        // A display narrower than the panel: its left edge.
        assert_eq!(
            place([100, 0, 20, 20], WIDTH, 1.0, Some([0, 0, 300, 600])).0,
            0
        );
    }

    #[test]
    fn the_items_display_is_the_one_holding_it() {
        let d = |x: i32, w: u32, primary: bool| Display {
            id: None,
            name: None,
            position: (x, 0),
            size: (w, 1000),
            scale: if primary { 2.0 } else { 1.0 },
            primary,
        };
        let all = [d(0, 3000, true), d(3000, 1920, false)];
        assert_eq!(display_of([3500, 0, 30, 30], &all), Some(&all[1]));
        assert_eq!(display_of([100, 0, 30, 30], &all), Some(&all[0]));
        assert_eq!(
            display_of([-500, 0, 30, 30], &all),
            Some(&all[0]),
            "else the primary"
        );
        assert_eq!(display_of([0, 0, 1, 1], &[]), None);
    }

    #[test]
    fn a_click_opens_closes_or_was_the_one_that_closed_it() {
        assert_eq!(on_click(true, None), Click::Hide);
        assert_eq!(on_click(false, None), Click::Show);
        assert_eq!(
            on_click(false, Some(Duration::from_millis(80))),
            Click::Stay
        );
        assert_eq!(on_click(false, Some(Duration::from_secs(3))), Click::Show);
    }

    #[test]
    fn the_glyph_is_a_mask_with_a_dot_for_alerts() {
        let n = ICON as usize;
        // A point on the 36-unit grid, as a pixel.
        let at = |g: f32| (g / GRID * ICON as f32) as usize;
        let alpha = |px: &[u8], x: f32, y: f32| px[(at(y) * n + at(x)) * 4 + 3];
        let (plain, alert) = (glyph(false), glyph(true));
        assert_eq!(plain.len(), n * n * 4);
        // Black: only the alpha channel carries the shape.
        assert!(plain.chunks(4).all(|p| p[..3] == [0, 0, 0]));
        assert_eq!(alpha(&plain, 17.5, 20.5), 255, "the middle bar");
        assert_eq!(alpha(&plain, 29.0, 7.0), 0, "no dot");
        assert_eq!(alpha(&alert, 29.0, 7.0), 255, "the dot");
        assert_eq!(alpha(&alert, 17.5, 20.5), 255, "the bars stay");
        let corner = || (at(24.0)..at(34.0)).flat_map(|x| (at(2.0)..at(12.0)).map(move |y| (x, y)));
        let raw = |px: &[u8], (x, y): (usize, usize)| px[(y * n + x) * 4 + 3];
        assert!(corner().all(|p| raw(&plain, p) == 0));
        assert!(corner().filter(|&p| raw(&alert, p) > 0).count() > 70);
    }

    #[test]
    fn only_a_run_asking_for_the_panel_gets_it() {
        let ask = |a: &[&str]| asks_for_panel(a.iter().map(|s| (*s).to_owned()));
        assert!(ask(&["--snap", "x.png", "--window", "menubar"]));
        assert!(ask(&["--window=menubar", "--snap", "x.png"]));
        assert!(ask(&["--list-windows"]));
        assert!(!ask(&["--snap", "x.png", "--scene", "sample"]));
        assert!(!ask(&["--window", "main"]));
    }

    #[test]
    fn a_press_opens_its_page_in_the_main_window() {
        let mut s = alert_app();
        Go::Machine.apply(&mut s);
        assert_eq!(s.nav, Nav::System);
        let pid = s.procs[0].pid;
        Go::Process(pid).apply(&mut s);
        assert_eq!(s.selected, Some(pid));
        Go::Overview.apply(&mut s);
        assert!(s.overview_visible());
        Go::Link(Link::System).apply(&mut s);
        assert_eq!(s.nav, Nav::System);
    }
}
