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
| `fan` | Run one daemon RPC call against multiple explicit targets |
| `hosts` | List hosts known from stored snapshots |
| `status` | Check whether the local daemon is running |
| `metrics` | Show stored process metrics from daemon sampling |
| `sample` | Collect a user-space CPU sample of a process |
| `cache` | Manage the local blob cache |
| `install-helper` | Install the privileged helper daemon |
| `install-daemon` | Install the user LaunchAgent for `spectra serve` |
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

`snapshot diff` also supports comparing snapshots across hosts when a
local match is not found: a bare host token resolves to that host's most
recent stored snapshot.

### Examples

```bash
spectra diff snap-20260504T095749Z-4829 live
spectra diff workstation work-mac                # latest snapshot on each host
spectra diff workstation@snap-20260504T095749Z-4829 work-mac@snap-20260503T170201Z-9932
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

## `spectra rules`

Evaluates the recommendations catalog against a live snapshot or a stored
snapshot. `./spectra.yml` is loaded automatically when present; use
`--rules-config` to point at a different override file.

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Emit JSON findings |
| `--snapshot` | empty | Evaluate a stored snapshot by ID |
| `--rules-config` | `./spectra.yml` if present | Rule override file |

Supported `spectra.yml` rule overrides:

```yaml
rules:
  disabled:
    - app-unsigned
  severity:
    jvm-eol-version: high
```

### Examples

```bash
spectra rules
spectra rules --json
spectra rules --snapshot snap-20260504T095749Z-4829
spectra rules --rules-config team-spectra.yml
```

## `spectra issues`

Persists recommendation findings as issues and lets users manage their
lifecycle. `spectra issues check` accepts the same `--snapshot` and
`--rules-config` flags as `spectra rules`.

### Examples

```bash
spectra issues check
spectra issues check --rules-config team-spectra.yml
spectra issues list --status open
spectra issues acknowledge issue-123
spectra issues dismiss issue-123
spectra issues update --status fixed issue-123
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

Shows unprivileged network state by default, including current routes,
DNS, VPN state, listening ports, and active per-process throughput from
`nettop`. Listening ports include bind address and process attribution when
`lsof` exposes it. `spectra network firewall` asks the privileged helper for
current pf firewall rules.

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
| `--tsnet` | false | Join the tailnet as a managed tsnet node |
| `--tsnet-addr` | `:7878` | Tailnet listen address for tsnet |
| `--tsnet-hostname` | local hostname | Tailnet hostname advertised through MagicDNS |
| `--tsnet-state-dir` | `~/.spectra/tsnet` | tsnet state directory |
| `--tsnet-ephemeral` | false | Register the tsnet node as ephemeral |
| `--tsnet-tags` | empty | Comma-separated Tailscale tags to advertise |
| `--log-file` | `~/Library/Logs/Spectra/daemon.jsonl` | JSONL daemon log path |
| `--no-log-file` | false | Disable daemon JSONL logging |
| `--daemon` | false | Start detached and return |

TCP RPC has no Spectra-layer authentication today. Keep it on loopback
for local use or expose it only through SSH, Tailscale, or firewall
controls you trust. `--tsnet` uses Tailscale identity and ACLs; first-run
enrollment uses existing tsnet state, `TS_AUTHKEY`, or a login URL written
to the daemon log or stderr.

### Examples

```bash
spectra serve
spectra serve --log-file /tmp/spectra-daemon.jsonl
spectra serve --no-log-file
spectra serve --tcp 127.0.0.1:7878
spectra serve --tcp 100.64.0.5:7878 --allow-remote
spectra serve --tsnet --tsnet-hostname work-mac
spectra serve --tsnet --tsnet-hostname work-mac --tsnet-tags tag:engineer
```

## `spectra install-daemon`

Installs a per-user LaunchAgent that runs `spectra serve` through
launchd. The plist is written to
`~/Library/LaunchAgents/dev.spectra.daemon.plist`; stdout/stderr from
launchd go to `~/Library/Logs/Spectra/daemon.launchd.*.log`.

| Form | Description |
|---|---|
| `spectra install-daemon [serve flags]` | Write, bootstrap, enable, and kickstart the LaunchAgent |
| `spectra install-daemon --no-load [serve flags]` | Write the plist without loading it |
| `spectra install-daemon print-plist [serve flags]` | Print the plist that would be installed |
| `spectra install-daemon status` | Run `launchctl print` for the agent |
| `spectra install-daemon uninstall` | Boot out and remove the LaunchAgent plist |

