//! Process families: related processes whose CPU is summed together, such as one simulator
//! device, Xcode, a build tree or a browser (Go's `family.go`).

use std::collections::{HashMap, HashSet};

use jiff::Timestamp;
use serde::Serialize;
use spectra_base::gojson::{self, GoDuration, GoTime};

use super::{Info, duration_seconds, sub_nanos};
use crate::gosort;

pub const FAMILY_SIMULATOR: &str = "simulator";
pub const FAMILY_XCODE: &str = "xcode";
pub const FAMILY_EMULATOR: &str = "emulator";
pub const FAMILY_BUILD: &str = "build";
pub const FAMILY_BROWSER: &str = "browser";
pub const FAMILY_OTHER: &str = "other";

const MAX_ANCESTOR_HOPS: usize = 64;
const HEAVIEST_MEMBERS: usize = 3;

/// One process inside a [`Family`], for the heaviest-members view.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Member {
    pub pid: i64,
    pub command: String,
    pub cpu_pct: f64,
    #[serde(skip)]
    pub age: GoDuration,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub age_seconds: i64,
}

/// A group of related processes (Go's `Family`). `cpu_pct` is in percent of one core;
/// `share_pct` is the family's share of the CPU used by all listed processes. `age` is the
/// oldest member's.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Family {
    pub key: String,
    pub kind: String,
    pub name: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub udid: String,
    pub members: i64,
    pub cpu_pct: f64,
    pub share_pct: f64,
    pub rss_kib: i64,
    /// The earliest member start.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<GoTime>,
    #[serde(skip)]
    pub age: GoDuration,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub age_seconds: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub heaviest: Vec<Member>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
struct FamId {
    key: String,
    kind: &'static str,
    name: String,
    udid: String,
}

const XCODE_NAMES: &[&str] = &["xcodebuild", "xcode", "xcbbuildservice", "sourcekitservice", "ibtoold", "swift-build", "simulator"];
/// Build drivers start build trees; their children are in the build family.
const BUILD_DRIVERS: &[&str] = &["gradle", "make", "gmake", "ninja", "cmake", "cargo", "bazel", "mvn", "sbt"];
/// Build tools count as build only under a driver (or Xcode).
const BUILD_TOOLS: &[&str] = &[
    "clang",
    "clang++",
    "ld",
    "swift-frontend",
    "swiftc",
    "javac",
    "kotlinc",
    "rustc",
    "compile",
    "link",
    "ccache",
    "cc",
    "c++",
    "gcc",
    "g++",
];
const BROWSER_BUNDLES: &[(&str, &str)] = &[
    ("google chrome.app", "Google Chrome"),
    ("chromium.app", "Chromium"),
    ("microsoft edge.app", "Microsoft Edge"),
    ("brave browser.app", "Brave Browser"),
    ("firefox.app", "Firefox"),
    ("safari.app", "Safari"),
    ("arc.app", "Arc"),
    ("opera.app", "Opera"),
];
const BROWSER_BINARIES: &[(&str, &str)] = &[
    ("chrome", "Google Chrome"),
    ("google-chrome", "Google Chrome"),
    ("google-chrome-stable", "Google Chrome"),
    ("chromium", "Chromium"),
    ("chromium-browser", "Chromium"),
    ("firefox", "Firefox"),
    ("firefox-bin", "Firefox"),
    ("brave", "Brave Browser"),
    ("brave-browser", "Brave Browser"),
    ("microsoft-edge", "Microsoft Edge"),
    ("opera", "Opera"),
];

