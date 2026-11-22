//! Which processes are moving bytes right now: `spectra network top` measures a short window
//! (5 s, one reading a second) off the UI thread, only while the Network page is open, and rests
//! between windows. What is listening, and the interface, VPN, proxy and DNS, come from the
//! Machine page's own `spectra network` reading, so the page runs nothing more for them.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::time::Duration;

use serde_json::Value;

use crate::app::App;
use crate::groups::Nav;
use crate::inspect::{Failure, Spectra};
use crate::system::Port;

/// How long one window measures, in seconds.
pub const WINDOW_SECS: u64 = 5;
/// How often Spectra reads the counters during a window.
pub const INTERVAL: &str = "1s";
/// Processes one window ranks.
pub const TOP: usize = 15;
/// Remote endpoints a row names before "+N more".
pub const REMOTES: usize = 3;
/// How long to rest, in seconds, between two good windows: a window keeps about one core busy.
pub const REST: f64 = 5.0;
/// How long one window may take before it's stopped: the window and a margin.
const DEADLINE: Duration = Duration::from_secs(WINDOW_SECS + 20);
/// The most a window may print.
const MAX_OUTPUT: usize = 4 << 20;
/// How long to wait, in seconds, after a failed window before the next.
pub const RETRY_FAILED_SECS: f64 = 15.0;
/// How long to wait, in seconds, before asking a Spectra without `network top` again.
const RETRY_UNSUPPORTED: f64 = 60.0;

/// One process that moved bytes in the window.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct User {
    pub pid: u32,
    /// The short name the counters give, often cut to 15 characters.
    pub command: String,
    pub bytes_in: u64,
    pub bytes_out: u64,
    /// Average bytes a second over the window.
    pub in_rate: f64,
    pub out_rate: f64,
    /// Remote endpoints, `203.0.113.4:443`.
    pub remotes: Vec<String>,
    /// First moved bytes after the window began.
    pub started: bool,
}

impl User {
    pub fn total(&self) -> u64 {
        self.bytes_in.saturating_add(self.bytes_out)
    }
}

/// One window, ranked.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Top {
    /// How long it measured, in milliseconds.
    pub duration_ms: u64,
    pub bytes_in: u64,
    pub bytes_out: u64,
    pub in_rate: f64,
    pub out_rate: f64,
    /// Processes that moved anything, ranked or not.
    pub active: u64,
    /// By bytes moved, most first; none that moved nothing.
    pub users: Vec<User>,
}

fn num(v: &Value, key: &str) -> Option<f64> {
    let v = v.get(key)?;
    v.as_f64()
        .or_else(|| v.as_str().and_then(|s| s.trim().parse().ok()))
        .filter(|n| n.is_finite())
}

fn bytes(v: &Value, key: &str) -> u64 {
    num(v, key).unwrap_or(0.0).max(0.0) as u64
}

fn rate(v: &Value, key: &str) -> f64 {
    num(v, key).unwrap_or(0.0).max(0.0)
}

