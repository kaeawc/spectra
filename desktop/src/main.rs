//! Spectra Desktop: a diagnosis-first view of every process on this machine, with JVMs in depth.
//!
//! `cargo run` opens the window. `cargo run -- --snap out.png --scene sample [--theme light]`
//! renders it headless with sample data; `--list-scenes` names the other screens.

mod app;
mod bundle;
mod data;
mod findings;
mod groups;
mod inspect;
mod jvm_tools;
mod live;
mod proc_findings;
mod procs;
mod run;
mod sample;
mod sample_jvm;
mod sample_system;
mod signal;
mod system;
mod system_findings;
mod tools;
mod ui;

use std::sync::mpsc::RecvTimeoutError;
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
    if let Some(tx) = s.system.tx.take() {
        system::spawn(tx, wake.clone());
    }
    s.wake = Some(wake.clone());
    if s.me.uid.is_none() {
        s.me.uid = signal::my_uid();
    }
    if let (Some(tx), Some(poke)) = (s.proc_tx.take(), s.poke_rx.take()) {
        // One call at a time: the next starts only after the last one landed, or at once when
        // a Quit was just sent.
        std::thread::spawn(move || {
            loop {
                let feed = procs::fetch();
                let too_old = matches!(feed, Err(procs::ProcError::TooOld(_)));
                if tx.send(feed).is_err() {
                    return;
                }
                wake();
                match poke.recv_timeout(if too_old { RETRY_OLD } else { REFRESH }) {
                    Err(RecvTimeoutError::Disconnected) => return,
                    _ => while poke.try_recv().is_ok() {},
                }
            }
        });
    }
}

/// Quitting stops every Spectra job the pages started (a memtrace can run 20 s), and waits a
/// moment for them to be killed, and for any queued `jfr stop`, since the process ends right
/// after.
fn on_quit(s: &mut App, _: &mut Tx) {
    // A flight recording the open page started ends with the app, like leaving the page.
    s.stop_recording_now();
    s.inspect.cancel_all();
    system::CANCEL.store(true, std::sync::atomic::Ordering::SeqCst);
    run::wait_idle(Duration::from_secs(2));
}

fn poll(s: &mut App, clock: f64, tx: &mut Tx) -> bool {
    let status = (s.feed_status(), s.system.statuses(s.now));
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
    // A Quit's check and send, and the look at a process that didn't end, run on threads.
    landed |= s.land_work();
    // An open page's tools, live readings and bundle, and its next live round once it's due.
    landed |= s.land_inspect();
    // Memory, thermal, load, power, disks and network, for the System page.
    landed |= s.land_system();
    s.tick_inspector();
    // A hung Spectra sends nothing, so look again on a timer to say the numbers are getting old;
    // an open page also looks for its next live round.
    if s.has_processes() || s.on_page() || s.nav == groups::Nav::System {
        tx.wake_after(STATUS_TICK);
    }
    let now_status = (s.feed_status(), s.system.statuses(s.now));
    // A reading growing old takes its finding away, with no new reading to say so.
    if now_status.1 != status.1 && !landed {
        s.refresh_findings();
    }
    landed || now_status != status
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
            ("quit", sample::quit_scene),
            ("quit-list", sample::quit_list_scene),
            ("quit-confirm", sample::quit_confirm_scene),
            ("force-confirm", sample::force_confirm_scene),
            ("quit-root", sample::quit_root_scene),
            ("quit-still", sample::quit_still_scene),
            ("quit-ended", sample::quit_ended_scene),
            ("quit-checking", sample::quit_checking_scene),
            ("quit-maybe", sample::quit_maybe_scene),
            ("quit-zombie", sample::quit_zombie_scene),
            ("app", sample::app_scene),
            ("app-loading", sample::app_loading_scene),
            ("tools", sample::tools_scene),
            ("tools-root", sample::tools_root_scene),
            ("tool-sample", sample::tool_sample_scene),
            ("tool-hang", sample::tool_hang_scene),
            ("tool-vmmap", sample::tool_vmmap_scene),
            ("tool-vmregions", sample::tool_vmregions_scene),
            ("tool-memtrace", sample::tool_memtrace_scene),
            ("tool-lineage", sample::tool_lineage_scene),
            ("tool-owner", sample::tool_owner_scene),
            ("tool-runtime", sample::tool_runtime_scene),
            ("tool-failed", sample::tool_failed_scene),
            ("jvm-tools", sample_jvm::jvm_tools_scene),
            ("tool-threads", sample_jvm::tool_threads_scene),
            ("tool-threads-deadlock", sample_jvm::tool_threads_deadlock_scene),
            ("tool-histogram", sample_jvm::tool_histogram_scene),
            ("tool-gc", sample_jvm::tool_gc_scene),
            ("tool-nmt", sample_jvm::tool_nmt_scene),
            ("tool-nmt-off", sample_jvm::tool_nmt_off_scene),
            ("tool-explain", sample_jvm::tool_explain_scene),
            ("tool-heapdump", sample_jvm::tool_heapdump_scene),
            ("tool-jfr", sample_jvm::tool_jfr_scene),
            ("tool-jfr-summary", sample_jvm::tool_jfr_summary_scene),
            ("system", sample_system::system_scene),
            ("system-pressure", sample_system::system_pressure_scene),
            ("system-failed", sample_system::system_failed_scene),
            ("system-linux", sample_system::system_linux_scene),
        ],
        setup: Some(setup),
        poll: Some(poll),
        on_key: Some(ui::on_key),
        ..AppSpec::new("Spectra", (1100.0, 700.0), app::init, ui::view)
    };
    rune::App::new(spec).chrome(chrome).on_quit(on_quit).run();
}