/// Go's `GroupFamilies`: CPU summed per family. Processes in no known family are grouped by
/// command name under kind `other`. `top_members` bounds each family's heaviest list (0 means
/// 3). Sorted by CPU, descending. Pure: `now` makes ages deterministic.
pub fn group_families(procs: &[Info], now: Timestamp, top_members: usize) -> Vec<Family> {
    let top = if top_members == 0 { HEAVIEST_MEMBERS } else { top_members };
    let mut by_pid: HashMap<i64, &Info> = HashMap::new();
    for p in procs {
        by_pid.insert(p.pid, p);
    }
    let ids = resolve_all(procs, &by_pid);
    let mut order: Vec<String> = Vec::new();
    let mut groups: HashMap<String, Family> = HashMap::new();
    let mut members: HashMap<String, Vec<Member>> = HashMap::new();
    let mut total = 0.0;
    let now_ns = now.as_nanosecond();
    for (p, id) in procs.iter().zip(ids) {
        let f = groups.entry(id.key.clone()).or_insert_with(|| {
            order.push(id.key.clone());
            Family { key: id.key.clone(), kind: id.kind.to_string(), name: id.name.clone(), udid: id.udid.clone(), ..Family::default() }
        });
        let cpu = proc_cpu(p);
        total += cpu;
        let age = proc_age(p, now_ns);
        f.members += 1;
        f.cpu_pct += cpu;
        f.rss_kib += p.rss_kib;
        if let Some(started) = proc_start(p, now, age)
            && f.started_at.is_none_or(|s| started.instant < s.instant)
        {
            f.started_at = Some(started);
        }
        if age > f.age {
            f.age = age;
        }
        members.entry(id.key).or_default().push(Member {
            pid: p.pid,
            command: p.command.clone(),
            cpu_pct: cpu,
            age,
            age_seconds: duration_seconds(age.0) as i64,
        });
    }
    let mut out: Vec<Family> = order
        .into_iter()
        .map(|key| {
            let mut f = groups.remove(&key).unwrap_or_default();
            if total > 0.0 {
                f.share_pct = f.cpu_pct / total * 100.0;
            }
            f.age_seconds = duration_seconds(f.age.0) as i64;
            f.heaviest = top_members_by_cpu(members.remove(&key).unwrap_or_default(), top);
            f
        })
        .collect();
    out.sort_by(|a, b| b.cpu_pct.total_cmp(&a.cpu_pct).then_with(|| a.key.cmp(&b.key)));
    out
}

fn top_members_by_cpu(mut ms: Vec<Member>, n: usize) -> Vec<Member> {
    gosort::slice(&mut ms, |a, b| if a.cpu_pct != b.cpu_pct { a.cpu_pct > b.cpu_pct } else { a.pid < b.pid });
    ms.truncate(n);
    ms
}

fn proc_cpu(p: &Info) -> f64 {
    if p.cpu_pct > p.cpu_percent { p.cpu_pct } else { p.cpu_percent }
}

fn proc_age(p: &Info, now_ns: i128) -> GoDuration {
    let mut age = p.elapsed;
    let start = proc_start_raw(p);
    if !start.is_zero() && now_ns != GoTime::zero().instant.as_nanosecond() && now_ns > start.instant.as_nanosecond() {
        let d = GoDuration(sub_nanos(now_ns, start.instant.as_nanosecond()));
        if d > age {
            age = d;
        }
    }
    age
}

fn proc_start_raw(p: &Info) -> GoTime {
    if !p.started_at.is_zero() { p.started_at } else { p.start_time }
}

fn proc_start(p: &Info, now: Timestamp, age: GoDuration) -> Option<GoTime> {
    let start = proc_start_raw(p);
    if !start.is_zero() {
        return Some(start);
    }
    if age.0 > 0 && now != GoTime::zero().instant {
        let t = Timestamp::from_nanosecond(now.as_nanosecond() - i128::from(age.0)).ok()?;
        return Some(GoTime::utc(t));
    }
    None
}

/// p's parent chain, nearest first, stopping at a missing parent, a cycle, or a parent that
/// started after its child (pid reuse).
fn ancestors<'a>(p: &'a Info, by_pid: &HashMap<i64, &'a Info>) -> Vec<&'a Info> {
    let mut out = Vec::new();
    let mut seen = HashSet::from([p.pid]);
    let mut cur = p;
    while out.len() < MAX_ANCESTOR_HOPS {
        let Some(&parent) = by_pid.get(&cur.ppid) else { break };
        if seen.contains(&parent.pid) {
            break;
        }
        let (ps, cs) = (proc_start_raw(parent), proc_start_raw(cur));
        if !ps.is_zero() && !cs.is_zero() && ps.instant > cs.instant {
            break;
        }
        seen.insert(parent.pid);
        out.push(parent);
        cur = parent;
    }
    out
}

