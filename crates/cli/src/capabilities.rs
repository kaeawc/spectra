//! `spectra capabilities` (`cmd/spectra/capabilities.go` and `internal/capabilities`): the
//! manifest of machine-readable interfaces this build supports, which outside consumers check
//! first. The interface list depends on the OS, chosen at run time from `ctx.os`.

use std::io::Write;

use spectra_base::gojson::Encoder;
use spectra_base::hostos::Kind;
use spectra_protocol::{
    CAPABILITIES_SCHEMA_VERSION, INTERFACE_CAPABILITIES, INTERFACE_INSPECT, INTERFACE_SNAPSHOT, INTERFACE_VERSION, OUTPUT_JSON,
    OUTPUT_TEXT, SCHEMA_CAPABILITIES, SCHEMA_INSPECT, SCHEMA_SNAPSHOT, SchemaRef, SpectraCapabilities, SpectraInterface,
    supported_result_schema_version,
};

use crate::api::{self, Cli};
use crate::flags::{FlagError, FlagSet};

/// Result schemas this repository owns beyond the protocol's own (Go's `localSchemaVersions`).
/// Bump a version on any breaking change to that command's JSON.
const LOCAL_SCHEMAS: &[(&str, i64)] = &[
    ("spectra.load", 1),
    ("spectra.orphans", 1),
    ("spectra.owner", 1),
    ("spectra.owner.under", 1),
    ("spectra.lineage", 1),
    ("spectra.spawns", 1),
    ("spectra.idle", 1),
    ("spectra.devices", 1),
    ("spectra.startup", 1),
    ("spectra.startup.change", 1),
    ("spectra.uninstall.plan", 1),
    ("spectra.uninstall.apply", 1),
];

/// Go's `ResultSchemaVersion`: the current version of a protocol-owned or local result schema.
pub fn result_schema_version(name: &str) -> Option<i64> {
    supported_result_schema_version(name).or_else(|| LOCAL_SCHEMAS.iter().find(|(n, _)| *n == name).map(|&(_, v)| v))
}

/// A JSON command outside the protocol's own set (Go's `entry`).
struct Entry {
    name: &'static str,
    schema: &'static str,
    darwin_only: bool,
    argv: &'static [&'static str],
}

const fn entry(name: &'static str, schema: &'static str, argv: &'static [&'static str]) -> Entry {
    Entry { name, schema, darwin_only: false, argv }
}

const fn darwin(name: &'static str, schema: &'static str, argv: &'static [&'static str]) -> Entry {
    Entry { name, schema, darwin_only: true, argv }
}

/// Go's `commandEntries`, in manifest order.
const COMMAND_ENTRIES: &[Entry] = &[
    entry(
        "load",
        "spectra.load",
        &[
            "load",
            "--json",
            "[--top <n>]",
            "[--root <dir>]...",
            "[--max-load-per-cpu <x>]",
            "[--min-free-gb <gib>]",
            "[--backoff-free-gb <gib>]",
            "[--sample <duration>]",
        ],
    ),
    entry("orphans", "spectra.orphans", &["orphans", "--json", "[--root <dir>]...", "[--min-age <duration>]"]),
    entry("owner", "spectra.owner", &["owner", "--json", "[--timeout <duration>]", "<target>"]),
    entry("owner.under", "spectra.owner.under", &["owner", "--under", "--json", "[--timeout <duration>]", "<dir>..."]),
    entry("lineage", "spectra.lineage", &["lineage", "--json", "<pid>"]),
    entry("spawns", "spectra.spawns", &["spawns", "--json", "[--interval <duration>]", "[--duration <duration>]", "[--pid <pid>]"]),
    entry(
        "idle",
        "spectra.idle",
        &[
            "idle",
            "--json",
            "[--root <dir>]...",
            "[--pids <pid,...>]",
            "[--min-age <duration>]",
            "[--max-cpu <pct>]",
            "[--window <duration>]",
            "[--timeout <duration>]",
        ],
    ),
    entry("devices", "spectra.devices", &["devices", "--json", "[--root <dir>]...", "[--start-adb]", "[--all-simulators]"]),
    darwin("startup", "spectra.startup", &["startup", "list", "--json", "[--scope user|system|all]", "[--include-apple]"]),
    darwin("startup.disable", "spectra.startup.change", &["startup", "disable", "<label>", "--json", "[--scope user]"]),
    darwin("startup.enable", "spectra.startup.change", &["startup", "enable", "<label>", "--json", "[--scope user]"]),
    darwin("uninstall.plan", "spectra.uninstall.plan", &["uninstall", "plan", "--json", "<app_path|bundle_id>"]),
    darwin(
        "uninstall.apply",
        "spectra.uninstall.apply",
        &[
            "uninstall",
            "apply",
            "--plan",
            "<plan_file>",
            "--json",
            "[--dry-run]",
            "[--quit]",
            "[--include-low-confidence]",
            "[--data-only]",
            "[--leftovers]",
        ],
    ),
];