The install and `print-plist` forms accept the serve-listener flags
`--sock`, `--tcp`, `--allow-remote`, `--tsnet`, `--tsnet-addr`,
`--tsnet-hostname`, `--tsnet-state-dir`, `--tsnet-ephemeral`,
`--tsnet-tags`, `--log-file`, and `--no-log-file`.

### Examples

```bash
spectra install-daemon
spectra install-daemon --tcp 127.0.0.1:7878
spectra install-daemon --tsnet --tsnet-hostname work-mac
spectra install-daemon print-plist --no-log-file
spectra install-daemon status
spectra install-daemon uninstall
```

## `spectra connect`

Calls a running daemon using the same JSON-RPC protocol as the local
Unix-socket client. Targets can be local sockets, explicit TCP listeners,
or MagicDNS names for daemons started with `--tsnet`.

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
| `spectra connect <target> host` | Call `inspect.host` |
| `spectra connect <target> inspect <App.app>` | Call `inspect.app` |
| `spectra connect <target> jvm` | Call `jvm.list` |
| `spectra connect <target> jvm <pid>` | Call `jvm.inspect` |
| `spectra connect <target> jvm-gc <pid>` | Call `jvm.gc_stats` |
| `spectra connect <target> jvm-threads <pid>` | Call `jvm.thread_dump` |
| `spectra connect <target> jvm-heap <pid>` | Call `jvm.heap_histogram` |
| `spectra connect <target> jvm-heap-dump <pid> [dest]` | Call `jvm.heap_dump` |
| `spectra connect <target> metrics` | Call `process.live` |
| `spectra connect <target> metrics <pid> [limit]` | Call `process.history` |
| `spectra connect <target> processes` | Call `process.list` |
| `spectra connect <target> processes <App.app>` | Call `process.list` scoped to bundles |
| `spectra connect <target> process-tree [App.app ...]` | Call `process.tree` |
| `spectra connect <target> sample <pid> [duration] [interval]` | Call `process.sample` |
| `spectra connect <target> network` | Call `network.state` |
| `spectra connect <target> connections` | Call `network.connections` |
| `spectra connect <target> network-by-app [App.app ...]` | Call `network.byApp` |
| `spectra connect <target> storage` | Call `storage.system` |
| `spectra connect <target> storage <App.app> [more.apps]` | Call `storage.byApp` |
| `spectra connect <target> power` | Call `power.state` |
| `spectra connect <target> rules [snapshot-id]` | Call `rules.check` |
| `spectra connect <target> issues check [snapshot-id]` | Call `issues.check` |
| `spectra connect <target> issues <machine-id> [status]` | Call `issues.list` |
| `spectra connect <target> issues list <machine-id> [status]` | Call `issues.list` |
| `spectra connect <target> issues update <issue-id> <status>` | Call `issues.update` |
| `spectra connect <target> issues acknowledge <issue-id>` | Call `issues.acknowledge` |
| `spectra connect <target> issues dismiss <issue-id>` | Call `issues.dismiss` |
| `spectra connect <target> jvm-jfr-start <pid> [name]` | Call `jvm.jfr.start` |
| `spectra connect <target> jvm-jfr-dump <pid> <dest> [name]` | Call `jvm.jfr.dump` |
| `spectra connect <target> jvm-jfr-stop <pid> [dest]` | Call `jvm.jfr.stop` |
| `spectra connect <target> jvm-jfr-summary <path>` | Call `jvm.jfr.summary` |
| `spectra connect <target> snapshot` | Call `snapshot.create` |
| `spectra connect <target> snapshot list` | Call `snapshot.list` |
| `spectra connect <target> snapshot get <id>` | Call `snapshot.get` |
| `spectra connect <target> snapshot diff <id-a> <id-b>` | Call `snapshot.diff` |
| `spectra connect <target> snapshot processes <id>` | Call `snapshot.processes` |
| `spectra connect <target> snapshot login-items <id>` | Call `snapshot.login_items` |
| `spectra connect <target> snapshot granted-perms <id>` | Call `snapshot.granted_perms` |
| `spectra connect <target> snapshot prune [keep]` | Call `snapshot.prune` |
| `spectra connect <target> toolchains` | Call `toolchain.scan` |
| `spectra connect <target> jdk` | Call `jdk.list` |
| `spectra connect <target> brew` | Call `toolchain.brew` |
| `spectra connect <target> runtimes` | Call `toolchain.runtimes` |
| `spectra connect <target> build-tools` | Call `toolchain.build_tools` |
| `spectra connect <target> cache` | Call `cache.stats` |
| `spectra connect <target> cache stats` | Call `cache.stats` |
| `spectra connect <target> cache clear [kind]` | Call `cache.clear` |
| `spectra connect <target> call <method> [json-params]` | Call an RPC method directly |

