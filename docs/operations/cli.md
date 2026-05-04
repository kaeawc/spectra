# CLI reference

The `spectra` binary's flags and behavior. Flag set is intentionally
small today; planned subcommands (daemon, helper, JVM) are listed at
the end of this page.

## Synopsis

```
spectra [flags] <App.app>...
spectra --all [flags]
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Emit JSON instead of the human-readable table |
| `--all` | false | Scan every `.app` under `/Applications` and `/Applications/Utilities` |
| `-v` | false | Show detection signals + full per-app metadata |
| `--network` | false | Extract embedded URL hosts (slower; scans `app.asar`) |
| `--version` | false | Print version and exit |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `2` | Bad usage (invalid flags) |

Per-app errors are written to stderr; the offending entry is dropped
and processing continues for other paths.

## Examples

### Single app, terse table

```bash
spectra /Applications/Slack.app
```

### Full inspection

```bash
spectra -v /Applications/Claude.app
```

Shows every section: metadata, security, declared+granted privacy,
dependencies, helpers, login items, running processes, storage, and
detection signals.

### Scan everything

```bash
spectra --all
```

Roughly 100 apps in 10s on Apple Silicon (8-core M-series). Worker
pool is `runtime.GOMAXPROCS(0)` capped at the number of paths.

### Network endpoints

```bash
spectra --network -v /Applications/Cursor.app
```

Adds the `hosts (N): ...` line with deduplicated URL hosts found in
the binary and `app.asar`. ~3× slower than without `--network`.

### JSON output for scripting

```bash
spectra --json /Applications/*.app | jq '.[] | select(.UI == "Tauri")'
```

The full `detect.Result` per app. See
[../reference/result-schema.md](../reference/result-schema.md) for the
shape.

## Output format (table)

```
APP                           UI                   RUNTIME       PACKAGING   CONFIDENCE
----------------------------------------------------------------------------------------
Claude                        Electron             Node+Chromium Squirrel    high
```

Verbose mode adds indented detail blocks beneath each row, one block
per non-empty field.

## Planned subcommands (daemon era)

```
spectra serve                       # run the collector daemon
spectra connect <host>              # client targeting a remote daemon
spectra list                        # alias for "scan all bundles via local daemon"
spectra inspect <bundle>            # alias for "inspect one via local daemon"
spectra diff <baseline-id> [host]   # snapshot diff
spectra jvm [pid]                   # JVM inspection
spectra cache stats / clear         # blob cache management
spectra install-helper              # privileged helper install (sudo)
```

See [../design/architecture.md](../design/architecture.md) for the
shape of the daemon RPC.