fn schema_ref(name: &str) -> SchemaRef {
    // Go panics on an unknown schema: every name here comes from the tables above.
    let version = result_schema_version(name).unwrap_or_else(|| panic!("capabilities: unsupported result schema {name}"));
    SchemaRef { name: name.to_string(), version }
}

fn argv(parts: &[&str]) -> Option<Vec<String>> {
    Some(parts.iter().map(|s| s.to_string()).collect())
}

fn json_interface(name: &str, parts: &[&str], schema: SchemaRef) -> SpectraInterface {
    SpectraInterface { name: name.to_string(), argv: argv(parts), output: OUTPUT_JSON.to_string(), result_schema: Some(schema) }
}

/// Go's `capabilities.Build`. `os` is [`Kind::Darwin`] or [`Kind::Linux`]; macOS-only
/// interfaces are listed on Darwin only.
pub fn build(spectra_version: &str, os: Kind, arch: &str) -> SpectraCapabilities {
    let darwin = os == Kind::Darwin;
    let mut interfaces = vec![SpectraInterface {
        name: INTERFACE_VERSION.to_string(),
        argv: argv(&["version"]),
        output: OUTPUT_TEXT.to_string(),
        result_schema: None,
    }];
    if darwin {
        interfaces.push(json_interface(INTERFACE_INSPECT, &["--json", "<app_path>..."], schema_ref(SCHEMA_INSPECT)));
    }
    interfaces.push(json_interface(INTERFACE_SNAPSHOT, &["snapshot", "--json", "[--no-apps]"], schema_ref(SCHEMA_SNAPSHOT)));
    for e in COMMAND_ENTRIES.iter().filter(|e| darwin || !e.darwin_only) {
        interfaces.push(json_interface(e.name, e.argv, schema_ref(e.schema)));
    }
    let own = SchemaRef { name: SCHEMA_CAPABILITIES.to_string(), version: CAPABILITIES_SCHEMA_VERSION };
    interfaces.push(json_interface(INTERFACE_CAPABILITIES, &["capabilities", "--json"], own.clone()));
    SpectraCapabilities {
        schema: own,
        spectra_version: spectra_version.to_string(),
        os: os.as_str().to_string(),
        arch: arch.to_string(),
        interfaces: Some(interfaces),
    }
}

/// Go's `runCapabilities`.
pub fn run(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let mut fs = FlagSet::new("spectra capabilities");
    let as_json = fs.bool("json", false, "Emit JSON");
    match fs.parse(args, cli.stderr) {
        Ok(()) => {}
        Err(FlagError::Help | FlagError::Invalid(_)) => return 2,
    }
    if fs.narg() != 0 {
        let _ = writeln!(cli.stderr, "usage: spectra capabilities [--json]");
        return 2;
    }
    let manifest = api::capabilities(cli.ctx);
    if as_json.get() {
        // Go's json.NewEncoder(os.Stdout).Encode: compact, HTML escaped (so the angle brackets of an
        // argv placeholder are written as unicode escapes, not literally).
        if let Err(e) = (Encoder { indent: false, escape_html: true }).encode(&mut *cli.stdout, &manifest) {
            let _ = writeln!(cli.stderr, "encode capabilities: {e}");
            return 1;
        }
        return 0;
    }
    write_text(cli.stdout, &manifest);
    0
}

