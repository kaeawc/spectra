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
| `baseline` | Manage baseline snapshots (alias for `snapshot baseline`) |
| `rules` | Evaluate recommendation rules against a snapshot |
| `issues` | List, check, or update persisted recommendation issues |
| `jvm` | List or inspect running JVM processes |
| `toolchain` | Show installed language runtimes and package managers |
| `network` | Show current routes, DNS, VPN, proxy, and listening ports |
| `power` | Show current battery and thermal state |
| `storage` | Show disk volumes and `~/Library` footprint |
| `process` | List running processes sorted by memory |
| `serve` | Run the local Unix-socket JSON-RPC daemon |
| `connect` | Call a Spectra daemon over Unix socket or TCP JSON-RPC |
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
spectra snapshot create            # explicit form; same as `snapshot`
spectra snapshot --no-apps         # ~50ms, just the host facts
spectra snapshot create --baseline pre-incident
spectra snapshot --json | jq .host
spectra snapshot --no-store        # ephemeral — do not write to DB
```

`spectra snapshot create` is an explicit alias for `spectra snapshot`.
When `--baseline` is set, a single positional argument is accepted as
the baseline name; `--name pre-incident` remains equivalent.

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

## `spectra diff`

Diffs two stored snapshots. `live` can be used as either side to
capture an ephemeral current snapshot without storing it.

### Examples

```bash
spectra diff snap-20260504T095749Z-4829 live
spectra diff baseline                  # newest baseline against live
spectra diff baseline pre-incident live
```

`spectra diff baseline <name|id> [live|id]` resolves the first operand
to a baseline by ID first, then by baseline name. When the name/id is
omitted, the newest baseline is used.

## `spectra snapshot prune`

Deletes old live snapshots beyond the retention limit. Baselines are
never pruned.

| Flag | Default | Description |
|---|---|---|
| `--keep` | `100` | Number of newest live snapshots to retain |
| `--json` | false | Emit JSON |

### Examples

```bash
spectra snapshot prune
spectra snapshot prune --keep 25 --json
```

## `spectra baseline`

Convenience alias for `spectra snapshot baseline`.

### Examples

```bash
spectra baseline list
spectra baseline drop snap-20260504T095749Z-4829
```

## `spectra jvm`

Lists or inspects running JVM processes and exposes JDK-tool diagnostics.

### Examples

```bash
spectra jvm
spectra jvm --json
spectra jvm 4012
spectra jvm thread-dump 4012
spectra jvm heap-histogram 4012
spectra jvm heap-dump --out /tmp/app.hprof 4012
spectra jvm gc-stats --json 4012
spectra jvm jfr start 4012 --name spectra
spectra jvm jfr dump 4012 --name spectra --out /tmp/app.jfr
spectra jvm jfr summary --json /tmp/app.jfr
spectra jvm jfr stop 4012 --name spectra
```

## `spectra network`

Shows unprivileged network state by default. `spectra network firewall`
asks the privileged helper for current pf firewall rules.

### Examples

```bash
spectra network
spectra network --json
spectra network connections --proto tcp --state established
spectra network firewall
spectra network firewall --json
```

## `spectra serve`

Runs the JSON-RPC daemon. By default it listens only on the current
user's Unix socket at `~/.spectra/sock`.

| Flag | Default | Description |
|---|---|---|
| `--sock` | `~/.spectra/sock` | Unix socket path |
| `--tcp` | empty | Optional TCP listen address, such as `127.0.0.1:7878` |
| `--allow-remote` | false | Allow `--tcp` to bind a non-loopback address |

TCP RPC has no Spectra-layer authentication today. Keep it on loopback
for local use or expose it only through SSH, Tailscale, or firewall
controls you trust.

### Examples

```bash
spectra serve
spectra serve --tcp 127.0.0.1:7878
spectra serve --tcp 100.64.0.5:7878 --allow-remote
```

## `spectra connect`

Calls a running daemon using the same JSON-RPC protocol as the local
Unix-socket client. This is the implemented remote transport today;
automatic tsnet registration and cross-host fan-out remain future work.

| Target | Meaning |
|---|---|
| `local` | Default local Unix socket |
| `unix:/path/to/sock` | Explicit Unix socket |
| `/path/to/sock` | Explicit Unix socket shorthand |
| `host:port` | TCP daemon |
| `host` | TCP daemon on port `7878` |

| Form | Description |
|---|---|
| `spectra connect <target>` | Call `health` |
| `spectra connect <target> status` | Call `health` |
| `spectra connect <target> call <method> [json-params]` | Call an RPC method directly |

### Examples

```bash
spectra connect local
spectra connect 127.0.0.1:7878 status
spectra connect work-mac call snapshot.create
spectra connect work-mac call inspect.app '{"path":"/Applications/Slack.app"}'
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