fn st(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

fn user(v: &Value) -> Option<User> {
    let pid = num(v, "pid").filter(|n| *n > 0.0 && *n <= f64::from(u32::MAX))? as u32;
    let remotes = v
        .get("remotes")
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .filter_map(Value::as_str)
                .map(str::trim)
                .filter(|r| !r.is_empty())
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default();
    Some(User {
        pid,
        command: st(v, "command"),
        bytes_in: bytes(v, "bytes_in"),
        bytes_out: bytes(v, "bytes_out"),
        in_rate: rate(v, "avg_in_bytes_per_sec"),
        out_rate: rate(v, "avg_out_bytes_per_sec"),
        remotes,
        started: v.get("started").and_then(Value::as_bool).unwrap_or(false),
    })
}

/// Ranks by bytes moved, most first, then by pid; drops what moved nothing.
pub fn rank(mut users: Vec<User>) -> Vec<User> {
    users.retain(|u| u.total() > 0);
    users.sort_by(|a, b| b.total().cmp(&a.total()).then(a.pid.cmp(&b.pid)));
    users
}

/// What `spectra network top --json` printed. Missing or null fields read as nothing; a
/// process without a pid is skipped. Anything but a JSON object fails.
pub fn parse_top(json: &str) -> Result<Top, String> {
    let v: Value = serde_json::from_str(json.trim())
        .map_err(|e| format!("Spectra's network top output wasn't JSON: {e}"))?;
    if !v.is_object() {
        return Err("Spectra's network top output wasn't a JSON object".into());
    }
    let users: Vec<User> = v
        .get("processes")
        .and_then(Value::as_array)
        .map(|a| a.iter().filter_map(user).collect())
        .unwrap_or_default();
    let totals = v.get("totals").filter(|t| t.is_object());
    let users = rank(users);
    let sum = |f: fn(&User) -> u64| users.iter().map(f).sum::<u64>();
    let (bytes_in, bytes_out, in_rate, out_rate, active) = match totals {
        Some(t) => (
            bytes(t, "bytes_in"),
            bytes(t, "bytes_out"),
            rate(t, "avg_in_bytes_per_sec"),
            rate(t, "avg_out_bytes_per_sec"),
            bytes(t, "active_processes"),
        ),
        None => (
            sum(|u| u.bytes_in),
            sum(|u| u.bytes_out),
            users.iter().map(|u| u.in_rate).sum(),
            users.iter().map(|u| u.out_rate).sum(),
            users.len() as u64,
        ),
    };
    Ok(Top {
        duration_ms: bytes(&v, "duration_ms"),
        bytes_in,
        bytes_out,
        in_rate,
        out_rate,
        active,
        users,
    })
}

/// Bytes as `512 B`, `48 KB` or `1.2 GB`.
pub fn fmt_amount(b: u64) -> String {
    if b < 1024 {
        format!("{b} B")
    } else {
        crate::tools::fmt_bytes(b)
    }
}

/// `spectra network top` for one window of `secs` seconds.
pub fn args(secs: u64) -> Vec<String> {
    vec![
        "network".to_owned(),
        "top".into(),
        "--json".into(),
        "--duration".into(),
        format!("{secs}s"),
        "--interval".into(),
        INTERVAL.into(),
        "--top".into(),
        TOP.to_string(),
    ]
}

/// A name with `\xHH` escapes (how the socket listing writes a space) turned back into text.
pub fn unescape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(i) = rest.find("\\x") {
        out.push_str(&rest[..i]);
        let hex = rest.get(i + 2..i + 4);
        match hex.and_then(|h| u8::from_str_radix(h, 16).ok()) {
            Some(b) if b.is_ascii() => {
                out.push(b as char);
                rest = &rest[i + 4..];
            }
            _ => {
                out.push_str("\\x");
                rest = &rest[i + 2..];
            }
        }
    }
    out.push_str(rest);
    out
}

/// Names the socket listing (9 characters) and the counters (15) cut short.
const CUT: usize = 9;

/// Whether the process the feed lists under a pid is the one Spectra saw there: the same name,
/// or one that starts with Spectra's name when Spectra's was cut short. A pid reused by another
/// process never matches, so its row opens nothing.
pub fn same_process(feed: &str, seen: &str) -> bool {
    let name = unescape(seen);
    if name.trim().is_empty() {
        return false;
    }
    feed == name || (seen.chars().count() >= CUT && feed.starts_with(&name))
}

/// Whether a bound address takes connections from every interface, not only this machine.
pub fn all_interfaces(addr: &str) -> bool {
    let a = addr.trim();
    matches!(a, "*" | "0.0.0.0" | "::" | "[::]" | "*:*")
        || a.starts_with("*:")
        || a.starts_with("0.0.0.0:")
        || a.starts_with("[::]:")
}

/// Whether a listening port is open on every interface.
pub fn exposed(p: &Port) -> bool {
    p.addrs.iter().any(|a| all_interfaces(a))
}

