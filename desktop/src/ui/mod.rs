//! The view: a sidebar of JVMs beside either every finding ("All JVMs") or one JVM's page.

mod card;
mod chart;
mod detail;
mod home;
#[cfg(test)]
mod tests;

use rune::{Cx, El, ScrollSpec, col};
use rune_kit::shell::{Sidebar, SidebarRow, SplitView, Toolbar};
use rune_kit::{Button, ButtonVariant, Kit};

use crate::app::App;
use crate::findings::Severity;
use card::dot;

fn sidebar(s: &App, k: &Kit) -> El {
    let overview = SidebarRow::new("All JVMs")
        .detail(s.jvms.len().to_string())
        .height(32.0)
        .selected(s.selected.is_none())
        .on_select(|s: &mut App, _| s.select(None))
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
                .on_select(move |s: &mut App, _| s.select(Some(pid)))
                .build(k)
                .key(format!("jvm-{pid}"));
            sb.child(r)
        })
        .build(k)
}

pub fn view(s: &App, cx: &mut Cx<App>) -> El {
    let k = Kit::neutral(cx.system_dark());
    let findings = s.findings();
    let page = match s.selected_jvm() {
        Some(j) => detail::detail(s, j, &k),
        None => home::home(s, &findings, &k),
    };
    let main = col()
        .w_full()
        .h_full()
        .min_h(0.0)
        .scroll(ScrollSpec {
            stick_bottom: false,
        })
        .child(page);

    let problems = findings
        .iter()
        .filter(|f| f.severity < Severity::Info)
        .count();
    let title = match s.selected_jvm() {
        Some(j) => j.name(),
        None if problems == 0 => "Nothing needs attention".to_owned(),
        None => format!("{problems} to look at"),
    };
    let pin = Button::new(if s.baseline.is_empty() {
        "Pin baseline"
    } else {
        "Clear baseline"
    })
    .variant(ButtonVariant::Ghost)
    .on_press(|s: &mut App| s.toggle_baseline())
    .build(&k)
    .key("pin-baseline");
    SplitView::new(main)
        .sidebar(&s.sidebar, |s: &mut App| &mut s.sidebar, sidebar(s, &k))
        .toolbar(Toolbar::new().title(title).trailing(pin))
        .titlebar_inset(cx.titlebar_inset())
        .reduced_motion(cx.is_reduced_motion())
        .build(&k)
}
