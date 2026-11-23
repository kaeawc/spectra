//! The daemon transport against a fake daemon on a socket in a temporary folder.

use super::*;
use std::os::unix::net::UnixListener;
use std::sync::atomic::AtomicUsize;

type Handler = Arc<dyn Fn(&str, &Value) -> Option<Result<Value, (i64, String)>> + Send + Sync>;

/// A fake daemon: answers `daemon.status` with `methods`, everything else with `handler`
/// (`None` never answers), records each request, and can push notifications.
struct Fake {
    path: PathBuf,
    calls: Arc<Mutex<Vec<(String, Value)>>>,
    conns: Arc<Mutex<Vec<UnixStream>>>,
    /// Connections the fake still has open: accepted and not yet closed by the app.
    open: Arc<AtomicUsize>,
}

/// A handler, typed so its closure takes any borrow.
fn h(
    f: impl Fn(&str, &Value) -> Option<Result<Value, (i64, String)>> + Send + Sync + 'static,
) -> Handler {
    Arc::new(f)
}

static DIRS: AtomicUsize = AtomicUsize::new(0);

fn socket() -> PathBuf {
    let n = DIRS.fetch_add(1, Ordering::SeqCst);
    let dir = std::env::temp_dir().join(format!("sdd-{}-{n}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    dir.join("d.sock")
}

impl Fake {
    fn serve(methods: &[&str], handler: Handler) -> Fake {
        Fake::serve_at(socket(), methods, handler)
    }

    fn serve_at(path: PathBuf, methods: &[&str], handler: Handler) -> Fake {
        Fake::serve_any(path, Some(methods), handler)
    }

    /// A daemon that takes connections and never answers anything, not even hello.
    fn silent() -> Fake {
        Fake::serve_any(socket(), None, h(|_, _| None))
    }

    fn serve_any(path: PathBuf, methods: Option<&[&str]>, handler: Handler) -> Fake {
        let _ = std::fs::remove_file(&path);
        let listener = UnixListener::bind(&path).unwrap();
        let methods: Option<Vec<String>> =
            methods.map(|m| m.iter().map(|m| (*m).to_owned()).collect());
        let (calls, conns) = (
            Arc::new(Mutex::new(Vec::new())),
            Arc::new(Mutex::new(Vec::new())),
        );
        let open = Arc::new(AtomicUsize::new(0));
        let (c, k, o) = (Arc::clone(&calls), Arc::clone(&conns), Arc::clone(&open));
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let Ok(stream) = stream else { return };
                lock(&k).push(stream.try_clone().unwrap());
                o.fetch_add(1, Ordering::SeqCst);
                let (c, handler, methods, o) = (
                    Arc::clone(&c),
                    Arc::clone(&handler),
                    methods.clone(),
                    Arc::clone(&o),
                );
                std::thread::spawn(move || {
                    // Counted closed once the app's end of it closes.
                    struct Closed(Arc<AtomicUsize>);
                    impl Drop for Closed {
                        fn drop(&mut self) {
                            self.0.fetch_sub(1, Ordering::SeqCst);
                        }
                    }
                    let _closed = Closed(o);
                    let mut out = stream.try_clone().unwrap();
                    for line in BufReader::new(stream).lines() {
                        let Ok(line) = line else { return };
                        let v: Value = serde_json::from_str(&line).unwrap();
                        let (id, method) = (
                            v["id"].clone(),
                            v["method"].as_str().unwrap_or_default().to_owned(),
                        );
                        lock(&c).push((method.clone(), v["params"].clone()));
                        let reply = if method == "daemon.status" {
                            methods.as_ref().map(|m| Ok(json!({"methods": m})))
                        } else {
                            handler(&method, &v["params"])
                        };
                        let msg = match reply {
                            None => continue,
                            Some(Ok(r)) => json!({"jsonrpc": "2.0", "id": id, "result": r}),
                            Some(Err((code, m))) => {
                                json!({"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": m}})
                            }
                        };
                        if writeln!(out, "{msg}").is_err() {
                            return;
                        }
                    }
                });
            }
        });
        Fake {
            path,
            calls,
            conns,
            open,
        }
    }

    fn calls(&self, method: &str) -> Vec<Value> {
        lock(&self.calls)
            .iter()
            .filter(|(m, _)| m == method)
            .map(|(_, p)| p.clone())
            .collect()
    }

    /// Pushes a notification on every open connection.
    fn push(&self, method: &str, params: Value) {
        for c in lock(&self.conns).iter_mut() {
            let _ = writeln!(
                c,
                "{}",
                json!({"jsonrpc": "2.0", "method": method, "params": params})
            );
        }
    }

    /// The daemon going away: its connections close and its socket goes.
    fn stop(&self) {
        for c in lock(&self.conns).drain(..) {
            let _ = c.shutdown(std::net::Shutdown::Both);
        }
        let _ = std::fs::remove_file(&self.path);
    }

    fn daemon(&self) -> Daemon {
        Daemon::new(self.path.clone(), Box::new(|| {}))
    }

    fn daemon_with(&self, timing: Timing) -> Daemon {
        Daemon::with_timing(self.path.clone(), Box::new(|| {}), timing)
    }
}

