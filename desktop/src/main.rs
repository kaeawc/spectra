//! Spectra Desktop: a diagnosis-first view of every process on this machine, with JVMs in depth.
//!
//! `cargo run` opens the window. `cargo run -- --snap out.png --scene sample [--theme light]`
//! renders it headless with sample data; `--list-scenes` names the other screens.

mod app;
mod bundle;
mod compare;
mod daemon;
mod data;
mod findings;
mod groups;
mod growth;
mod inspect;
mod jvm_tools;
mod live;
mod logs;
mod network;
mod proc_findings;
mod procs;
mod run;
mod sample;
mod sample_activity;
mod sample_compare;
mod sample_growth;
mod sample_jvm;
mod sample_logs;
mod sample_network;
mod sample_startup;
mod sample_storage;
mod sample_system;
mod sample_tree;
mod sample_whatswrong;
mod signal;
mod spawns;
mod startup;
mod storage;
mod system;
mod system_findings;
mod tools;
mod tree;
mod ui;
mod whatswrong;

use std::time::Duration;

use app::App;
use rune::{AppSpec, Chrome, Tx};

/// How often the "Updated … ago" status is looked at while nothing lands.
const STATUS_TICK: Duration = Duration::from_secs(1);

fn setup(s: &mut App, wake: rune::Waker) {
    // A headless render of a sample scene shows the sample, not this machine.
    if rune::tooling::headless_run().is_some() && s.proc_loaded {
        return;
    }
    // Calls Spectra's daemon already answers go to it instead of a new process.
    daemon::install();
    // Each feed reads once per ask; the window asks after the last reading rested, and not
    // while it's hidden.
    if let (Some(tx), Some(go)) = (s.feed_tx.take(), s.jvm_go_rx.take()) {
        let wake = wake.clone();
        std::thread::spawn(move || {
            while go.recv().is_ok() {
                if tx.send(data::fetch()).is_err() {
                    return;
                }
                wake();
            }
        });
    }
    if let Some(tx) = s.system.tx.take() {
        system::spawn(tx, wake.clone());
    }
    s.wake = Some(wake.clone());
    // "Right now" on the Overview looks while the Overview shows.
    s.whatswrong.auto = true;
    s.growth.auto = true;
    if s.me.uid.is_none() {
        s.me.uid = signal::my_uid();
    }
    if let (Some(tx), Some(poke)) = (s.proc_tx.take(), s.poke_rx.take()) {
        // One call at a time: the window asks for the next only after the last one landed, every
        // couple of seconds while it's shown. The daemon answers from its shared snapshot, or with
        // one taken now after a Quit or Kill.
        std::thread::spawn(move || {
            while let Ok(fresh) = poke.recv() {
                let feed = if fresh {
                    daemon::fresh(procs::fetch)
                } else {
                    procs::fetch()
                };
                if tx.send(feed).is_err() {
                    return;
                }
                wake();
            }
        });
    }
    s.tick_feeds();
}

/// Quitting stops every Spectra job the pages started (a memtrace can run 20 s), and waits a
/// moment for them to be killed, and for any queued `jfr stop`, since the process ends right
/// after.
fn on_quit(s: &mut App, _: &mut Tx) {
    // A flight recording the open page started ends with the app, like leaving the page.
    s.stop_recording_now();
    s.inspect.cancel_all();
    system::CANCEL.store(true, std::sync::atomic::Ordering::SeqCst);
    s.activity.cancel();
    s.network.cancel();
    startup::CANCEL.store(true, std::sync::atomic::Ordering::SeqCst);
    s.whatswrong.auto = false;
    s.growth.auto = false;
    s.whatswrong.cancel();
    s.cancel_storage();
    s.cancel_compare();
    s.growth.cancel();
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
    landed |= land_machine(s);
    // Processes starting and exiting: rounds run only while the Activity page is open or
    // Keep recording is on, and stop once neither holds.
    landed |= s.tick_activity();
    if s.activity_wanted() {
        tx.wake_after(STATUS_TICK);
    }
    // Which processes move bytes: windows run only while the Network page is open.
    landed |= s.tick_network();
    if s.network_wanted() {
        tx.wake_after(STATUS_TICK);
    }
    // What starts at login, and a Disable or Enable's answer, for the Startup page.
    landed |= s.land_startup();
    // "Right now": a look when the Overview opens, then every 30 s while it shows.
    landed |= s.tick_whatswrong();
    // A folder's size or Time Machine's snapshots, for the Storage page; while a folder is
    // being measured, the page's "started … ago" keeps counting (tick_storage says when it changed).
    landed |= s.tick_storage();
    // The Compare page's snapshot list, and a comparison or snapshot on its way.
    landed |= s.tick_compare();
    if s.window_shown() && s.compare.busy() {
        tx.wake_after(STATUS_TICK);
    }
    if s.window_shown() && (s.overview_visible() || s.storage.busy()) {
        tx.wake_after(STATUS_TICK);
    }
    // Memory jumps and alerts: a look when the page opens, then every 30 s while it shows.
    landed |= s.tick_growth();
    if s.window_shown() && s.growth_visible() {
        tx.wake_after(STATUS_TICK);
    }
    // An open page's live rounds rest while the window is hidden.
    s.tick_live();
    if let Some(due) = s.tick_feeds() {
        tx.wake_at(due);
    }
    // A hung Spectra sends nothing, so look again on a timer to say the numbers are getting old;
    // an open page also looks for its next live round.
    if s.window_shown() && (s.has_processes() || s.on_page() || s.nav == groups::Nav::System) {
        tx.wake_after(STATUS_TICK);
    }
    let now_status = (s.feed_status(), s.system.statuses(s.now));
    // A reading growing old takes its finding away, with no new reading to say so.
    if now_status.1 != status.1 && !landed {
        s.refresh_findings();
    }
    landed || now_status != status
}

