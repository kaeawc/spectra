//! Sample data for the Hosts pages' headless scenes and tests, from invented fixtures in the
//! shape `spectra-remote call` prints (tests/fixtures/remote): a CI build Mac with Gradle and
//! Kotlin daemons, a Mac whose agent is too old to list processes, and one that can't be reached.

use crate::app::App;
use crate::groups::Nav;
use crate::remote::{Fail, Host, answer, check_host, parse_health, parse_jvms, parse_procs};
use crate::sample::sample_scene;

pub const HEALTH: &str = include_str!("../tests/fixtures/remote/health.json");
pub const HEALTH_OLD: &str = include_str!("../tests/fixtures/remote/health-old.json");
pub const PROCESSES: &str = include_str!("../tests/fixtures/remote/process-list.json");
pub const JVMS: &str = include_str!("../tests/fixtures/remote/jvm-list.json");

const UNREACHABLE: &str =
    "Couldn't get an answer from studio-mac:7878: dial tcp 100.101.7.12:7878: i/o timeout";

fn result(response: &str) -> serde_json::Value {
    answer(Some(0), response, "", "sample").expect("the remote fixture reads")
}

fn host(name: &str, target: &str) -> Host {
    check_host(name, target).expect("a sample host checks out")
}

/// The sample Mac, with three other Macs listed: the build Mac read in full, an old agent and an
/// unreachable one. Returns the build Mac's id.
fn listed(s: &mut App) -> u64 {
    sample_scene(s);
    s.select(None);
    // Sample scenes never run the controller.
    s.hosts.threaded = false;
    s.hosts.remote = std::sync::Arc::new(NoRemote);
    let now = s.now;
    let build = s.hosts.push(host("Build Mac", "build-mac"));
    let old = s.hosts.push(host("Lab Mac", "lab-mac.tail1234.ts.net"));
    let gone = s.hosts.push(host("Studio", "studio-mac"));
    for h in &mut s.hosts.list {
        if h.id == build {
            h.health.value = Some((parse_health(&result(HEALTH)).unwrap(), now - 12.0));
            h.procs.value = Some((parse_procs(&result(PROCESSES)).unwrap(), now - 4.0));
            h.jvms.value = Some((parse_jvms(&result(JVMS)).unwrap(), now - 20.0));
        } else if h.id == old {
            h.health.value = Some((parse_health(&result(HEALTH_OLD)).unwrap(), now - 8.0));
        } else if h.id == gone {
            h.health.error = Some(UNREACHABLE.into());
        }
    }
    build
}

/// Answers nothing: a scene that asks gets a plain failure rather than running anything.
struct NoRemote;

impl crate::remote::Remote for NoRemote {
    fn call(
        &self,
        _: &str,
        _: &str,
        _: std::time::Duration,
        _: &std::sync::atomic::AtomicBool,
    ) -> Result<serde_json::Value, Fail> {
        Err(Fail {
            missing: false,
            why: "sample scenes don't ask other Macs".into(),
        })
    }
}

/// Keeps a scene from asking again: every reading is fresh for the next minute or so.
fn rest(s: &mut App) {
    for h in s.hosts.list.iter().map(|h| h.id).collect::<Vec<_>>() {
        s.hosts.sample_rest(h, s.now + 3600.0);
    }
}

/// The list: a connected Mac, an old agent and an unreachable one, with the add form.
pub fn hosts_scene(s: &mut App) {
    listed(s);
    rest(s);
    s.nav = Nav::Hosts;
}

/// No other Macs yet.
pub fn hosts_empty_scene(s: &mut App) {
    sample_scene(s);
    s.select(None);
    s.nav = Nav::Hosts;
}

/// Spectra Remote's controller isn't installed here.
pub fn hosts_missing_scene(s: &mut App) {
    hosts_scene(s);
    s.hosts.missing = Some(
        "Couldn't run spectra-remote: No such file or directory (os error 2). Install Spectra Remote's controller (spectra-remote, from kaeawc/spectra-proxy) or set SPECTRA_REMOTE_BIN.".into(),
    );
}

/// The build Mac's page: its processes grouped busiest first, and its JVMs.
pub fn host_scene(s: &mut App) {
    let build = listed(s);
    rest(s);
    s.nav = Nav::Host(build);
}

/// The build Mac's page with its Java processes opened.
pub fn host_group_scene(s: &mut App) {
    host_scene(s);
    let procs: Vec<&crate::procs::Process> =
        s.hosts.list[0].procs.get().into_iter().flatten().collect();
    let sort = crate::groups::Sort {
        key: crate::groups::SortKey::Cpu,
        descending: true,
    };
    let id = crate::groups::group(&procs, sort)
        .into_iter()
        .find(|g| g.name == "java")
        .map(|g| g.id);
    if let Some(id) = id {
        s.hosts.open.insert(id);
    }
}

/// A Mac whose agent doesn't offer process.list.
pub fn host_unsupported_scene(s: &mut App) {
    listed(s);
    rest(s);
    s.nav = Nav::Host(s.hosts.list[1].id);
}

/// A Mac that can't be reached.
pub fn host_unreachable_scene(s: &mut App) {
    listed(s);
    rest(s);
    s.nav = Nav::Host(s.hosts.list[2].id);
}