### Examples

```bash
spectra connect local
spectra connect 127.0.0.1:7878 status
spectra connect work-mac inspect /Applications/Slack.app
spectra connect work-mac jvm
spectra connect work-mac jvm-threads 4012
spectra connect work-mac metrics
spectra connect work-mac metrics 4012 120
spectra connect work-mac cache
spectra connect work-mac cache clear detect
spectra connect work-mac issues local-machine
spectra connect work-mac issues local-machine open
spectra connect work-mac issues update issue-123 fixed
spectra connect work-mac issues acknowledge issue-123
spectra connect work-mac issues dismiss issue-456
spectra connect work-mac jvm-jfr-start 4012
spectra connect work-mac jvm-jfr-start 4012 spectra
spectra connect work-mac jvm-jfr-dump 4012 /tmp/recording.jfr spectra
spectra connect work-mac jvm-jfr-stop 4012 /tmp/recording.jfr
spectra connect work-mac jvm-jfr-summary /tmp/recording.jfr
spectra connect work-mac jvm-heap-dump 4012
spectra connect work-mac jvm-heap-dump 4012 /tmp/heap.hprof
spectra connect work-mac processes
spectra connect work-mac network
spectra connect work-mac storage /Applications/Slack.app
spectra connect work-mac issues check
spectra connect work-mac issues check snap-1
spectra connect work-mac toolchains
spectra connect work-mac snapshot
spectra connect work-mac snapshot diff snap-before snap-after
spectra connect work-mac call jvm.heap_dump '{"pid":4012,"confirm_sensitive":true}'
```

## `spectra hosts`

Lists hosts already known to the local SQLite store from persisted
snapshots. This is not live daemon discovery yet; it is the local record
of machines Spectra has seen. `spectra fan` uses this list when
`--hosts` is omitted.

| Flag | Default | Meaning |
|---|---:|---|
| `--json` | false | Emit JSON instead of a table |
| `--probe` | false | Probe each host with `health` RPC and show reachability |
| `--discover` | false | Merge tailscale peers from `tailscale status --json` into host list |

### Examples

```bash
spectra hosts
spectra hosts --json
spectra hosts --probe
spectra hosts --probe --json
spectra hosts --discover
``` 

## `spectra fan`

Runs one `spectra connect` call against multiple daemon targets in
parallel and prints a JSON envelope with one result per target. `--hosts`
is optional; when omitted, fan-out uses hosts from the local store.

| Flag | Default | Meaning |
|---|---:|---|
| `--hosts` | optional | Comma-separated daemon targets (`local`, `host`, `host:port`, `unix:/path`). Omit to use hosts from local `spectra hosts` data. |
| `--probe` | false | Probe each target with `health` RPC and skip unreachable hosts when true. |
| `--discover` | false | Include tailscale peers from `tailscale status --json` and merge with local-known hosts. |
| `--timeout` | `3s` | Dial/read timeout per target |

The command accepts the same typed shortcuts and raw `call` form as
`spectra connect`.

### Examples

```bash
spectra fan --hosts work-mac,alice-laptop status
spectra fan --hosts work-mac,alice-laptop inspect /Applications/Slack.app
spectra fan --hosts work-mac,alice-laptop jvm
spectra fan --hosts work-mac,alice-laptop jdk
spectra fan --hosts work-mac,alice-laptop snapshot
spectra fan --hosts work-mac,alice-laptop call network.connections
spectra fan inspect /Applications/Slack.app
spectra fan --discover status
spectra fan --discover --probe inspect /Applications/Slack.app
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
