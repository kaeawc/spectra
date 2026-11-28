//! The menu bar panel: this Mac at a glance under the menu bar item. A headline, CPU and memory
//! over the last few minutes, the worst thing to look at, the busiest processes, the fullest disk
//! and the thermal state, and a way to the window. Each row opens the window on its page.

use rune::{Color, Cursor, Cx, El, Shape, canvas, col, el, row, text};
use rune_kit::{Button, ButtonVariant, Kit};

use super::card::{dot, ink};
use super::chart::{SPAN, scale_y, x_at};
use crate::findings::Severity;
use crate::menubar::{Go, MenuBar, Snapshot, go, open_main};
use crate::procs::{fmt_cpu, fmt_mem};
use crate::system::{Pressure, ThermalState, fmt_size};

const SPARK_H: f32 = 28.0;

/// The headline: nothing to look at, how many things, or still reading.
pub fn headline(s: &Snapshot) -> String {
    if !s.loaded {
        "Reading this Mac…".to_owned()
    } else if s.healthy {
        "Nothing needs attention".to_owned()
    } else {
        format!("{} to look at", s.needs_look)
    }
}

fn press(target: Go) -> impl Fn(&mut MenuBar, &mut rune::Tx) + 'static {
    move |m: &mut MenuBar, tx: &mut rune::Tx| {
        m.pressed = Some(target.clone());
        go(target.clone());
        tx.close_window();
    }
}

/// A row that opens `target` in the window: highlighted under the pointer, read as a button.
fn pressable(key: &str, label: String, target: Go, k: &Kit) -> El {
    row()
        .key(key.to_owned())
        .role("button")
        .label(label)
        .focusable(true)
        .cursor(Cursor::Pointer)
        .w_full()
        .gap(8.0)
        .center_y()
        .px(8.0)
        .py(6.0)
        .radius(k.radius.md)
        .hover(k.c.hover)
        .on_press_tx(press(target))
}

fn spark(values: &[f64], color: Color, k: &Kit) -> El {
    if values.len() < 2 {
        return el().w_full().h(SPARK_H).center().child(
            text("Collecting readings")
                .font(k.ty.small)
                .color(k.c.text_faint),
        );
    }
    let n = values.len();
    let points: Vec<[f32; 2]> = scale_y(values, SPARK_H)
        .iter()
        .enumerate()
        .map(|(i, y)| [x_at(i, n), *y])
        .collect();
    canvas(
        Shape::new()
            .stroke(points, 1.5, color)
            .view_box(SPAN, SPARK_H),
    )
    .w_full()
    .h(SPARK_H)
}

fn tile(key: &str, label: &str, value: String, line: El, target: Go, k: &Kit) -> El {
    let c = &k.c;
    col()
        .key(key.to_owned())
        .role("button")
        .label(format!("{label}: {value}"))
        .focusable(true)
        .cursor(Cursor::Pointer)
        .w(0.0)
        .grow(1.0)
        .min_w(0.0)
        .gap(4.0)
        .py(8.0)
        .px(10.0)
        .radius(k.radius.md)
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .hover(c.hover)
        .on_press_tx(press(target))
        .child(text(label.to_owned()).font(k.ty.small).color(c.text_muted))
        .child(
            text(value)
                .key(format!("{key}-now"))
                .font(k.ty.small_medium.features("'tnum' 1"))
                .color(c.text_base)
                .truncate()
                .min_w(0.0),
        )
        .child(line)
}

fn pressure_ink(p: Pressure, k: &Kit) -> Color {
    match p {
        Pressure::Normal => k.c.success.fg,
        Pressure::Warning => k.c.warning.fg,
        Pressure::Critical => k.c.danger.fg,
        Pressure::Unknown => k.c.text_faint,
    }
}

fn charts(s: &Snapshot, k: &Kit) -> Option<El> {
    let mut tiles = row().key("mb-charts").w_full().gap(8.0);
    let mut any = false;
    if let Some(cpu) = s.cpu_now {
        tiles = tiles.child(tile(
            "mb-cpu",
            "CPU",
            match s.cores {
                0 => format!("{cpu:.0}%"),
                n => format!("{:.1} of {n} cores busy", cpu / 100.0),
            },
            spark(&s.cpu, k.c.text_accent, k),
            Go::Machine,
            k,
        ));
        any = true;
    }
    if let Some(m) = &s.mem {
        let value = match m.free_pct {
            Some(free) => format!("{} · {free:.0}% free", m.pressure.label()),
            None => m.pressure.label().to_owned(),
        };
        tiles = tiles.child(tile(
            "mb-mem",
            "Memory",
            value,
            spark(&s.memory, pressure_ink(m.pressure, k), k),
            Go::Machine,
            k,
        ));
        any = true;
    }
    any.then_some(tiles)
}

