//! Spectra's daemon: one long-lived connection to its socket, newline-delimited JSON-RPC 2.0,
//! so a reading the daemon already holds costs a message instead of a new `spectra` process.
//!
//! Every Spectra call still goes through [`crate::run::run_until`]; it asks [`answer`] first,
//! and [`route`] decides, in one table, whether the daemon answers that call or the CLI runs.
//! Each entry applies only while `daemon.status` lists its method, so a newer daemon's methods
//! switch on by themselves.
//!
//! The connection is made, and remade after the daemon restarts, on a thread of its own: no call
//! waits on another call's reconnect, a call the daemon won't answer never waits at all, and one
//! it would answer waits at most half its deadline for the connection before the CLI runs it.
//!
//! A generic `exec` method (with `exec.cancel`) that runs any CLI interface inside the daemon
//! would plug in at [`route`]: a call [`TABLE`] doesn't name would go to it once the daemon lists
//! it, mapping its answer's stdout, stderr and exit code onto [`crate::run::Output`]. No daemon
//! lists it yet, so those calls run the CLI.

use std::cell::Cell;
use std::collections::{HashMap, HashSet};
use std::ffi::OsStr;
use std::io::{BufRead, BufReader, Write};
use std::net::Shutdown;
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::mpsc::{Receiver, RecvTimeoutError, Sender, channel};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, OnceLock};
use std::time::{Duration, Instant};

use serde_json::{Value, json};

const STATUS: &str = "daemon.status";
/// Pushes `watch.sample` (and `alerts.event`) notifications on the connection that asked.
const SUBSCRIBE: &str = "alerts.subscribe";
/// The process list from the daemon's short-lived shared snapshot: `{sampled_at, processes}`,
/// the rows `spectra process --json` prints; asked with `classify: true` for the `--classify`
/// fields, which a daemon without that leaves out (or refuses as invalid params), and with
/// `refresh: true` for one taken now rather than the snapshot.
const PROCESS_LIST: &str = "process.list";
/// The host sample: load, CPUs, memory pressure.
const WATCH: &str = "watch.current";
/// The Machine page's readings, in the CLI's `--json` shape.
const MEMORY: &str = "memory.current";
const THERMAL: &str = "thermal.current";
const POWER: &str = "power.current";
const NETWORK: &str = "network.current";
const WHATSWRONG: &str = "whatswrong.current";
/// The alert history: `[alert]`, asked with `{state, limit}`, as `spectra alerts --json` prints.
const ALERTS_LIST: &str = "alerts.list";
/// Acknowledges one alert, `{id}`, and answers it. A write: it only ever runs for an explicit
/// press, and an answer to it that isn't a plain success is never retried through the CLI.
const ALERTS_ACK: &str = "alerts.ack";
/// JSON-RPC's "method not found" and "invalid params": the daemon can't take the call at all,
/// so its route is dropped until the next connection. Any other error is this call's alone.
const NOT_FOUND: i64 = -32601;
const INVALID_PARAMS: i64 = -32602;
/// A pushed host sample younger than this answers a load reading without asking.
const FRESH: Duration = Duration::from_secs(20);
/// How often a waiting call looks at its cancel flag.
const CHECK: Duration = Duration::from_millis(50);

/// How long connecting may take.
#[derive(Clone, Copy, Debug)]
struct Timing {
    /// For the connection to say which methods it has, and to take the subscription.
    hello: Duration,
    /// After the daemon couldn't be reached, how long calls go to the CLI before it's tried again.
    retry: Duration,
    /// For the socket to appear after `spectra daemon start`.
    start_wait: Duration,
}

const TIMING: Timing = Timing {
    hello: Duration::from_secs(3),
    retry: Duration::from_secs(30),
    start_wait: Duration::from_secs(5),
};

