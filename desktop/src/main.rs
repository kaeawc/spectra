//! Spectra Desktop: a diagnosis-first view of every process on this machine, with JVMs in depth.
//!
//! `cargo run` opens the window. `cargo run -- --snap out.png --scene sample [--theme light]`
//! renders it headless with sample data; `--list-scenes` names the other screens.

mod app;
mod data;
mod findings;
mod groups;
mod proc_findings;
mod procs;
mod run;
mod sample;
mod ui;

use std::time::Duration;

use app::{App, REFRESH};
use rune::{AppSpec, Chrome, Tx};

/// How long to wait before asking a Spectra that's too old again, in case it was updated.
const RETRY_OLD: Duration = Duration::from_secs(30);
/// How often the "Updated … ago" status is looked at while nothing lands.
const STATUS_TICK: Duration = Duration::from_secs(1);

fn setup(s: &mut App, wake: rune::Waker) {
    // A headless render of a sample scene shows the sample, not this machine.
    if rune::tooling::headless_run().is_some() && s.proc_loaded {
        return;
    }
    if let Some(tx) = s.feed_tx.take() {
        let wake = wake.clone();
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
    if let Some(tx) = s.proc_tx.take() {
        // One call at a time: the next starts only after the last one landed.
        std::thread::spawn(move || {
            loop {
                let feed = procs::fetch();
                let too_old = matches!(feed, Err(procs::ProcError::TooOld(_)));
                if tx.send(feed).is_err() {
                    return;
                }
                wake();
                std::thread::sleep(if too_old { RETRY_OLD } else { REFRESH });
            }
        });
    }
}

fn poll(s: &mut App, clock: f64, tx: &mut Tx) -> bool {
    let status = s.feed_status();
    s.now = clock;
    let mut landed = false;
    while let Some(feed) = s.feed.as_ref().and_then(|rx| rx.try_recv().ok()) {
        s.land(feed);
        landed = true;
    }
    while let Some(feed) = s.proc_feed.as_ref().and_then(|rx| rx.try_recv().ok()) {
        s.land_procs(feed);
        landed = true;
    }
    // A hung Spectra sends nothing, so look again on a timer to say the numbers are getting old.
    if s.has_processes() {
        tx.wake_after(STATUS_TICK);
    }
    landed || s.feed_status() != status
}

fn main() {
    let chrome = Chrome {
        full_size_content: true,
        traffic_lights: Some((16.0, 17.0)),
        drag_regions: true,
    };
    let spec = AppSpec {
        min_size: Some((640.0, 420.0)),
        scenes: &[
            ("sample", sample::sample_scene),
            ("all", sample::all_scene),
            ("apps", sample::apps_scene),
            ("group", sample::group_scene),
            ("process", sample::process_scene),
            ("signed", sample::signed_scene),
            ("detail", sample::detail_scene),
            ("stale", sample::stale_scene),
            ("old", sample::old_scene),
        ],
        setup: Some(setup),
        poll: Some(poll),
        on_key: Some(ui::on_key),
        ..AppSpec::new("Spectra", (1100.0, 700.0), app::init, ui::view)
    };
    rune::App::new(spec).chrome(chrome).run();
}