/// The listening ports, those open on every interface first, then by port.
pub fn listening(ports: &[Port]) -> Vec<&Port> {
    let mut out: Vec<&Port> = ports.iter().collect();
    out.sort_by(|a, b| {
        exposed(b)
            .cmp(&exposed(a))
            .then(a.port.cmp(&b.port))
            .then_with(|| a.proto.cmp(&b.proto))
            .then(a.pid.cmp(&b.pid))
    });
    out
}

/// Why a window has no ranking.
#[derive(Clone, Debug, PartialEq)]
pub enum Fail {
    /// An older Spectra without `network top`.
    Unsupported(String),
    Failed(String),
    /// Cancelled: the page was left, or the app quit.
    Stopped,
}

/// What a finished run's output means. An older Spectra without `network top` reads `top` as a
/// stray argument, stops parsing flags there (so `--json` is never seen), prints its human
/// network summary and exits 0: output that isn't a JSON object is that, not a failure.
pub fn read_top(out: &str) -> Result<Top, Fail> {
    if !out.trim_start().starts_with('{') {
        return Err(Fail::Unsupported(NO_TOP.into()));
    }
    parse_top(out).map_err(Fail::Failed)
}

const NO_TOP: &str = "This Spectra has no `spectra network top`. Update Spectra to see which processes use the network.";

/// What a failed run means.
pub fn classify(why: &str) -> Fail {
    let lower = why.to_ascii_lowercase();
    let old = lower.contains("unknown command")
        || lower.contains("unknown subcommand")
        || lower.contains("flag provided but not defined")
        || (lower.contains("stat ") && lower.contains("no such file"));
    if old {
        Fail::Unsupported(NO_TOP.into())
    } else if lower.contains("not supported") || lower.contains("unsupported") {
        Fail::Unsupported("Spectra can't measure traffic per process on this system.".into())
    } else {
        Fail::Failed(why.trim().to_owned())
    }
}

/// One window's result, for window number `job`.
pub struct Round {
    pub job: u64,
    pub result: Result<Top, Fail>,
}

/// A window in flight.
struct Flight {
    job: u64,
    cancel: Arc<AtomicBool>,
}

/// The Network page's state: the last window, and the one in flight.
pub struct NetworkPage {
    pub top: Option<Top>,
    /// What went wrong with the last window; cleared by a good one.
    pub error: Option<Fail>,
    /// The window clock when the last good window landed.
    pub updated: Option<f64>,
    /// The window clock before which no window starts.
    pub next: f64,
    /// False in sample scenes: their pids are made up, so nothing measures on its own.
    pub auto: bool,
    pub spectra: Arc<dyn Spectra>,
    flight: Option<Flight>,
    job: u64,
    tx: Sender<Round>,
    rx: Receiver<Round>,
}

impl Default for NetworkPage {
    fn default() -> NetworkPage {
        NetworkPage::new()
    }
}

/// The real Spectra, its output capped at `MAX_OUTPUT`.
struct Capped;

impl Spectra for Capped {
    fn run(
        &self,
        args: &[String],
        deadline: Duration,
        cancel: &AtomicBool,
    ) -> Result<String, Failure> {
        let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
        let mut cmd = std::process::Command::new(&bin);
        cmd.args(args);
        let out =
            crate::run::run_until(cmd, deadline, MAX_OUTPUT, cancel).map_err(|e| match e {
                crate::run::Ended::Cancelled => Failure::Cancelled,
                crate::run::Ended::Failed(e) => Failure::Failed(crate::procs::run_failure(&bin, e)),
            })?;
        if !out.status.success() {
            return Err(Failure::Failed(crate::inspect::failure_line(
                &bin,
                args,
                &out.stderr,
                out.status.code(),
            )));
        }
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    }
}