#[derive(Clone, Debug, PartialEq)]
pub enum Error {
    /// No daemon to talk to: the socket is missing or refused, it was tried too recently, or it
    /// has nothing to say yet.
    Unavailable(String),
    /// The daemon answered with a JSON-RPC error.
    Rpc {
        code: i64,
        message: String,
    },
    /// The connection dropped before the answer came.
    Disconnected,
    Timeout,
    Cancelled,
    /// The answer wasn't the shape the call expects.
    Shape(String),
    /// The daemon answered but couldn't do all of it, and said why (it couldn't classify the
    /// processes this time): a failed reading, not a reason to stop asking it.
    Warned(String),
}

impl Error {
    /// What went wrong, in words.
    pub fn why(&self) -> String {
        match self {
            Error::Unavailable(why) | Error::Shape(why) | Error::Warned(why) => why.clone(),
            Error::Rpc { code, message } => format!("{message} ({code})"),
            Error::Disconnected => "the connection dropped".into(),
            Error::Timeout => "it didn't answer in time".into(),
            Error::Cancelled => "stopped".into(),
        }
    }
}

type Reply = Result<Value, Error>;
type Pending = Arc<Mutex<HashMap<u64, Sender<Reply>>>>;
type Sample = Arc<Mutex<Option<(Instant, Value)>>>;

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

/// One open connection and the reader thread that answers its calls. Dropping the last handle
/// shuts the socket, so the reader thread ends with it.
struct Conn {
    writer: Mutex<UnixStream>,
    pending: Pending,
    alive: Arc<AtomicBool>,
    /// Lines read so far, to tell a daemon that went silent from one slow at one call.
    heard: Arc<AtomicU64>,
}

impl Conn {
    fn send(
        &self,
        next: &AtomicU64,
        method: &str,
        params: &Value,
    ) -> Result<(u64, Receiver<Reply>), Error> {
        let id = next.fetch_add(1, Ordering::Relaxed) + 1;
        let (tx, rx) = channel();
        lock(&self.pending).insert(id, tx);
        let mut line =
            json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params}).to_string();
        line.push('\n');
        if lock(&self.writer).write_all(line.as_bytes()).is_err() {
            lock(&self.pending).remove(&id);
            return Err(Error::Disconnected);
        }
        Ok((id, rx))
    }

    /// Ends the connection now: the reader sees the end and fails every call still waiting.
    fn close(&self) {
        let _ = lock(&self.writer).shutdown(Shutdown::Both);
    }
}

impl Drop for Conn {
    fn drop(&mut self) {
        self.close();
    }
}

/// Counts a reader thread for as long as it runs.
struct Reader(Arc<AtomicUsize>);

impl Reader {
    fn new(count: &Arc<AtomicUsize>) -> Reader {
        count.fetch_add(1, Ordering::SeqCst);
        Reader(Arc::clone(count))
    }
}

impl Drop for Reader {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}

/// The connection's state, changed only briefly under its lock; connecting happens outside it.
#[derive(Default)]
struct Link {
    conn: Option<Arc<Conn>>,
    /// A connection is being made on its own thread.
    connecting: bool,
    /// When the last attempt failed.
    failed_at: Option<Instant>,
    /// Whether an attempt has finished, so the methods the daemon lists are known.
    tried: bool,
}

/// The connection as a call finds it.
enum Ready {
    Conn(Arc<Conn>),
    /// Being made; `tried` once an earlier attempt finished, so the methods are known.
    Pending {
        tried: bool,
    },
    /// Unreachable, and not tried again yet.
    Down,
}

struct Shared {
    socket: PathBuf,
    /// Starts the daemon when its socket is missing; tried once per run.
    start: Box<dyn Fn() + Send + Sync>,
    started: AtomicBool,
    timing: Timing,
    link: Mutex<Link>,
    /// Told when a connection attempt ends.
    changed: Condvar,
    /// The methods the daemon listed when last connected: read without waiting on a connection.
    methods: Mutex<HashSet<String>>,
    next: AtomicU64,
    /// The last host sample the daemon pushed, and when it came.
    sample: Sample,
    /// Reader threads running.
    readers: Arc<AtomicUsize>,
}

