//! The macOS-only gate (`cmd/spectra/unsupported.go`): off macOS, a macOS-only subcommand is
//! answered with one uniform unsupported result instead of failing its own way.

use spectra_base::gojson::Encoder;
use spectra_base::hostos::{Kind, Unsupported};

use crate::api::Cli;

/// A subcommand with no implementation off macOS.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DarwinOnly {
    pub feature: &'static str,
    pub hint: &'static str,
}

const APP_INSPECTION_HINT: &str =
    "app inspection reads macOS .app bundles, Mach-O binaries, code signatures, and the TCC database; it has no equivalent on this OS";

/// Go's `nativeprofile.OffDarwinHint`.
const LINUX_STUCK_PROCESS_HINT: &str = "on Linux, `spectra procdiag <pid>` diagnoses a stuck or slow process from /proc";

const fn only(feature: &'static str, hint: &'static str) -> DarwinOnly {
    DarwinOnly { feature, hint }
}

/// Go's `darwinOnlyCommands`. Features and hints that Go takes from other packages
/// (`services`, `logquery`, `nativeprofile`, `symbolicate`) are inlined; the contract cases pin
/// them.
pub const DARWIN_ONLY: &[(&str, DarwinOnly)] = &[
    ("inspect", only("app-bundle inspection", APP_INSPECTION_HINT)),
    ("list", only("app-bundle inspection", APP_INSPECTION_HINT)),
    ("timemachine", only("Time Machine", "")),
    ("services", only("launchd services", "on Linux, use systemctl list-units")),
    ("startup", only("startup items", "")),
    ("logs", only("unified log queries", "on Linux, use journalctl")),
    ("updates", only("macOS install history", "")),
    ("uninstall", only("app uninstall", "")),
    ("schedule", only("launchd snapshot scheduling", "run `spectra snapshot` from cron or a systemd timer instead")),
    ("sample", only("process sampling (sample(1))", LINUX_STUCK_PROCESS_HINT)),
    ("flamegraph", only("native flamegraphs (sample(1))", LINUX_STUCK_PROCESS_HINT)),
    ("hang", only("main-thread hang classification (sample(1))", LINUX_STUCK_PROCESS_HINT)),
    ("spindump", only("spindump stack reports", LINUX_STUCK_PROCESS_HINT)),
    (
        "vmmap",
        only("vmmap memory composition", "on Linux, read /proc/<pid>/smaps_rollup; `spectra procdiag <pid>` covers cgroup memory limits"),
    ),
    ("vmregions", only("vmmap region composition", "on Linux, read /proc/<pid>/smaps")),
    ("lsmp", only("Mach port tables", "")),
    ("whois", only("XPC/Mach service attribution", "")),
    ("malloc-history", only("malloc stack logging", "")),
    ("symbolicate", only("atos symbolication", "")),
];

/// The gate entry for `name`, when it is macOS-only.
pub fn darwin_only(name: &str) -> Option<DarwinOnly> {
    DARWIN_ONLY.iter().find(|(n, _)| *n == name).map(|(_, d)| *d)
}

/// Go's `gateDarwinOnly`: answers a macOS-only subcommand off macOS and returns its exit code,
/// or `None` when the command should run. `inspect -h` still runs, so its help prints.
pub fn gate(cli: &mut Cli<'_>, name: &str, args: &[String]) -> Option<i32> {
    let d = darwin_only(name)?;
    if cli.os == Kind::Darwin {
        return None;
    }
    if name == "inspect" && args.iter().any(|a| is_help_flag(a)) {
        return None;
    }
    Some(report(cli, name, &Unsupported::new(d.feature, cli.os), d.hint, wants_json(args)))
}

