# Capabilities manifest

`spectra capabilities --json` emits one JSON object describing the CLI interfaces this build supports. `spectra capabilities` prints a short human-readable table. The manifest is local and requires no host inspection.

The manifest uses `SpectraCapabilities` from the standard-library-only
`github.com/kaeawc/spectra-protocol/protocol/v1` package. Its decoder validates
the emitted document and its result-schema advertisements.

```json
{
  "schema": {"name": "spectra.capabilities", "version": 1},
  "spectra_version": "dev",
  "os": "darwin",
  "arch": "arm64",
  "interfaces": [
    {"name": "version", "argv": ["version"], "output": "text"},
    {"name": "inspect", "argv": ["--json", "<app_path>..."], "output": "json", "result_schema": {"name": "spectra.inspect", "version": 1}},
    {"name": "snapshot", "argv": ["snapshot", "--json", "[--no-apps]"], "output": "json", "result_schema": {"name": "spectra.snapshot", "version": 1}},
    {"name": "load", "argv": ["load", "--json", "[--top <n>]", "[--root <dir>]...", "[--max-load-per-cpu <x>]", "[--min-free-gb <gib>]", "[--backoff-free-gb <gib>]", "[--sample <duration>]"], "output": "json", "result_schema": {"name": "spectra.load", "version": 1}},
    {"name": "orphans", "argv": ["orphans", "--json", "[--root <dir>]...", "[--min-age <duration>]"], "output": "json", "result_schema": {"name": "spectra.orphans", "version": 1}},
    {"name": "owner", "argv": ["owner", "--json", "[--timeout <duration>]", "<target>"], "output": "json", "result_schema": {"name": "spectra.owner", "version": 1}},
    {"name": "owner.under", "argv": ["owner", "--under", "--json", "[--timeout <duration>]", "<dir>..."], "output": "json", "result_schema": {"name": "spectra.owner.under", "version": 1}},
    {"name": "lineage", "argv": ["lineage", "--json", "<pid>"], "output": "json", "result_schema": {"name": "spectra.lineage", "version": 1}},
    {"name": "spawns", "argv": ["spawns", "--json", "[--interval <duration>]", "[--duration <duration>]", "[--pid <pid>]"], "output": "json", "result_schema": {"name": "spectra.spawns", "version": 1}},
    {"name": "idle", "argv": ["idle", "--json", "[--root <dir>]...", "[--pids <pid,...>]", "[--min-age <duration>]", "[--max-cpu <pct>]", "[--window <duration>]", "[--timeout <duration>]"], "output": "json", "result_schema": {"name": "spectra.idle", "version": 1}},
    {"name": "startup", "argv": ["startup", "list", "--json", "[--scope user|system|all]", "[--include-apple]"], "output": "json", "result_schema": {"name": "spectra.startup", "version": 1}},
    {"name": "startup.disable", "argv": ["startup", "disable", "<label>", "--json", "[--scope user]"], "output": "json", "result_schema": {"name": "spectra.startup.change", "version": 1}},
    {"name": "startup.enable", "argv": ["startup", "enable", "<label>", "--json", "[--scope user]"], "output": "json", "result_schema": {"name": "spectra.startup.change", "version": 1}},
    {"name": "uninstall.plan", "argv": ["uninstall", "plan", "--json", "<app_path|bundle_id>"], "output": "json", "result_schema": {"name": "spectra.uninstall.plan", "version": 1}},
    {"name": "uninstall.apply", "argv": ["uninstall", "apply", "--plan", "<plan_file>", "--json", "[--dry-run]", "[--quit]", "[--include-low-confidence]", "[--data-only]", "[--leftovers]"], "output": "json", "result_schema": {"name": "spectra.uninstall.apply", "version": 1}},
    {"name": "capabilities", "argv": ["capabilities", "--json"], "output": "json", "result_schema": {"name": "spectra.capabilities", "version": 1}}
  ]
}
```

`spectra_version` is the exact text printed by `spectra version`, without its trailing newline. `os` and `arch` are the build's Go runtime platform. The `inspect`, `startup`, `startup.disable`, `startup.enable`, `uninstall.plan` and `uninstall.apply` interfaces are advertised only on macOS builds, where app inspection and launchd are available. `inspect` returns an array of [result objects](result-schema.md); `spawns` returns newline-delimited JSON records; every other JSON interface returns a single object. The version interface is plain text and therefore omits `result_schema`.

Interface names are unique, so a command with several output shapes has one entry per shape: `owner` and `owner.under`, `startup` (list) and `startup.disable`/`startup.enable`, `uninstall.plan` and `uninstall.apply`. The JSON shape of every Spectra-owned schema (`spectra.load`, `spectra.orphans`, `spectra.owner`, `spectra.owner.under`, `spectra.lineage`, `spectra.spawns`, `spectra.idle`, `spectra.startup`, `spectra.startup.change`, `spectra.uninstall.plan`, `spectra.uninstall.apply`) is documented in [Command result schemas](command-schemas.md).

The `argv` values describe supported invocation shapes, not every flag. Angle brackets mark a value supplied by the caller (for `inspect`, an absolute app path); the ellipsis permits repeating it. Square brackets mark an optional flag, and `a|b` inside one marks alternatives. A snapshot caller may also pass `--no-store` to avoid local persistence.

## Stability

Proxy consumers must check each JSON interface's `result_schema` name and version before decoding its result. Fields may be added within a major schema version. Removing or renaming a field, or changing its JSON type, requires a result schema version bump. This rule applies independently to each schema. The protocol module owns the inspect, snapshot and capabilities schemas; Spectra owns the rest, and a test fails when one of their Go result types drifts from its documented field list. A new capability can add an interface entry without changing an existing result schema.