impl Shared {
    /// Makes a connection, on the connecting thread, and says how it went.
    fn reconnect(&self) {
        let r = self.open();
        let mut l = lock(&self.link);
        match r {
            Ok((conn, methods)) => {
                *lock(&self.methods) = methods;
                l.conn = Some(Arc::new(conn));
                l.failed_at = None;
            }
            Err(_) => {
                lock(&self.methods).clear();
                l.failed_at = Some(Instant::now());
            }
        }
        l.tried = true;
        l.connecting = false;
        drop(l);
        self.changed.notify_all();
    }

    /// Dials (starting the daemon once if its socket is missing), asks which methods it has, and
    /// subscribes to the host samples it pushes. Any failure drops the connection, which shuts
    /// the socket and so ends its reader thread.
    fn open(&self) -> Result<(Conn, HashSet<String>), Error> {
        if !self.socket.exists() && !self.started.swap(true, Ordering::SeqCst) {
            (self.start)();
            let since = Instant::now();
            while !self.socket.exists() && since.elapsed() < self.timing.start_wait {
                std::thread::sleep(CHECK);
            }
        }
        let stream = UnixStream::connect(&self.socket)
            .map_err(|e| Error::Unavailable(format!("{}: {e}", self.socket.display())))?;
        let reader = stream
            .try_clone()
            .map_err(|e| Error::Unavailable(e.to_string()))?;
        let conn = Conn {
            writer: Mutex::new(stream),
            pending: Arc::default(),
            alive: Arc::new(AtomicBool::new(true)),
            heard: Arc::default(),
        };
        {
            let (pending, alive, heard, sample) = (
                Arc::clone(&conn.pending),
                Arc::clone(&conn.alive),
                Arc::clone(&conn.heard),
                Arc::clone(&self.sample),
            );
            let counted = Reader::new(&self.readers);
            std::thread::Builder::new()
                .name("spectra-daemon".into())
                .spawn(move || {
                    let _counted = counted;
                    read_loop(reader, &pending, &alive, &heard, &sample);
                })
                .map_err(|e| Error::Unavailable(e.to_string()))?;
        }
        let wait = |rx: Receiver<Reply>| {
            rx.recv_timeout(self.timing.hello)
                .unwrap_or(Err(Error::Timeout))
        };
        let (_, rx) = conn.send(&self.next, STATUS, &Value::Null)?;
        let status = wait(rx)?;
        let mut methods: HashSet<String> = status
            .get("methods")
            .and_then(Value::as_array)
            .map(|a| {
                a.iter()
                    .filter_map(|m| m.as_str().map(str::to_owned))
                    .collect()
            })
            .unwrap_or_default();
        // Without the subscription the load reading asks instead of reading pushes.
        if methods.contains(SUBSCRIBE) {
            let (_, rx) = conn.send(&self.next, SUBSCRIBE, &json!({"samples": true}))?;
            if wait(rx).is_err() {
                methods.remove(SUBSCRIBE);
            }
        }
        Ok((conn, methods))
    }
}

/// A CLI call the daemon can answer, and how.
pub struct Entry {
    /// The CLI call's leading words.
    pub cli: &'static [&'static str],
    /// The daemon method that has to be listed for this entry to apply.
    pub method: &'static str,
    /// Answers the call with what the CLI would have printed.
    answer: fn(&Daemon, &[&str], Duration, &AtomicBool) -> Result<String, Error>,
}

