//! Headless runs of the Network page: the sidebar row, the state line, the ranking and its
//! links, the listening ports and their flag, the Machine page's link, and an older Spectra.

use rune::{Env, Runtime};

use crate::app::{App, init};
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::sample_network::{network_measuring_scene, network_scene, network_unsupported_scene};
use crate::sample_system::system_scene;

fn scene(f: fn(&mut App)) -> Runtime<App> {
    let mut s = init();
    f(&mut s);
    // Nothing in these runs may start a real Spectra.
    s.network.auto = false;
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(s, super::view, (980.0, 640.0), env);
    rt.on_key = Some(super::on_key);
    rt.frame(0.0);
    rt
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

fn has(rt: &mut Runtime<App>, key: &str) -> bool {
    let k = key.to_owned();
    rt.update(move |_, tx| tx.scroll_into_view(k));
    settle(rt);
    rt.find(key).is_some()
}

fn text_of(rt: &Runtime<App>, key: &str) -> Option<String> {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

#[test]
fn the_network_row_sits_under_activity_and_opens_the_page() {
    let mut rt = scene(sample_scene);
    let (act, net, all) = (
        rt.find("activity").unwrap(),
        rt.find("network").unwrap(),
        rt.find("all").unwrap(),
    );
    assert!(act.1 < net.1 && net.1 < all.1, "right under Activity");
    press(&mut rt, "network");
    assert_eq!(rt.state.nav, Nav::Network);
    assert!(rt.state.network_open());
    assert!(rt.find("net-page").is_some(), "the Network page shows");
    assert!(rt.find("procs").is_none(), "not a process list");
    assert_eq!(rt.state.shortcut_target(), None, "no list cursor to quit");
    assert!(has(&mut rt, "net-use-waiting"), "nothing measured yet");
}

#[test]
fn the_state_line_names_interface_vpn_proxy_and_dns() {
    let rt = scene(network_scene);
    assert_eq!(
        text_of(&rt, "net-state-iface-value").as_deref(),
        Some("en0 via 192.168.1.1")
    );
    assert_eq!(
        text_of(&rt, "net-state-vpn-value").as_deref(),
        Some("On (utun4)")
    );
    assert_eq!(
        text_of(&rt, "net-state-proxy-value").as_deref(),
        Some("HTTPS proxy.example.test:8443")
    );
    assert_eq!(
        text_of(&rt, "net-state-dns-value").as_deref(),
        Some("192.168.1.1, 1.1.1.1")
    );
}

#[test]
fn the_ranking_lists_the_busiest_first_with_bytes_rates_and_remotes() {
    let mut rt = scene(network_scene);
    let (node, java) = (
        locate(&mut rt, "net-use-1320"),
        locate(&mut rt, "net-use-4101"),
    );
    assert!(node.1 < java.1, "node moved the most");
    assert_eq!(
        text_of(&rt, "net-use-1320-in").as_deref(),
        Some("19 MB · 3.8 MB/s")
    );
    assert_eq!(
        text_of(&rt, "net-use-1320-out").as_deref(),
        Some("0 B · 0 B/s")
    );
    assert_eq!(
        text_of(&rt, "net-use-4101-remotes").as_deref(),
        Some("127.0.0.1:50833, 172.217.165.219:443, 104.16.72.101:443 +1")
    );
    assert_eq!(
        text_of(&rt, "net-use-1311-remotes").as_deref(),
        Some("None open at the end")
    );
    assert!(
        has(&mut rt, "net-use-1311-new"),
        "gh started during the window"
    );
    assert!(
        text_of(&rt, "net-use-total")
            .unwrap()
            .starts_with("19 processes moved"),
        "{:?}",
        text_of(&rt, "net-use-total")
    );
    assert!(
        text_of(&rt, "net-status")
            .unwrap()
            .starts_with("Measured over 5 s, 3 s ago."),
        "{:?}",
        text_of(&rt, "net-status")
    );
}

#[test]
fn a_row_opens_its_process_only_when_the_name_matches() {
    let mut rt = scene(network_scene);
    // The counters cut "Slack Helper (Renderer)" to 15 characters: still the same process.
    assert!(has(&mut rt, "net-use-901-who-open"));
    // pid 1201 is a claude in the feed, a curl to Spectra: a reused pid opens nothing.
    assert!(!has(&mut rt, "net-use-1201-who-open"));
    assert!(has(&mut rt, "net-use-1201-who"));
    press(&mut rt, "net-use-1200-who-open");
    assert_eq!(rt.state.selected, Some(1200), "claude's page");
    assert!(
        !rt.state.network_open(),
        "a page over it stops the measuring"
    );
    press(&mut rt, "back");
    assert_eq!(
        (rt.state.nav, rt.state.selected),
        (Nav::Network, None),
        "back to the Network page"
    );
}

#[test]
fn listening_puts_every_interface_first_and_flags_it() {
    let mut rt = scene(network_scene);
    assert_eq!(
        text_of(&rt, "net-listen-summary").as_deref(),
        Some(
            "8 ports listening; 5 on all interfaces, so other devices on your network may reach them."
        )
    );
    assert!(has(&mut rt, "net-port-8080-tcp-4101-exposed"));
    assert!(has(&mut rt, "net-port-22-tcp-0-exposed"));
    assert!(
        !has(&mut rt, "net-port-3000-tcp-1320-exposed"),
        "loopback only"
    );
    let at = |key: &str| {
        rt.elements()
            .iter()
            .position(|e| e.key.as_deref() == Some(key))
            .unwrap_or_else(|| panic!("no {key}"))
    };
    assert!(
        at("net-port-8080-tcp-4101") < at("net-port-3000-tcp-1320"),
        "every interface first"
    );
    // java is java; postgres on python3's pid is someone else; ControlCenter isn't in the feed.
    assert!(has(&mut rt, "net-port-8080-tcp-4101-who-open"));
    assert!(!has(&mut rt, "net-port-5432-tcp-1340-who-open"));
    assert!(!has(&mut rt, "net-port-5000-tcp-683-who-open"));
    // An escaped, cut "Slack " is Slack Helper (GPU).
    assert!(has(&mut rt, "net-port-49368-tcp-902-who-open"));
    assert_eq!(
        text_of(&rt, "net-port-22-tcp-0-who").as_deref(),
        Some("Unknown")
    );
    press(&mut rt, "net-port-11434-tcp-1370-who-open");
    assert_eq!(rt.state.selected, Some(1370), "ollama's page");
}

#[test]
fn the_machine_page_links_here_instead_of_listing_ports() {
    let mut rt = scene(system_scene);
    assert!(has(&mut rt, "sys-fact-listening"), "the count stays");
    assert!(
        !has(&mut rt, "net-ports"),
        "the list is on the Network page"
    );
    press(&mut rt, "sys-open-network");
    assert_eq!(rt.state.nav, Nav::Network);
    assert!(has(&mut rt, "net-ports"));
}

#[test]
fn the_first_window_says_it_is_measuring() {
    let mut rt = scene(network_measuring_scene);
    assert_eq!(
        text_of(&rt, "net-status").as_deref(),
        Some("Measuring for 5 s…")
    );
    assert!(has(&mut rt, "net-use-waiting") && has(&mut rt, "net-ports"));
    press(&mut rt, "system");
    rt.update(|s, _| {
        s.tick_network();
    });
    assert!(!rt.state.network.measuring(), "leaving stops the window");
}

#[test]
fn an_older_spectra_still_shows_what_is_listening() {
    let mut rt = scene(network_unsupported_scene);
    assert!(has(&mut rt, "net-unsupported"));
    assert!(!has(&mut rt, "net-use-waiting"), "no ranking to wait for");
    assert!(has(&mut rt, "net-ports") && has(&mut rt, "net-state"));
}
