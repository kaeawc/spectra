//! Sample network readings for the Network page's headless scenes and tests: a window in which a
//! build pulls dependencies while a few apps talk, what is listening, an empty first window
//! still measuring, and an older Spectra without `spectra network top`.

use crate::app::App;
use crate::groups::Nav;
use crate::network::{Fail, parse_top};
use crate::sample::sample_scene;
use crate::system::parse_network;

/// Trimmed from `spectra network top --json`, pids moved onto the sample's processes.
pub const TOP: &str = include_str!("../tests/fixtures/network/top.json");
/// Trimmed from `spectra network --json`, with a proxy and a VPN set.
pub const NETWORK: &str = include_str!("../tests/fixtures/network/network.json");

/// The sample overview with the network read and nothing measuring on its own.
fn base(s: &mut App) {
    sample_scene(s);
    s.network.auto = false;
    let now = s.now;
    s.system.network.land(
        Ok(parse_network(NETWORK).expect("sample network parses")),
        now,
    );
}

/// The Network page after a window: a build and a JVM pulling bytes, an agent and Slack
/// talking, and a reused pid that opens nothing.
pub fn network_scene(s: &mut App) {
    base(s);
    s.show(Nav::Network);
    s.network.top = Some(parse_top(TOP).expect("sample window parses"));
    s.network.updated = Some(s.now - 3.0);
}

/// The Network page just opened: the first window is still measuring.
pub fn network_measuring_scene(s: &mut App) {
    base(s);
    s.show(Nav::Network);
    s.network.sample_flight();
}

/// An older Spectra without `spectra network top`: what is listening still shows.
pub fn network_unsupported_scene(s: &mut App) {
    base(s);
    s.show(Nav::Network);
    s.network.error = Some(Fail::Unsupported(
        "This Spectra has no `spectra network top`. Update Spectra to see which processes use the network."
            .into(),
    ));
}