/// The one table of which CLI calls the daemon answers instead of a new process.
pub static TABLE: &[Entry] = &[
    // The main process feed, from the daemon's shared snapshot, asked for on the feed's cadence.
    Entry {
        cli: &["process", "-json", "--classify"],
        method: PROCESS_LIST,
        answer: processes,
    },
    // Every JVM, from the daemon's cached list (a reading runs a JDK tool per JVM).
    Entry {
        cli: &["jvm", "-json"],
        method: "jvm.list",
        answer: jvms,
    },
    // `spectra load` lists every process for a load average the host sample already holds.
    Entry {
        cli: &["load"],
        method: WATCH,
        answer: load,
    },
    // The Machine page's readings, each the same JSON the CLI's `--json` prints.
    Entry {
        cli: &["memory", "--json"],
        method: MEMORY,
        answer: memory,
    },
    Entry {
        cli: &["thermal", "--json"],
        method: THERMAL,
        answer: thermal,
    },
    Entry {
        cli: &["power", "--json"],
        method: POWER,
        answer: power,
    },
    Entry {
        cli: &["network", "--json"],
        method: NETWORK,
        answer: network,
    },
    // The Overview's "Right now", from the daemon's cached look (stale by at most minutes).
    Entry {
        cli: &["whatswrong", "--json"],
        method: WHATSWRONG,
        answer: whatswrong,
    },
    // The Memory and alerts page: the alert history, and acknowledging one alert (a write).
    Entry {
        cli: &["alerts", "--json"],
        method: ALERTS_LIST,
        answer: alert_list,
    },
    Entry {
        cli: &["alerts", "ack"],
        method: ALERTS_ACK,
        answer: alert_ack,
    },
];

/// The daemon's own management, which always runs the CLI.
fn management(args: &[&str]) -> bool {
    matches!(args.first(), None | Some(&"daemon") | Some(&"capabilities"))
}

/// The entry that answers `args`, given the methods the daemon lists; `None` runs the CLI.
pub fn route(args: &[&str], methods: &HashSet<String>) -> Option<&'static Entry> {
    if management(args) {
        return None;
    }
    TABLE
        .iter()
        .find(|e| args.starts_with(e.cli) && methods.contains(e.method))
}

thread_local! {
    /// Set while this thread's process list should be taken now rather than from the snapshot.
    static FRESH_LIST: Cell<bool> = const { Cell::new(false) };
}

/// Runs `f` with any process list it asks the daemon for taken now, not from the shared
/// snapshot: after a Quit or Kill, so the ended process goes from the list at once.
pub fn fresh<T>(f: impl FnOnce() -> T) -> T {
    FRESH_LIST.with(|c| c.set(true));
    let r = f();
    FRESH_LIST.with(|c| c.set(false));
    r
}

pub struct Daemon {
    s: Arc<Shared>,
}

impl Daemon {
    pub fn new(socket: PathBuf, start: Box<dyn Fn() + Send + Sync>) -> Daemon {
        Daemon::with_timing(socket, start, TIMING)
    }

    fn with_timing(socket: PathBuf, start: Box<dyn Fn() + Send + Sync>, timing: Timing) -> Daemon {
        Daemon {
            s: Arc::new(Shared {
                socket,
                start,
                started: AtomicBool::new(false),
                timing,
                link: Mutex::default(),
                changed: Condvar::new(),
                methods: Mutex::default(),
                next: AtomicU64::new(0),
                sample: Arc::default(),
                readers: Arc::default(),
            }),
        }
    }

    /// The connection as it is now, without waiting for one: starts connecting on its own thread
    /// when there's no connection, none is being made, and the last attempt wasn't too recent.
    /// `since` is when the asking call began; an attempt that failed after it is down for it.
    fn link(&self, since: Instant) -> Ready {
        let mut l = lock(&self.s.link);
        if let Some(c) = l.conn.as_ref().filter(|c| c.alive.load(Ordering::SeqCst)) {
            return Ready::Conn(Arc::clone(c));
        }
        l.conn = None;
        if l.connecting {
            return Ready::Pending { tried: l.tried };
        }
        if l.failed_at
            .is_some_and(|at| at >= since || at.elapsed() < self.s.timing.retry)
        {
            return Ready::Down;
        }
        l.connecting = true;
        let tried = l.tried;
        drop(l);
        let s = Arc::clone(&self.s);
        let spawned = std::thread::Builder::new()
            .name("spectra-daemon-connect".into())
            .spawn(move || s.reconnect());
        if spawned.is_err() {
            let mut l = lock(&self.s.link);
            l.connecting = false;
            l.failed_at = Some(Instant::now());
            l.tried = true;
            return Ready::Down;
        }
        Ready::Pending { tried }
    }

    /// Waits up to `limit` for a connection attempt to end.
    fn wait_change(&self, limit: Duration) {
        let l = lock(&self.s.link);
        if l.connecting {
            let _ = self.s.changed.wait_timeout(l, limit);
        }
    }

