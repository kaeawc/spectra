# CLI reference

The `spectra` binary dispatches to one of several subcommands. The
default (no subcommand) inspects the `.app` bundles passed as
positional args, preserving compatibility with the original flag-only
shape.

## Synopsis

```
spectra <subcommand> [flags] [args]
spectra [flags] <App.app>...     # routes to `inspect` (default)
spectra --remote <target> <subcommand> [args]
```

`--remote`, `--target`, and `--rpc-target` are global client flags. When one
is present before the subcommand, Spectra translates the command to a daemon
JSON-RPC call and prints indented JSON. Without one of those flags, commands
run in-process on the local machine.

`--verbose` (alias `--debug`, or the `SPECTRA_DEBUG` environment variable) is a
global flag that routes otherwise-silent enhancement and collection failures —
such as history-attach or cache errors — to stderr. It can appear before or
after the subcommand and does not affect stdout, so JSON output stays parseable.
The short `-v` is unaffected: it still means "show detection signals" for
`inspect`.

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
| `db` | Discover and inspect databases apps connect to (read-only; postgres) |
| `toolchain` | Show installed language runtimes and package managers |
| `network` | Show current routes, DNS, VPN, proxy, and listening ports |
| `power` | Show current battery and thermal state |
| `storage` | Show disk volumes and `~/Library` footprint |
| `process` | List running processes sorted by memory |
| `crash` | Audit post-mortem readiness before a crash happens |
| `web` | Diff an Electron app's `app.asar` payload between versions |
| `whatswrong` | Ranked whole-system triage: why is this Mac slow right now? |
| `playbook` | Show diagnostic playbooks and command plans |
| `serve` | Run the local Unix-socket JSON-RPC daemon |
| `connect` | Call a Spectra daemon over Unix socket or TCP JSON-RPC |
| `fan` | Run one daemon RPC call against multiple explicit targets |
| `hosts` | List hosts known from stored snapshots |
| `fleet` | Cross-host symptom rollups and version-drift matrices |
| `bisect` | Find the snapshot where a rule started firing, and what changed alongside it |
| `reconcile` | Print an advisory plan to make one host's toolchain match another's |
| `status` | Check whether the local daemon is running |
| `metrics` | Show stored process metrics from daemon sampling |
| `sample` | Collect a user-space CPU sample of a process |
| `cache` | Manage the local blob cache |
| `install-helper` | Install the privileged helper daemon |
| `install-daemon` | Install the user LaunchAgent for `spectra serve` |
| `schedule` | Schedule periodic snapshot capture via a launchd agent |
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
spectra --remote work-mac inspect /Applications/Slack.app
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
| `--rules` | empty | Comma-separated YAML rule files or globs to add to the catalog |
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
spectra rules --rules rules/*.yml
spectra rules --rules-config team-spectra.yml
spectra rules validate --rules rules/*.yml
spectra rules list --rules rules/*.yml
spectra rules explain --rules rules/*.yml jvm-eol-version
```

## `spectra issues`

Persists recommendation findings as issues and lets users manage their
lifecycle. `spectra issues check` accepts the same `--snapshot`, `--rules`,
and `--rules-config` flags as `spectra rules`.

### Examples

```bash
spectra issues check
spectra issues check --rules rules/*.yml
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
spectra jvm explain --samples 6 --interval 10s 4012
spectra jvm thread-dump --summary 4012
spectra jvm heap-histogram --suspects 20 4012
spectra jvm heap-dump --out /tmp/app.hprof 4012
spectra jvm gc-stats --json 4012
spectra jvm vm-memory --json 4012
spectra jvm jmx status 4012
spectra jvm jmx start-local 4012
spectra jvm flamegraph --event wall --duration 30 --out /tmp/app.html 4012
spectra jvm jfr start 4012 --name spectra
spectra jvm jfr dump 4012 --name spectra --out /tmp/app.jfr
spectra jvm jfr summary --json /tmp/app.jfr
spectra jvm jfr analyze /tmp/app.jfr
spectra jvm jfr view gc-pauses /tmp/app.jfr
spectra jvm jfr stop 4012 --name spectra
```

## `spectra runtime`

Identifies a live process's language runtime and lists the diagnostics available
for it — one entry point when you have a PID but don't yet know whether it's a
JVM, Node/Electron, Go, .NET, Python, or a native binary. It classifies from the
executable path, the full command line, and two cheap pure-Go probes: the
embedded Go build info (`debug/buildinfo`) fingerprints a Go binary, and a
`dotnet-diagnostic-<pid>-*-socket` in the temp directory identifies .NET. It
reports the evidence that decided the class and routes to the next step — an
existing Spectra command where one exists (`spectra jvm <pid>`,
`spectra web processes`) or the standard technique otherwise (SIGUSR1 + CDP for
Node/Electron, a SIGQUIT goroutine dump or net/http/pprof for Go, the
`dotnet-*` diagnostic tools for .NET, `py-spy` for Python) — always including
the universal `spectra sample <pid>` fallback. It is read-only: it advises, and
never signals, attaches to, or profiles the process itself.

### Examples

```bash
spectra runtime 4012
spectra runtime attach 4012
spectra runtime --json 4012
```

## `spectra db`

Discovers databases that apps on this host connect to, then inspects them
read-only: schema, foreign-key relationships, and per-table health stats.
PostgreSQL, MySQL/MariaDB, SQLite, MongoDB, and Redis/Valkey are supported; the engine is inferred from the
DSN (`postgres://` / `mysql://` / `sqlite://` / `mongodb://` / `redis://` URLs, go-sql-driver's `user@tcp(host)/db`
form, a bare `.db`/`.sqlite` path, or libpq keyword form). Sessions are forced read-only with statement
and lock timeouts, so inspection cannot write or stall the server.
Connection strings resolve from `--dsn`, then `SPECTRA_DB_DSN`, then
`DATABASE_URL`, then libpq `PG*` env vars. `sample` reads row data, which
may contain PII — it is recorded in the artifact manifest at `very-high`
sensitivity.

### Examples

```bash
spectra db discover
spectra db overview --dsn postgres://app@10.0.0.5/orders
spectra db schema --schema public --json
spectra db relations
spectra db stats
spectra db sample --limit 20 billing.invoices
```

See [../inspection/databases.md](../inspection/databases.md).

## `spectra xattr-inspect`

Surfaces the macOS extended attributes that record a file's origin and security
state — normally invisible without `xattr`/`mdls`. Each argument is a file or a
directory (directories are walked, bounded). For each file it lists the notable
extended attributes present, each classified (quarantine / provenance /
where-froms / security / other), parses the `com.apple.quarantine` record into
the downloading agent, timestamp, and flags, extracts the `where-froms` source
URLs from the attribute value, and flags whether an AppleDouble `._` sidecar
exists. The summary counts files with quarantine, provenance, where-froms, and
AppleDouble sidecars. It is read-only and never modifies an attribute.

### Examples

```bash
spectra xattr-inspect ~/Downloads/installer.dmg
spectra xattr-inspect --json /Applications/Some.app
spectra xattr-inspect ~/Downloads
```

## `spectra symbolicate`

Resolves raw stack addresses to `symbol + file:line` using `atos`, against a
Mach-O image or its `.dSYM`. This is the enabling primitive for the
native-capture features — a captured stack (from a core, a crash report, or a
spindump) is just hex until it is symbolicated. Give the image with `-o`, its
load address with `-l`, and the addresses as arguments or on stdin (one per
line). For each address it reports the resolved symbol and `file:line` when
`atos` provides them; an address `atos` cannot resolve (it echoes the address
back) is reported as unresolved rather than dropped. `--arch` selects a slice of
a universal binary. Read-only, no root.

### Examples

```bash
spectra symbolicate -o /Applications/Some.app/Contents/MacOS/Some -l 0x10a400000 0x10a4b31a0 0x10a4b3200
spectra symbolicate -o ./Some.app.dSYM -l 0x10a400000 --arch arm64 --json 0x10a4b31a0
lldb-derived-addresses.txt | spectra symbolicate -o ./Some -l 0x10a400000
```

## `spectra spindump`

Captures and summarizes a `spindump` report — per-thread call trees with
per-frame sample counts — which is otherwise hundreds of lines of indented text.
The parser is the core and works two ways: `--input <file>` parses a report you
already have (no root), and `spectra spindump <pid>` captures one. Because
`spindump` must run as root to sample a live process, `--sudo` prepends `sudo`,
and a permission failure is reported as "run with --sudo (or as root)" rather
than a raw error. The summary shows the capture duration and, per process, its
name, pid, and the heaviest symbols (top frames by sample count, with thread and
dispatch-queue headers excluded). `--out <file>` also saves the raw report; raw
frame addresses can be fed through `spectra symbolicate`. `--json` emits the
structured result.

### Examples

```bash
spectra spindump --duration 3 --sudo 4012
spectra spindump --input /tmp/foo.spindump.txt
spectra spindump --sudo --out /tmp/foo.txt --json 4012
```

## `spectra vmmap`

Summarizes a process's memory composition from `vmmap --summary` — a footprint
number has no shape on its own, and the raw table is a wall of columns. It
reports the physical footprint and its peak, then per region type the
virtual / resident / dirty / swapped sizes, ranked by dirty size (the real
memory cost), top `--top` (default 8), plus the TOTAL row. `vmmap --summary`
works without root for a same-user process; for another user's process it needs
root, so a permission failure is reported as "re-run as root (sudo)". `--json`
emits the structured result.

### Examples

```bash
spectra vmmap 4012
spectra vmmap --top 15 4012
spectra vmmap --json 4012
```

## `spectra lsmp`

Summarizes a process's Mach port table from `lsmp -p`. A Mach port leak — a
service accumulating send rights until it hits the per-task limit and wedges —
is invisible in the raw per-port table; this counts port entries by right type
(recv / send / send-once / port-set) and flags a suspiciously high total. The
parser keys on the leading hex port name and the documented rights keyword, so
column-spacing differences between macOS versions don't break it. `lsmp`
requires root (it uses `task_for_pid`), so `--sudo` prepends `sudo` and a
permission failure is reported as "re-run with --sudo (or as root)". `--json`
emits the structured result.

### Examples

```bash
spectra lsmp --sudo 4012
spectra lsmp --json --sudo 4012
```

## `spectra power`

Shows current host power and thermal state: AC/battery source, battery
percentage, thermal pressure, active sleep/display assertions, and a short
per-process energy-impact sample. This command is about power state, not
hardware fan control. For remote multi-host execution, use `spectra fan ... power`;
`fan` means fan-out to multiple daemons.

### Examples

```bash
spectra power
spectra power --json
spectra connect work-mac power
spectra fan --hosts work-mac,alice-laptop power
```

See [../inspection/power-thermal.md](../inspection/power-thermal.md).

### `spectra power log`

Where `spectra power` shows the instantaneous state, `spectra power log`
shows power *history*: recent sleep / wake / dark-wake events (with the
wake reason) from `pmset -g log`, and which process is currently holding a
sleep-preventing assertion (`pmset -g assertions`) — the answer to "what
kept my Mac awake / drained it overnight?". Both reads are unprivileged.
`-n` caps the number of events shown; `--json` emits the structured report.

```bash
spectra power log
spectra power log -n 40
spectra power log --json
```

## `spectra network`

Shows unprivileged network state by default, including current routes,
DNS, VPN state, listening ports, and active per-process throughput from
`nettop`. Listening ports include bind address and process attribution when
`lsof` exposes it. `spectra network capture` asks the privileged helper for
bounded tcpdump captures and can summarize completed pcap files without
retaining request or response bodies. `spectra network firewall` asks the
privileged helper for current pf firewall rules.
`spectra network diagnose` starts from a running application, PID, or command,
infers endpoints from that app's current sockets, and joins per-process
throughput, DNS, route/proxy/VPN context, TCP/TLS probes, and traceroute
output. Positional hosts and `--ports` narrow the inferred app endpoints; when
no app scope is provided, positional hosts can be used as explicit probe
targets.

For each TLS endpoint the probe reports the full presented certificate chain
(subject, issuer, validity, days-to-expiry, and the base64 SHA-256 SPKI pin per
certificate, so pins can be compared against an app's pinned set), the leaf key
type/size and signature algorithm, and whether the chain validates against the
macOS system trust store (`trust=valid` / `trust=UNTRUSTED` with the reason).
It flags a leaf `expires_in` within 21 days and marks likely TLS interception
(`intercepted=…`) when the leaf is issued by a known interception vendor, is
self-signed (verified by signature, not just name), or does not chain to a
trusted root. An interception root that has been installed into the system
trust store under an unrecognized name validates as trusted and is only caught
by the vendor-name check — pure Go exposes no per-anchor trust provenance to
tell a private/enterprise root from a public CA. When library verification
fails because the chain is untrusted or expired, the presented certificates are
recovered from the verification error so the chain is still captured and
explained (`trust=UNTRUSTED trust_error=…`) rather than hidden behind a
handshake failure.

`spectra network captive` replicates Apple's captive-portal probe: it GETs
`http://captive.apple.com/hotspot-detect.html` over plain HTTP **without
following redirects**, with a short timeout and a bounded body read, and
classifies the link. It reports `CLEAR` only when the response is `200` with
Apple's canonical success page. It flags a `CAPTIVE PORTAL` when the probe is
redirected (3xx + Location), returns `511 Network Authentication Required`, or
returns `200` with a body that isn't the success page — the pattern of a hotel
or airport login gate that makes a dead link look "up". A `Via` (or proxy
`Server`) header marks the link as `PROXIED` even when the success page is
returned, revealing a transparent proxy. The command exits non-zero when a
portal is detected or the probe fails, so scripts can gate on real connectivity.

### Examples

```bash
spectra network
spectra network --json
spectra network connections --proto tcp --state established
spectra network diagnose --app /Applications/Slack.app
spectra network diagnose --pid 412
spectra network diagnose --app /Applications/Slack.app --ports 443 api.example.com
spectra network captive
spectra network captive --json
spectra network capture start --interface en0 --duration 30s --proto tcp --host api.example.com --port 443
spectra network capture stop --summarize netcap-1
spectra network capture summarize --json /var/tmp/spectra-netcap/501/netcap-1.pcap
spectra network firewall
spectra network firewall --json
```

## `spectra storage db-check`

Inspects the health of embedded SQLite databases — the state stores macOS apps
scatter through `~/Library/Application Support` and `~/Library/Containers`.
Each argument is a database file or a directory (directories are walked for
files with the SQLite header). Every database is opened **read-only** — the
file under inspection is never written — and reported with its size, page
geometry, journal mode, free-page fragmentation, the size of any
un-checkpointed `-wal` sidecar, and the result of `PRAGMA quick_check`. A
database is counted as a problem when it fails integrity, carries WAL bloat
over 32 MiB, or has a freelist above 25% of its pages; one that cannot be
opened (missing, locked by a live writer, or not a database) is reported with
its error instead of failing the whole run.

### Examples

```bash
spectra storage db-check ~/Library/Messages/chat.db
spectra storage db-check --json "$HOME/Library/Application Support/MyApp"
spectra storage db-check app1.db app2.db
```

## `spectra storage cache-triage`

Classifies each subdirectory of a cache root (default `~/Library/Caches`) so you
can reclaim space without deleting real data. `~/Library/Caches` is usually the
largest safe win, but some apps stash cookies, tokens, or SQLite state there, so
"delete all caches" is risky. Each subdirectory is sized and classed as:

- **safe** — a pure build/tooling cache that fully reconstructs from source or
  network (Go build cache, npm/yarn/pnpm, pip, CocoaPods, Homebrew, node-gyp,
  Playwright, …);
- **regenerable** — an ordinary app runtime cache (the default), safe to delete
  and rebuilt on demand;
- **risky** — a subtree containing markers of real data (cookies, credentials or
  tokens, `.sqlite`/`.db`, LevelDB) that should be reviewed before deletion.

Risky data outweighs a safe-looking name, so the classification is conservative.
The command is read-only — it never deletes anything — and reports total cache
bytes, reclaimable bytes (safe + regenerable), and the risky bytes held back.

### Examples

```bash
spectra storage cache-triage
spectra storage cache-triage --json
spectra storage cache-triage "$HOME/Library/Containers/com.example.App/Data/Library/Caches"
```

## `spectra crash readiness`

Audits whether the machine can produce a debuggable crash *before* one
happens, so you don't discover an unrecoverable configuration after the
fact. Checks the host crash-capture prerequisites — `kern.coredump`,
`kern.corefile`, the `RLIMIT_CORE` soft limit, whether `/cores` exists, and
`com.apple.CrashReporter` `DialogType` — and, when given an app path, whether
that app is attachable (hardened runtime plus the
`com.apple.security.get-task-allow` entitlement). Each finding is `ok`,
`warn`, or `critical` with a concrete fix. Purely diagnostic; it never
changes system settings.

### Examples

```bash
spectra crash readiness
spectra crash readiness --json
spectra crash readiness /Applications/MyApp.app
```

## `spectra crash list`

Sweeps the DiagnosticReports directory macOS writes crash reports into and
prints a newest-first inventory — the answer to "what has been crashing on
this Mac lately?" without opening Console. It recursively walks
`~/Library/Logs/DiagnosticReports/` (including `Retired/` and dotfile
reports), parses each `.ips` via the crash decoder, and skips legacy /
unparseable files rather than failing. `--limit` caps the rows, `--dir`
overrides the directory, and `--json` emits the structured inventory.

### Examples

```bash
spectra crash list
spectra crash list --limit 50 --json
```

## `spectra crash signatures`

Groups the crash reports from `crash list` into recurring **signatures** —
keyed on process + exception + top faulting frame — and ranks them by
occurrence, so a flaky app's dozen identical crashes collapse into one line
with a count and a first-seen/last-seen window. Each signature shows a
sample report for drill-down. `--json` structured; `--limit`/`--dir` as with
`crash list`.

### Examples

```bash
spectra crash signatures
spectra crash signatures --json
```

## `spectra crash state`

Reconstructs what the machine was doing around a crash. Given a `.ips`
report, it finds the **nearest stored snapshot by time** (within `--window`,
default 1h) and shows the machine context at that moment — top processes by
RSS and thermal state — reported as **correlational, not cause**, and
bounded by snapshot cadence. `--host` selects the host; `--json` structured.
A precursor to a unified incident timeline.

### Examples

```bash
spectra crash state ~/Library/Logs/DiagnosticReports/MyApp-2026.ips
spectra crash state --window 30m --json report.ips
```

## `spectra crash oom`

Filters the crash inventory to **memory / OOM terminations** and attributes
each. The reliable path is `EXC_RESOURCE(MEMORY)` — a high-watermark memory
kill, already decoded — surfaced with the process, the limit/observed, and
the plain-language reason; jetsam / low-memory bug_types are surfaced
best-effort. Ordinary segfaults are excluded. `--json` structured;
`--limit`/`--dir` as with `crash list`.

### Examples

```bash
spectra crash oom
spectra crash oom --json
```

## `spectra crash inspect`

Decodes a modern macOS `.ips` crash report (the JSON reports written to
`~/Library/Logs/DiagnosticReports`) into a readable summary — the decoded
exception and termination reason, the faulting thread, and its stack with
frames resolved to image + symbol — without opening Console.app. `--all`
shows every thread; `--json` emits the structured report. Legacy pre-2020
plain-text `.crash` reports are detected and reported as such.

### Examples

```bash
spectra crash inspect ~/Library/Logs/DiagnosticReports/MyApp-2026-08-05-101500.ips
spectra crash inspect --all report.ips
spectra crash inspect --json report.ips
```

## `spectra crash resource`

Decodes an `EXC_RESOURCE` / watchdog-class kill — a `.cpu_resource` /
`.wakeups_resource` report, or an `EXC_RESOURCE` termination inside an
ordinary crash report. These read like mystery crashes but are the OS
killing the process for exceeding a CPU-time, wakeups, I/O, or memory
ledger limit. The command names the resource flavor, explains the cause in
plain language, surfaces the limit/observed/window when the report carries
them, and prints the offending thread's stack. `--json` emits the
structured report.

### Examples

```bash
spectra crash resource ~/Library/Logs/DiagnosticReports/Helper-2026-08-05.cpu_resource.ips
spectra crash resource --json report.ips
```

## `spectra web asar-diff`

Diffs two Electron apps' `app.asar` payloads to answer "what changed in
this app's JavaScript when it auto-updated?" — a question Activity Monitor
and the extract-only `asar` CLI cannot. It parses each archive's header
(read-only; no extraction) into a file inventory with per-file SHA256, then
reports added/removed/changed files plus the capability drift that matters:
new npm packages, new native `.node` add-ons, and new embedded endpoint
hosts introduced by the update. `--json` emits the structured diff.

### Examples

```bash
spectra web asar-diff /Volumes/backup/Slack.app /Applications/Slack.app
spectra web asar-diff --json ./old/App.app ./new/App.app
```

## `spectra web fuses`

Audits an Electron app's build-time security **fuses** — toggles baked into
the framework binary. An app that ships with `RunAsNode` or
`EnableNodeCliInspectArguments` enabled, or `EnableEmbeddedAsarIntegrityValidation`
disabled, is a local code-injection surface: any process can relaunch the
signed app as an arbitrary Node interpreter or attach a debugger. The command
decodes the fuse wire and reports a security posture (`critical` / `warn` /
`info`) per dangerous setting. `--json` emits the structured config. Reads
binary bytes only; runs nothing.

### Examples

```bash
spectra web fuses /Applications/SomeChatApp.app
spectra web fuses --json /Applications/SomeChatApp.app
```

## `spectra web processes`

Attributes a running Electron/Chromium app's memory to each helper role.
Activity Monitor shows a dozen identically named "Helper (Renderer)"
processes; this decodes each one's Chromium role from its command line
(`--type=renderer|gpu-process|utility`, `--utility-sub-type`,
`--renderer-client-id`) and rolls up RSS per role — browser, renderer, GPU,
utility — flagging the renderer whose memory most exceeds the renderer
median. `--json` emits the structured topology.

### Examples

```bash
spectra web processes /Applications/Slack.app
spectra web processes --json /Applications/Slack.app
```

## `spectra web symbolicate`

Resolves minified generated positions back to their original source
locations using a Source Map v3 file — the primitive that turns
`app.js:1:284620` in a crash report or JS stack into
`src/sync/engine.ts:214:9 (flush)`. Pass the `.js.map` and one or more
`line:col` positions (1-based line, 0-based column). `--json` emits
structured results.

### Examples

```bash
spectra web symbolicate app.js.map 1:284620
spectra web symbolicate --json app.js.map 1:284620 1:9931
```

## `spectra web leveldb-health`

Reports the structural health of the LevelDB stores Chromium/Electron apps use
for IndexedDB, Local Storage, and Session Storage. Each argument is a LevelDB
store directory or a parent directory to scan (a directory containing a
`CURRENT` file is a store). Inspection is **file-level only** — it counts the
sorted table files (`.ldb`/`.sst`) and write-ahead logs (`.log`), reads the
small `CURRENT` file to check the manifest it names actually exists, and sums
the store size — so it never decodes tables, never opens the store for writing,
and does not contend with a running browser. A store is flagged when `CURRENT`
is missing/unreadable or points at an absent manifest, when the table-file
count crosses the compaction-backlog threshold, or when the write-ahead log is
bloated (an unclean shutdown or heavy pending writes). The persistent `LOCK`
file is ignored — its presence is normal and does not indicate the store is in
use.

### Examples

```bash
spectra web leveldb-health "$HOME/Library/Application Support/MyApp/Local Storage/leveldb"
spectra web leveldb-health --json "$HOME/Library/Application Support/MyApp/IndexedDB"
```

## `spectra anomalies`

Flags processes whose latest RSS sits well above their own recent baseline,
using an EWMA mean+variance / z-score detector over the metrics the daemon
samples — a sudden jump or step change, e.g. "pid 8123 (com.corp.helper)
RSS 2.1 GB is +4.3σ vs baseline (~600 MB)". (It compares the latest point to
the baseline, so a gradual creep the baseline follows won't trip it — that's
a job for a dedicated trend detector.)

Series are keyed on **PID** (the metrics ring is PID-keyed); the current
process name is attached best-effort and exited PIDs are labeled as such.
A series needs a few baseline points before it can flag (no cold-start
false positives). `-z` and `--min-samples` tune sensitivity; `--json`
emits the structured findings.

### Examples

```bash
spectra anomalies
spectra anomalies -z 4 --min-samples 10 --json
```

## `spectra whatswrong`

A single whole-system triage: samples memory pressure and swap
(`kern.memorystatus_vm_pressure_level`, `vm.swapusage`), thermal state, and
the top CPU/RSS processes, then returns a **ranked, plain-language** list of
likely causes instead of a raw table — the whole-machine counterpart to the
app/PID-scoped `rules`/`issues`. A healthy machine says so. `--json` keeps
the structured signals alongside the ranking so it's auditable.

### Examples

```bash
spectra whatswrong
spectra whatswrong --json
```

## `spectra schedule`

Installs a per-user LaunchAgent that runs `spectra snapshot` on an interval,
so `spectra bisect` and `spectra anomalies` have a dense capture history to
work over (both are only as good as how often snapshots and metrics were
taken). Mirrors `install-daemon`: `install --interval <dur>` writes
`~/Library/LaunchAgents/dev.spectra.snapshot.plist` (with `StartInterval`)
and bootstraps it with launchd; `uninstall` removes it; `status` reports
whether it's loaded; `print-plist` prints the plist without writing.
`--no-load` writes the plist without loading it.

### Examples

```bash
spectra schedule install --interval 30m
spectra schedule status
spectra schedule uninstall
spectra schedule print-plist --interval 1h
```

## `spectra playbook`

Shows problem-first diagnostic workflows over existing collectors. Use it
to list playbooks, inspect one workflow, emit a command-only plan, or return
the playbook definition as JSON.

```bash
spectra playbook
spectra playbook jvm-memory
spectra playbook --commands network-failure
spectra playbook --json storage-bloat
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
| `--tsnet-allow-logins` | empty | Comma-separated Tailscale login names allowed to connect |
| `--tsnet-allow-nodes` | empty | Comma-separated Tailscale node names allowed to connect |
| `--artifact-policy` | `confirm` | Artifact write policy: `confirm`, `deny`, or `allow` |
| `--log-file` | `~/Library/Logs/Spectra/daemon.jsonl` | JSONL daemon log path |
| `--no-log-file` | false | Disable daemon JSONL logging |
| `--daemon` | false | Start detached and return |

TCP RPC has no Spectra-layer authentication today. Keep it on loopback
for local use or expose it only through SSH, Tailscale, or firewall
controls you trust. `--tsnet` uses Tailscale identity and ACLs; first-run
enrollment uses existing tsnet state, `TS_AUTHKEY`, or a login URL written
to the daemon log or stderr. `--tsnet-allow-logins` and
`--tsnet-allow-nodes` add an optional Spectra-side allowlist on top of
Tailscale ACLs.

`--artifact-policy=confirm` requires sensitive RPC artifact methods to
carry `confirm_sensitive: true`. `deny` rejects daemon artifact writes
entirely, which is useful for shared or unattended remote daemons.
`allow` keeps auditing enabled but skips the confirmation gate for
trusted automation.

### Examples

```bash
spectra serve
spectra serve --log-file /tmp/spectra-daemon.jsonl
spectra serve --no-log-file
spectra serve --tcp 127.0.0.1:7878
spectra serve --tcp 100.64.0.5:7878 --allow-remote
spectra serve --tsnet --tsnet-hostname work-mac
spectra serve --tsnet --tsnet-hostname work-mac --tsnet-tags tag:engineer
spectra serve --tsnet --tsnet-allow-logins alice@example.com,bob@example.com
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
`--tsnet-tags`, `--tsnet-allow-logins`, `--tsnet-allow-nodes`,
`--artifact-policy`, `--log-file`, and `--no-log-file`.

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
| `spectra connect <target> diff <id-a> <id-b>` | Call `snapshot.diff` |
| `spectra connect <target> metrics` | Call `process.live` |
| `spectra connect <target> metrics <pid> [limit]` | Call `process.history` |
| `spectra connect <target> processes` | Call `process.list` |
| `spectra connect <target> processes <App.app>` | Call `process.list` scoped to bundles |
| `spectra connect <target> process-tree [App.app ...]` | Call `process.tree` |
| `spectra connect <target> sample <pid> [duration] [interval]` | Call `process.sample` |
| `spectra connect <target> network` | Call `network.state` |
| `spectra connect <target> connections` | Call `network.connections` |
| `spectra connect <target> network firewall` | Call `network.firewall` |
| `spectra connect <target> network-by-app [App.app ...]` | Call `network.byApp` |
| `spectra connect <target> network-capture-start <iface> [duration_ms=N] [snap_len=N] [proto=tcp\|udp] [host=HOST] [port=N]` | Call `helper.net_capture.start` |
| `spectra connect <target> network-capture-stop <handle>` | Call `helper.net_capture.stop` |
| `spectra connect <target> network-capture-summarize <pcap-path> [limit=N]` | Call `network.capture.summarize` |
| `spectra connect <target> network-diagnose [app_path=P] [pid=N] [target=HOST] [port=N] [timeout_ms=N]` | Call `network.diagnose` |
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
spectra connect work-mac network firewall
spectra connect work-mac storage /Applications/Slack.app
spectra connect work-mac issues check
spectra connect work-mac issues check snap-1
spectra connect work-mac toolchains
spectra connect work-mac snapshot
spectra connect work-mac snapshot diff snap-before snap-after
spectra connect work-mac diff snap-a snap-b
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
| `--discover-daemons` | false | Discover Tailscale peers running reachable Spectra daemons |

### Examples

```bash
spectra hosts
spectra hosts --json
spectra hosts --probe
spectra hosts --probe --json
spectra hosts --discover
spectra hosts --discover-daemons
``` 

## `spectra bisect`

Bisects one host's stored snapshot series to find the first snapshot where a
rule started firing, then diffs it against its predecessor to surface the
changes that co-occurred (an app version bump, a new login item, a JDK
swap). Because snapshots are periodic, the timing is bounded by capture
cadence, and the co-occurring changes are reported as **correlated, not
the cause**. `--host` selects the host (defaults to the only stored one);
`--json` emits the structured result.

### Examples

```bash
spectra bisect app-no-hardened-runtime
spectra bisect app-no-hardened-runtime --host work-mac --json
```

## `spectra reconcile`

Turns toolchain drift from a read-only diff into an actionable checklist:
an **advisory, print-only** plan to make one host's toolchain match
another's, over the snapshots in the local store. It compares JDKs (by
major, naming the target's vendor/release), Homebrew formulae (install /
upgrade to the target version), the language runtimes, and `JAVA_HOME` /
active-JVM-manager differences.

Every line is a **description, not a runnable command** — install steps
name the target's vendor/cask/release rather than assert an exact command
that may not exist on your tap. Nothing is executed. `--from` selects the
source host (defaults to this machine); `--json` emits the structured plan.

### Examples

```bash
spectra reconcile ci-mac-1
spectra reconcile ci-mac-1 --from my-laptop --json
```

## `spectra fleet`

Aggregates the snapshots already in the local store (keyed by machine UUID,
so it can hold multiple hosts from `snapshot` + fan-out) into cross-host
answers — the grouped result that fan-out enabled but never produced.

- `spectra fleet symptom <rule-id>` groups hosts by whether that rule fires
  against their latest snapshot ("which Macs trip `app-no-hardened-runtime`?").
- `spectra fleet issues` rolls every rule finding up across hosts,
  deduplicated by (rule, subject), into one entry with the member host list.
  Findings rank most-widespread first ("5 hosts trip app-no-hardened-runtime").
- `spectra fleet drift --jdk` / `--app <bundleID>` prints a per-host version
  matrix ("why does it repro on CI only?" — `laptop=21.0.6, ci-mac=17.0.10`).

A host with no usable snapshot shows as `unknown` for a dimension rather
than a false "missing". `--json` emits the structured result.

### Examples

```bash
spectra fleet symptom app-no-hardened-runtime
spectra fleet drift --jdk
spectra fleet drift --app com.tinyapp.jvm --json
spectra fleet issues
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
| `--discover-daemons` | false | Discover Tailscale peers running reachable Spectra daemons and merge with local-known hosts. |
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
spectra fan --discover-daemons status
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
