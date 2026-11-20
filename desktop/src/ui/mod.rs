//! The view: a sidebar of places (the overview, every process, categories and runtimes) beside
//! the place's page, or one process's page over it.

mod card;
mod chart;
mod detail;
mod home;
mod list;
mod process;
#[cfg(test)]
mod tests;

use rune::{Cx, El, Key, KeyEvent, ScrollSpec, Tx, col};
use rune_kit::shell::{self, Sidebar, SidebarRow, SidebarSection, SplitView, Toolbar};
use rune_kit::{Button, ButtonVariant, Kit};

use crate::app::App;
use crate::groups::{Category, Nav};
use crate::procs::{Runtime, fmt_count};
use card::dot;

/// Keys nothing focused took: the sidebar toggle, and Esc or Cmd-[ to go back from a page.
pub fn on_key(s: &mut App, ev: &KeyEvent, _: &mut Tx) -> bool {
    if shell::is_sidebar_toggle(ev) {
        s.sidebar.toggle();
        return true;
    }
    if s.selected.is_some() && (ev.key == Key::Escape || ev.is_command('[')) {
        s.back();
        return true;
    }
    false
}

fn place(s: &App, nav: Nav, key: String, count: Option<usize>, k: &Kit) -> El {
    let mut r = SidebarRow::new(nav.title())
        .height(30.0)
        .selected(s.nav == nav && s.selected.is_none())
        .on_select(move |s: &mut App, _| s.show(nav));
    if let Some(n) = count {
        r = r.detail(fmt_count(n as u64));
    }
    r.build(k).key(key)
}

fn sidebar(s: &App, k: &Kit) -> El {
    let problems = s.problems();
    let overview = place(s, Nav::Overview, "overview".into(), Some(problems).filter(|n| *n > 0), k);
    let mut sb = Sidebar::new().header(overview);
    if s.has_processes() {
        sb = sb.child(place(s, Nav::All, "all".into(), Some(s.procs.len()), k));
        let kinds = Category::ALL.iter().zip(s.stats.kinds).map(|(&c, n)| {
            place(s, Nav::Category(c), format!("cat-{}", c.id()), Some(n), k)
        });
        sb = sb.section(SidebarSection::new("Kinds").key("kinds").rows(kinds));
        let runtimes: Vec<El> = Runtime::ALL
            .iter()
            .zip(s.stats.runtimes)
            .filter(|(_, n)| *n > 0)
            .map(|(&r, n)| place(s, Nav::Runtime(r), format!("rt-{}", r.id()), Some(n), k))
            .collect();
        sb = sb.section(SidebarSection::new("Runtimes").key("runtimes").rows(runtimes));
    } else if !s.jvms.is_empty() {
        // Without the process feed, the JVMs are listed as before.
        let jvms = s.jvms.iter().map(|j| {
            let pid = j.pid;
            SidebarRow::new(j.name())
                .height(44.0)
                .avatar(dot(s.worst(pid), k))
                .subtitle(format!("pid {pid} · JDK {}", j.jdk_version))
                .selected(s.selected == Some(pid))
                .on_select(move |s: &mut App, _| s.select(Some(pid)))
                .build(k)
                .key(format!("jvm-{pid}"))
        });
        sb = sb.section(SidebarSection::new("JVMs").key("jvms").rows(jvms));
    }
    sb.build(k)
}

/// What the main pane shows and the toolbar's title for it.
enum Page<'a> {
    Process(&'a crate::procs::Process),
    Jvm(&'a crate::data::Jvm),
    Home,
    List,
}

fn page_of(s: &App) -> Page<'_> {
    if let Some(pid) = s.selected {
        if let Some(p) = s.process(pid) {
            return Page::Process(p);
        }
        if let Some(j) = s.jvm(pid) {
            return Page::Jvm(j);
        }
    }
    if s.nav == Nav::Overview {
        Page::Home
    } else {
        Page::List
    }
}

/// What a page is called: a JVM by what it runs, anything else by its name.
fn page_name(s: &App, pid: u32) -> String {
    match (s.jvm(pid), s.process(pid)) {
        (Some(j), _) => j.name(),
        (None, Some(p)) => p.name().into_owned(),
        (None, None) => format!("pid {pid}"),
    }
}

pub fn view(s: &App, cx: &mut Cx<App>) -> El {
    let k = Kit::neutral(cx.system_dark());
    let page = page_of(s);
    let (body, title) = match page {
        Page::Process(p) => (process::page(s, p, &k), page_name(s, p.pid)),
        Page::Jvm(j) => (detail::detail(s, j, &k), j.name()),
        Page::List => (list::list(s, &k), s.nav.title().to_owned()),
        Page::Home => {
            let problems = s.problems();
            let title = if problems == 0 {
                "Nothing needs attention".to_owned()
            } else {
                format!("{problems} to look at")
            };
            (home::home(s, &k), title)
        }
    };
    // The list scrolls inside its table; every other page scrolls as a whole.
    let main = match page {
        Page::List => col().w_full().h_full().min_h(0.0).child(body),
        _ => col()
            .w_full()
            .h_full()
            .min_h(0.0)
            .scroll(ScrollSpec {
                stick_bottom: false,
            })
            .child(body),
    };
    let mut toolbar = Toolbar::new().title(title);
    if s.selected.is_some() {
        let to = s.back_to().map_or_else(|| s.nav.title().to_owned(), |pid| page_name(s, pid));
        let back = Button::new(format!("‹ {to}"))
            .variant(ButtonVariant::Ghost)
            .on_press(|s: &mut App| s.back())
            .build(&k)
            .key("back");
        toolbar = toolbar.leading(back);
    }
    if !s.jvms.is_empty() && matches!(page, Page::Home | Page::Jvm(_)) || page_is_jvm(s, &page) {
        let pin = Button::new(if s.baseline.is_empty() {
            "Pin baseline"
        } else {
            "Clear baseline"
        })
        .variant(ButtonVariant::Ghost)
        .on_press(|s: &mut App| s.toggle_baseline())
        .build(&k)
        .key("pin-baseline");
        toolbar = toolbar.trailing(pin);
    }
    SplitView::new(main)
        .sidebar(&s.sidebar, |s: &mut App| &mut s.sidebar, sidebar(s, &k))
        .toolbar(toolbar)
        .titlebar_inset(cx.titlebar_inset())
        .reduced_motion(cx.is_reduced_motion())
        .build(&k)
}

/// A process page for a process that is a JVM: its JVM numbers can be pinned too.
fn page_is_jvm(s: &App, page: &Page<'_>) -> bool {
    matches!(page, Page::Process(p) if s.jvm(p.pid).is_some())
}