    /// Drops `conn` if it's still the live one, so the next call reconnects.
    fn forget(&self, conn: &Arc<Conn>) {
        let mut l = lock(&self.s.link);
        if l.conn.as_ref().is_some_and(|c| Arc::ptr_eq(c, conn)) {
            l.conn = None;
        }
        drop(l);
        conn.close();
    }

    #[cfg(test)]
    fn has(&self, method: &str) -> bool {
        lock(&self.s.methods).contains(method)
    }

    /// The last host sample the daemon pushed, if it's fresh and holds a reading.
    pub fn pushed_sample(&self) -> Option<Value> {
        let s = lock(&self.s.sample);
        s.as_ref()
            .filter(|(at, v)| at.elapsed() < FRESH && taken(v))
            .map(|(_, v)| v.clone())
    }

    /// Calls `method` on the live connection; never waits for one to be made.
    pub fn call(
        &self,
        method: &str,
        params: Value,
        deadline: Duration,
        cancel: &AtomicBool,
    ) -> Reply {
        let start = Instant::now();
        let Ready::Conn(conn) = self.link(start) else {
            return Err(Error::Unavailable("not connected to the daemon".into()));
        };
        let heard = conn.heard.load(Ordering::SeqCst);
        let (id, rx) = match conn.send(&self.s.next, method, &params) {
            Ok(sent) => sent,
            Err(e) => {
                self.forget(&conn);
                return Err(e);
            }
        };
        loop {
            if cancel.load(Ordering::Relaxed) {
                lock(&conn.pending).remove(&id);
                return Err(Error::Cancelled);
            }
            let left = deadline.saturating_sub(start.elapsed());
            if left.is_zero() {
                lock(&conn.pending).remove(&id);
                // A daemon that has said nothing at all since is hung: dropped, so the next call
                // reconnects. One still answering other calls is only slow at this one.
                if conn.heard.load(Ordering::SeqCst) == heard {
                    self.forget(&conn);
                }
                return Err(Error::Timeout);
            }
            match rx.recv_timeout(left.min(CHECK)) {
                Ok(r) => return r,
                Err(RecvTimeoutError::Timeout) => {}
                Err(RecvTimeoutError::Disconnected) => return Err(Error::Disconnected),
            }
        }
    }

    /// Answers `args` from the daemon when [`route`] sends it there. `None` runs the CLI: the
    /// table doesn't name the call, the daemon is unreachable or still connecting after half the
    /// deadline, or it doesn't know the call (an older daemon). Any other failure of a listed
    /// method is a failed reading, never a CLI run.
    pub fn answer(
        &self,
        args: &[&str],
        deadline: Duration,
        cancel: &AtomicBool,
    ) -> Option<Result<String, Error>> {
        // A call no daemon method could ever answer runs the CLI at once, even while the first
        // connection is still being made.
        if management(args) || !TABLE.iter().any(|e| args.starts_with(e.cli)) {
            return None;
        }
        let start = Instant::now();
        // The CLI keeps at least the other half if the connection doesn't come.
        let patience = deadline / 2;
        let entry = loop {
            let ready = self.link(start);
            let entry = route(args, &lock(&self.s.methods));
            match ready {
                Ready::Conn(_) => break entry?,
                Ready::Down => return None,
                // The methods are known from the last connection: one it won't answer runs the
                // CLI without waiting.
                Ready::Pending { tried: true } if entry.is_none() => return None,
                Ready::Pending { .. } => {
                    if cancel.load(Ordering::Relaxed) {
                        return Some(Err(Error::Cancelled));
                    }
                    let waited = start.elapsed();
                    if waited >= patience {
                        return None;
                    }
                    self.wait_change((patience - waited).min(CHECK));
                }
            }
        };
        let left = deadline.saturating_sub(start.elapsed());
        match (entry.answer)(self, args, left, cancel) {
            Ok(s) => Some(Ok(s)),
            Err(e @ (Error::Cancelled | Error::Timeout | Error::Warned(_))) => Some(Err(e)),
            // A write the daemon may have taken is never run a second time through the CLI.
            Err(e) if entry.method == ALERTS_ACK && !matches!(e, Error::Unavailable(_)) => {
                Some(Err(e))
            }
            // A daemon that lists the method but can't take this call the CLI's way (an older one
            // that doesn't classify processes) isn't asked again until it reconnects: the CLI
            // runs instead of a wasted round trip every reading.
            Err(
                Error::Shape(_)
                | Error::Rpc {
                    code: NOT_FOUND | INVALID_PARAMS,
                    ..
                },
            ) => {
                lock(&self.s.methods).remove(entry.method);
                None
            }
            // The connection went away before the call was sent: no daemon to ask, so the CLI.
            Err(Error::Unavailable(_)) => None,
            // Anything else (its own collection timing out on a busy Mac, a dropped connection) is
            // a failed reading from a daemon that has the method: it's asked again next time, and
            // no process is started for it.
            Err(e) => Some(Err(e)),
        }
    }
}

