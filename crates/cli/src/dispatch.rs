//! Subcommand dispatch (Go's `dispatch`, `subcommandList` and `stripVerboseFlags` in
//! `cmd/spectra/main.go` and `verbose.go`).
//!
//! Every subcommand Go has is listed, in Go's order, so `help` and the fallback to `inspect`
//! behave the same. A subcommand whose handler is [`Handler::NotYetPorted`] has no Rust
//! implementation yet: it goes through [`not_yet_ported`], the one place such commands end up,
//! which says so on stderr and exits [`EXIT_NOT_PORTED`]. That answer is the Rust build's own;
//! it is never output parity with Go. The macOS-only gate still runs first, so off macOS those
//! commands give Go's unsupported report.

use spectra_base::env::Env;

use crate::api::Cli;
use crate::{help, unsupported};

/// Exit code of a subcommand the Rust build does not implement yet (sysexits' `EX_UNAVAILABLE`).
/// Go never exits with it, so a caller can tell "not ported" from any Go result.
pub const EXIT_NOT_PORTED: i32 = 69;

/// What runs a subcommand.
#[derive(Clone, Copy)]
pub enum Handler {
    /// Ported: the handler gets the arguments after the subcommand name.
    Run(fn(&mut Cli<'_>, &[String]) -> i32),
    /// Not ported yet; see [`not_yet_ported`].
    NotYetPorted,
}

/// One entry in the dispatch table (Go's `subcommand`).
#[derive(Clone, Copy)]
pub struct Subcommand {
    pub name: &'static str,
    pub desc: &'static str,
    pub handler: Handler,
}

const fn pending(name: &'static str, desc: &'static str) -> Subcommand {
    Subcommand { name, desc, handler: Handler::NotYetPorted }
}

const fn ported(name: &'static str, desc: &'static str, f: fn(&mut Cli<'_>, &[String]) -> i32) -> Subcommand {
    Subcommand { name, desc, handler: Handler::Run(f) }
}

/// Go's `subcommandList()`, in its order (help prints it in this order).
pub const SUBCOMMANDS: &[Subcommand] = &[
    pending("inspect", "Inspect .app bundles (default; runs when no subcommand given)"),
    ported("list", "Inspect every .app under /Applications", run_list),
    pending("snapshot", "Capture a structured snapshot of host + installed apps"),
    pending("jvm", "List or inspect running JVM processes"),
    pending("db", "Discover and inspect databases apps connect to (read-only; postgres, mysql, sqlite, mongodb, redis)"),
    pending("toolchain", "Show installed language runtimes and package managers"),
    pending("network", "Show current network state (routes, DNS, VPN, proxy, listening ports)"),
    pending("power", "Show current battery and thermal state"),
    pending("thermal", "Detect thermal throttling and CPU speed limits (exit 0 nominal, 3 warm, 4 throttled)"),
    pending("impact", "Compute the documented per-pid energy impact score from delta records"),
    pending("devices", "List Android devices/emulators (adb), booted iOS simulators, and emulator processes by worktree"),
    ported("memory", "Show memory pressure and swap (VM compressor on macOS; MemAvailable, PSI and zswap on Linux)", crate::memory::run),
    pending("storage", "Show disk volumes and ~/Library footprint"),
    pending("du", "Show the largest directories and files under a path by allocated size"),
    pending("fswatch", "Watch a directory tree by polling and report which files and directories keep changing"),
    pending("timemachine", "Show Time Machine status, destinations, and local snapshots"),
    pending("services", "Show launchd jobs and plist schedules"),
    pending("startup", "List what launches at login or boot; disable or enable user launch agents"),
    pending("logs", "Run bounded unified log queries"),
    pending("updates", "Show macOS install/update log entries"),
    pending("system", "Show system resource limits and saturation"),
    pending("load", "Admit, hold or back off a new job from load and memory, with per-worktree attribution"),
    pending("uninstall", "Plan (read-only) and apply a conservative app uninstall that moves only your own files to the Trash"),
    pending("orphans", "Find leftover processes (parent pid 1) whose working directory is under a worktree root"),
    pending("process", "List running processes sorted by memory (RSS)"),
    pending("playbook", "Show diagnostic playbooks and command plans"),
    pending("diff", "Diff two stored snapshots (alias for snapshot diff)"),
    pending("rules", "Evaluate recommendations rules against a snapshot"),
    pending("issues", "List, check, or update persisted issues from the recommendations engine"),
    pending("baseline", "Manage baseline snapshots (list, drop)"),
    pending("fleet", "Cross-host rollups: which hosts trip a rule, and version-drift matrices"),
    pending("bisect", "Find the snapshot where a rule started firing, and what changed alongside it"),
    pending("reconcile", "Print an advisory plan to make one host's toolchain match another's"),
    pending("anomalies", "Flag processes deviating from their rolling RSS baseline"),
    pending("install-helper", "Install the privileged helper daemon (requires sudo)"),
    pending("schedule", "Schedule periodic snapshot capture via a launchd agent"),
    pending("daemon", "Manage the local per-user diagnostic daemon"),
    pending("alerts", "List, acknowledge, watch, and summarize daemon alerts"),
    pending("sample", "Collect a user-space CPU sample of a running process"),
    pending("symbolicate", "Resolve raw stack addresses to symbol + file:line via atos"),
    pending("spindump", "Capture and summarize a per-thread stack report (heaviest stacks)"),
    pending("vmmap", "Summarize a process's memory composition by region (dirty/resident/swapped)"),
    pending("vmregions", "Composition by sharing/protection/backing, with RWX (W^X) flagging"),
    pending("lsmp", "Summarize a process's Mach port table (right counts, leak detection)"),
    pending("whois", "Attribute a busy XPC/Mach service to the client processes driving it"),
    pending("owner", "Who has this? Processes behind a port, an open file, a pid, or working under a directory (--under)"),
    pending("idle", "Find long-idle holders under a root: alive, near 0% CPU, no busy descendants (exit 3 when found)"),
    pending("lineage", "Show a process's ancestor chain and descendant subtree with RSS/CPU totals"),
    pending("malloc-history", "Attribute allocations to call stacks (requires MallocStackLogging)"),
    pending("flamegraph", "Render a native CPU flamegraph (SVG) from a process sample"),
    pending("hang", "Classify why a process's main thread is hung (lock/I-O/spin/idle)"),
    pending("procdiag", "Linux: diagnose a stuck or slow process from /proc (D-state, lock wait, fd leak, CPU spin, cgroup limits)"),
    ported("pressure", "Linux: show pressure-stall (PSI) and cgroup v2 memory/CPU/pids accounting", crate::pressure::run),
    pending("timeline", "Chronological incident timeline: process starts, unified-log errors, and installs"),
    pending(
        "spawns",
        "Trace process starts and exits live to catch short-lived children (polling; misses processes shorter than --interval)",
    ),
    pending("memtrace", "Sample a process's RSS over time (optionally its whole subtree) and say whether it is growing"),
    pending("runtime", "Identify a live PID's language runtime and the diagnostics available for it"),
    pending("xattr-inspect", "Show quarantine, provenance, where-froms, and AppleDouble state of files"),
    pending("core", "Inspect crashed-process core files and suggest offline analyzers"),
    pending("crash", "Audit post-mortem readiness (can this machine produce a debuggable crash?)"),
    pending("web", "Diff an Electron app's app.asar payload between two versions"),
    pending("whatswrong", "Ranked whole-system triage: why is this Mac slow right now?"),
    pending("cache", "Manage the local blob cache (stats, clear)"),
    ported("version", "Print Spectra version and exit", run_version),
    ported("capabilities", "Describe the machine-readable interfaces this build supports", crate::capabilities::run),
    ported("help", "Show this help text", run_help),
];

/// The table entry named `name`.
pub fn find(name: &str) -> Option<&'static Subcommand> {
    SUBCOMMANDS.iter().find(|sc| sc.name == name)
}

/// Go's `stripVerboseFlags`: removes `--verbose`/`--debug` (one or two dashes, before or after
/// the subcommand) and reports whether verbose output was asked for, also when `SPECTRA_DEBUG`
/// is set and non-empty. `-v` is left alone: `spectra inspect -v` means "show detection
/// signals".
pub fn strip_verbose_flags(args: &[String], env: &dyn Env) -> (Vec<String>, bool) {
    let mut verbose = env.var("SPECTRA_DEBUG").is_some_and(|v| !v.is_empty());
    let mut rest = Vec::with_capacity(args.len());
    for a in args {
        match a.as_str() {
            "--verbose" | "-verbose" | "--debug" | "-debug" => verbose = true,
            _ => rest.push(a.clone()),
        }
    }
    (rest, verbose)
}

/// Go's `dispatch` after the verbose flags are stripped: the first argument picks a subcommand
/// when it names one and is not a flag; otherwise every argument goes to `inspect`. No
/// arguments print the help on stderr and exit 2.
pub fn dispatch(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let Some(first) = args.first() else {
        help::write(cli.stderr);
        return 2;
    };
    if !first.starts_with('-')
        && let Some(sc) = find(first)
    {
        return run_subcommand(cli, sc, &args[1..]);
    }
    // No subcommand matched: default to inspect with the full argument list.
    run_named(cli, "inspect", args)
}

/// Runs `sc` behind the macOS-only gate.
fn run_subcommand(cli: &mut Cli<'_>, sc: &Subcommand, args: &[String]) -> i32 {
    if let Some(code) = unsupported::gate(cli, sc.name, args) {
        return code;
    }
    match sc.handler {
        Handler::Run(f) => f(cli, args),
        Handler::NotYetPorted => not_yet_ported(cli, sc.name),
    }
}

fn run_named(cli: &mut Cli<'_>, name: &str, args: &[String]) -> i32 {
    match find(name) {
        Some(sc) => run_subcommand(cli, sc, args),
        None => not_yet_ported(cli, name),
    }
}

/// THE NOT-YET-PORTED PATH. A subcommand the Go build implements and the Rust build does not
/// yet: one line on stderr, nothing on stdout (not even with `--json`, so no output can be
/// mistaken for Go's), and [`EXIT_NOT_PORTED`]. Not parity: Go would run the command.
pub fn not_yet_ported(cli: &mut Cli<'_>, name: &str) -> i32 {
    let _ = writeln!(cli.stderr, "spectra {name}: not yet ported to the Rust build; use the Go build of spectra for this command");
    EXIT_NOT_PORTED
}

fn run_version(cli: &mut Cli<'_>, _args: &[String]) -> i32 {
    let _ = writeln!(cli.stdout, "{}", crate::api::version(cli.ctx));
    0
}

fn run_help(cli: &mut Cli<'_>, _args: &[String]) -> i32 {
    help::write(cli.stdout);
    0
}

/// Go's `runList`: `inspect --all` with the same arguments.
fn run_list(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let args = list_inspect_args(args);
    // Go calls runInspect directly, skipping the gate; on macOS that is the same thing.
    match find("inspect").map(|sc| sc.handler) {
        Some(Handler::Run(f)) => f(cli, &args),
        _ => not_yet_ported(cli, "inspect"),
    }
}

/// Go's `listInspectArgs`.
pub fn list_inspect_args(args: &[String]) -> Vec<String> {
    let mut next = Vec::with_capacity(args.len() + 1);
    next.push("--all".to_string());
    next.extend_from_slice(args);
    next
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::Ctx;
    use spectra_base::env::MapEnv;
    use spectra_base::hostos::Kind;

    fn strs(a: &[&str]) -> Vec<String> {
        a.iter().map(|s| s.to_string()).collect()
    }

    // Go: TestStripVerboseFlags.
    #[test]
    fn strip_verbose_flags_table() {
        let env = MapEnv::new([("SPECTRA_DEBUG", "")]);
        let cases: &[(&str, &[&str], &[&str], bool)] = &[
            ("none", &["rules", "--json"], &["rules", "--json"], false),
            ("long verbose after subcommand", &["rules", "--verbose"], &["rules"], true),
            ("debug before subcommand", &["--debug", "rules"], &["rules"], true),
            ("single dash", &["-verbose", "rules", "-debug"], &["rules"], true),
            ("preserves inspect -v", &["inspect", "-v", "/App"], &["inspect", "-v", "/App"], false),
            ("not with a value", &["rules", "--verbose=true"], &["rules", "--verbose=true"], false),
        ];
        for &(name, input, want_rest, want_verbose) in cases {
            let (rest, verbose) = strip_verbose_flags(&strs(input), &env);
            assert_eq!((rest, verbose), (strs(want_rest), want_verbose), "{name}");
        }
    }

    // Go: TestStripVerboseFlagsHonoursEnv.
    #[test]
    fn strip_verbose_flags_honours_env() {
        assert!(strip_verbose_flags(&strs(&["rules"]), &MapEnv::new([("SPECTRA_DEBUG", "1")])).1);
        assert!(!strip_verbose_flags(&strs(&["rules"]), &MapEnv::default()).1);
    }

    // Go: TestSubcommandListIncludesList / IncludesBaseline.
    #[test]
    fn table_includes_aliases() {
        assert!(find("list").is_some());
        assert!(find("baseline").is_some());
        assert_eq!(SUBCOMMANDS.len(), 66, "Go's subcommandList has 66 entries");
    }

    // Go: TestListInspectArgsPrependsAll.
    #[test]
    fn list_inspect_args_prepends_all() {
        assert_eq!(list_inspect_args(&strs(&["-v", "--json"])), strs(&["--all", "-v", "--json"]));
    }

    fn run_on(os: Kind, args: &[&str]) -> (i32, String, String) {
        let ctx = Ctx { os, env: std::sync::Arc::new(MapEnv::default()), ..Ctx::system("v-test") };
        let (mut out, mut err) = (Vec::new(), Vec::new());
        let code = crate::run(&strs(args), &ctx, &mut out, &mut err);
        (code, String::from_utf8(out).expect("utf8"), String::from_utf8(err).expect("utf8"))
    }

    // Go: TestDispatchGatesDefaultInspect.
    #[test]
    fn unknown_first_argument_falls_back_to_inspect() {
        let (code, out, err) = run_on(Kind::Linux, &["/Applications/Slack.app"]);
        assert_eq!((code, out.as_str()), (1, ""));
        assert!(err.starts_with("spectra inspect: app-bundle inspection: unsupported on host OS (linux)"), "{err}");
        let (code, out, _) = run_on(Kind::Linux, &["--json", "/Applications/Slack.app"]);
        assert_eq!(code, 1);
        assert!(out.contains("\"command\": \"inspect\""), "{out}");
        // A flag first never names a subcommand, even when it looks like one.
        let (code, _, err) = run_on(Kind::Darwin, &["-version"]);
        assert_eq!(
            (code, err.as_str()),
            (EXIT_NOT_PORTED, "spectra inspect: not yet ported to the Rust build; use the Go build of spectra for this command\n")
        );
    }

    #[test]
    fn verbose_flags_are_stripped_before_dispatch() {
        assert_eq!(run_on(Kind::Linux, &["--verbose", "version", "--debug"]), (0, "v-test\n".into(), String::new()));
        assert_eq!(run_on(Kind::Linux, &["-debug"]).0, 2, "only verbose flags is no arguments");
    }

    #[test]
    fn not_yet_ported_commands_say_so_and_print_nothing_on_stdout() {
        for args in [&["storage", "--json"][..], &["snapshot", "-h"]] {
            let (code, out, err) = run_on(Kind::Linux, args);
            assert_eq!(code, EXIT_NOT_PORTED, "{args:?}");
            assert_eq!(out, "", "{args:?}");
            assert_eq!(
                err,
                format!("spectra {}: not yet ported to the Rust build; use the Go build of spectra for this command\n", args[0])
            );
        }
        // On macOS the gate lets macOS-only commands through, to the same path; list runs as
        // inspect, as in Go.
        assert_eq!(run_on(Kind::Darwin, &["vmmap", "1"]).0, EXIT_NOT_PORTED);
        assert!(run_on(Kind::Darwin, &["list"]).2.starts_with("spectra inspect: not yet ported"));
        // inspect -h passes the gate off macOS (Go prints inspect's help there).
        assert_eq!(run_on(Kind::Linux, &["inspect", "-h"]).0, EXIT_NOT_PORTED);
    }

    #[test]
    fn verbose_routes_the_logger() {
        let ctx = Ctx::system("v");
        let (mut out, mut err) = (Vec::new(), Vec::new());
        assert!(!Cli::new(&ctx, false, &mut out, &mut err).logger.enabled(spectra_base::logger::Level::Debug));
        assert!(Cli::new(&ctx, true, &mut out, &mut err).logger.enabled(spectra_base::logger::Level::Debug));
    }
}
