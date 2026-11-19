//! Spectra Desktop: a diagnosis-first view of the JVMs on this machine.
//!
//! `cargo run` opens the window. `cargo run -- --snap out.png --scene sample [--theme light]`
//! renders it headless with sample data; `--scene detail` opens the leaking JVM's page.

mod app;
mod data;
mod findings;
mod ui;

use std::time::Duration;

use app::App;
use rune::{AppSpec, Chrome, KeyEvent, Tx};
use rune_kit::shell;

const REFRESH: Duration = Duration::from_secs(2);

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

fn on_key(s: &mut App, ev: &KeyEvent, _: &mut Tx) -> bool {
    if shell::is_sidebar_toggle(ev) {
        s.sidebar.toggle();
        return true;
    }
    false
}

fn main() {
    let chrome = Chrome {
        full_size_content: true,
        traffic_lights: Some((16.0, 17.0)),
        drag_regions: true,
    };
    let spec = AppSpec {
        min_size: Some((640.0, 420.0)),
        scenes: &[("sample", app::sample_scene), ("detail", app::detail_scene)],
        setup: Some(setup),
        poll: Some(poll),
        on_key: Some(on_key),
        ..AppSpec::new("Spectra", (980.0, 640.0), app::init, ui::view)
    };
    rune::App::new(spec).chrome(chrome).run();
}