/// Memory, thermal, load, power, disks and network. The System page shows them all, the Network
/// page its listening ports and the Storage page its disks; elsewhere they show only through the
/// findings they change, so a reading that changes nothing on screen redraws nothing.
fn land_machine(s: &mut App) -> bool {
    let findings = finding_keys(&s.findings);
    let shown = shown_reading(s);
    s.land_system()
        && (s.nav == groups::Nav::System
            || shown_reading(s) != shown
            || finding_keys(&s.findings) != findings)
}

/// The machine reading the page on screen draws from: when it last landed and what failed since.
fn shown_reading(s: &App) -> Option<(Option<f64>, Option<system::Fail>)> {
    match s.nav {
        groups::Nav::Network => Some((s.system.network.updated, s.system.network.error.clone())),
        groups::Nav::Storage => Some((s.system.storage.updated, s.system.storage.error.clone())),
        _ => None,
    }
}

/// What a finding shows, to tell whether a reading changed any.
fn finding_keys(findings: &[findings::Finding]) -> Vec<(&'static str, u32, String, String)> {
    findings
        .iter()
        .map(|f| (f.rule, f.pid, f.title.clone(), f.detail.clone()))
        .collect()
}

/// The window going off screen rests every feed and the machine readings; coming back reads
/// at once.
fn on_visibility(s: &mut App, shown: bool, tx: &mut Tx) {
    s.visible = shown;
    system::PAUSED.store(!shown, std::sync::atomic::Ordering::SeqCst);
    if shown {
        tx.wake_after(Duration::ZERO);
    }
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
            ("logs", sample_logs::logs_scene),
            ("logs-empty", sample_logs::logs_empty_scene),
            ("logs-timeout", sample_logs::logs_timeout_scene),
            ("jvm-tools", sample_jvm::jvm_tools_scene),
            ("tool-threads", sample_jvm::tool_threads_scene),
            (
                "tool-threads-deadlock",
                sample_jvm::tool_threads_deadlock_scene,
            ),
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
            ("activity", sample_activity::activity_scene),
            ("activity-storm", sample_activity::activity_storm_scene),
            (
                "activity-unsupported",
                sample_activity::activity_unsupported_scene,
            ),
            ("network", sample_network::network_scene),
            ("network-measuring", sample_network::network_measuring_scene),
            (
                "network-unsupported",
                sample_network::network_unsupported_scene,
            ),
            ("startup", sample_startup::startup_scene),
            ("startup-confirm", sample_startup::startup_confirm_scene),
            ("startup-failed", sample_startup::startup_failed_scene),
            ("storage", sample_storage::storage_scene),
            ("storage-measuring", sample_storage::storage_measuring_scene),
            ("storage-inside", sample_storage::storage_inside_scene),
            ("storage-failed", sample_storage::storage_failed_scene),
            ("compare", sample_compare::compare_scene),
            ("compare-list", sample_compare::compare_list_scene),
            ("compare-diffing", sample_compare::compare_diffing_scene),
            ("compare-empty", sample_compare::compare_empty_scene),
            ("compare-failed", sample_compare::compare_failed_scene),
            ("compare-no-list", sample_compare::compare_no_list_scene),
            ("growth", sample_growth::growth_scene),
            ("growth-empty", sample_growth::growth_empty_scene),
            ("growth-history", sample_growth::growth_history_scene),
            (
                "growth-unavailable",
                sample_growth::growth_unavailable_scene,
            ),
            ("tree", sample_tree::tree_scene),
            ("tree-search", sample_tree::tree_search_scene),
            ("right-now", sample_whatswrong::right_now_scene),
            ("right-now-clear", sample_whatswrong::right_now_clear_scene),
        ],
        setup: Some(setup),
        poll: Some(poll),
        on_key: Some(ui::on_key),
        ..AppSpec::new("Spectra", (1100.0, 700.0), app::init, ui::view)
    };
    rune::App::new(spec)
        .chrome(chrome)
        .on_quit(on_quit)
        .on_visibility(on_visibility)
        .run();
}

#[cfg(test)]
mod tests {
    use super::*;
    use groups::Nav;
    use system::{Network, Reading};

    #[test]
    fn a_machine_reading_redraws_the_page_that_shows_it() {
        let mut s = app::init();
        let tx = s.system.tx.clone().expect("no reading thread in a test");
        let lands = |s: &mut App, nav: Nav, r: Reading| {
            s.nav = nav;
            s.now += 1.0;
            tx.send(r).unwrap();
            land_machine(s)
        };
        assert!(lands(
            &mut s,
            Nav::Network,
            Reading::Network(Ok(Network::default()))
        ));
        assert!(lands(
            &mut s,
            Nav::Storage,
            Reading::Storage(Ok(Vec::new()))
        ));
        assert!(lands(
            &mut s,
            Nav::System,
            Reading::Network(Ok(Network::default()))
        ));
        // A page that doesn't show it, with no finding changed: nothing to redraw.
        assert!(!lands(
            &mut s,
            Nav::All,
            Reading::Network(Ok(Network::default()))
        ));
        assert!(!lands(
            &mut s,
            Nav::Network,
            Reading::Storage(Ok(Vec::new()))
        ));
        assert!(!lands(
            &mut s,
            Nav::Storage,
            Reading::Network(Ok(Network::default()))
        ));
    }
}