/// Waits for `cond`, up to two seconds.
fn eventually(cond: impl Fn() -> bool) -> bool {
    let since = Instant::now();
    while !cond() {
        if since.elapsed() > SEC {
            return false;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    true
}

/// Connects `d`, waiting for it.
fn connect(d: &Daemon) {
    let since = Instant::now();
    assert!(
        eventually(|| matches!(d.link(since), Ready::Conn(_))),
        "never connected"
    );
}

fn never() -> AtomicBool {
    AtomicBool::new(false)
}

const SEC: Duration = Duration::from_secs(2);

fn current(load1: f64) -> Value {
    json!({"sample": {"at": "2026-11-22T00:00:00Z", "load1": load1, "ncpu": 16, "memory_pressure": "normal"}, "active_alerts": []})
}

fn set(methods: &[&str]) -> HashSet<String> {
    methods.iter().map(|m| (*m).to_owned()).collect()
}

fn routed(args: &[&str], methods: &[&str]) -> &'static str {
    route(args, &set(methods)).map_or("cli", |e| e.method)
}

#[test]
fn the_table_routes_a_call_to_the_daemon_only_while_it_lists_the_method() {
    let all = ["watch.current", "process.list", "jvm.list"];
    assert_eq!(
        routed(&["load", "--json", "--top", "1"], &all),
        "watch.current"
    );
    assert_eq!(
        routed(&["process", "-json", "--classify"], &all),
        "process.list"
    );
    assert_eq!(routed(&["jvm", "-json"], &all), "jvm.list");
    // An older daemon without the method: the CLI runs.
    assert_eq!(
        routed(&["process", "-json", "--classify"], &["watch.current"]),
        "cli"
    );
    assert_eq!(routed(&["load", "--json"], &[]), "cli");
    // A call the table doesn't name.
    assert_eq!(routed(&["memory", "--json"], &all), "cli");
    // An unclassified process list isn't what the feed asks for.
    assert_eq!(routed(&["process", "-json"], &all), "cli");
}

#[test]
fn no_call_goes_to_a_generic_exec_yet() {
    // A daemon listing `exec` still has every call the table doesn't name run the CLI.
    let m = ["exec", "exec.cancel", "watch.current"];
    assert_eq!(routed(&["owner", "--json", "7"], &m), "cli");
    assert_eq!(routed(&["memory", "--json"], &m), "cli");
    assert_eq!(routed(&["load", "--json"], &m), "watch.current");
    assert_eq!(routed(&["daemon", "start"], &m), "cli");
    assert_eq!(routed(&["capabilities", "--json"], &m), "cli");
}

#[test]
fn load_comes_from_the_host_sample_in_the_shape_the_cli_prints() {
    let f = Fake::serve(
        &["watch.current"],
        h(|m, _| (m == "watch.current").then(|| Ok(current(3.5)))),
    );
    let d = f.daemon();
    let out = d
        .answer(&["load", "--json", "--top", "1"], SEC, &never())
        .unwrap()
        .unwrap();
    let load = crate::system::parse_load(&out).unwrap();
    assert_eq!((load.load1, load.cpus), (3.5, 16));
}

#[test]
fn a_method_the_daemon_lacks_or_refuses_runs_the_cli() {
    let f = Fake::serve(&["daemon.status"], h(|_, _| Some(Ok(Value::Null))));
    assert!(
        f.daemon()
            .answer(&["load", "--json"], SEC, &never())
            .is_none()
    );
    let f = Fake::serve(
        &["watch.current"],
        h(|_, _| Some(Err((-32601, "method not found".into())))),
    );
    assert!(
        f.daemon()
            .answer(&["load", "--json"], SEC, &never())
            .is_none()
    );
}

#[test]
fn no_daemon_runs_the_cli_and_starts_the_daemon_once() {
    let started = Arc::new(AtomicUsize::new(0));
    let s = Arc::clone(&started);
    let d = Daemon::with_timing(
        socket(),
        Box::new(move || {
            s.fetch_add(1, Ordering::SeqCst);
        }),
        Timing {
            start_wait: Duration::from_millis(200),
            ..TIMING
        },
    );
    // The start wait passes with no socket; the next call doesn't try again for a while.
    assert!(d.answer(&["load", "--json"], SEC, &never()).is_none());
    let at = Instant::now();
    assert!(d.answer(&["load", "--json"], SEC, &never()).is_none());
    assert!(
        at.elapsed() < Duration::from_millis(500),
        "a down daemon isn't dialled on every call"
    );
    assert_eq!(started.load(Ordering::SeqCst), 1);
}

#[test]
fn reconnects_after_the_daemon_restarts() {
    let handler = h(|m, _| (m == "watch.current").then(|| Ok(current(1.0))));
    let f = Fake::serve(&["watch.current"], Arc::clone(&handler));
    let d = f.daemon();
    assert!(
        d.answer(&["load", "--json"], SEC, &never())
            .unwrap()
            .is_ok()
    );
    f.stop();
    std::thread::sleep(Duration::from_millis(100));
    let g = Fake::serve_at(f.path.clone(), &["watch.current"], handler);
    let out = d
        .answer(&["load", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    assert!(out.contains("\"load1\":1.0"), "{out}");
    assert_eq!(
        g.calls("daemon.status").len(),
        1,
        "it said hello to the new daemon"
    );
}

#[test]
fn a_daemon_that_does_not_answer_runs_out_of_time() {
    let f = Fake::serve(&["watch.current"], h(|_, _| None));
    let d = f.daemon();
    let at = Instant::now();
    let r = d.answer(&["load", "--json"], Duration::from_millis(300), &never());
    assert_eq!(r, Some(Err(Error::Timeout)));
    assert!(at.elapsed() < Duration::from_secs(2));
}

#[test]
fn a_cancelled_call_stops_waiting() {
    let f = Fake::serve(&["watch.current"], h(|_, _| None));
    let d = f.daemon();
    let cancel = AtomicBool::new(true);
    assert_eq!(
        d.answer(&["load", "--json"], Duration::from_secs(10), &cancel),
        Some(Err(Error::Cancelled))
    );
}

#[test]
fn a_pushed_host_sample_answers_without_asking() {
    let f = Fake::serve(
        &["watch.current", "alerts.subscribe"],
        h(|m, _| match m {
            "alerts.subscribe" => Some(Ok(json!({"subscribed": true}))),
            _ => Some(Ok(current(9.0))),
        }),
    );
    let d = f.daemon();
    connect(&d);
    assert_eq!(f.calls("alerts.subscribe"), vec![json!({"samples": true})]);
    f.push(
        "watch.sample",
        json!({"at": "2026-11-22T00:00:00Z", "load1": 7.25, "ncpu": 8}),
    );
    let since = Instant::now();
    while d.pushed_sample().is_none() && since.elapsed() < SEC {
        std::thread::sleep(Duration::from_millis(10));
    }
    let out = d
        .answer(&["load", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    assert!(out.contains("7.25"), "{out}");
    assert!(f.calls("watch.current").is_empty(), "the push answered it");
}

fn rows(at: &str, pid: u32) -> Value {
    json!({"sampled_at": at, "processes": [{"pid": pid, "command": "x", "role": "app", "runtime": "native"}]})
}

#[test]
fn the_process_feed_comes_from_the_shared_snapshot() {
    let f = Fake::serve(
        &["process.list"],
        h(|_, _| Some(Ok(rows("2026-11-22T00:00:00Z", 42)))),
    );
    let out = f
        .daemon()
        .answer(&["process", "-json", "--classify"], SEC, &never())
        .unwrap()
        .unwrap();
    let procs = crate::procs::parse(&out).unwrap();
    assert_eq!(procs[0].pid, 42);
}

#[test]
fn the_process_feed_asks_for_classified_rows_and_never_subscribes() {
    let f = Fake::serve(
        &["process.list", "process.subscribe"],
        h(|m, _| match m {
            "process.subscribe" => Some(Ok(json!({"subscribed": true}))),
            _ => Some(Ok(rows("t", 1))),
        }),
    );
    let d = f.daemon();
    let args = ["process", "-json", "--classify"];
    d.answer(&args, SEC, &never()).unwrap().unwrap();
    // Each reading asks; the same snapshot asked for twice is answered twice, at once.
    let at = Instant::now();
    d.answer(&args, SEC, &never()).unwrap().unwrap();
    assert!(
        at.elapsed() < Duration::from_millis(500),
        "it didn't wait for a push"
    );
    assert!(f.calls("process.subscribe").is_empty());
    assert_eq!(
        f.calls("process.list"),
        vec![json!({"classify": true}), json!({"classify": true})]
    );
}

#[test]
fn after_a_quit_the_list_is_taken_now() {
    let f = Fake::serve(&["process.list"], h(|_, _| Some(Ok(rows("t", 1)))));
    let d = f.daemon();
    let args = ["process", "-json", "--classify"];
    fresh(|| d.answer(&args, SEC, &never())).unwrap().unwrap();
    d.answer(&args, SEC, &never()).unwrap().unwrap();
    assert_eq!(
        f.calls("process.list"),
        vec![
            json!({"classify": true, "refresh": true}),
            json!({"classify": true})
        ]
    );
    // A daemon that refuses `refresh` gives its snapshot, and keeps the route.
    let f = Fake::serve(
        &["process.list"],
        h(|_, p| {
            Some(if p.get("refresh").is_some() {
                Err((-32602, "invalid params".into()))
            } else {
                Ok(rows("t", 2))
            })
        }),
    );
    let d = f.daemon();
    let out = fresh(|| d.answer(&args, SEC, &never())).unwrap().unwrap();
    assert!(out.contains("\"pid\":2"), "{out}");
    assert!(d.has("process.list"));
}

#[test]
fn a_daemon_that_does_not_classify_leaves_the_feed_to_the_cli() {
    // It ignores the flag: rows without roles.
    let f = Fake::serve(
        &["process.list"],
        h(|_, _| {
            Some(Ok(
                json!({"sampled_at": "t", "processes": [{"pid": 1, "command": "x"}]}),
            ))
        }),
    );
    let d = f.daemon();
    let args = ["process", "-json", "--classify"];
    assert!(d.answer(&args, SEC, &never()).is_none());
    assert!(d.answer(&args, SEC, &never()).is_none());
    assert_eq!(
        f.calls("process.list").len(),
        1,
        "not asked again on every reading"
    );
    // It refuses the flag as invalid params.
    let f = Fake::serve(
        &["process.list"],
        h(|_, p| (p.get("classify").is_some()).then(|| Err((-32602, "invalid params".into())))),
    );
    let d = f.daemon();
    assert!(d.answer(&args, SEC, &never()).is_none());
    assert!(!d.has("process.list"));
}

#[test]
fn a_classification_that_failed_is_a_failed_reading_not_a_reason_to_stop() {
    let f = Fake::serve(
        &["process.list"],
        h(|_, _| {
            Some(Ok(
                json!({"sampled_at": "t", "warning": "classify: timed out",
                "processes": [{"pid": 1, "command": "x"}]}),
            ))
        }),
    );
    let d = f.daemon();
    let args = ["process", "-json", "--classify"];
    assert_eq!(
        d.answer(&args, SEC, &never()),
        Some(Err(Error::Warned("classify: timed out".into())))
    );
    assert!(d.has("process.list"), "it's asked again next time");
}

#[test]
fn the_jvm_list_is_read_in_either_shape() {
    let f = Fake::serve(
        &["jvm.list"],
        h(|_, _| Some(Ok(json!([{"pid": 5, "main_class": "a.B"}])))),
    );
    let out = f
        .daemon()
        .answer(&["jvm", "-json"], SEC, &never())
        .unwrap()
        .unwrap();
    assert_eq!(crate::data::parse(&out).unwrap()[0].pid, 5);
    let f = Fake::serve(
        &["jvm.list"],
        h(|_, _| Some(Ok(json!({"jvms": [{"pid": 6}]})))),
    );
    let out = f
        .daemon()
        .answer(&["jvm", "-json"], SEC, &never())
        .unwrap()
        .unwrap();
    assert_eq!(crate::data::parse(&out).unwrap()[0].pid, 6);
    let f = Fake::serve(&["jvm.list"], h(|_, _| Some(Ok(Value::Null))));
    assert_eq!(
        f.daemon().answer(&["jvm", "-json"], SEC, &never()),
        Some(Ok("[]".into()))
    );
}

#[test]
fn only_plain_spectra_calls_are_routed() {
    let f = Fake::serve(&["watch.current"], h(|_, _| Some(Ok(current(2.0)))));
    let d = f.daemon();
    let mut cmd = Command::new(bin());
    cmd.args(["load", "--json"]);
    assert!(answer_with(&d, &cmd, SEC, &never()).is_some());
    let mut other = Command::new("/bin/echo");
    other.args(["load", "--json"]);
    assert!(answer_with(&d, &other, SEC, &never()).is_none());
    let mut with_env = Command::new(bin());
    with_env.args(["load", "--json"]).env("HOME", "/var/empty");
    assert!(answer_with(&d, &with_env, SEC, &never()).is_none());
}

/// A fake daemon that lists `method` and answers it with `reading`.
fn reading_daemon(method: &'static str, reading: Value) -> Fake {
    Fake::serve(
        &[method],
        h(move |m, _| (m == method).then(|| Ok(reading.clone()))),
    )
}

#[test]
fn the_machine_readings_route_to_the_daemon_only_while_it_lists_them() {
    let all = [
        "memory.current",
        "thermal.current",
        "power.current",
        "network.current",
    ];
    for (args, method) in [
        (["memory", "--json"], "memory.current"),
        (["thermal", "--json"], "thermal.current"),
        (["power", "--json"], "power.current"),
        (["network", "--json"], "network.current"),
    ] {
        assert_eq!(routed(&args, &all), method);
        assert_eq!(routed(&args, &[method]), method);
        assert_eq!(routed(&args, &[]), "cli", "{method} unlisted runs the CLI");
        assert_eq!(routed(&args, &["exec"]), "cli");
    }
    // The Network page's windows are another call.
    assert_eq!(
        routed(&["network", "top", "--json", "--duration", "3s"], &all),
        "cli"
    );
}

#[test]
fn memory_comes_from_the_daemon_in_the_shape_the_cli_prints() {
    let f = reading_daemon(
        "memory.current",
        json!({"physical_bytes": 17179869184u64, "pressure_level": "warn", "swap": {"used_bytes": 1024}}),
    );
    let out = f
        .daemon()
        .answer(&["memory", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    let m = crate::system::parse_memory(&out).unwrap();
    assert_eq!((m.physical, m.swap_used), (17179869184.0, 1024.0));
    assert_eq!(f.calls("memory.current"), vec![json!({})]);
}

#[test]
fn thermal_comes_from_the_daemon_in_the_shape_the_cli_prints() {
    let f = reading_daemon(
        "thermal.current",
        json!({"verdict": "throttled", "evidence": "speed limit 70%", "limits": {"cpu_speed_limit": 70}}),
    );
    let out = f
        .daemon()
        .answer(&["thermal", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    let t = crate::system::parse_thermal(&out).unwrap();
    assert_eq!(t.state, crate::system::ThermalState::Throttled);
    assert_eq!(t.speed_limit, Some(70.0));
}

#[test]
fn power_comes_from_the_daemon_in_the_shape_the_cli_prints() {
    let f = reading_daemon(
        "power.current",
        json!({"battery": {"pct": 42, "on_battery": true}, "thermal": {"pressure": "nominal"}}),
    );
    let out = f
        .daemon()
        .answer(&["power", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    let p = crate::system::parse_power(&out).unwrap();
    assert_eq!(
        p.battery.map(|b| (b.pct, b.on_battery)),
        Some((Some(42.0), true))
    );
    assert_eq!(p.thermal_pressure.as_deref(), Some("nominal"));
}

#[test]
fn network_comes_from_the_daemon_in_the_shape_the_cli_prints() {
    let f = reading_daemon(
        "network.current",
        json!({"listening_ports": [{"port": 8080, "proto": "tcp", "pid": 9, "local_addr": "127.0.0.1"}]}),
    );
    let out = f
        .daemon()
        .answer(&["network", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    let n = crate::system::parse_network(&out).unwrap();
    assert_eq!(
        n.ports.iter().map(|p| p.port).collect::<Vec<_>>(),
        vec![8080]
    );
}

#[test]
fn a_machine_reading_the_daemon_does_not_list_or_garbles_runs_the_cli() {
    // Listed but not this one: the CLI, and the daemon is never asked for it.
    let f = reading_daemon("memory.current", json!({"physical_bytes": 1}));
    let d = f.daemon();
    for args in [
        ["thermal", "--json"],
        ["power", "--json"],
        ["network", "--json"],
    ] {
        assert!(d.answer(&args, SEC, &never()).is_none(), "{args:?}");
    }
    assert!(f.calls("thermal.current").is_empty());
    // Listed, but its answer isn't a reading: the CLI, and the method isn't asked again.
    let f = reading_daemon("power.current", Value::Null);
    let d = f.daemon();
    assert!(d.answer(&["power", "--json"], SEC, &never()).is_none());
    assert!(d.answer(&["power", "--json"], SEC, &never()).is_none());
    assert_eq!(f.calls("power.current").len(), 1);
}

#[test]
fn one_failed_call_does_not_take_the_route_away() {
    // A transient error (the daemon's own internal error) is a failed reading: no CLI run, and
    // the daemon is asked again next time.
    let failed = Arc::new(AtomicBool::new(false));
    let once = Arc::clone(&failed);
    let f = Fake::serve(
        &["memory.current"],
        h(move |_, _| {
            Some(if once.swap(true, Ordering::SeqCst) {
                Ok(json!({"physical_bytes": 1}))
            } else {
                Err((-32000, "collector busy".into()))
            })
        }),
    );
    let d = f.daemon();
    let args = ["memory", "--json"];
    assert_eq!(
        d.answer(&args, SEC, &never()),
        Some(Err(Error::Rpc {
            code: -32000,
            message: "collector busy".into()
        })),
        "a failed reading, not a CLI run"
    );
    assert!(d.has("memory.current"));
    assert!(d.answer(&args, SEC, &never()).unwrap().is_ok());
    assert_eq!(f.calls("memory.current").len(), 2);
    // Method not found does take it away, until the next connection.
    let f = Fake::serve(
        &["memory.current"],
        h(|_, _| Some(Err((-32601, "method not found".into())))),
    );
    let d = f.daemon();
    assert!(d.answer(&args, SEC, &never()).is_none());
    assert!(d.answer(&args, SEC, &never()).is_none());
    assert_eq!(f.calls("memory.current").len(), 1);
}

#[test]
fn a_host_sample_not_taken_yet_runs_the_cli_and_asks_again() {
    // Before its first sample the daemon answers zeros.
    let zero = json!({"sample": {"at": "0001-01-01T00:00:00Z", "load1": 0.0, "ncpu": 0}, "active_alerts": []});
    let f = reading_daemon("watch.current", zero);
    let d = f.daemon();
    assert!(d.answer(&["load", "--json"], SEC, &never()).is_none());
    assert!(d.answer(&["load", "--json"], SEC, &never()).is_none());
    assert_eq!(f.calls("watch.current").len(), 2, "asked again each time");
    assert!(d.has("watch.current"));
    // CPUs but a zero time, or no time at all, is no sample either.
    for sample in [
        json!({"at": "0001-01-01T00:00:00Z", "load1": 1.0, "ncpu": 8}),
        json!({"load1": 1.0, "ncpu": 8}),
        json!({"at": "2026-11-22T00:00:00Z", "load1": 1.0, "ncpu": 0}),
    ] {
        assert!(!taken(&sample), "{sample}");
    }
    // A pushed one not taken yet doesn't answer either.
    let f = Fake::serve(
        &["watch.current", "alerts.subscribe"],
        h(|m, _| match m {
            "alerts.subscribe" => Some(Ok(json!({"subscribed": true}))),
            _ => Some(Ok(current(4.0))),
        }),
    );
    let d = f.daemon();
    connect(&d);
    f.push(
        "watch.sample",
        json!({"at": "0001-01-01T00:00:00Z", "ncpu": 0}),
    );
    assert!(eventually(|| lock(&d.s.sample).is_some()));
    assert!(d.pushed_sample().is_none());
    let out = d
        .answer(&["load", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    assert!(out.contains("4.0"), "{out}");
}

#[test]
fn a_failed_hello_leaves_no_thread_or_connection_behind() {
    let f = Fake::silent();
    let d = f.daemon_with(Timing {
        hello: Duration::from_millis(50),
        retry: Duration::ZERO,
        ..TIMING
    });
    // Once the first hello failed the methods are known (none), so a call doesn't wait for the
    // next attempt: each is given time to fail.
    for _ in 0..5 {
        assert!(d.answer(&["load", "--json"], SEC, &never()).is_none());
        std::thread::sleep(Duration::from_millis(150));
    }
    let n = lock(&f.conns).len();
    assert!(n >= 5, "it dialled each time: {n}");
    assert!(
        eventually(|| d.s.readers.load(Ordering::SeqCst) == 0),
        "{} reader threads left",
        d.s.readers.load(Ordering::SeqCst)
    );
    assert!(
        eventually(|| f.open.load(Ordering::SeqCst) == 0),
        "{} connections left open",
        f.open.load(Ordering::SeqCst)
    );
}

#[test]
fn a_call_keeps_its_deadline_while_a_reconnect_is_stuck() {
    let f = reading_daemon("watch.current", current(1.0));
    let d = f.daemon();
    assert!(
        d.answer(&["load", "--json"], SEC, &never())
            .unwrap()
            .is_ok()
    );
    // The daemon restarts as one that takes the connection and never says hello.
    f.stop();
    let g = Fake::serve_any(f.path.clone(), None, h(|_, _| None));
    assert!(eventually(|| d.s.readers.load(Ordering::SeqCst) == 0));
    let at = Instant::now();
    let r = d.answer(&["load", "--json"], Duration::from_secs(1), &never());
    assert!(matches!(r, None | Some(Err(Error::Timeout))), "{r:?}");
    assert!(
        at.elapsed() < Duration::from_millis(1500),
        "{:?}",
        at.elapsed()
    );
    assert!(lock(&d.s.link).connecting, "the reconnect is still stuck");
    // A call the daemon never answered goes to the CLI without waiting at all.
    let at = Instant::now();
    assert!(
        d.answer(&["sample", "-json", "7"], Duration::from_secs(10), &never())
            .is_none()
    );
    assert!(
        at.elapsed() < Duration::from_millis(500),
        "{:?}",
        at.elapsed()
    );
    // And a cancelled one stops waiting.
    let cancel = AtomicBool::new(true);
    assert_eq!(
        d.answer(&["load", "--json"], Duration::from_secs(10), &cancel),
        Some(Err(Error::Cancelled))
    );
    drop(g);
}

#[test]
fn the_first_call_waits_only_half_its_deadline_for_a_daemon_that_never_says_hello() {
    let f = Fake::silent();
    let d = f.daemon();
    let at = Instant::now();
    assert!(
        d.answer(&["load", "--json"], Duration::from_secs(2), &never())
            .is_none()
    );
    // Half the deadline is 1 s; the rest is slack for a loaded runner.
    let took = at.elapsed();
    assert!(took < Duration::from_millis(1800), "{took:?}");
}

#[test]
fn a_call_no_route_names_never_waits_for_the_first_connection() {
    // The daemon takes the connection and never says hello: the first connection hangs.
    let g = Fake::silent();
    let d = g.daemon();
    let at = Instant::now();
    assert!(
        d.answer(&["sample", "-json", "7"], Duration::from_secs(10), &never())
            .is_none()
    );
    assert!(
        at.elapsed() < Duration::from_millis(500),
        "{:?}",
        at.elapsed()
    );
}

#[test]
fn right_now_routes_to_the_daemon_only_while_it_lists_it() {
    let m = ["whatswrong.current"];
    assert_eq!(routed(&["whatswrong", "--json"], &m), "whatswrong.current");
    assert_eq!(routed(&["whatswrong", "--json"], &[]), "cli");
}

#[test]
fn right_now_comes_from_the_daemon_in_the_shape_the_cli_prints() {
    let f = reading_daemon(
        "whatswrong.current",
        serde_json::from_str(crate::whatswrong::tests::BUSY).unwrap(),
    );
    let out = f
        .daemon()
        .answer(&["whatswrong", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    let from_daemon = crate::whatswrong::parse(&out).unwrap();
    let from_cli = crate::whatswrong::parse(crate::whatswrong::tests::BUSY).unwrap();
    assert_eq!(from_daemon, from_cli);
}

const LIST_ARGS: [&str; 6] = ["alerts", "--json", "--state", "all", "--limit", "100"];

#[test]
fn the_alert_calls_route_to_the_daemon_only_while_it_lists_them() {
    assert_eq!(routed(&LIST_ARGS, &["alerts.list"]), "alerts.list");
    assert_eq!(routed(&LIST_ARGS, &["alerts.ack"]), "cli");
    assert_eq!(routed(&LIST_ARGS, &[]), "cli");
    let ack = ["alerts", "ack", "abc", "--json"];
    assert_eq!(routed(&ack, &["alerts.ack", "alerts.list"]), "alerts.ack");
    assert_eq!(routed(&ack, &["alerts.list"]), "cli");
    // `spectra anomalies` has no daemon method: it runs the CLI.
    assert_eq!(
        routed(&["anomalies", "--json"], &["alerts.list", "alerts.ack"]),
        "cli"
    );
}

#[test]
fn the_alert_history_asks_for_the_state_and_limit_and_comes_back_as_the_cli_prints_it() {
    let list = json!([{"id": "a", "title": "Load climbing", "state": "resolved"}]);
    let reply = list.clone();
    let f = Fake::serve(
        &["alerts.list"],
        h(move |m, _| (m == "alerts.list").then(|| Ok(reply.clone()))),
    );
    let d = f.daemon();
    let out = d.answer(&LIST_ARGS, SEC, &never()).unwrap().unwrap();
    assert_eq!(serde_json::from_str::<Value>(&out).unwrap(), list);
    assert_eq!(
        f.calls("alerts.list"),
        [json!({"state": "all", "limit": 100})]
    );
    // An empty history is an empty list, not a failure.
    let f = Fake::serve(
        &["alerts.list"],
        h(|m, _| (m == "alerts.list").then_some(Ok(Value::Null))),
    );
    let out = f
        .daemon()
        .answer(&LIST_ARGS, SEC, &never())
        .unwrap()
        .unwrap();
    assert_eq!(out, "[]");
}

#[test]
fn acknowledging_sends_the_id_once_and_never_retries_through_the_cli() {
    let f = Fake::serve(
        &["alerts.ack"],
        h(|m, p| {
            (m == "alerts.ack").then(|| {
                if p["id"] == "known" {
                    Ok(json!({"id": "known", "acked_at": "2026-11-23T04:05:00Z"}))
                } else {
                    Err((-32602, "unknown alert id".into()))
                }
            })
        }),
    );
    let d = f.daemon();
    let ok = d
        .answer(&["alerts", "ack", "known", "--json"], SEC, &never())
        .unwrap()
        .unwrap();
    assert!(ok.contains("acked_at"));
    // A refusal comes back as the answer: running the CLI would write a second time.
    let refused = d
        .answer(&["alerts", "ack", "nope", "--json"], SEC, &never())
        .expect("not handed to the CLI");
    assert_eq!(refused.unwrap_err().why(), "unknown alert id (-32602)");
    assert!(
        d.has("alerts.ack"),
        "a refused id doesn't take the route away"
    );
    assert_eq!(f.calls("alerts.ack").len(), 2);
    // No id, no call.
    // An answer that is an error, not `None` (which would run the CLI and write).
    assert!(
        matches!(
            d.answer(&["alerts", "ack", "--json"], SEC, &never()),
            Some(Err(Error::Shape(_)))
        ),
        "a CLI fallback would fail this"
    );
    assert_eq!(f.calls("alerts.ack").len(), 2);
}

#[test]
fn nothing_acknowledges_unless_asked() {
    // Reading the history, the anomalies or anything else never sends alerts.ack.
    let f = Fake::serve(
        &["alerts.list", "alerts.ack"],
        h(|m, _| (m == "alerts.list").then(|| Ok(json!([])))),
    );
    let d = f.daemon();
    d.answer(&LIST_ARGS, SEC, &never());
    assert!(d.answer(&["anomalies", "--json"], SEC, &never()).is_none());
    assert!(f.calls("alerts.ack").is_empty());
}

#[test]
fn a_busy_daemon_timing_out_its_own_collection_starts_no_process() {
    // What a starved daemon says on a loaded Mac: the reading fails, the CLI never runs.
    let f = Fake::serve(
        &["process.list", "whatswrong.current", "power.current"],
        h(|m, _| {
            Some(Err((
                -32603,
                format!("{m}: collection timed out after 15s: context deadline exceeded"),
            )))
        }),
    );
    let d = f.daemon();
    for args in [
        &["process", "-json", "--classify"][..],
        &["whatswrong", "--json"][..],
        &["power", "--json"][..],
    ] {
        let got = d.answer(args, SEC, &never());
        assert!(
            matches!(got, Some(Err(Error::Rpc { code: -32603, .. }))),
            "{args:?}: {got:?}"
        );
    }
    assert!(
        d.has("process.list") && d.has("power.current"),
        "still asked next time"
    );
}