fn top(s: &Snapshot, k: &Kit) -> Option<El> {
    let c = &k.c;
    let t = s.top.as_ref()?;
    let words = col()
        .grow(1.0)
        .shrink()
        .min_w(0.0)
        .gap(2.0)
        .child(
            text(t.title.clone())
                .key("mb-top-title")
                .font(k.ty.small_medium)
                .color(c.text_base)
                .truncate()
                .help_when_truncated(t.title.clone()),
        )
        .child(
            text(t.subject.clone())
                .key("mb-top-subject")
                .font(k.ty.small)
                .color(c.text_muted)
                .truncate(),
        );
    Some(
        pressable(
            "mb-top",
            format!("{}: {}", t.title, t.subject),
            t.go.clone(),
            k,
        )
        .fill(c.layer_01)
        .border(1.0, c.border_weak)
        .child(dot(t.severity, k))
        .child(words)
        .child(text("›").font(k.ty.ui).color(c.text_faint)),
    )
}

fn busiest(s: &Snapshot, k: &Kit) -> Option<El> {
    let c = &k.c;
    if s.busiest.is_empty() {
        return None;
    }
    let figures = k.ty.small.features("'tnum' 1");
    let mut list = col().key("mb-busiest").w_full().gap(0.0).child(
        text("Busiest now")
            .font(k.ty.small_medium)
            .color(c.text_muted)
            .px(8.0)
            .pb(2.0),
    );
    for (i, p) in s.busiest.iter().enumerate() {
        let line = pressable(
            &format!("mb-proc-{i}"),
            format!("Open {}", p.name),
            Go::Process(p.pid),
            k,
        )
        .py(4.0)
        .child(
            text(p.name.clone())
                .key(format!("mb-proc-{i}-name"))
                .font(k.ty.small)
                .color(c.text_base)
                .truncate()
                .grow(1.0)
                .shrink()
                .min_w(0.0),
        )
        .child(
            text(fmt_cpu(p.cpu))
                .key(format!("mb-proc-{i}-cpu"))
                .font(figures)
                .color(c.text_base)
                .w(52.0)
                .align(rune::Align::End),
        )
        .child(
            text(fmt_mem(p.rss_kib))
                .font(figures)
                .color(c.text_muted)
                .w(64.0)
                .align(rune::Align::End),
        );
        list = list.child(line);
    }
    Some(list)
}

fn thermal_ink(t: ThermalState, k: &Kit) -> Color {
    match t {
        ThermalState::Nominal => ink(Severity::Healthy, k),
        ThermalState::Throttled => ink(Severity::Warning, k),
        ThermalState::Warm | ThermalState::Unknown => k.c.text_muted,
    }
}

fn machine_line(s: &Snapshot, k: &Kit) -> Option<El> {
    let c = &k.c;
    if s.disk.is_none() && s.thermal.is_none() {
        return None;
    }
    let mut line = pressable("mb-machine", "Show the Machine page".into(), Go::Machine, k);
    if let Some(d) = &s.disk {
        let words = format!("{} free on {}", fmt_size(d.avail), d.label);
        let color = if d.used_pct >= crate::system::VOLUME_WARN {
            ink(Severity::Warning, k)
        } else {
            c.text_base
        };
        line = line.child(
            text(words)
                .key("mb-disk")
                .font(k.ty.small)
                .color(color)
                .truncate()
                .shrink()
                .min_w(0.0),
        );
    }
    line = line.child(el().grow(1.0));
    if let Some(t) = s.thermal {
        line = line
            .child(el().size(6.0, 6.0).radius(3.0).fill(thermal_ink(t, k)))
            .child(
                text(t.label())
                    .key("mb-thermal")
                    .font(k.ty.small)
                    .color(c.text_muted),
            );
    }
    Some(line)
}

fn head(s: &Snapshot, k: &Kit) -> El {
    let c = &k.c;
    let severity = match (&s.top, s.healthy) {
        (Some(t), _) => t.severity,
        (None, true) => Severity::Healthy,
        (None, false) => Severity::Info,
    };
    let mut r = pressable("mb-head", "Show the Overview".into(), Go::Overview, k).py(4.0);
    if s.loaded {
        r = r.child(dot(severity, k));
    }
    r = r.child(
        text(headline(s))
            .key("mb-headline")
            .font(k.ty.ui_medium)
            .color(c.text_base)
            .truncate()
            .shrink()
            .min_w(0.0),
    );
    if let Some(status) = &s.status {
        r = r.child(el().grow(1.0)).child(
            text(status.clone())
                .key("mb-status")
                .role("status")
                .font(k.ty.small)
                .color(c.text_faint)
                .truncate()
                .shrink()
                .min_w(0.0)
                .help_when_truncated(status.clone()),
        );
    }
    r
}