impl NetworkPage {
    pub fn new() -> NetworkPage {
        let (tx, rx) = channel();
        NetworkPage {
            top: None,
            error: None,
            updated: None,
            next: 0.0,
            auto: true,
            spectra: Arc::new(Capped),
            flight: None,
            job: 0,
            tx,
            rx,
        }
    }

    /// Whether a window is measuring.
    pub fn measuring(&self) -> bool {
        self.flight.is_some()
    }

    /// A window that never lands and runs nothing, so a sample scene shows the page measuring.
    pub fn sample_flight(&mut self) {
        self.job += 1;
        self.flight = Some(Flight {
            job: self.job,
            cancel: Arc::new(AtomicBool::new(false)),
        });
    }

    /// Stops the window in flight, if any. Its result, when it comes, is dropped.
    pub fn cancel(&mut self) {
        if let Some(f) = self.flight.take() {
            f.cancel.store(true, Ordering::SeqCst);
        }
    }
}

impl App {
    /// Whether the Network page is on screen, with no process page over it.
    pub fn network_open(&self) -> bool {
        self.nav == Nav::Network && !self.on_page()
    }

    /// Whether windows should run now: only while the page is open.
    pub fn network_wanted(&self) -> bool {
        self.network.auto && self.network_open()
    }

    /// Lands finished windows, stops the one in flight once the page is left, and starts the
    /// next once the page is open and the rest is over. True when anything landed.
    pub fn tick_network(&mut self) -> bool {
        let mut landed = false;
        while let Ok(r) = self.network.rx.try_recv() {
            landed |= self.land_network(r);
        }
        if !self.network_wanted() {
            self.network.cancel();
        } else if self.network.flight.is_none() && self.now >= self.network.next {
            self.start_network_round();
            // The status line now says a window is running: draw it.
            landed = true;
        }
        landed
    }

    fn start_network_round(&mut self) {
        let n = &mut self.network;
        n.job += 1;
        let (job, cancel) = (n.job, Arc::new(AtomicBool::new(false)));
        n.flight = Some(Flight {
            job,
            cancel: Arc::clone(&cancel),
        });
        let (spectra, tx, wake) = (Arc::clone(&n.spectra), n.tx.clone(), self.wake.clone());
        // Quit waits for this window from now until it's done, even before Spectra starts.
        let hold = crate::run::pending();
        let thread_tx = tx.clone();
        let started = std::thread::Builder::new()
            .name("spectra-network-top".into())
            .spawn(move || {
                let result = match spectra.run(&args(WINDOW_SECS), DEADLINE, &cancel) {
                    Ok(out) => read_top(&out),
                    Err(Failure::Cancelled) => Err(Fail::Stopped),
                    Err(Failure::Failed(why)) => Err(classify(&why)),
                };
                let _ = thread_tx.send(Round { job, result });
                drop(hold);
                if let Some(wake) = wake {
                    wake();
                }
            });
        if let Err(e) = started {
            let _ = tx.send(Round {
                job,
                result: Err(Fail::Failed(format!(
                    "Couldn't start a thread to measure the network: {e}."
                ))),
            });
        }
    }

    /// Lands one window, if it's the one in flight. True when it changed anything.
    pub fn land_network(&mut self, r: Round) -> bool {
        let now = self.now;
        let n = &mut self.network;
        if n.flight.as_ref().is_none_or(|f| f.job != r.job) {
            return false;
        }
        n.flight = None;
        match r.result {
            Ok(top) => {
                n.top = Some(top);
                n.error = None;
                n.updated = Some(now);
                n.next = now + REST;
            }
            Err(Fail::Stopped) => return false,
            Err(fail) => {
                n.next = now
                    + match fail {
                        Fail::Unsupported(_) => RETRY_UNSUPPORTED,
                        _ => RETRY_FAILED_SECS,
                    };
                n.error = Some(fail);
            }
        }
        true
    }

