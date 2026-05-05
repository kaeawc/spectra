# CLI reference

The `spectra` binary dispatches to one of several subcommands. The
default (no subcommand) inspects the `.app` bundles passed as
positional args, preserving compatibility with the original flag-only
shape.

## Synopsis

```
spectra <subcommand> [flags] [args]
spectra [flags] <App.app>...     # routes to `inspect` (default)
```

## Subcommands

| Name | Description |
|---|---|
| `inspect` | Inspect `.app` bundles (default; runs when no subcommand given) |
| `list` | Inspect every `.app` under `/Applications` |
| `snapshot` | Capture a structured snapshot of host + installed apps |
| `diff` | Diff two stored snapshots |
| `rules` | Evaluate recommendation rules against a snapshot |
| `issues` | List, check, or update persisted recommendation issues |
| `jvm` | List or inspect running JVM processes |
| `toolchain` | Show installed language runtimes and package managers |
| `network` | Show current routes, DNS, VPN, proxy, and listening ports |
| `power` | Show current battery and thermal state |
| `storage` | Show disk volumes and `~/Library` footprint |
| `process` | List running processes sorted by memory |
| `serve` | Run the local Unix-socket JSON-RPC daemon |
| `status` | Check whether the local daemon is running |
| `metrics` | Show stored process metrics from daemon sampling |
| `sample` | Collect a user-space CPU sample of a process |
| `cache` | Manage the local blob cache |
| `install-helper` | Install the privileged helper daemon |
| `version` | Print Spectra version and exit |
| `help` | Show top-level help |

`spectra help` lists the subcommands; `spectra <sub> --help` shows
flags for one.

## `spectra inspect` (default)

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Emit JSON instead of the human-readable table |
| `--all` | false | Scan every `.app` under `/Applications` and `/Applications/Utilities` |
| `-v` | false | Show detection signals + full per-app metadata |
| `--network` | false | Extract embedded URL hosts (slower; scans `app.asar`) |

### Examples

```bash
spectra /Applications/Slack.app                # one app, terse
spectra -v /Applications/Claude.app            # full per-app dump
spectra --all                                  # scan /Applications + Utilities
spectra list -v                                # same scan with an explicit subcommand
spectra --network -v /Applications/Cursor.app  # add embedded URL hosts
spectra --json /Applications/*.app | jq '.[] | select(.UI == "Tauri")'
```

### Output (table)

```
APP                           UI                   RUNTIME       PACKAGING   CONFIDENCE
----------------------------------------------------------------------------------------
Claude                        Electron             Node+Chromium Squirrel    high
```

Verbose mode adds indented detail blocks beneath each row, one block
per non-empty field.

## `spectra snapshot`

Captures a [system-inventory snapshot](../design/system-inventory.md)
of the local machine and persists it to `~/.spectra/spectra.db`
(SQLite, WAL mode). Snapshots include host facts, installed app
inspection, processes, JVMs, toolchains, network state, storage state,
power state, and selected sysctls. The relational store keeps summary
rows for apps, processes, login items, and granted privacy permissions
alongside the full JSON snapshot blob.

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Emit JSON instead of the human summary |
| `--network` | false | Forwarded to per-app `Detect()` |
| `--no-apps` | false | Skip installed-app inspection |
| `--no-store` | false | Do not persist the snapshot to the local database |
| `--baseline` | false | Save as an immutable baseline snapshot |
| `--name` | empty | Human label for the snapshot, usually with `--baseline` |

### Examples

```bash
spectra snapshot                   # full snapshot, persisted + printed
spectra snapshot --no-apps         # ~50ms, just the host facts
spectra snapshot --json | jq .host
spectra snapshot --no-store        # ephemeral — do not write to DB
```

## `spectra snapshot list`

Lists snapshots stored in `~/.spectra/spectra.db`, newest first.

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Emit JSON array |

### Examples

```bash
spectra snapshot list
spectra snapshot list --json | jq '.[0].id'
```

### Output

```
ID                                KIND      TAKEN AT              APPS
------------------------------------------------------------------------
snap-20260504T095749Z-4829        live      2026-05-04 09:57:49Z  61
snap-20260503T140012Z-1234        live      2026-05-03 14:00:12Z  58
```

## `spectra snapshot show <id>`

Prints the per-app table for one stored snapshot.

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Emit JSON |

### Examples

```bash
spectra snapshot show snap-20260504T095749Z-4829
spectra snapshot show snap-20260504T095749Z-4829 --json | jq .apps
```

### Output (human)

```
=== Spectra snapshot ===
id:             snap-20260504T095749Z-4829
kind:           live
taken-at:       2026-05-04T09:57:49Z

host:           mac.lan
machine-uuid:   6C8E2AC7-...
os:             macOS 15.6.1 (24G90)
cpu:            Apple M3 Max (16 cores, arm64)
ram:            128.0 GB
uptime:         1d 6h 20m
spectra:        v0.1.0

apps:           61 inspected
by-ui:
  AppKit                     9
  ComposeDesktop             3
  Electron                  17
  ...
```

## `spectra version`

Prints the build version (typically a git describe from `make build`).

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `2` | Bad usage (invalid flags or missing args) |

Per-app errors are written to stderr; the offending entry is dropped
and processing continues for other paths.

## Remaining planned subcommands

```
spectra connect <host>              # client targeting a remote daemon
```

See [../design/architecture.md](../design/architecture.md) for the
shape of the daemon RPC.