/// `{sampled_at, processes}` as rows as JSON. Rows without a role weren't classified, which the
/// process feed needs: the error carries the daemon's `warning` when it said why (on the answer
/// or its rows), and is `None` for a daemon that doesn't classify at all.
fn process_rows(mut v: Value) -> Result<String, Option<String>> {
    let warning = |v: &Value| {
        v.get("warning")
            .and_then(Value::as_str)
            .filter(|w| !w.is_empty())
            .map(str::to_owned)
    };
    let top = warning(&v);
    let rows = v.get_mut("processes").map(Value::take).ok_or(None)?;
    let list = rows.as_array().ok_or(None)?;
    if list.is_empty() || list.iter().all(|r| r.get("role").is_none()) {
        return Err(top.or_else(|| list.iter().find_map(warning)));
    }
    Ok(rows.to_string())
}

/// The main process feed: `spectra process -json --classify`'s rows, from the daemon's shared
/// snapshot, or taken now inside [`fresh`].
fn processes(
    d: &Daemon,
    _: &[&str],
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    let start = Instant::now();
    let fresh = FRESH_LIST.with(Cell::get);
    let params = if fresh {
        json!({"classify": true, "refresh": true})
    } else {
        json!({"classify": true})
    };
    let v = match d.call(PROCESS_LIST, params, deadline, cancel) {
        // A daemon that doesn't take `refresh` gives its snapshot instead.
        Err(Error::Rpc {
            code: INVALID_PARAMS,
            ..
        }) if fresh => d.call(
            PROCESS_LIST,
            json!({"classify": true}),
            deadline.saturating_sub(start.elapsed()),
            cancel,
        )?,
        r => r?,
    };
    process_rows(v).map_err(|warning| match warning {
        Some(why) => Error::Warned(why),
        None => Error::Shape("process.list had no classified processes".into()),
    })
}

/// `spectra jvm -json`'s list.
fn jvms(d: &Daemon, _: &[&str], deadline: Duration, cancel: &AtomicBool) -> Result<String, Error> {
    match d.call("jvm.list", json!({}), deadline, cancel)? {
        v @ Value::Array(_) => Ok(v.to_string()),
        Value::Null => Ok("[]".into()),
        Value::Object(mut o) => match o.remove("jvms") {
            Some(v @ Value::Array(_)) => Ok(v.to_string()),
            _ => Err(Error::Shape("jvm.list had no list".into())),
        },
        _ => Err(Error::Shape("jvm.list had no list".into())),
    }
}

/// Whether a host sample holds a reading: until the daemon takes its first, a few seconds after
/// it starts, `watch.current` answers zeros (no CPUs, a zero time).
fn taken(s: &Value) -> bool {
    s.get("ncpu").and_then(Value::as_u64).is_some_and(|n| n > 0)
        && s.get("at")
            .and_then(Value::as_str)
            .is_some_and(|at| !at.is_empty() && !at.starts_with("0001-01-01"))
}