pub fn view(m: &MenuBar, cx: &mut Cx<MenuBar>) -> El {
    let k = Kit::neutral(cx.system_dark());
    let c = &k.c;
    let s = &m.snap;
    let mut body = col()
        .key("mb")
        .w_full()
        .gap(10.0)
        .padding(10.0)
        .child(head(s, &k));
    if let Some(e) = charts(s, &k) {
        body = body.child(e);
    }
    if let Some(e) = top(s, &k) {
        body = body.child(e);
    }
    if let Some(e) = busiest(s, &k) {
        body = body.child(e);
    }
    if let Some(e) = machine_line(s, &k) {
        body = body.child(e);
    }
    let open = Button::new("Open Spectra Desktop")
        .variant(ButtonVariant::Ghost)
        .on_press_tx(|m: &mut MenuBar, tx: &mut rune::Tx| {
            m.pressed = None;
            open_main();
            tx.close_window();
        })
        .build(&k)
        .key("mb-open");
    body = body
        .child(el().w_full().h(1.0).fill(c.border_weak))
        .child(row().w_full().child(open));
    col()
        .key("mb-panel")
        .w_full()
        .radius(12.0)
        .clip_rounded(12.0)
        .fill(c.bg_base)
        .border(1.0, c.border_base)
        .child(body)
}

#[cfg(test)]
mod tests {
    use rune::{Env, Runtime};

    use super::*;
    use crate::menubar::{WIDTH, snapshot};
    use crate::sample_menubar::{alert_app, healthy_app};

    fn panel(snap: Snapshot, dark: bool) -> Runtime<MenuBar> {
        let state = MenuBar {
            snap,
            pressed: None,
        };
        let env = Env {
            system_dark: dark,
            scale: 2.0,
        };
        let mut rt = Runtime::new(state, view, (WIDTH, 480.0), env);
        rt.frame(0.0);
        for _ in 0..10 {
            rt.frame(1.0 / 60.0);
        }
        rt
    }

    fn text_of(rt: &Runtime<MenuBar>, key: &str) -> Option<String> {
        rt.elements()
            .into_iter()
            .find(|e| e.key.as_deref() == Some(key))
            .and_then(|e| e.text)
    }

    fn press_key(rt: &mut Runtime<MenuBar>, key: &str) {
        let (x, y) = rt
            .find(key)
            .unwrap_or_else(|| panic!("nothing keyed {key}"));
        rt.pointer_down(x, y);
        rt.pointer_up(x, y);
        for _ in 0..5 {
            rt.frame(1.0 / 60.0);
        }
    }

    #[test]
    fn an_alert_shows_every_part() {
        let s = alert_app();
        let snap = snapshot(&s);
        let rt = panel(snap.clone(), true);
        for key in [
            "mb-head",
            "mb-cpu",
            "mb-mem",
            "mb-top",
            "mb-proc-0",
            "mb-proc-1",
            "mb-proc-2",
            "mb-disk",
            "mb-thermal",
            "mb-open",
        ] {
            assert!(rt.find(key).is_some(), "the panel shows {key}");
        }
        assert!(rt.find("mb-proc-3").is_none(), "three processes, no more");
        assert_eq!(
            text_of(&rt, "mb-headline"),
            Some(format!("{} to look at", snap.needs_look))
        );
        assert_eq!(
            text_of(&rt, "mb-top-title"),
            snap.top.as_ref().map(|t| t.title.clone())
        );
        assert_eq!(
            text_of(&rt, "mb-proc-0-name"),
            Some(snap.busiest[0].name.clone())
        );
        assert_eq!(text_of(&rt, "mb-thermal").as_deref(), Some("Throttled"));
    }

    #[test]
    fn a_healthy_mac_says_so_with_no_top_line() {
        let rt = panel(snapshot(&healthy_app()), false);
        assert_eq!(
            text_of(&rt, "mb-headline").as_deref(),
            Some("Nothing needs attention")
        );
        assert!(rt.find("mb-top").is_none());
        assert!(rt.find("mb-cpu").is_some() && rt.find("mb-proc-0").is_some());
    }

    #[test]
    fn before_a_reading_it_says_it_is_reading() {
        let rt = panel(Snapshot::default(), true);
        assert_eq!(
            text_of(&rt, "mb-headline").as_deref(),
            Some("Reading this Mac…")
        );
        for key in ["mb-cpu", "mb-mem", "mb-top", "mb-proc-0", "mb-machine"] {
            assert!(rt.find(key).is_none(), "nothing read for {key}");
        }
        assert!(rt.find("mb-open").is_some());
    }

    #[test]
    fn a_row_press_goes_to_its_page() {
        let snap = snapshot(&alert_app());
        let pid = snap.busiest[1].pid;
        let top = snap.top.clone().unwrap().go;
        let mut rt = panel(snap, true);
        press_key(&mut rt, "mb-proc-1");
        assert_eq!(rt.state.pressed, Some(Go::Process(pid)));
        press_key(&mut rt, "mb-top");
        assert_eq!(rt.state.pressed, Some(top));
        press_key(&mut rt, "mb-mem");
        assert_eq!(rt.state.pressed, Some(Go::Machine));
        press_key(&mut rt, "mb-head");
        assert_eq!(rt.state.pressed, Some(Go::Overview));
        press_key(&mut rt, "mb-open");
        assert_eq!(rt.state.pressed, None);
    }
}