fn write_text(out: &mut dyn Write, m: &SpectraCapabilities) {
    let _ = writeln!(out, "Spectra {} ({}/{})", m.spectra_version, m.os, m.arch);
    let _ = writeln!(out, "Capabilities schema: {} v{}", m.schema.name, m.schema.version);
    let _ = writeln!(out, "{:<16} {:<7} {:<27} ARGV", "NAME", "OUTPUT", "RESULT SCHEMA");
    for iface in m.interfaces.iter().flatten() {
        let schema = iface.result_schema.as_ref().map_or_else(|| "-".to_string(), |s| format!("{} v{}", s.name, s.version));
        let argv = iface.argv.as_deref().unwrap_or_default().join(" ");
        let _ = writeln!(out, "{:<16} {:<7} {:<27} {}", iface.name, iface.output, schema, argv);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use spectra_protocol::decode_spectra_capabilities;

    fn names(m: &SpectraCapabilities) -> Vec<&str> {
        m.interfaces.iter().flatten().map(|i| i.name.as_str()).collect()
    }

    // Go: TestBuild.
    #[test]
    fn build_darwin_shape() {
        let m = build("test-version", Kind::Darwin, "arm64");
        assert_eq!((m.spectra_version.as_str(), m.os.as_str(), m.arch.as_str()), ("test-version", "darwin", "arm64"));
        let interfaces = m.interfaces.as_deref().expect("interfaces");
        assert_eq!(interfaces.len(), 4 + COMMAND_ENTRIES.len());
        assert_eq!(interfaces[0].result_schema, None, "the text version interface has no result schema");
        assert_eq!(interfaces[1].argv.as_deref(), Some(&["--json".to_string(), "<app_path>...".to_string()][..]));
        assert_eq!(interfaces.last().map(|i| i.name.as_str()), Some("capabilities"));
        let json = spectra_base::gojson::to_vec(&m).expect("encode");
        decode_spectra_capabilities(&json).expect("protocol accepts the darwin manifest");
    }

    // Go: TestBuildDeclaresWaaaghCommands.
    #[test]
    fn build_declares_waaagh_commands() {
        let want = [
            ("load", "spectra.load"),
            ("orphans", "spectra.orphans"),
            ("owner", "spectra.owner"),
            ("owner.under", "spectra.owner.under"),
            ("lineage", "spectra.lineage"),
            ("spawns", "spectra.spawns"),
            ("idle", "spectra.idle"),
            ("devices", "spectra.devices"),
            ("startup", "spectra.startup"),
            ("startup.disable", "spectra.startup.change"),
            ("startup.enable", "spectra.startup.change"),
            ("uninstall.plan", "spectra.uninstall.plan"),
            ("uninstall.apply", "spectra.uninstall.apply"),
        ];
        let m = build("v", Kind::Darwin, "arm64");
        for (name, schema) in want {
            let iface = m.interface(name).unwrap_or_else(|| panic!("missing interface {name}"));
            assert_eq!(iface.output, "json", "{name}");
            assert_eq!(iface.result_schema, Some(SchemaRef { name: schema.into(), version: 1 }), "{name}");
            assert!(iface.argv.as_deref().is_some_and(|a| a.last().is_some_and(|s| !s.is_empty())), "{name}");
        }
        let under = m.interface("owner.under").expect("owner.under").argv.clone().expect("argv");
        assert_eq!(under[..3], ["owner", "--under", "--json"]);
    }

    // Go: TestBuildOmitsMacOSOnlyCommandsOffMacOS.
    #[test]
    fn build_omits_macos_only_commands_on_linux() {
        let m = build("test-version", Kind::Linux, "amd64");
        let json = spectra_base::gojson::to_vec(&m).expect("encode");
        decode_spectra_capabilities(&json).expect("protocol accepts the linux manifest");
        for name in ["inspect", "startup", "startup.disable", "startup.enable", "uninstall.plan", "uninstall.apply"] {
            assert!(m.interface(name).is_none(), "linux manifest advertises {name}");
        }
        for name in ["load", "orphans", "owner", "owner.under", "lineage", "spawns", "idle", "devices"] {
            assert!(m.interface(name).is_some(), "linux manifest omits {name}");
        }
        assert_eq!(names(&m).len(), 3 + COMMAND_ENTRIES.iter().filter(|e| !e.darwin_only).count());
    }

    // Go: TestResultSchemaVersion.
    #[test]
    fn result_schema_versions() {
        for name in [SCHEMA_INSPECT, SCHEMA_SNAPSHOT, SCHEMA_CAPABILITIES, "spectra.load", "spectra.uninstall.apply"] {
            assert!(result_schema_version(name).is_some_and(|v| v >= 1), "{name}");
        }
        assert_eq!(result_schema_version("spectra.nope"), None);
        for (name, _) in LOCAL_SCHEMAS {
            assert_eq!(supported_result_schema_version(name), None, "{name} shadows a protocol-owned schema");
        }
    }

    #[test]
    fn json_escapes_html_like_go() {
        let ctx = api::Ctx { os: Kind::Darwin, arch: "arm64".into(), ..api::Ctx::system("v") };
        let (mut out, mut err) = (Vec::new(), Vec::new());
        assert_eq!(crate::run(&["capabilities".into(), "--json".into()], &ctx, &mut out, &mut err), 0);
        let text = String::from_utf8(out).expect("utf8");
        let bs = char::from(92);
        assert!(
            text.contains(&format!("{bs}u003capp_path{bs}u003e...")) && !text.contains("<app_path>") && text.ends_with("}\n"),
            "{text}"
        );
    }

    #[test]
    fn rejects_positional_arguments_and_unknown_flags() {
        let ctx = api::Ctx { os: Kind::Linux, ..api::Ctx::system("v") };
        let run = |args: &[&str]| {
            let args: Vec<String> = args.iter().map(|s| s.to_string()).collect();
            let (mut out, mut err) = (Vec::new(), Vec::new());
            let code = crate::run(&args, &ctx, &mut out, &mut err);
            (code, out, String::from_utf8(err).expect("utf8"))
        };
        assert_eq!(run(&["capabilities", "extra"]), (2, vec![], "usage: spectra capabilities [--json]\n".to_string()));
        let (code, out, err) = run(&["capabilities", "--nope"]);
        assert_eq!((code, out.len()), (2, 0));
        assert!(
            err.starts_with("flag provided but not defined: -nope\nUsage of spectra capabilities:\n  -json\n    \tEmit JSON\n"),
            "{err}"
        );
    }
}