    /// Whether `pid` is in the process feed under the name Spectra gave it: only then does a
    /// row open its page.
    pub fn same_process(&self, pid: u32, seen: &str) -> bool {
        self.process(pid)
            .is_some_and(|p| same_process(&p.name(), seen))
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::sync::Mutex;
    use std::time::Instant;

    pub const TOP_JSON: &str = include_str!("../tests/fixtures/network/top.json");
    pub const SPARSE: &str = include_str!("../tests/fixtures/network/top-sparse.json");
    pub const NETWORK: &str = include_str!("../tests/fixtures/network/network.json");

    #[test]
    fn the_fixture_parses_and_ranks_by_bytes_moved() {
        let top = parse_top(TOP_JSON).unwrap();
        let order: Vec<(u32, &str)> = top
            .users
            .iter()
            .map(|u| (u.pid, u.command.as_str()))
            .collect();
        assert_eq!(
            order,
            [
                (1320, "node"),
                (4101, "java"),
                (1200, "claude"),
                (901, "Slack Helper (R"),
                (1311, "gh"),
                (1201, "curl"),
            ],
            "node moved the most, though Spectra listed java first"
        );
        let java = &top.users[1];
        assert_eq!((java.bytes_in, java.bytes_out), (10_647_871, 31_375));
        assert_eq!(java.remotes.len(), 4);
        assert!((java.in_rate - 2_099_757.6).abs() < 0.1);
        let gh = &top.users[4];
        assert!(gh.started && gh.remotes.is_empty());
        assert!(!java.started);
        assert!(top.users[5].remotes.is_empty(), "null remotes read as none");
        assert_eq!(
            (top.bytes_in, top.bytes_out, top.active),
            (30_892_114, 104_936, 19)
        );
        assert_eq!(top.duration_ms, 5071);
    }

    #[test]
    fn missing_and_null_fields_read_as_nothing() {
        let top = parse_top(SPARSE).unwrap();
        let order: Vec<(u32, u64, u64)> = top
            .users
            .iter()
            .map(|u| (u.pid, u.bytes_in, u.bytes_out))
            .collect();
        // No pid: skipped. Moved nothing: dropped. A number as a string still counts.
        assert_eq!(order, [(80, 4096, 0), (77, 0, 2048), (78, 512, 0)]);
        assert_eq!(top.users[1].remotes, ["10.0.0.2:443"]);
        assert_eq!(top.users[2].command, "");
        assert_eq!(unescape(&top.users[0].command), "Slack ");
        assert!((top.users[0].in_rate - 819.2).abs() < 1e-9);
        // No totals: summed from the rows.
        assert_eq!((top.bytes_in, top.bytes_out, top.active), (4608, 2048, 3));
        let empty = parse_top("{}").unwrap();
        assert!(empty.users.is_empty() && empty.bytes_in == 0);
        assert!(parse_top("[]").is_err());
        assert!(parse_top("nettop: oops").is_err());
    }

    #[test]
    fn ranking_breaks_ties_by_pid_and_drops_the_idle() {
        let u = |pid, i, o| User {
            pid,
            bytes_in: i,
            bytes_out: o,
            ..User::default()
        };
        let ranked = rank(vec![u(9, 10, 0), u(3, 0, 0), u(5, 4, 6), u(2, 1, 1)]);
        let pids: Vec<u32> = ranked.iter().map(|u| u.pid).collect();
        assert_eq!(pids, [5, 9, 2]);
    }

    #[test]
    fn all_interfaces_is_the_wildcard_address_on_either_family() {
        for a in [
            "*",
            "0.0.0.0",
            "::",
            "[::]",
            "*:*",
            "*:5000",
            "0.0.0.0:22",
            "[::]:80",
        ] {
            assert!(all_interfaces(a), "{a}");
        }
        for a in ["127.0.0.1", "[::1]", "::1", "192.168.1.5", "", "localhost"] {
            assert!(!all_interfaces(a), "{a}");
        }
        let n = crate::system::parse_network(NETWORK).unwrap();
        let shown: Vec<(u32, bool)> = listening(&n.ports)
            .iter()
            .map(|p| (p.port, exposed(p)))
            .collect();
        assert_eq!(
            shown,
            [
                (22, true),
                (5000, true),
                (5432, true),
                (7000, true),
                (8080, true),
                (3000, false),
                (11434, false),
                (49368, false),
            ],
            "every interface first, then by port; 8080's two families are one row"
        );
    }

    #[test]
    fn the_proxy_and_vpn_come_from_the_machine_reading() {
        let n = crate::system::parse_network(NETWORK).unwrap();
        assert_eq!(
            n.proxy,
            ["HTTPS proxy.example.test:8443"],
            "an empty one isn't set"
        );
        assert!(n.vpn && n.vpn_ifaces == ["utun4"]);
        let none = crate::system::parse_network(r#"{"proxy": {}}"#).unwrap();
        assert!(none.proxy.is_empty());
        let null = crate::system::parse_network(r#"{"proxy": null}"#).unwrap();
        assert!(null.proxy.is_empty());
    }

    #[test]
    fn a_pid_links_only_when_its_name_matches() {
        assert!(same_process("node", "node"));
        assert!(
            same_process("Slack Helper (Renderer)", "Slack Helper (R"),
            "cut at 15"
        );
        assert!(same_process("ControlCenter", "ControlCe"), "cut at 9");
        assert!(
            same_process("Slack Helper (GPU)", "Slack\\x20"),
            "an escaped space"
        );
        assert!(!same_process("claude", "curl"), "a reused pid");
        assert!(
            !same_process("nodemon", "node"),
            "a short name must match whole"
        );
        assert!(!same_process("python3", "postgres"));
        assert!(!same_process("anything", ""));
        assert_eq!(unescape("a\\x20b\\xzz\\x"), "a b\\xzz\\x");
    }

    #[test]
    fn an_older_spectra_is_told_apart_from_a_failure() {
        assert!(matches!(
            classify("flag provided but not defined: -top"),
            Fail::Unsupported(_)
        ));
        assert!(matches!(
            classify("spectra: unknown subcommand \"top\""),
            Fail::Unsupported(_)
        ));
        // Before `network top`, Spectra printed its human summary for `network top --json`
        // and exited 0.
        assert!(matches!(
            read_top("Network\n  Default route: en0 via 192.168.1.1\n"),
            Err(Fail::Unsupported(_))
        ));
        assert!(matches!(read_top("{ not json"), Err(Fail::Failed(_))));
        assert_eq!(
            classify("nettop: permission denied\n"),
            Fail::Failed("nettop: permission denied".into())
        );
        assert_eq!(
            args(5).join(" "),
            "network top --json --duration 5s --interval 1s --top 15"
        );
    }

    struct Fake {
        calls: Mutex<Vec<Vec<String>>>,
        cancelled: AtomicBool,
        out: Option<Result<String, String>>,
    }

    impl Spectra for Fake {
        fn run(
            &self,
            args: &[String],
            _: Duration,
            cancel: &AtomicBool,
        ) -> Result<String, Failure> {
            self.calls.lock().unwrap().push(args.to_vec());
            if let Some(out) = &self.out {
                return out.clone().map_err(Failure::Failed);
            }
            let start = Instant::now();
            while !cancel.load(Ordering::SeqCst) {
                assert!(start.elapsed() < Duration::from_secs(10), "never cancelled");
                std::thread::sleep(Duration::from_millis(5));
            }
            self.cancelled.store(true, Ordering::SeqCst);
            Err(Failure::Cancelled)
        }
    }

    fn faked(out: Option<Result<String, String>>) -> (App, Arc<Fake>) {
        let fake = Arc::new(Fake {
            calls: Mutex::new(Vec::new()),
            cancelled: AtomicBool::new(false),
            out,
        });
        let mut s = crate::app::init();
        s.network.spectra = fake.clone();
        (s, fake)
    }

    fn calls(f: &Fake) -> usize {
        f.calls.lock().unwrap().len()
    }

    fn wait_for(what: &str, f: impl Fn() -> bool) {
        let start = Instant::now();
        while !f() {
            assert!(start.elapsed() < Duration::from_secs(5), "{what}");
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    fn land(s: &mut App) {
        let start = Instant::now();
        while !s.tick_network() {
            assert!(start.elapsed() < Duration::from_secs(5), "no window landed");
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    #[test]
    fn windows_run_only_while_the_page_is_open_and_stop_on_leave() {
        let (mut s, fake) = faked(None);
        s.tick_network();
        assert!(!s.network.measuring(), "nothing measures on the overview");
        s.show(Nav::System);
        s.tick_network();
        assert!(!s.network.measuring(), "nor on the Machine page");
        s.show(Nav::Network);
        s.tick_network();
        assert!(s.network.measuring());
        wait_for("the window started", || calls(&fake) == 1);
        assert_eq!(fake.calls.lock().unwrap()[0], args(WINDOW_SECS));
        s.tick_network();
        assert_eq!(calls(&fake), 1, "one window at a time");
        s.show(Nav::Activity);
        s.tick_network();
        assert!(!s.network.measuring());
        wait_for("leaving cancels it", || {
            fake.cancelled.load(Ordering::SeqCst)
        });
        // The cancelled window's answer lands nowhere.
        assert!(!s.tick_network());
        assert!(s.network.top.is_none() && s.network.error.is_none());
    }

    #[test]
    fn a_process_page_over_the_network_page_stops_the_window() {
        let (mut s, fake) = faked(None);
        crate::sample::sample_scene(&mut s);
        s.show(Nav::Network);
        s.tick_network();
        assert!(s.network.measuring());
        s.open(1320);
        s.tick_network();
        assert!(!s.network.measuring(), "a page over it is leaving it");
        wait_for("cancelled", || fake.cancelled.load(Ordering::SeqCst));
    }

    #[test]
    fn a_good_window_lands_then_rests() {
        let (mut s, fake) = faked(Some(Ok(TOP_JSON.to_owned())));
        s.show(Nav::Network);
        s.now = 100.0;
        s.tick_network();
        land(&mut s);
        let top = s.network.top.as_ref().unwrap();
        assert_eq!(top.users[0].pid, 1320);
        assert_eq!(s.network.updated, Some(100.0));
        assert_eq!(s.network.next, 100.0 + REST);
        s.tick_network();
        assert!(!s.network.measuring(), "resting");
        assert_eq!(calls(&fake), 1);
        s.now = 100.0 + REST;
        s.tick_network();
        assert!(s.network.measuring(), "the next window after the rest");
        land(&mut s);
        assert_eq!(calls(&fake), 2);
    }

    #[test]
    fn a_failed_window_keeps_the_last_ranking_and_waits_longer() {
        let (mut s, _fake) = faked(Some(Err("nettop: permission denied".into())));
        s.network.top = Some(parse_top(TOP_JSON).unwrap());
        s.show(Nav::Network);
        s.now = 10.0;
        s.tick_network();
        land(&mut s);
        assert_eq!(
            s.network.error,
            Some(Fail::Failed("nettop: permission denied".into()))
        );
        assert!(s.network.top.is_some(), "the last ranking stays");
        assert_eq!(s.network.next, 10.0 + RETRY_FAILED_SECS);
    }

    #[test]
    fn the_sample_measures_nothing() {
        let mut s = crate::app::init();
        s.network.auto = false;
        s.show(Nav::Network);
        s.tick_network();
        assert!(!s.network.measuring());
    }

    /// The real `spectra network top`, for one short window: `SPECTRA_BIN=... cargo test --
    /// --ignored network_top_live`.
    #[test]
    #[ignore]
    fn network_top_live() {
        std::env::var("SPECTRA_BIN").expect("SPECTRA_BIN");
        let cancel = AtomicBool::new(false);
        let out = Capped.run(&args(2), DEADLINE, &cancel).unwrap();
        parse_top(&out).unwrap();
    }
}