/// Go's `reportUnsupported`: the report as JSON on stdout when JSON was asked for, else one line
/// on stderr. Exits 1, like any failed command; the JSON's `"error": "unsupported_platform"` is
/// what tells the cases apart.
pub fn report(cli: &mut Cli<'_>, command: &str, err: &Unsupported, hint: &str, as_json: bool) -> i32 {
    let r = err.command_report(command, hint);
    if as_json {
        // Go's json.Encoder with SetIndent("", "  ") and SetEscapeHTML(false); its write error
        // is ignored there too.
        let _ = Encoder { indent: true, escape_html: false }.encode(&mut *cli.stdout, &r);
        return 1;
    }
    let mut msg = format!("spectra {command}: {}", r.report.message);
    if !hint.is_empty() {
        msg.push_str(&format!(" ({hint})"));
    }
    let _ = writeln!(cli.stderr, "{msg}");
    1
}

/// Whether `args` ask for JSON (Go's `wantsJSON`).
pub fn wants_json(args: &[String]) -> bool {
    args.iter().any(|a| matches!(a.as_str(), "--json" | "-json" | "--json=true" | "-json=true"))
}

/// Go's `isHelpFlag`.
pub fn is_help_flag(a: &str) -> bool {
    matches!(a, "-h" | "--help" | "-help")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::Ctx;
    use crate::dispatch::SUBCOMMANDS;

    fn linux() -> Ctx {
        Ctx { os: Kind::Linux, ..Ctx::system("test") }
    }

    fn gate_on(ctx: &Ctx, name: &str, args: &[&str]) -> (Option<i32>, String, String) {
        let (mut out, mut err) = (Vec::new(), Vec::new());
        let args: Vec<String> = args.iter().map(|s| s.to_string()).collect();
        let code = gate(&mut Cli::new(ctx, false, &mut out, &mut err), name, &args);
        (code, String::from_utf8(out).expect("utf8"), String::from_utf8(err).expect("utf8"))
    }

    // Go: TestGateDarwinOnlyJSON.
    #[test]
    fn gate_json() {
        let (code, out, err) = gate_on(&linux(), "vmmap", &["--json", "123"]);
        assert_eq!(code, Some(1));
        let got: serde_json::Value = serde_json::from_str(&out).expect("stdout is JSON");
        assert_eq!(got["command"], "vmmap");
        assert_eq!(got["error"], "unsupported_platform");
        assert_eq!(got["feature"], "vmmap memory composition");
        assert_eq!(got["os"], "linux");
        assert_eq!(got["message"], "vmmap memory composition: unsupported on host OS (linux)");
        assert!(got["hint"].as_str().is_some_and(|h| !h.is_empty()), "vmmap should point at the Linux alternative");
        assert_eq!(err, "");
    }

    // Go: TestGateDarwinOnlyText.
    #[test]
    fn gate_text() {
        let (code, out, err) = gate_on(&linux(), "schedule", &["install"]);
        assert_eq!((code, out.as_str()), (Some(1), ""));
        assert!(err.contains("unsupported on host OS (linux)") && err.contains("systemd timer"), "{err}");
    }

    // Go: TestGateDarwinOnlyPassesThrough.
    #[test]
    fn gate_passes_through() {
        let darwin = Ctx { os: Kind::Darwin, ..Ctx::system("test") };
        assert_eq!(gate_on(&darwin, "vmmap", &[]), (None, String::new(), String::new()));
        for cmd in ["load", "process", "memory", "orphans", "jvm"] {
            assert_eq!(gate_on(&linux(), cmd, &["--json"]).0, None, "{cmd} gated on Linux");
        }
        for h in ["-h", "--help", "-help"] {
            assert_eq!(gate_on(&linux(), "inspect", &[h]), (None, String::new(), String::new()), "inspect {h}");
        }
    }

    // Go: TestDarwinOnlyCommandsExist.
    #[test]
    fn darwin_only_commands_exist() {
        for (name, _) in DARWIN_ONLY {
            assert!(SUBCOMMANDS.iter().any(|sc| sc.name == *name), "DARWIN_ONLY lists {name}, which is not a subcommand");
        }
    }

    #[test]
    fn wants_json_spellings() {
        for a in ["--json", "-json", "--json=true", "-json=true"] {
            assert!(wants_json(&[a.to_string()]), "{a}");
        }
        for a in ["--json=false", "json", "-j"] {
            assert!(!wants_json(&[a.to_string()]), "{a}");
        }
    }
}