/// Go's `resolveAll`: each simulator tree gets one identity. A `launchd_sim` with no UDID of its
/// own (keyed by pid) takes the single UDID its tree exposes; several or none keep the pid key.
fn resolve_all(procs: &[Info], by_pid: &HashMap<i64, &Info>) -> Vec<FamId> {
    let mut ids: Vec<FamId> = Vec::with_capacity(procs.len());
    let mut roots = vec![0i64; procs.len()];
    let mut udids: HashMap<i64, Vec<FamId>> = HashMap::new();
    for (i, p) in procs.iter().enumerate() {
        let id = resolve_family(p, by_pid);
        if id.kind == FAMILY_SIMULATOR {
            roots[i] = launchd_root(p, by_pid);
            if roots[i] != 0 && !id.udid.is_empty() {
                let set = udids.entry(roots[i]).or_default();
                match set.iter_mut().find(|x| x.udid == id.udid) {
                    Some(x) => *x = id.clone(),
                    None => set.push(id.clone()),
                }
            }
        }
        ids.push(id);
    }
    for (i, id) in ids.iter_mut().enumerate() {
        if id.kind == FAMILY_SIMULATOR
            && id.udid.is_empty()
            && let Some(set) = udids.get(&roots[i])
            && set.len() == 1
        {
            *id = set[0].clone();
        }
    }
    ids
}

/// The pid of p or its nearest `launchd_sim` ancestor, or 0.
fn launchd_root(p: &Info, by_pid: &HashMap<i64, &Info>) -> i64 {
    if command_name(p).to_lowercase() == "launchd_sim" {
        return p.pid;
    }
    ancestors(p, by_pid).into_iter().find(|a| command_name(a).to_lowercase() == "launchd_sim").map_or(0, |a| a.pid)
}

/// Go's `resolveFamily`: a simulator device (its own UDID path, or a `launchd_sim` ancestor)
/// always wins; otherwise the process is classified directly, generic processes and build tools
/// inheriting from the nearest Xcode, emulator or build-driver ancestor.
fn resolve_family(p: &Info, by_pid: &HashMap<i64, &Info>) -> FamId {
    let chain = ancestors(p, by_pid);
    if let Some(id) = simulator_family(p) {
        return id;
    }
    if let Some(id) = chain.iter().find_map(|a| simulator_family(a)) {
        return id;
    }
    let direct = direct_family(p);
    if let Some(id) = &direct
        && id.kind != FAMILY_BUILD
        && !is_build_tool(p)
    {
        return id.clone();
    }
    for a in &chain {
        let Some(aid) = direct_family(a) else { continue };
        if aid.kind == FAMILY_XCODE || aid.kind == FAMILY_EMULATOR {
            return aid;
        }
        if aid.kind == FAMILY_BUILD && is_build_tool(p) {
            return aid;
        }
    }
    if let Some(id) = direct {
        return id;
    }
    let name = command_name(p);
    FamId { key: format!("{FAMILY_OTHER}:{name}"), kind: FAMILY_OTHER, name, udid: String::new() }
}

fn simulator_family(p: &Info) -> Option<FamId> {
    let is_launchd = command_name(p).to_lowercase() == "launchd_sim";
    // Only launchd_sim documents its device path as an argument; other processes' arguments
    // (tail -f .../Devices/<UDID>/log) must not count.
    let text = if is_launchd { format!("{} {}", p.full_command_line, p.executable_path) } else { exe_text(p) };
    if let Some(udid) = find_sim_udid(&text) {
        let udid = udid.to_uppercase();
        return Some(FamId { key: format!("{FAMILY_SIMULATOR}:{udid}"), kind: FAMILY_SIMULATOR, name: format!("Simulator {udid}"), udid });
    }
    is_launchd.then(|| FamId {
        key: format!("{FAMILY_SIMULATOR}:launchd_sim-{}", p.pid),
        kind: FAMILY_SIMULATOR,
        name: format!("Simulator (launchd_sim pid {})", p.pid),
        udid: String::new(),
    })
}

/// Go's `(?i)CoreSimulator/Devices/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`:
/// the UDID of the leftmost match.
fn find_sim_udid(text: &str) -> Option<&str> {
    const PREFIX: &[u8] = b"coresimulator/devices/";
    let b = text.as_bytes();
    (0..b.len()).find_map(|i| {
        let rest = b.get(i..)?;
        if rest.len() < PREFIX.len() + 36 || !rest[..PREFIX.len()].eq_ignore_ascii_case(PREFIX) {
            return None;
        }
        let u = &rest[PREFIX.len()..PREFIX.len() + 36];
        let ok = u.iter().enumerate().all(|(j, c)| if matches!(j, 8 | 13 | 18 | 23) { *c == b'-' } else { c.is_ascii_hexdigit() });
        ok.then(|| &text[i + PREFIX.len()..i + PREFIX.len() + 36])
    })
}