/// The host sample: the last one pushed, else asked for. One not taken yet is unavailable, so
/// the CLI reads this one and the daemon is asked again next time.
fn sample(d: &Daemon, deadline: Duration, cancel: &AtomicBool) -> Result<Value, Error> {
    if let Some(s) = d.pushed_sample() {
        return Ok(s);
    }
    let mut v = d.call(WATCH, Value::Null, deadline, cancel)?;
    match v.get_mut("sample").map(Value::take) {
        Some(s) if taken(&s) => Ok(s),
        Some(s) if s.is_object() => Err(Error::Unavailable(
            "the daemon hasn't taken a host sample yet".into(),
        )),
        _ => Err(Error::Shape("watch.current had no sample".into())),
    }
}

/// `spectra load --json`'s numbers, from the host sample.
fn load(d: &Daemon, _: &[&str], deadline: Duration, cancel: &AtomicBool) -> Result<String, Error> {
    let s = sample(d, deadline, cancel)?;
    let load1 = s
        .get("load1")
        .and_then(Value::as_f64)
        .ok_or_else(|| Error::Shape("sample had no load1".into()))?;
    let cpus = s.get("ncpu").and_then(Value::as_u64).unwrap_or(0);
    Ok(json!({"load1": load1, "cpus": cpus}).to_string())
}

/// A reading the daemon answers in the CLI's `--json` shape, passed on as it came.
fn reading(
    d: &Daemon,
    method: &str,
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    match d.call(method, json!({}), deadline, cancel)? {
        v @ Value::Object(_) => Ok(v.to_string()),
        _ => Err(Error::Shape(format!("{method} had no reading"))),
    }
}

/// `spectra memory --json`.
fn memory(
    d: &Daemon,
    _: &[&str],
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    reading(d, MEMORY, deadline, cancel)
}

/// `spectra thermal --json`.
fn thermal(
    d: &Daemon,
    _: &[&str],
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    reading(d, THERMAL, deadline, cancel)
}

/// `spectra power --json`.
fn power(d: &Daemon, _: &[&str], deadline: Duration, cancel: &AtomicBool) -> Result<String, Error> {
    reading(d, POWER, deadline, cancel)
}

/// `spectra network --json`.
fn network(
    d: &Daemon,
    _: &[&str],
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    reading(d, NETWORK, deadline, cancel)
}

/// `spectra whatswrong --json`.
fn whatswrong(
    d: &Daemon,
    _: &[&str],
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    reading(d, WHATSWRONG, deadline, cancel)
}

/// The value after `flag` in `args`.
fn flag_value<'a>(args: &[&'a str], flag: &str) -> Option<&'a str> {
    args.iter()
        .position(|a| *a == flag)
        .and_then(|i| args.get(i + 1))
        .copied()
}

/// `spectra alerts --json [--state S] [--limit N]`: the alert history.
fn alert_list(
    d: &Daemon,
    args: &[&str],
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    let mut params = serde_json::Map::new();
    params.insert(
        "state".into(),
        json!(flag_value(args, "--state").unwrap_or("firing")),
    );
    if let Some(n) = flag_value(args, "--limit").and_then(|n| n.parse::<u32>().ok()) {
        params.insert("limit".into(), json!(n));
    }
    match d.call(ALERTS_LIST, Value::Object(params), deadline, cancel)? {
        v @ Value::Array(_) => Ok(v.to_string()),
        Value::Null => Ok("[]".into()),
        _ => Err(Error::Shape("alerts.list had no list".into())),
    }
}

/// `spectra alerts ack <id> --json`: the acknowledged alert.
fn alert_ack(
    d: &Daemon,
    args: &[&str],
    deadline: Duration,
    cancel: &AtomicBool,
) -> Result<String, Error> {
    let id = args
        .get(2)
        .filter(|id| !id.starts_with('-'))
        .ok_or_else(|| Error::Shape("alerts ack had no id".into()))?;
    match d.call(ALERTS_ACK, json!({"id": id}), deadline, cancel)? {
        v @ Value::Object(_) => Ok(v.to_string()),
        _ => Err(Error::Shape("alerts.ack had no alert".into())),
    }
}

