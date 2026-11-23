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
`prompts` capabilities, and `completions`. The set never changes while it runs, so it sends no
list-changed notifications. Resource subscriptions are covered under
[Live resources](#live-resources).
Tools that only read carry the `readOnlyHint` annotation. `snapshot`,
`diagnose`, `jvm`, `network`, `issues`, `cache`, `alerts` and `triage` do not,
because some of their operations store snapshots or issues, acknowledge alerts,
clear the cache, attach to a JVM, invoke an MBean, start a packet capture, or
save JVM and file-descriptor samples to the local store. Of the
[job](#jobs) tools, `job_status`, `job_result` and `job_list` are read-only;
`job_start` (which can wrap `snapshot`, `diagnose` and `triage`) and
`job_cancel` are not.

Tool calls and resource reads run one at a time. `ping` and the list methods
are still answered while a long call such as `spawns` runs. To run a slow
tool without blocking other calls, start it as a [job](#jobs).

### Jobs

A job runs one slow tool call in the background. Every client can use jobs
today; they are ordinary tools:

| Tool | Arguments | Result |
| --- | --- | --- |
| `job_start` | `tool`, `arguments` (that tool's arguments) | The job record, returned at once |
| `job_status` | `job_id` | The job record, with the tool's latest progress |
| `job_result` | `job_id` | The tool's own result (text, `resource_link` items and `structuredContent`, as a direct call returns them) |
| `job_cancel` | `job_id` | The job record, now `cancelled` |
| `job_list` | none | `{"jobs": [...]}`: this session's jobs, oldest first |

`job_start` wraps only slow tools: `spawns`, `idle`, `load`, `orphans`,
`triage`, `diagnose`, `snapshot` (operation `create` or `baseline`),
`process` (operation `sample`) and `jvm` (operation `flamegraph`). It checks
the arguments exactly as the tool does, so a bad call fails at `job_start`
rather than in the background. For example:

```json
{"name": "job_start", "arguments": {"tool": "spawns", "arguments": {"pid": 123, "duration_seconds": 30}}}
```

A job record has `jobId`, `tool`, `operation`, `status`, `statusMessage`,
`createdAt`, `lastUpdatedAt`, `ttl` and `pollInterval` (milliseconds),
`progress` (the latest `progress`, `total` and `message` the tool reported,
the same values a direct call sends as progress notifications),
`resultAvailable`, and `expiresAt` once it has finished. `status` is
`working` while the job is queued or running, then `completed`, `failed` or
`cancelled`. A tool that returns an error result makes the job `failed`, and
`job_result` returns that error result. `job_result` on a working job is an
error; poll `job_status` every `pollInterval` instead. `job_cancel` makes the
job `cancelled` at once and cancels its tool's context.

Limits:

- At most 2 jobs run at once. Further jobs are accepted and queue in start
  order (`working`, with a `queued behind N job(s)` status message); a queued
  job that is cancelled leaves the queue at once and never runs.
- A job's `arguments` may be at most 1 MiB; a job drops them once it starts
  (or is cancelled), keeping only its result.
- A `job_start` whose request is already cancelled registers nothing. Once a
  job is registered it runs independently of that request, and the call
  returns its job id.
- At most 32 jobs are working (queued or running) at once; `job_start` refuses
  a job beyond that.
- A finished job and its result are kept for 15 minutes (`ttl`), and at most
  32 finished jobs and 32 MiB of results are kept; the oldest go first.
- A result over 4 MiB of text fails the job; call the tool directly or narrow
  its arguments.
- Job ids are 128-bit random values. A job is visible only to the MCP session
  that started it: another session gets `unknown job_id`.
- Jobs do not hold the call slot, so direct calls and the job tools are
  answered while jobs run. When the session ends or the server stops, every
  job is cancelled.

The jobs follow the MCP Tasks extension (`io.modelcontextprotocol/tasks`,
SEP-2663): the statuses are the task statuses (`input_required` is reserved;
no wrapped tool asks for input), the record fields are the task fields with
`taskId` named `jobId`, and `job_result` returns the original call's result as
`tasks/result` does. Once the Go SDK supports the extension, a task-augmented
`tools/call` for a wrapped tool will create a job in the same store, and
`tasks/get`, `tasks/result`, `tasks/cancel` and `tasks/list` will read the
same records; the job tools stay for clients without Tasks support.

### Cancellation and end of input

A `notifications/cancelled` for a running call cancels the tool's context,
so collectors that take one stop early, and the server sends no reply for
that request. A call cancelled while it waits behind another one never runs.
When stdin closes with calls still running (`printf ... | spectra-mcp`), the
server waits up to 30 seconds for their replies before it exits.

### Progress

A call whose `_meta` carries a `progressToken` gets `notifications/progress`
from the tools with a natural measure of progress, at most four a second:

| Tool | Progress |
| --- | --- |
| `spawns` | seconds traced of `duration_seconds` (as `total`), with the event count in the message |
| `idle` | seconds of the CPU `window` elapsed |
| `process` `sample`, `jvm` `flamegraph`, `power` `impact` | seconds of the sampling duration elapsed |
| `load` | phases: reading load and memory, collecting processes, sampling CPU |
| `snapshot` `create`, `diagnose`, `triage` | phases: building the snapshot, evaluating rules, saving or persisting |

Phase progress counts the phases started and has no `total`, because the
phases a call runs depend on its arguments. Other tools send no progress.

### Structured output

Every tool lists an `outputSchema`, and a successful call returns
`structuredContent` holding the same value as its text content; the text is
unchanged for clients that ignore structured output. Error results carry
text only. The report tools' schemas are inferred from the Go result types
the CLI's `--json` output uses; a field whose type marshals itself (a
custom JSON or text marshaler) is left unconstrained, and `lineage` (a
recursive tree) and `owner` (two result shapes) get a plain object schema.
Every other tool returns the `summary`/`evidence`/`next_actions`/`raw`/
`timestamp` envelope, whose `raw` is free-form.

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

## Tools that are macOS-only

`inspect_app`, `services`, `logs`, `updates` and `timemachine`, and the
`process` operations `by_app` and `sample`, read macOS-only sources (`.app`
bundles, launchd, the unified log, Time Machine, `sample(1)`). Their
descriptions say so. Off macOS they return a structured tool error rather
than empty data or a failed exec:

```json
{"error": "unsupported_platform", "feature": "launchd services", "os": "linux",
 "message": "launchd services: unsupported on host OS (linux)",
 "hint": "on Linux, use systemctl list-units"}
```

`feature` and `hint` may be absent. Any other tool error caused by a feature
missing on the host OS uses the same shape.

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

The alerts resource has two shapes. With the daemon running it is a bare JSON
array of firing alerts (empty when none fire). Without it, it is an object,
`{"daemon": "not running", "message": ..., "alerts": []}`, so a subscriber can
tell a quiet host from a missing daemon. If the daemon accepts the connection
but its alert stream fails before delivering anything (an older or broken
daemon), the object reads `{"daemon": "alerts unavailable", "message": <reason>,
"alerts": []}`, and host health falls back to local sampling. Both shapes carry
the `private`, `ttlMs` 5000 cache hint, so a client may reuse a read for 5
seconds unless it is notified first. A burst of alert events is coalesced into
one `resources/updated` about 1 second after the first event.

The host-health resource is built from the same sample as the `host_health`
tool: the daemon's current sample when it runs, otherwise one taken locally.
`load_verdict` is `hold` when the 1-minute load reaches 1.5 per CPU (the
`load` tool's default limit). While a session subscribes, `load_verdict` is the
watcher's level with the 1.35 release hysteresis, the same one that drives
notifications, so content and updates agree.

While a session subscribes to either resource, one watcher shared by all
sessions runs:

- **Daemon running.** It follows the daemon's alert stream. Alert events
  send one debounced update for `spectra://alerts/firing`. Each daemon sample is compared
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

## Profiler selection

`jvm` `flamegraph` always runs the `asprof` found on the server's `PATH`.
The tool does not accept an `asprof_path` argument, and a call that passes one
is refused: an MCP client is usually an AI agent, so a client-chosen
executable would turn a prompt injection into arbitrary code execution as the
user. Put async-profiler's `bin` directory on `PATH` to use a non-standard
install; the CLI keeps its explicit `--asprof` override.

## Resource links and completions

### Artifact links

Tools that leave an artifact behind add a `resource_link` content item after
their text (the text and `structuredContent` are unchanged). The link points at
`spectra://artifact/{kind}/{id}`, a resource template a client reads with
`resources/read`:

| Tool | Kind | Read returns |
| --- | --- | --- |
| `process` `sample` | `sample` | The sample report as `text/plain` |
| `jvm` `flamegraph` | `flamegraph` | The profile the capture wrote into a fresh file in the artifacts directory, in `dest`'s format: `text/html` for `.html`, `image/svg+xml` for `.svg`, `text/plain` for `.collapsed`, `.folded` and `.txt`, `application/octet-stream` for `.jfr`. A `dest` gets a copy of that file; `dest` itself is never read back |
| `jvm` `heap_dump` | `heap` | Metadata only (`application/json`): pid, local `path`, size, whether the file still exists. The dump itself is never served |
| `snapshot` `create` (with `store`) and `get` | `snapshot` | The stored snapshot as `application/json` |

Spectra serves only files it wrote itself, under `~/.spectra/artifacts/<kind>/`
(mode 0600, directory 0700). `~/.spectra`, `~/.spectra/artifacts` and each kind
directory must be real directories (not symlinks) owned by the current user
and not group- or world-writable; an owned `~/.spectra/artifacts` is tightened
to 0700, and otherwise Spectra neither stores nor serves artifacts. A request
names a kind and an id, never a path: the id must match
`[A-Za-z0-9][A-Za-z0-9_.-]{0,127}` and contain no `..`, and the file is opened
without following symlinks and must be a regular file the current user owns.
Anything else answers "resource not found". Reads of more than 8 MiB of raw
artifact bytes (about 10.7 MiB once base64-encoded) return a `truncated` JSON
document with the local path instead of the bytes; non-text artifacts are
returned as a base64 `blob` with their `mimeType`. A tool that cannot store its
artifact still succeeds, just without a link. Each save sweeps its kind:
artifacts older than 7 days go, then the oldest until the kind holds at most
512 MiB.

`process` `sample` and `jvm` `flamegraph` take `duration_seconds` 1–60 (0 or
omitted is the default), and `jvm` `flamegraph` takes `event` `cpu`, `wall`,
`alloc`, `lock`, `itimer` or `ctimer`; `job_start` checks both up front.
`jvm` `flamegraph`'s `dest` extension picks asprof's output format, and the
capture keeps it: `.html` (the default when `dest` is omitted), `.jfr`,
`.collapsed`, `.folded`, `.svg` (async-profiler 1.x) or `.txt` (a text
summary). Any other extension is refused before the capture starts. A capture
is written into `~/.spectra/artifacts/.staging/` (same ownership and mode
checks, never served or swept) and moved into `flamegraph/` only once it
succeeds, so a concurrent sweep cannot delete it; a `dest` copy over 64 MiB
fails and leaves `dest` as it was.

Writes to the local store are atomic per call. `snapshot` (`store`) saves the
snapshot and its indexed process, login-item and permission rows in one
transaction; `diagnose` (`persist`) and `issues` `check` save the snapshot and
every issue update in one transaction. A call cancelled before that commit
stores nothing; a cancellation that lands while the commit is already running
does not undo it, so the call can be reported cancelled with everything
stored. Either way the store never holds half a call's writes. Check the store
(`snapshot` `list`, `issues` `list`) before retrying such a call: a retry
stores another snapshot, though its issues are upserted, not duplicated.

### Completions

The server advertises `completions` and answers `completion/complete` in
under 200 ms, returning at most 50 values with `hasMore` set when it cut the
list. Process, JVM and stored-snapshot listings are reused for 2 seconds; a
listing slower than 150 ms is answered from the previous one (or empty) while
it finishes in the background. The process listing starts loading when the
session starts, so the first `pid` completion is normally answered from it.

| Argument | Values |
| --- | --- |
| `pid` | Live pids that start with the typed digits, or whose command name contains the typed text. The `jvm-memory-debug` prompt completes JVM pids only |
| `roots`, `under` | Absolute (or `~`) directory paths that start with the typed text, directories only, hidden ones only when asked for; empty completes the home directory's subdirectories |
| `id_a`, `id_b` | Stored snapshot ids |
| `kind`, `id` of the artifact template | The artifact kinds; stored ids of the kind already chosen in the completion context, newest first |

Any other argument name completes to an empty list. MCP completions apply to
prompt arguments and resource-template variables, so tool arguments such as
`roots` are completed only when a client sends the same argument name against a
prompt or the template.

## Host report tools

These tools answer with exactly the JSON document the matching CLI command
prints with `--json` (not the `summary`/`raw` envelope other tools use),
built by the same package function, so a script and an agent parse one
shape. None of them starts the daemon, and none signals or kills anything
(`devices` with `start_adb` starts an adb server, which keeps running).
Durations are Go duration strings such as `"90s"` or `"10m"`. Paths must be
absolute because the server's working directory is arbitrary.

| Tool | Parameters | Result |
| --- | --- | --- |
| `load` | `roots`, `max_load_per_cpu` (default 1.5), `min_free_gb` (default 8), `backoff_free_gb` (default 4), `top` (1–50, default 5) | `spectra load --json`: verdict, reason, exit_code, cause, top tables, families, dirs, devices |
| `orphans` | `roots` (required), `min_age` (default `"1m"`), `no_daemon` | `spectra orphans --json`: groups of candidates and by-design daemons, totals, limitations |
| `lineage` | `pid` (required) | `spectra lineage --json`: ancestors, descendant tree, subtree totals |
| `owner` | exactly one of `target` (`:PORT`, `tcp:PORT`, `udp:PORT`, an absolute path, or a pid) or `under` (directories) | `spectra owner --json`, or `spectra owner --under --json` |
| `spawns` | `pid` (0 = all), `duration_seconds` (1–30, default 10), `interval_millis` (50 up to the duration, default 250) | `{events: [...], summary: {...}}` (see below) |
| `devices` | `roots` (optional), `start_adb` | `spectra devices --json`: adb devices (USB, emulator, remote), booted iOS simulators (macOS), emulator/simulator processes by root; adb is asked only when its server already runs unless `start_adb` |
| `idle` | `roots` and/or `pids` (one is required), `min_age` (default `"10m"`), `max_cpu` (default 1), `window` (100ms–30s, default `"5s"`) | `spectra idle --json`: holders with parent chain, descendants, waits |

Verdicts and findings that the CLI reports through a non-zero exit code
(`load` hold or backoff, orphans or idle holders found, no owner) are normal
results here; read `exit_code`, `total` or `holders`. A failed lookup (an
unreadable load average, an unknown pid, a CPU window that could not be
measured) is a tool error.

`spawns` holds up other tool calls for its whole duration unless it is
cancelled. The CLI streams one JSON
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