fn is_build_tool(p: &Info) -> bool {
    BUILD_TOOLS.contains(&command_name(p).to_lowercase().as_str())
}

fn direct_family(p: &Info) -> Option<FamId> {
    let name = command_name(p).to_lowercase();
    let fam = |key: &str, kind: &'static str, name: &str| FamId { key: key.to_string(), kind, name: name.to_string(), udid: String::new() };
    if name == "emulator" || name.starts_with("qemu-") {
        return Some(fam(FAMILY_EMULATOR, FAMILY_EMULATOR, "Emulator (qemu)"));
    }
    if let Some(app) = browser_name(p) {
        return Some(fam(&format!("{FAMILY_BROWSER}:{app}"), FAMILY_BROWSER, app));
    }
    let exe = exe_text(p).to_lowercase();
    if XCODE_NAMES.contains(&name.as_str()) || exe.contains("/xcode.app/") || name.starts_with("com.apple.dt.") {
        return Some(fam(FAMILY_XCODE, FAMILY_XCODE, "Xcode"));
    }
    if BUILD_DRIVERS.contains(&name.as_str()) || is_gradle_jvm(&name, &p.full_command_line) {
        return Some(fam(FAMILY_BUILD, FAMILY_BUILD, "Build tools"));
    }
    None
}

/// The executable's app bundle (macOS) or binary name (Linux), never arguments or directories.
fn browser_name(p: &Info) -> Option<&'static str> {
    let exe = exe_text(p);
    let lower = exe.to_lowercase();
    if let Some(i) = lower.find(".app/") {
        let bundle = &lower[..i + 4];
        let bundle = bundle.rsplit('/').next().unwrap_or(bundle);
        return BROWSER_BUNDLES.iter().find(|(b, _)| *b == bundle).map(|(_, n)| *n);
    }
    let base = go_base(first_token(&exe)).to_lowercase();
    BROWSER_BINARIES.iter().find(|(b, _)| *b == base).map(|(_, n)| *n)
}

fn first_token(s: &str) -> &str {
    s.split_whitespace().next().unwrap_or(s)
}

/// Go's `filepath.Base`.
pub(crate) fn go_base(path: &str) -> String {
    if path.is_empty() {
        return ".".to_string();
    }
    let trimmed = path.trim_end_matches('/');
    if trimmed.is_empty() {
        return "/".to_string();
    }
    trimmed.rsplit('/').next().unwrap_or(trimmed).to_string()
}

/// The short executable name.
fn command_name(p: &Info) -> String {
    if !p.command.is_empty() { go_base(&p.command) } else { go_base(&p.executable_path) }
}

fn is_gradle_jvm(name: &str, cmdline: &str) -> bool {
    if name != "java" {
        return false;
    }
    let s = cmdline.to_lowercase();
    ["gradledaemon", "gradlewrappermain", "gradle-launcher", "kotlin-daemon", "kotlincompilerdaemon"].iter().any(|k| s.contains(k))
}

/// Go's `exeText`: the executable part of the process, without arguments.
fn exe_text(p: &Info) -> String {
    if !p.executable_path.is_empty() {
        return p.executable_path.clone();
    }
    let s = p.full_command_line.as_str();
    if !s.starts_with('/') {
        // A relative argv[0]: arguments may contain paths, so use only the name.
        return first_token(s).to_string();
    }
    // App-bundle executables may contain spaces; allow them only when no absolute-path
    // argument starts before the bundle marker.
    let marker = s.find(".app/Contents/");
    let arg_start = s.find(" /");
    let Some(marker) = marker else { return first_token(s).to_string() };
    if arg_start.is_some_and(|a| a < marker) {
        return first_token(s).to_string();
    }
    match s[marker..].find(" -") {
        Some(i) => s[..marker + i].to_string(),
        None => s.to_string(),
    }
}

#[cfg(test)]
mod tests;