fn read_loop(
    stream: UnixStream,
    pending: &Pending,
    alive: &AtomicBool,
    heard: &AtomicU64,
    sample: &Sample,
) {
    for line in BufReader::new(stream).lines() {
        let Ok(line) = line else { break };
        let Ok(mut v) = serde_json::from_str::<Value>(&line) else {
            continue;
        };
        if let Some(id) = v.get("id").and_then(Value::as_u64) {
            // Only replies count as the daemon answering: pushed samples keep coming from one
            // that has stopped answering calls.
            heard.fetch_add(1, Ordering::SeqCst);
            let reply = match v.get_mut("error").map(Value::take) {
                Some(e) if !e.is_null() => Err(Error::Rpc {
                    code: e.get("code").and_then(Value::as_i64).unwrap_or(0),
                    message: e
                        .get("message")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned(),
                }),
                _ => Ok(v.get_mut("result").map(Value::take).unwrap_or(Value::Null)),
            };
            if let Some(tx) = lock(pending).remove(&id) {
                let _ = tx.send(reply);
            }
            continue;
        }
        if v.get("method").and_then(Value::as_str) == Some("watch.sample") {
            let params = v.get_mut("params").map(Value::take).unwrap_or(Value::Null);
            *lock(sample) = Some((Instant::now(), params));
        }
    }
    alive.store(false, Ordering::SeqCst);
    for (_, tx) in lock(pending).drain() {
        let _ = tx.send(Err(Error::Disconnected));
    }
}

fn bin() -> String {
    std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into())
}

/// Where the daemon listens: `$SPECTRA_DAEMON_DIR`, else `$XDG_RUNTIME_DIR/spectra` on Linux,
/// else `~/.spectra`, as Spectra itself decides.
pub fn socket_path() -> Option<PathBuf> {
    let dir = match std::env::var_os("SPECTRA_DAEMON_DIR").filter(|d| !d.is_empty()) {
        Some(d) => PathBuf::from(d),
        None => match std::env::var_os("XDG_RUNTIME_DIR")
            .filter(|d| cfg!(target_os = "linux") && !d.is_empty())
        {
            Some(d) => Path::new(&d).join("spectra"),
            None => Path::new(&std::env::var_os("HOME")?).join(".spectra"),
        },
    };
    Some(dir.join("daemon.sock"))
}

/// Runs `spectra daemon start` and waits a few seconds for it to return; never kills it, since
/// what it starts is the daemon.
fn start_daemon() {
    let Ok(mut child) = Command::new(bin())
        .args(["daemon", "start"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
    else {
        return;
    };
    let since = Instant::now();
    while since.elapsed() < TIMING.start_wait {
        if !matches!(child.try_wait(), Ok(None)) {
            return;
        }
        std::thread::sleep(CHECK);
    }
}

static DAEMON: OnceLock<Daemon> = OnceLock::new();

/// Sends the Spectra calls the daemon can answer to it from now on. Connects on the first call.
pub fn install() {
    if let Some(socket) = socket_path() {
        let _ = DAEMON.set(Daemon::new(socket, Box::new(start_daemon)));
    }
}

/// The daemon's answer for `cmd`, when it's a Spectra call the daemon takes. `None` runs `cmd`.
pub fn answer(
    cmd: &Command,
    deadline: Duration,
    cancel: &AtomicBool,
) -> Option<Result<String, Error>> {
    answer_with(DAEMON.get()?, cmd, deadline, cancel)
}

/// A call with its own environment (the disk reading's empty home) always runs the CLI.
fn answer_with(
    d: &Daemon,
    cmd: &Command,
    deadline: Duration,
    cancel: &AtomicBool,
) -> Option<Result<String, Error>> {
    if cmd.get_program() != OsStr::new(&bin()) || cmd.get_envs().next().is_some() {
        return None;
    }
    let args: Vec<&str> = cmd.get_args().map(OsStr::to_str).collect::<Option<_>>()?;
    d.answer(&args, deadline, cancel)
}

#[cfg(test)]
#[path = "daemon_tests.rs"]
mod tests;
