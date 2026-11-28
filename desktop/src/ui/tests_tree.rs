//! Headless runs of the process list as a tree: the toggle, opening branches with the triangle
//! and the arrow keys, Return and a double click, a search, and readings where pids come and go.

use rune::{Env, Key, KeyEvent, Runtime, TextEdit};
use rune_kit::SortDir;

use crate::app::{App, init};
use crate::groups::Nav;
use crate::groups::tests::proc;
use crate::procs::Process;
use crate::sample::sample_scene;
use crate::sample_tree::tree_search_scene;
use crate::tree::View;

fn runtime(state: App) -> Runtime<App> {
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(state, crate::ui::view, (980.0, 640.0), env);
    rt.on_key = Some(crate::ui::on_key);
    rt.frame(0.0);
    rt
}

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    runtime(s)
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

fn locate(rt: &mut Runtime<App>, key: &str) -> (f32, f32) {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    rt.find(key)
        .unwrap_or_else(|| panic!("nothing keyed {key} on screen"))
}

fn press(rt: &mut Runtime<App>, key: &str) {
    let (x, y) = locate(rt, key);
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

fn double_press(rt: &mut Runtime<App>, key: &str) {
    let (x, y) = locate(rt, key);
    for _ in 0..2 {
        rt.pointer_down(x, y);
        rt.pointer_up(x, y);
    }
    settle(rt);
}

fn key(rt: &mut Runtime<App>, k: Key) {
    rt.key(KeyEvent::from(k));
    settle(rt);
}

fn ids(rt: &Runtime<App>) -> Vec<&str> {
    rt.state.rows.iter().map(|r| r.id.as_str()).collect()
}

/// Every process, as a tree.
fn tree() -> Runtime<App> {
    let mut rt = scene(sample_scene);
    press(&mut rt, "all");
    press(&mut rt, "view-mode/segment1");
    rt
}

#[test]
fn the_toggle_shows_the_tree_and_is_kept_from_place_to_place() {
    let mut rt = tree();
    assert_eq!(rt.state.view, View::Tree);
    assert!(
        rt.find("p:1000").is_some(),
        "iTerm2, a child of launchd, is a root"
    );
    assert!(
        rt.find("p:1101").is_none(),
        "its shells are under it, closed"
    );
    assert!(rt.state.rows.iter().all(|r| r.depth == 0));
    assert_eq!(rt.state.tree_matches, rt.state.procs.len());

    press(&mut rt, "cat-apps");
    assert_eq!(
        rt.state.view,
        View::Tree,
        "the choice stays while the app runs"
    );
    press(&mut rt, "all");
    press(&mut rt, "view-mode/segment0");
    assert_eq!(rt.state.view, View::Grouped);
    assert!(
        rt.find("g:app::Applications:Slack.app").is_some(),
        "grouped again"
    );
}

#[test]
fn the_triangle_and_left_and_right_open_and_close_a_branch() {
    let mut rt = tree();
    press(&mut rt, "twisty-p1000");
    assert!(rt.state.tree_open.contains(&1000));
    assert!(
        rt.find("p:1101").is_some(),
        "its shells are listed under it"
    );
    assert_eq!(rt.state.selected, None, "opening a branch opens no page");

    press(&mut rt, "p:1101");
    assert_eq!(rt.state.table.cursor(), Some("p:1101"));
    key(&mut rt, Key::Right);
    assert!(
        rt.state.tree_open.contains(&1101),
        "Right opens a closed branch"
    );
    assert!(
        rt.find("p:1200").is_some(),
        "claude, under the shell that started it"
    );
    key(&mut rt, Key::Right);
    let first = rt.state.table.cursor().unwrap().to_owned();
    let child: u32 = first.trim_start_matches("p:").parse().unwrap();
    assert_eq!(
        rt.state.tree.parent_of(child),
        Some(1101),
        "a second Right steps to its first child"
    );
    key(&mut rt, Key::Left);
    assert_eq!(
        rt.state.table.cursor(),
        Some("p:1101"),
        "Left on a leaf steps to the parent"
    );
    key(&mut rt, Key::Left);
    assert!(
        !rt.state.tree_open.contains(&1101),
        "Left closes an open branch"
    );
    assert!(rt.find("p:1200").is_none());
    key(&mut rt, Key::Left);
    assert_eq!(rt.state.table.cursor(), Some("p:1000"), "and then steps up");
    key(&mut rt, Key::Down);
    assert_eq!(
        rt.state.table.cursor(),
        Some(ids(&rt)[ids(&rt).iter().position(|i| *i == "p:1000").unwrap() + 1])
    );
    assert_eq!(rt.state.selected, None, "arrowing opens nothing");
}

#[test]
fn return_and_a_double_click_open_the_process_page() {
    let mut rt = tree();
    press(&mut rt, "p:1000");
    key(&mut rt, Key::Enter);
    assert_eq!(rt.state.selected, Some(1000), "Return opens its page");
    key(&mut rt, Key::Escape);
    assert_eq!(
        (rt.state.selected, rt.state.nav, rt.state.view),
        (None, Nav::All, View::Tree)
    );
    double_press(&mut rt, "p:910");
    assert_eq!(
        rt.state.selected,
        Some(910),
        "a double click opens Safari's own process, not its app page"
    );
}

#[test]
fn a_search_shows_matches_under_the_processes_that_started_them() {
    let mut rt = scene(tree_search_scene);
    let rows = ids(&rt);
    // Every zsh, each under its terminal; nothing else.
    for want in ["p:1000", "p:1010", "p:1101", "p:1104"] {
        assert!(rows.contains(&want), "{want} is listed");
    }
    assert!(
        !rows.contains(&"p:1200"),
        "claude doesn't match and sits below a match"
    );
    assert!(!rows.contains(&"p:900"), "Slack doesn't match");
    assert_eq!(rt.state.tree_matches, 5);
    assert!(
        rt.state.tree_open.is_empty(),
        "a search opens branches without remembering them"
    );
    assert!(
        rt.find("twisty-p1101").is_none(),
        "a match with nothing kept under it has no triangle"
    );
    press(&mut rt, "twisty-p1000");
    assert!(
        !ids(&rt).contains(&"p:1101"),
        "a branch closes during a search"
    );
    rt.state.query = TextEdit::new("");
    rt.state.rebuild();
    rt.state.query = TextEdit::new("zsh");
    rt.state.rebuild();
    assert!(ids(&rt).contains(&"p:1101"), "a new search opens it again");
}

fn under(pid: u32, ppid: u32, name: &str) -> Process {
    let mut p = proc(pid, name, 1.0, 1_000.0);
    p.ppid = ppid;
    p
}

#[test]
fn open_branches_survive_readings_where_pids_come_and_go() {
    let mut s = init();
    s.show(Nav::All);
    s.set_view(View::Tree);
    s.sort_by("sub_cpu", SortDir::Descending);
    s.land_procs(Ok(vec![
        under(10, 1, "shell"),
        under(11, 10, "make"),
        under(12, 11, "cc"),
        under(20, 1, "daemon"),
    ]));
    s.toggle_tree(10);
    s.toggle_tree(11);
    let rows = |s: &App| s.rows.iter().map(|r| r.id.clone()).collect::<Vec<_>>();
    assert_eq!(rows(&s), ["p:10", "p:11", "p:12", "p:20"]);
    // cc ends, a linker starts under make, a new daemon appears.
    s.land_procs(Ok(vec![
        under(10, 1, "shell"),
        under(11, 10, "make"),
        under(13, 11, "ld"),
        under(20, 1, "daemon"),
        under(30, 1, "new"),
    ]));
    assert!(
        s.tree_open.contains(&10) && s.tree_open.contains(&11),
        "still open"
    );
    assert!(
        rows(&s).contains(&"p:13".to_owned()),
        "the new child shows under the open branch"
    );
    // make ends: what it held is forgotten, and comes back closed under the same pid.
    s.land_procs(Ok(vec![under(10, 1, "shell"), under(20, 1, "daemon")]));
    assert!(!s.tree_open.contains(&11));
    s.land_procs(Ok(vec![
        under(10, 1, "shell"),
        under(11, 10, "make"),
        under(14, 11, "cc"),
        under(20, 1, "daemon"),
    ]));
    assert_eq!(
        rows(&s),
        ["p:10", "p:11", "p:20"],
        "a new process under an old pid starts closed"
    );
}
