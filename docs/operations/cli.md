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
| `snapshot` | Capture a structured snapshot of host + installed apps |
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
of the local machine. Today only `host` info and the `apps` slice are
populated; processes / JVMs / JDKs / toolchains / network / storage
state will land alongside the daemon.

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Emit JSON instead of the human summary |
| `--network` | false | Forwarded to per-app `Detect()` |
| `--no-apps` | false | Skip the apps inventory (host-only, very fast) |

### Examples

```bash
spectra snapshot                # full snapshot, human-readable
spectra snapshot --no-apps      # ~50ms, just the host facts
spectra snapshot --json | jq .host
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

## Planned subcommands (daemon era)

```
spectra serve                       # run the collector daemon
spectra connect <host>              # client targeting a remote daemon
spectra list                        # alias for "scan all bundles via local daemon"
spectra diff <baseline-id> [host]   # snapshot diff
spectra jvm [pid]                   # JVM inspection
spectra cache stats / clear         # blob cache management
spectra install-helper              # privileged helper install (sudo)
```

See [../design/architecture.md](../design/architecture.md) for the
shape of the daemon RPC.
