# MCP tools

`spectra-mcp` serves local diagnostic tools over stdio. It does not start the
daemon.

## Protocol

The server is built on the official MCP Go SDK
(`github.com/modelcontextprotocol/go-sdk`, pinned to v1.8.0) and speaks
newline-delimited JSON-RPC on stdin and stdout. It negotiates every revision
the SDK supports: `2026-07-28`, `2025-11-25`, `2025-06-18`, `2025-03-26` and
`2024-11-05`. A client that sends `initialize` gets the version it asked for,
or `2025-11-25` when it asks for one the server does not know. A `2026-07-28`
client skips the handshake: it calls `server/discover` and sends the protocol
version in each request's `_meta`.

The server advertises the `tools`, `resources` (with `subscribe`) and
`prompts` capabilities. The set never changes while it runs, so it sends no
list-changed notifications. Resource subscriptions are covered under
[Live resources](#live-resources).
Tools that only read carry the `readOnlyHint` annotation. `snapshot`,
`diagnose`, `jvm`, `network`, `issues`, `cache`, `alerts` and `triage` do not,
because some of their operations store snapshots or issues, acknowledge alerts,
clear the cache, attach to a JVM, invoke an MBean, start a packet capture, or
save JVM and file-descriptor samples to the local store.

Tool calls and resource reads run one at a time. `ping` and the list methods
are still answered while a long call such as `spawns` runs.

## Daemon-backed tools

Start the local service with `spectra daemon start` or install it with
`spectra daemon install` to use these daemon-backed tools:

| Tool | Parameters | Result |
| --- | --- | --- |
| `host_health` | `samples` (0–240, optional) | Current host sample, active alerts, and optional recent samples |
| `alerts` | `action` (`list` or `ack`), `state` (`firing`, `resolved`, `all`), `limit`, `id` for ack | Alert list or acknowledged alert |
| `process` | `operation: "history"`, `pid`, optional `limit` | Recent metrics for the PID |

The `host_health` and `alerts` tools return a structured tool error with a
startup hint when the daemon is unavailable. The `process` history operation
retains its unavailable error with the same hint. Daemon calls have a two-second
timeout and reconnect once after a broken connection. The existing MCP tools
using in-process collectors remain available without the daemon.

## Live resources

Two resources describe this host and change while the server runs. A client
can subscribe to them (`resources/subscribe` up to `2025-11-25`, or a
`subscriptions/listen` stream on `2026-07-28`) and re-read them when it gets
`notifications/resources/updated`. On `2026-07-28` the notification goes only
to sessions whose listen stream names the URI. The static markdown resources
accept a subscription and never send an update.

| URI | Content | Cache hint |
| --- | --- | --- |
| `spectra://alerts/firing` | The daemon's firing alerts as a JSON array. Without the daemon: `{"daemon": "not running", "message": ..., "alerts": []}` | `private`, `ttlMs` 5000 |
| `spectra://host/health` | Compact JSON: `daemon` (`running` or `not running`), `at`, `ncpu`, `load1`/`load5`/`load15`, `load_per_cpu`, `load_verdict` (`admit` or `hold`), `memory_pressure`, `mem_free_pct`, `swap_used_mb`, `data_free_gb`, `thermal_throttled`, and `active_alerts` when the daemon is running | `private`, `ttlMs` 10000 |

The host-health resource is built from the same sample as the `host_health`
tool: the daemon's current sample when it runs, otherwise one taken locally.
`load_verdict` is `hold` when the 1-minute load reaches 1.5 per CPU (the
`load` tool's default limit).

While a session subscribes to either resource, one watcher shared by all
sessions runs:

- **Daemon running.** It follows the daemon's alert stream. Each alert event
  sends an update for `spectra://alerts/firing`. Each daemon sample is compared
  with the last one notified, and `spectra://host/health` is updated when the
  load verdict, memory pressure level or thermal state changes, at most once
  every 10 seconds. Once the verdict is `hold`, it stays there until load drops
  below 1.35 per CPU, so load near the limit does not flap.
- **No daemon.** Subscriptions are still accepted. The watcher samples the host
  locally every 30 seconds while a session subscribes to host health, applies
  the same thresholds, and tries to reach the daemon again at the same pace.
  Reads between polls reuse the last local sample.
- **Daemon starts or stops.** Both subscribed resources are updated, because
  their content changes.

The watcher stops when the last subscription ends or its session closes, so an
idle server neither samples the host nor holds a daemon connection.
`SPECTRA_NO_DAEMON=1` keeps the server on the local path.

## Host report tools

These tools answer with exactly the JSON document the matching CLI command
prints with `--json` (not the `summary`/`raw` envelope other tools use),
built by the same package function, so a script and an agent parse one
shape. None of them starts the daemon, and none signals or kills anything.
Durations are Go duration strings such as `"90s"` or `"10m"`. Paths must be
absolute because the server's working directory is arbitrary.

| Tool | Parameters | Result |
| --- | --- | --- |
| `load` | `roots`, `max_load_per_cpu` (default 1.5), `min_free_gb` (default 8), `backoff_free_gb` (default 4), `top` (1–50, default 5) | `spectra load --json`: verdict, reason, exit_code, cause, top tables, families, dirs, devices |
| `orphans` | `roots` (required), `min_age` (default `"1m"`), `no_daemon` | `spectra orphans --json`: groups of candidates and by-design daemons, totals, limitations |
| `lineage` | `pid` (required) | `spectra lineage --json`: ancestors, descendant tree, subtree totals |
| `owner` | exactly one of `target` (`:PORT`, `tcp:PORT`, `udp:PORT`, an absolute path, or a pid) or `under` (directories) | `spectra owner --json`, or `spectra owner --under --json` |
| `spawns` | `pid` (0 = all), `duration_seconds` (1–30, default 10), `interval_millis` (50 up to the duration, default 250) | `{events: [...], summary: {...}}` (see below) |
| `idle` | `roots` and/or `pids` (one is required), `min_age` (default `"10m"`), `max_cpu` (default 1), `window` (100ms–30s, default `"5s"`) | `spectra idle --json`: holders with parent chain, descendants, waits |

Verdicts and findings that the CLI reports through a non-zero exit code
(`load` hold or backoff, orphans or idle holders found, no owner) are normal
results here; read `exit_code`, `total` or `holders`. A failed lookup (an
unreadable load average, an unknown pid, a CPU window that could not be
measured) is a tool error.

`spawns` holds up other tool calls for its whole duration. The CLI streams one JSON
object per event and then a summary line; the tool collects the same values
into one document: `events` holds the event objects in order (at most 5000;
`events_dropped` counts the rest, which the summary still counts), `summary`
is the summary line's object, and `notes` holds the notices the CLI writes to
stderr, such as the scoped pid exiting mid-trace.

`orphans` asks an already-running daemon for recorded spawn origins, as the
CLI does; pass `no_daemon` to skip that. Unlike the CLI, `orphans` and `idle`
have no current-directory default for `roots`.

`load` is bounded at one minute, `orphans` and `lineage` at 30 seconds,
`owner` at 30 seconds (the CLI's `--timeout` default), and `idle` at one
minute (the CLI's `--timeout` default).
