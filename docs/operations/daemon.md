# Local daemon

Spectra has a local, per-user daemon over a Unix socket; no network listener;
cross-machine access remains in Spectra Remote.

```bash
spectra daemon start
spectra daemon status --json
spectra daemon logs -n 50
spectra daemon stop
```

`spectra daemon run` is the sole foreground server entry point. `start` launches
that command in a detached session. `install` configures a macOS LaunchAgent or
Linux systemd user service to launch the same command. `uninstall` removes the
service, and `print-plist` prints the macOS agent configuration without installing
it. `run --idle-timeout 10m` can stop an unmanaged daemon after inactivity; the
default is no idle timeout.

The daemon keeps its socket, lock, and PID file in `~/.spectra` on macOS. On
Linux, it uses `$XDG_RUNTIME_DIR/spectra` when available and falls back to
`~/.spectra`. `SPECTRA_DAEMON_DIR` overrides the socket directory. The socket
directory is mode 0700 and the socket is mode 0600. Each connection is checked
against the kernel's peer UID before a request is accepted.
`SPECTRA_DAEMON_LOG` overrides the log file location for isolated runs.
`daemon stop` asks a reachable daemon to shut down over the socket. If the
socket is unavailable, it checks the lock before using the PID file. A free
lock means no daemon is running; stop removes stale PID and socket files and
does not signal that PID. A held lock with no PID file means startup is still
in progress; stop waits and rechecks the lock without signaling PID 0. A held
lock with a known PID permits a bounded SIGTERM fallback. Status reports a
starting daemon when the lock is held but the PID file is not yet available.

The transport is newline-delimited JSON-RPC 2.0. `daemon.status` reports the
protocol, version, PID, start time, socket, and methods. `daemon.shutdown`
responds before stopping. A persistent connection can receive server-to-client
notifications for future subscriptions.

## Profiling the daemon

`spectra daemon profile [--kind heap|allocs|goroutine|cpu] [--seconds N] [--json]`
calls the built-in `daemon.profile` method (`kind`, `seconds`). The daemon writes
one Go runtime profile to `profiles/<kind>-<UTC time>-<random>.pprof` in its socket
directory (mode 0600) and returns the path with a heap and goroutine summary.
`heap` runs a garbage collection first so the profile is current; `cpu` records
for `seconds` (default 10, at most 120) and only one CPU profile runs at a time.
Nothing is collected until the method is called, it uses the same owner-only
socket and peer-UID check as every other method, and it opens no listener. Read
a profile with `go tool pprof <spectra binary> <file>`, and compare two heap
profiles with `go tool pprof -base <earlier> <binary> <later>`. A profile is written
under a `.pprof.partial` name and renamed when complete; a failed write (for
example a full disk) removes it and returns the error. After each write the
daemon keeps the newest 20 `.pprof` files in that directory and deletes older
ones; it never touches `.partial` files, so one left by a crash mid-capture
stays until removed by hand.

## Monitoring and alerts

`daemon run` samples host health every 15 seconds. `--watch-interval 2s` changes
the interval and `--no-notify` disables desktop notifications. The watch uses
one bulk `ps` call per tick for process counts, CPU, RSS, top processes, and process
history. When Java processes are present, one additional `ps` call fetches the
arguments for up to 64 Java PIDs so Gradle and Kotlin daemons can be classified.
It also reads load average, memory pressure and swap, kernel limits,
free space on the data volume, thermal state, and its own CPU time. By default it does not
scan apps, collect snapshots, or inspect protected paths. On macOS, thermal
state requires one `pmset -g therm` call; on Linux, thermal throttling is not
reported where the kernel has no common direct signal. Limits without a cheap
current-usage counter are reported with a zero limit percentage and do not
trigger an alert. If the daemon averages more than 2% CPU over ten ticks, the
watch doubles its interval up to four times the base; it returns to the base
below 0.5%.

With `--assess-gatekeeper` (macOS, off by default) the daemon also
re-assesses installed apps with `spctl` in the background, one at a time
and under the usual 15 s cap, so `GatekeeperStatus` is usually served from
the 24 h verdict cache. Enable it with `spectra daemon start
--assess-gatekeeper` or `spectra daemon install --assess-gatekeeper`; both
pass it to the `daemon run` they launch, and `install` writes it into the
LaunchAgent plist or systemd unit so service restarts keep it. To change the
setting, stop (or reinstall) the daemon and start it again with or without the
flag; `start` against a running daemon leaves it as it is. Background runs share
the process-wide one-at-a-time assessor with interactive requests, so an
interactive `--assess-gatekeeper` may wait up to 15 s behind a background
`spctl`. Apps whose assessment times out are not cached and are retried on the
next hourly pass. See [Security inspection](../inspection/security.md).

An independent process-spawn probe runs every five seconds, including while a
regular sample is blocked or backed off. On macOS it reads `kern.proc.all` and
on Linux it reads `/proc`; neither backend forks to list processes. It counts new PIDs since the
previous probe and the current user's processes. It keeps the latest 120 probes
in memory. When a process condition fires or escalates, it queues one sample
for background storage and includes the five busiest parents and commands in the
`sample.spawn` payload. A full storage queue drops new writes and logs a
rate-limited warning; probing and subscriber events continue. Parent arguments
are read only for those five parents during an active condition. Desktop
notification failures are logged without retrying the probe.
The fast probe logs an overrun and skips a probe if collection exceeds its
interval; a minute of probe work over 1% daemon CPU is logged without changing
the fast interval.

The same probe records spawn origins for `spectra orphans`. For each of the
daemon user's processes it keeps, keyed by (pid, start time) so a reused pid
never inherits another process's record: the parent it had when first seen
(kept after reparenting), that parent's name, and the first probe that no
longer saw it (the exit was at most one probe earlier). For processes that are
some recorded process's parent it also reads argv and cwd while they live, at
most 32 per probe; on Linux by reading `/proc`, on macOS with one batched
`lsof -p` call per probe that finds new parents (the only fork the probe
makes). The store is memory only and bounded to 4,096 records (argv and cwd
truncated): exited records no live process descends from are dropped after 24
hours or evicted first when full, then exited ancestors of live processes,
then the oldest live records. Records are lost when the daemon restarts, and a
process whose parent exited within one probe of its start is seen already
reparented, so its origin is unknown.

`spawn.rate` warns at 40 new PIDs/s for two consecutive probes and is critical
at 120/s for one. `procs.uid` warns at 40% and is critical at 70% of
`kern.maxprocperuid` (or the daemon's finite Linux `RLIMIT_NPROC`).
`procs.total` warns at 50% and is critical at 80% of `kern.maxproc` (or Linux
`threads-max`). All three resolve after three clear probes. `spectra alerts
health` shows the latest process counts and spawn rate, including top parents
while an alert is active.

A sustained-CPU detector catches long-lived busy daemons that the
instantaneous probes miss, such as `diagnosticd`, `OSLogService`, or
`mds_stores` pinned for hours. Each regular tick adds one `ps -A` call (pid,
ppid, `%cpu`, `etime`, command) of its own, separate from the bulk sample call,
plus, on Linux, one `/proc/<pid>/stat` read per process. A process is tracked
by PID plus start time, so a reused PID starts a fresh history.

CPU usage is taken from `ps %cpu` on macOS, which is a recent, decayed value.
On Linux `ps %cpu` is a lifetime average, so the detector instead divides the
change in cumulative user+system CPU time (from `/proc/<pid>/stat`, assuming
the standard 100 ticks per second) by the wall time between two samples of the
same PID and start time; a first sighting yields no sample. An old daemon that
becomes busy therefore fires on schedule, and one that goes idle stops counting
immediately.

The `cpu.sustained:<name>:<pid>` warning fires when at least 90% of the samples
in a rolling 15-minute window are at or above 50% CPU and the window is covered
(measured from the newest sample older than the window, so tick jitter cannot
make a full window unreachable) with at least three above-threshold samples
(more for long windows); a short dip is tolerated, bursty load is not. The
window must be at least 16 base watch intervals, so it holds four samples even
when self-guard backoff stretches the tick to four times the base; a shorter
window logs an error and is widened. The suspend gap is sized for the worst-case
backed-off interval (four times the base) so self-guard backoff does not
trigger it. Resolution lags on purpose: the alert keeps
firing until fewer than half of the window samples are above the threshold, so
expect up to roughly half a window after the load stops, and then two clear
ticks. After a gap longer than the larger of five minutes and twelve base
watch intervals (for example sleep) the history is discarded, but an alert that was
already firing is kept, with its original ID and since-time, until at least four
fresh samples (fewer if the window cannot hold four) show the load is gone; an alert that was not yet firing starts over.

The alert detail names the process, average CPU, how long it has been busy,
process age, the owner chain (for example `launchd(1) > Terminal(50) >
zsh(60) > log(70)`), a one-shot summary, and a suggested next command (`sample
<pid> 10` on macOS, `top -H -p <pid>` on Linux). The summary is macOS `sample`
top of stack or Linux busiest threads. At most one summary is collected per tick,
with a three second limit, so a new alert may first appear without one. After a daemon restart, restored sustained alerts are carried like alerts across
a suspend gap: they stay firing, with the same ID, until fresh samples disprove
them. The alert detail records the original since-time and the process start time (`up 5h0m0s (started 2026-01-01T07:00:00Z)`), and a restart restores both from it without any schema change: the duration shown continues from the original since-time rather than the alert's fired time, and a process that reuses the PID with the same name but a different start time does not adopt the old alert. Alerts written by an older version lack these fields and fall back to the fired time and a name-only match. If the probe's `ps` call fails, that tick is skipped entirely and no alert
is resolved on missing evidence. After five consecutive failed probes the daemon logs a warning (and another every five
further failures, plus an info line on recovery) and appends "[probe unavailable: ...]" to the
detail of every firing sustained alert, emitted to subscribers as an `updated` event and persisted immediately (a failed write is retried on later failed ticks). The alerts stay
firing with the same ID, because resolving them as healthy would be a claim
without evidence; the note tells the reader they are stale. The note disappears
when the probe recovers and the alert is re-evaluated, with another `updated` event and an immediate stored-detail write. The probe takes its own timestamp when it runs,
because `ps` elapsed times are only meaningful against the snapshot time. The probe runs last in the watch
tick, after alert persistence, notifications and maintenance, so a slow sample
never delays them. A process that `ps` lists but whose `/proc` stat read fails
for a tick keeps its state and previous snapshot rather than resolving; if it
stays unreadable for a whole window (for example `hidepid`), the alert resolves. Readings that resume after unreadable ticks spanning more than eight base watch intervals start a fresh history (an alert already firing keeps its since-time), so an old sample never stands in for window coverage.
A firing alert needs at least two fresh samples below the threshold to clear,
and a reused PID with a new start time never inherits an old alert. `sample`
needs root for daemons such as `diagnosticd`; when it fails the detail says
"sample unavailable (needs root)" with the exact `sudo sample <pid> 10`
command to run, and the attempt is retried after 30 minutes. For `diagnosticd` and `OSLogService` the detail notes
that a client is usually polling or streaming the unified log (`log stream`,
`log show`, OSLogStore); for `mds_stores` it points at Spotlight indexing.
`spectra alerts` lists these like any other alert, and the stored detail is
refreshed every five minutes while the alert is firing.

Warnings include load above 1.5 times CPU count for two samples, rising load,
memory pressure for two samples, swap growth above 1 GB in five minutes,
resource limits above 70%, less than 20 GB free, thermal throttling for two
samples, a process kind above 600% CPU for three samples, and high Gradle,
simulator, emulator, or Codex plus Claude counts. Critical alerts cover load
above three times CPU count for two samples, critical memory pressure, limits
above 90%, and less than 5 GB free. Alerts clear after two healthy evaluations.

Thresholds can be overridden in `<daemon directory>/watch.yml`. Sections are
`load`, `memory`, `limits`, `disk`, `thermal`, `kinds`, `spawn`, `sustained`, and `jvm_history`; field names are the
snake case names in the defaults, such as `load.warn_multiple` or
`disk.critical_gb`. The `spawn` keys are `interval` (duration, minimum `1s`),
`rate_warn`, `rate_critical`, `uid_warn_pct`, `uid_critical_pct`,
`total_warn_pct`, and `total_critical_pct`. The `sustained` keys are `cpu_pct`
(default 50), `window` (duration, default `15m`, at least 16 watch intervals), `min_fraction`
(above 0.5 up to 1, default 0.9), and `allowlist`, a list of case-insensitive
process-name patterns for expected long-running work, for example
`allowlist: [backupd, "mds*"]`. Patterns match the process base name only,
not the path or arguments, and on Linux the name is the kernel `comm`, which is
truncated to 15 characters, so write the pattern against the truncated name. An unknown YAML key logs an error and keeps all defaults.
Non-positive thresholds log an error and use the default for that field while
preserving other valid settings.

### JVM history sampler

Trend rules (JVM heap trend, `jvm-native-memory-growth`) need several samples of
the same JVM over time. `spectra rules` and MCP `evaluateRules` record one per
run, so without a daemon the history only builds when someone reruns them. The
daemon can build it in the background. It is **off by default**: sampling
attaches `jps`, `jcmd`, and `jstat` to every JVM of the user, so an operator
opts in through `watch.yml`:

```yaml
jvm_history:
  enabled: true
  interval: 5m   # 1m to 1h, default 5m
  timeout: 30s   # per-pass budget, 1s up to the interval, default 30s
```

An out-of-range value logs a config error and falls back to its default; the
other settings stay in effect. Five-minute samples give `jvm-native-memory-growth`
its minimum (4 samples spanning 15 minutes) in about 15 to 20 minutes.

Each pass uses the same collection and persistence path as `spectra rules`
(`jvm.CollectAllStatus` then `AttachJVMHistory`), through the daemon's
`proc.Runner` on macOS and Linux alike. One NMT summary `jcmd` is made only for
JVMs started with `-XX:NativeMemoryTracking`; Spectra never enables NMT. A pass
skips quietly when no JVM is running. It is bounded by `timeout`: when the budget
expires, child processes are killed and the pass is abandoned, and daemon
shutdown cancels a pass in flight. The first pass runs 10 seconds after the
daemon starts. Passes never overlap.

A failed pass (`jps` missing, collection or database error) is logged at debug.
After three consecutive failures the daemon logs one warning, and another every
three further failures, and stretches the interval to four times the
configured value until a pass succeeds, which logs an info line. A host with no `jps` (no JDK) logs a single warning the first time and debug lines after that, with the same backoff, and an info line when `jps` appears. Shutdown waits for a pass in flight (up to the pass timeout plus 5 seconds) so no sample is written after the daemon closes its database. Persistence honors the pass context: a write or commit waiting on a SQLite lock is abandoned and rolled back within about 50 ms of cancellation. A write stalled in disk I/O cannot be interrupted, so the wait is a cap, not a guarantee: after it elapses the daemon logs a warning and continues shutdown, and closing the database then waits for that write. Per-JVM
failures (a `jcmd` that cannot attach) are absorbed by the collector and leave
that JVM without a sample for the pass.

Samples share the host-sample retention: the hourly prune deletes JVM samples
older than seven days (about 2,000 rows per JVM at 5 minutes), while rules read
only the most recent 60 per process lifetime.

The watch keeps per-process history (CPU and RSS from the bulk `ps` call) in
memory for the last 30 minutes per process, capped at 1,800 samples, so at the
default 15-second interval a process holds about 120 samples. A process that has
not been seen for 30 minutes is dropped at the next once-a-minute flush. Each
flush writes only the minutes not yet written plus the newest minute (which may
have been partial), so its cost follows the number of live processes rather than
the daemon's uptime. Minutes whose database write fails are retried at the next
flush while the process's samples are in memory; a process dropped after 30
minutes takes its unwritten minutes with it, and the failure is logged once per
run of failed flushes. The two-hour in-memory window of host samples that alerts
evaluate is trimmed in place.

The watch stores JSON samples for seven days and resolved alerts for 30 days
in the Spectra SQLite database. `SPECTRA_WATCH_DB` overrides the database path
for isolated runs. Process metrics are aggregated each minute. The RPC methods
are `watch.current`, `watch.samples` (`since`, `limit`), `alerts.list` (`state`,
`limit`), `alerts.ack` (`id`), `alerts.subscribe` (`samples`), and
`process.history` (`pid`, `limit`), and `process.origins` (`processes`: up to
4,096 `{pid, start_time}` pairs; a start time matches within two seconds). A subscription sends `alerts.event`
notifications and optionally `watch.sample` notifications until its connection
closes. Slow subscribers can miss events; clients can recover from the stored
history. Desktop notifications are best effort, capped at six per hour and
cooled down per alert key for 15 minutes. LaunchAgent sessions may lack desktop
notification permission.

## JVM methods

Two methods let MCP and desktop clients skip a cold CLI collection of the
user's JVMs.

`jvm.list` takes optional `refresh` (bool) and returns an array of JVM records
in the same shape as `spectra jvm --json` (an empty array when none run). The
daemon caches the list for 5 seconds. Concurrent callers share one collection,
and a caller that disconnects or times out does not cancel it for the others.
`refresh: true` skips the cache; it joins a collection that is already in
flight rather than starting a second one. A collection is capped at 30 seconds:
`jps` and `jcmd` children are killed at the deadline, waiting callers get an
error, and the next call starts a new collection. When `jps` is not on `PATH`
the call fails with `jps not found in PATH; JVM discovery unavailable` instead
of returning an empty list. Failed collections are never cached. A caller whose
connection closes stops waiting.

`jvm.findings` takes `pid` (required, positive) and returns
`{"pid": N, "findings": [...]}`, where each finding has the `rules` JSON shape
(`rule_id`, `severity`, `message`, `fix`, `subject`). Each call builds a fresh
host snapshot without apps, storage, launchd services, or update logs, then runs
the built-in rule catalog against it. Processes, toolchains, JVMs, and host
facts are still collected. The result keeps findings whose subject names the
process: `PID N` on its own or followed by a space (the per-JVM rules and
`fd-pressure`), or ending in ` pid N` (`process-memory-hog`). A `pid` of zero or
less is an invalid-params error. A pid that is not among the running JVMs fails
with `pid N is not a running JVM`, followed by the snapshot's warnings in
parentheses when there are any (for example, `jps` missing). A successful result
carries those warnings in `warnings`. Each call is capped at 60 seconds, JVM
tool children are killed at the deadline, and the call stops early when the
client's connection closes.

Known limitations of `jvm.findings`:

- It attaches no JVM history. `jvm-native-memory-growth` never fires, and
  `jvm-gc-pressure` cannot use the old-gen trend to drop a steady-state
  finding. Use `spectra rules` or MCP `evaluateRules` for trend-aware results.
- It uses only the built-in catalog. Rules and overrides from `spectra.yml` are
  not applied.
- It takes no thread dump, so `jvm-deadlock` does not fire. Use
  `spectra rules --thread-dump-pid <pid>`.

### Per-pid reads

Six methods return exactly the JSON that the matching `spectra jvm ... --json`
command prints for one JVM. The CLI and the daemon build it with the same
function, so a client can switch from spawning `spectra` to calling the daemon
without changing its parser. Every method takes `pid` (required, positive
integer). Absent or `null` params, a missing pid, a pid of zero or less, or a
field of the wrong type is an invalid-params error (`-32602`). Each call runs
`jcmd` or `jstat` against the target again, except that `jvm.explain` reuses
`VM.system_properties` and `VM.command_line` already read for the same pid and
process start time, since they cannot change for the life of a JVM.

| Method | CLI command | Extra params |
| --- | --- | --- |
| `jvm.explain` | `spectra jvm explain --json <pid>` | `samples`, `interval_ms`, `soft_refs` |
| `jvm.gc_stats` | `spectra jvm gc-stats --json <pid>` | none |
| `jvm.heap_histogram` | `spectra jvm heap-histogram --json <pid>` | `suspects` |
| `jvm.thread_dump` | `spectra jvm thread-dump --json <pid>` | none |
| `jvm.vm_memory` | `spectra jvm vm-memory --json <pid>` | none |
| `jvm.nmt` | `spectra jvm nmt [detail\|diff] --json <pid>` | `mode` |

- `jvm.explain`: `samples` (default 1, at most 120) and `interval_ms` (default
  1000) match `--samples` and `--interval`. A trend is sampled when `samples`
  is above 1, and `(samples - 1) * interval_ms` may not exceed 10 minutes.
  `soft_refs` (default `true`) matches `--soft-refs`. It runs
  `GC.class_histogram`, which makes the target JVM do a full GC, just as the
  CLI does.
- `jvm.heap_histogram`: `suspects` (default 20, may not be negative) is the
  number of largest classes to rank, like `--suspects`. Like the CLI, it runs
  `GC.class_histogram`, which makes the target do a full GC. `heap-histogram
  compare` reads saved files and is not served.
- `jvm.thread_dump`: returns `{"summary": ..., "dump": ..., "code_sources": ...}` (`code_sources` as the CLI fills it, from the JVM's launch class path; the daemon keeps its jar index cache between calls). `captured_at` is
  the daemon's clock. The CLI's raw and `--summary` text modes have no RPC
  form. The daemon does not save the dump to the blob cache or the artifact
  manifest, as the CLI does.
- `jvm.vm_memory`: when every section fails (for example, the pid is not a
  JVM), the CLI still prints the JSON but exits 1. The method returns that same
  JSON as a success, so read the per-section `error` fields.
- `jvm.nmt`: `mode` is `summary` (the default), `detail`, or `diff`, matching
  `spectra jvm nmt`, `nmt detail`, and `nmt diff`. `diff` reports growth since
  a baseline that `spectra jvm nmt baseline <pid>` recorded earlier.
  `baseline` changes state in the target JVM, so the daemon does not offer it.
  `compare` reads saved files and is not served either. `--top` affects only
  the text output.

When the CLI fails, the method fails with the CLI's stderr line as the error
message (`-32603`). Examples: `gc-stats failed for PID 42: jstat: ...`,
`explain failed for PID 42: JVM PID 42 not found or not inspectable`, or
`native memory tracking is not enabled; restart the JVM with ...`. No method
writes files, takes a heap dump, starts JFR, or attaches an agent. Each call
is capped at 60 seconds; `jvm.explain` gets its trend window on top of that.
JDK tool children are killed at the deadline or when the client's connection
closes. The error is then `<method>: timed out after <d>: context deadline
exceeded` or `<method>: context canceled`. The methods behave the same on
Linux and macOS, since `jcmd` and `jstat` are the only backends. They are
looked up on the daemon's `PATH`, which can differ from your shell's.

## Clients

The CLI and stdio MCP server discover the running local daemon through its Unix
socket. They never start it automatically. Run `spectra daemon start` or install
the per-user service before using alert and host-health commands.
`SPECTRA_NO_DAEMON=1` and `--no-daemon` apply to daemon-consuming commands:
`spectra alerts`, `spectra whatswrong` enrichment, `spectra orphans` origins, and the MCP `host_health`,
`alerts`, and process-history tools. The `spectra daemon start|stop|status|logs`
subcommands ignore this flag and environment variable because they manage the
daemon itself rather than acting as its clients. The CLI warns about a daemon
version mismatch and continues; `whatswrong` keeps its local diagnosis if the
daemon is unavailable or does not answer within one second.

`spectra alerts` lists firing alerts by default. Use `--state all` or
`--state resolved` and `--limit N` to adjust the list, `ack <id>` to acknowledge,
`health` for the current host sample, and `watch --samples` for alert events and
optional samples. `--json` emits JSON, including one JSON object per line for
`watch`. Ctrl-C exits the watch cleanly.

The MCP server connects lazily and reconnects once if the socket closes. Its
`host_health` and `alerts` tools and `process` history operation require the
daemon; other local collector operations continue without it. See the
[MCP tool reference](mcp.md).

## Process methods

Two methods serve the process table to clients that poll it, such as the
desktop app, without spawning `spectra process --json` each time.

`process.list` takes optional `refresh` (bool) and returns
`{"sampled_at": "<RFC3339>", "processes": [...]}`. `processes` holds the same
rows and fields as `spectra process --json` with no flags (RSS descending; an
empty array if none). The snapshot is cached for 2 seconds. Concurrent callers
share one collection, and a caller that disconnects does not cancel it for the
others. `refresh: true` skips the cache but joins a collection already in
flight. A collection is capped at 15 seconds; on timeout or collector panic
nothing is cached. A caller waiting on a collection when an earlier snapshot
exists gets that snapshot with `"stale": true` and `"stale_reason"` (its
`sampled_at` says how old) instead of an error, and does not wait past 3
seconds unless it asked for `refresh: true`; only a caller with no earlier
snapshot gets the error. The rows are collected on
demand rather than taken from the watch sampler, which keeps only aggregates and
the top CPU consumers.

`process.list` also takes `classify` (bool). When true, the rows carry the
`spectra process --json --classify` fields (`role`, `runtime`, `app`,
`signing`, `evidence`). The classified snapshot is cached and single-flighted
separately from the plain one, built once from the latest plain collection, so
concurrent classified callers share one classification and plain callers never
pay for it. Classification uses the on-disk classify cache. If the classifier
fails or exceeds its own 15 second cap, the call still succeeds: rows the
classifier finished (it gets up to 2 more seconds to hand back in-flight work)
keep their fields, the rest are left unknown, and the payload carries a
`warning` string. A cold classified call is therefore bounded by roughly 32
seconds: 15 for collection, 15 for classification and the 2 second grace.
`sampled_at` stays the collection time, but the 2 second cache is measured from
when classification finished. The daemon rebuilds its classifier every 10
minutes to bound its in-memory memo; the on-disk classify cache stays warm.
Subscribers of both kinds on one tick share a single collection and
`sampled_at`.

`process.subscribe` takes optional `classify` (bool) and returns
`{"subscribed": true}`. With `classify: true` the subscriber receives classified
payloads; the daemon classifies each tick only while at least one current
subscriber asked for it, and plain subscribers keep receiving plain payloads. The
connection then receives `process.update` notifications whose params are the
same `{sampled_at, processes}` payload, once per `--watch-interval` (default 15
seconds), starting with an immediate one when the first subscriber arrives. The
daemon collects only while at least one subscriber exists, and never in the
background otherwise. Slow subscribers can miss updates; the next one supersedes
them. The subscription ends when the connection closes.

## Host methods

Five methods serve the host facts a desktop Machine page polls, so clients skip
spawning `spectra memory|thermal|power|network|whatswrong --json` per poll. Each
takes optional `refresh` (bool) and returns exactly the JSON the matching CLI
command prints with `--json`. Each method has its own cache; concurrent callers
share one collection, a caller that disconnects does not cancel it for the
others, and `refresh: true` skips the cache but joins a collection already in
flight. A collection is capped at 20 seconds (15 for `power.current` and `whatswrong.current`), its child
processes are killed at the cap, and a timeout, collector error, or collector
panic is returned to the waiting callers and never cached. On Linux each method
returns what the CLI returns there, or the same unsupported error, except for
the `network.current` proxy fields described below.

The two slow methods, `power.current` and `whatswrong.current`, serve
stale-while-revalidate: once a value is past its cache time but still inside a
stale window, the call returns it at once and starts one background collection
to replace it. A failed background collection leaves the old value in place
and the next call retries. A value older than the stale window, the first call
after the daemon starts, and any `refresh: true` call wait for a collection.

A slow or failed collection does not cost a caller that already has a reading.
When a call has to wait on a collection and an earlier good value exists, it
waits at most 3 seconds (not with `refresh: true`), and if the collection
fails it never errors: either way it gets the earlier value with
`"stale": true`, `"stale_reason"` and `"sampled_at"` (when that reading was
taken) added. The collection carries on in the background and refills the
cache for the next call. Only a call with no earlier reading, such as the
first after the daemon starts, waits for the collection and can fail. This
matters under heavy load: the daemon runs at background priority, so a
collection can take most of its cap exactly when someone opens a monitor.

### `memory.current`

The `spectra memory --json` object (`memstate.MemoryState`). Cached for 2
seconds. macOS cgo builds read the Mach VM statistics; Linux reads
`/proc/meminfo`, `/proc/vmstat` and `/proc/pressure/memory` (adding `os`,
`available_bytes`, `psi` and `pressure_reason`). Other builds return the "not
supported" error the CLI prints.

### `thermal.current`

The `spectra thermal --json` object (verdict, evidence, findings, with
`samples: 1`). Cached for 5 seconds. macOS reads `pmset -g therm`; Linux reads
`/sys` thermal zones.

### `power.current`

The `spectra power --json` envelope in baseline mode (`mode: "baseline"`,
`interval_ms: 1000`; battery, thermal, assertions, and top energy users from
`pmset` and `top`). Cached for 5 seconds, with a 2 minute stale window. The
`--top`, `--joules`, and `--deep` modes are not served. The baseline runs
`top -l 2 -s 1`, which walks every process twice a second apart: a cold call
takes 2 to 3 seconds on an idle Mac and can pass 15 seconds under heavy load
with thousands of processes. That is why the cap is 15 seconds, below the 20
seconds desktop clients allow a call, and why clients polling within the stale
window get the cached value at once.

### `network.current`

The `spectra network --json` object (interfaces, DNS, routes, VPN and proxy
state). Cached for 10 seconds.

On macOS the proxy fields come from system-wide `scutil` settings and match the
CLI. On Linux they come from the daemon's own environment (`http_proxy`,
`https_proxy`, `all_proxy` as of daemon start), not the caller's shell, so they
can differ from `spectra network --json`.

### `whatswrong.current`

The `spectra whatswrong --json` object (ranked `causes`, process `families`,
`daemon_alerts`, `trend`, and the raw `signals`). Cached for 15 seconds, with a
5 minute stale window, so a client polling every 30 seconds gets an immediate
answer. The daemon fills `daemon_alerts` and `trend` from its own monitoring
state, as the CLI does when it finds a running daemon. A cold call runs the
same probes as the CLI (memory pressure, `pmset` and `top`, the process list,
and XPC attribution of a busy service) and is capped at 15 seconds. The
process list leaves out the daemon's own probe subprocesses, such as the `top`
that runs alongside it.

## Load and idle reports

`load.report` and `idle.report` return exactly what `spectra load --json` and
`spectra idle --json` print, for the flags in the params. The CLI commands
call them when a daemon is running (unless `--no-daemon` or
`SPECTRA_NO_DAEMON=1`), and fall back to measuring locally when no daemon
answers or it predates these methods.

They are not cached. On Linux both share one CPU tracker: from the first call
it reads every process's `/proc/<pid>/stat` once a second, keeps 12 seconds of
readings, and stops after 10 minutes without a call. A call whose window has a
reading at least that old (and not much older) diffs against it instead of
sleeping; otherwise it samples as the CLI does. macOS samples as the CLI does.

Durations are milliseconds and roots must be absolute (the daemon's working
directory is not the caller's).

- `load.report` params: `top`, `roots`, `max_load_per_cpu`, `min_free_gb`,
  `backoff_free_gb`, `sample_ms`, `boot_window_ms`, as the CLI flags, and
  optional `caller_pid`, whose cgroup the `cgroup` block describes (the CLI
  sends its own; the daemon's cgroup can differ). Capped at 30 seconds.
- `idle.report` params: `roots` and/or `pids` (one is required), `min_age_ms`,
  `max_cpu`, `window_ms`, `timeout_ms` (at most 5 minutes; the call's cap) and
  `exclude_pid`: the caller, whose ancestors and descendants are left out as a
  local run leaves out its own.

Both stop when the client disconnects. Invalid params return `-32602`.

## Inventory methods

Four methods serve the toolchain inventory, the incident timeline, and the
stored-snapshot reads, so clients skip spawning `spectra toolchain|timeline|snapshot
list|snapshot diff --json`. Each returns exactly the JSON the matching CLI
command prints with `--json` for the same flags, because the CLI and the daemon
build it with the same function. Nothing is cached: every call collects
anew. Each call has its own server-side cap; when it passes, or the client's
connection closes, the call's context is cancelled, its child processes are
killed, and the call fails (`<method>: timed out after <cap>` or
`<method>: context canceled`) instead of returning partial data. Parameters
are an object (absent or `null` means all defaults); durations are
milliseconds. Bad parameters are an invalid-params error (-32602). On Linux
each method returns what the CLI returns there.

### `toolchain.current`

Takes no parameters and returns the `spectra toolchain --json` object (JDKs,
Node, Python, Go, Ruby, Rust, build tools, JVM managers, Homebrew, and `env`).
Capped at 60 seconds. Fields the CLI derives from its environment (`env`,
`active` runtimes, `JAVA_HOME` matches, `PATH` lookups) come from the daemon's
environment as of daemon start, not the caller's shell, so they can differ
from the CLI run in a shell with a different `PATH` or `JAVA_HOME`.

### `timeline.report`

Takes optional `since_ms` (positive; default 3600000, the CLI's `--since 1h`)
and returns the `spectra timeline --since <d> --json` object
(`{"events": [...]}`). On macOS the sources are process starts, unified-log
errors, and install history; on Linux, process starts only, as with the CLI.
A source that fails is left out, as in the CLI; the CLI prints the error on
stderr and the daemon writes it to its log. Capped at 2 minutes.

### `snapshot.list`

Takes optional `kind` (`live` or `baseline`) and returns the
`spectra snapshot list --json` array of stored snapshot rows, newest first, or
`[]` when none match. It reads the daemon's database, which is the CLI's
unless `SPECTRA_WATCH_DB` relocates it. Capped at 10 seconds.

### `snapshot.diff`

Returns the `spectra snapshot diff --json` result for one of three operand
forms:

- `{"a": "<id>", "b": "<id>|live"}`: `spectra snapshot diff <a> <b>`.
- `{"baseline": true, "a": "<name|id>", "b": "<id>|live"}`: `spectra diff
  baseline [a] [b]`; `a` defaults to the newest baseline and `b` to `live`.
- `{"since_ms": N}`: `spectra snapshot diff --since <d>`, the newest stored
  snapshot older than the window against a live one.

`live` captures a fresh snapshot (apps included, as in the CLI), so such a
call can take a while on macOS; the cap is 3 minutes. Any other combination
is an invalid-params error. A missing snapshot fails with the CLI's message,
for example `snapshot "x": not found`.

## Process and service methods

Four methods serve process lineage, owner lookups, launchd services and
unified-log queries, so clients skip spawning `spectra lineage|owner|services|logs
--json`. Each returns exactly the JSON the matching CLI command prints with
`--json` for the same flags, because the CLI and the daemon build it with the
same function. Nothing is cached: every call collects anew. Each call has its
own server-side cap; when it passes, or the client's connection closes, the
call's context is cancelled, its child processes (`ps`, `lsof`, `launchctl`,
`log show`) are killed, and the call fails (`<method>: timed out after <cap>`
or `<method>: context canceled`) instead of returning partial data. Parameters
are an object; durations are milliseconds; paths must be absolute, because the
daemon's working directory is not the caller's. Bad parameters are an
invalid-params error (-32602). An error the CLI prints on stderr comes back as
the error message, with the same text.

The daemon runs as the caller's user, so processes owned by other users are
as visible, or as hidden, as they are to the CLI run without `sudo`.

`services.list` and `logs.query` are macOS-only, as are the CLI commands. Off
macOS they fail (-32603) with the CLI's message, and the error's `data` is the
object `spectra services|logs --json` prints there, with `command`, `error`
(`"unsupported_platform"`), `feature`, `os`, `message` and `hint` fields.

### `lineage.report`

Takes `pid` (required, positive) and returns the `spectra lineage --json <pid>`
object: the ancestor chain, the descendant tree with subtree RSS and CPU
totals, and flags. A pid that is not running fails with
`lineage: pid N: no such process`, as in the CLI. Capped at 30 seconds.

### `owner.report`

Answers `spectra owner --json <target>` or `spectra owner --under --json
<dir>...`:

- `target`: `:8080`, `tcp:8080`, `udp:53`, an absolute path, or a pid. Returns
  the owner object (`target`, `holders`, `incomplete`, `hint`).
- `under: true` with `dirs` (absolute directories): returns the `--under`
  object, the processes whose working directory is one of `dirs` or below it.
  The daemon and its own children are left out. Pass the caller's pid in
  `exclude_pid` to leave the caller and its children out too, as the CLI
  leaves out itself.
- `timeout_ms` caps the lookup (default 30000, the CLI's `--timeout`; at most
  300000).

As with the CLI, no holder is an empty `holders` array, not an error. The CLI's
exit codes 3 and 4 have no RPC equivalent: read `holders` and `incomplete`.

### `services.list`

Takes optional `domain` (`system`, the default, `user` or `all`), `label`
(substring, case-insensitive), `running` and `on_demand` (bools), and returns
the `spectra services --json` object (`jobs`, `collected_at`). `launchctl list`
runs in the daemon's launchd session. For a LaunchAgent or a daemon started
from a login shell, that is the same user session the CLI sees. Capped at
30 seconds.

### `logs.query`

Takes optional `process`, `subsystem`, `level` (`Default`, `Info`, `Error`,
`Fault`), `last_ms` (default 3600000), `grep` (a regular expression matched
against each message and format string), `top` (maximum rows, default 5000)
and `allow_long_window` (bool), and returns the `spectra logs --json` object
(`entries`, `truncated`, `clock`, `query_duration`). A window over 24 hours
without `allow_long_window`, or a `grep` that does not compile, is an
invalid-params error with the CLI's message. Raw `--predicate` queries and
`--stream` are not served. Capped at 2 minutes.

## Crash and symbolication methods

Five methods serve crash-report reads and `atos` symbolication, so clients
skip spawning `spectra crash list|signatures|oom|inspect --json` and
`spectra symbolicate --json`. Each returns exactly the JSON the matching CLI
command prints with `--json` for the same flags, because the CLI and the
daemon build it with the same function. Nothing is cached: every call reads
the reports anew. Each call has its own server-side cap; when it passes, or the
client's connection closes, the call's context is cancelled, the directory
walk stops, any `atos` or `mdfind` child is killed, and the call fails
(`<method>: timed out after <cap>` or `<method>: context canceled`) instead of
returning partial data. Parameters are an object; paths must be absolute,
because the daemon's working directory is not the caller's. Bad parameters are
an invalid-params error (-32602). An error the CLI prints on stderr, such as an
unreadable or legacy report, comes back as the error message with the same
text. All five are read-only.

### `crash.list`, `crash.signatures`, `crash.oom`

Take optional `dir` (an absolute directory of `.ips` reports, searched
recursively, including `Retired/` and dotfiles) and `limit` (default 25, the
CLI's `--limit`; `0` keeps every row). They return the arrays `spectra crash
list|signatures|oom --json` print: reports newest first, recurring signatures
ranked by count, and memory or jetsam kills newest first. Unreadable or
unparseable reports are skipped, as in the CLI. Capped at 2 minutes.

Without `dir` they read the caller's `~/Library/Logs/DiagnosticReports` on
macOS (the daemon runs as the caller's user). Off macOS there is no default
directory, as with the CLI: the call fails (-32603) with
`macOS DiagnosticReports: ... (linux); pass dir with an absolute directory of
.ips reports`, and the error's `data` is the unsupported object, with
`command`, `error` (`"unsupported_platform"`), `feature`, `os`, `message`
and `hint` fields. With `dir` they work on Linux too.

### `crash.inspect`

Takes `path` (required, an absolute `.ips` file), `source_lines` (bool, as
`--source-lines`) and `all` (bool, as `--all`, which only changes the CLI's
text output: the JSON always holds every thread). Returns the
`spectra crash inspect --json` object: the decoded report with `frame_details`
per thread, `images` with `image_on_disk`, and with `source_lines` the
`file`, `line`, `column` and `resolved_by` of each frame. Off macOS
`source_lines` resolves nothing and sets `source_lines_unsupported`, as in
the CLI. Capped at 3 minutes; each `atos` or `mdfind` call also has the CLI's
10 second limit.

### `symbolicate.addresses`

Takes `binary` (required, an absolute Mach-O or `.dSYM` path, the CLI's `-o`),
`load_address` (required, hex, `-l`), optional `arch` (`--arch`, such as
`arm64`) and `addresses` (required, hex, at most 4096), and returns the
`spectra symbolicate --json` object (`binary`, `load_address`, `arch`,
`frames`). Addresses must be hex values (the CLI passes anything through to
`atos`), so no parameter can reach `atos` as a flag. Capped at 1 minute.
macOS-only, as is the CLI: off macOS it fails (-32603) with the CLI's message,
and the error's `data` is the object `spectra symbolicate --json` prints there.

## Profiling jobs

`spectra sample`, `spindump` and `flamegraph` capture for seconds, so the
daemon runs them as background jobs instead of holding a call open: start a
job, poll it, then read the result. The result is exactly the JSON the
matching CLI command prints with `--json` for the same flags, because the CLI
and the daemon build it with the same function. Polling (rather than progress
notifications on one long call) lets a client reconnect or poll from another
connection, and keeps every call short.

- `profile.start` takes `kind` (`sample`, `spindump` or `flamegraph`) and
  that kind's parameters, checks them, and returns the job record at once.
- `profile.status` `{id}` returns the record: `id`, `kind`, `pid` or `input`,
  `status` (`working`, `completed`, `failed`, `cancelled`), `error` (a failed
  job's error, the text the CLI prints on stderr), `created_at`,
  `updated_at`, `deadline_at`, `expires_at` (once finished),
  `poll_interval_ms` (a suggested poll interval, 1000) and
  `result_available`.
- `profile.result` `{id}` returns a completed job's result. For a job that is
  still working, failed, or was cancelled, it fails (-32603) with the job's
  error (or `still working` / `was cancelled`), and the error's `data` is the
  job record.
- `profile.cancel` `{id}` cancels a working job: its capture's child process
  (`sample`, `spindump`) is killed and the job ends `cancelled`. On a finished
  job it does nothing. It returns the record.
- `profile.list` returns `{"jobs": [...]}`, every job still held, newest
  first.

Jobs are bounded:

- At most 2 captures run at once. `profile.start` beyond that fails until one
  ends (a cancelled capture holds its slot until its child process has
  exited). There is no queue.
- Each job has a deadline of its capture duration plus 30 seconds. A job
  that passes it fails with `profile <kind>: timed out after <limit>`.
- A finished job, with its result, is kept for 10 minutes, and only the 16
  most recently finished are kept (the cap applies as each job finishes, and
  expired results are released every 30 seconds even if nobody asks). After
  that, its `id` is an invalid-params error (-32602), as is an `id` the
  daemon never issued. A result over 8 MiB fails the job.
- Jobs are not tied to the connection that started them. Any client of the
  daemon (the daemon only accepts its own user) can poll or cancel by `id`.
  Stopping the daemon cancels every job as soon as shutdown starts.

Parameters (durations are milliseconds, a whole number of seconds up to
60000, as the CLI's `--duration`; paths must be absolute):

- `sample`: `pid` (required), `duration_ms` (default 1000), `interval_ms`
  (default 10, at most 1000). The result is the `spectra sample --json`
  object. The daemon does not cache the raw report or record an artifact
  (the CLI's default without `--no-cache` does both).
- `spindump`: `pid` with `duration_ms` (default 2000), or `input`, an
  existing spindump report to parse. The result is the
  `spectra spindump --json` object. The daemon runs as the caller's user,
  without `sudo`: a live capture needs root, so for most daemons it fails
  with the CLI's `spindump requires root` error and only `input` succeeds.
  `--out` is not served.
- `flamegraph`: `pid` with `duration_ms` (default 2000), or `input`, an
  existing sample report to fold, plus `out` (required), where the SVG is
  written as the caller's user. The result is the
  `spectra flamegraph --json` object (`svg`, `stack_count`, `stacks`). The
  SVG at `out` is the only file a profiling job publishes (live captures use
  a temporary `sample` report file, removed after use). The SVG is staged
  in a private (0600) `.spectra-svg-*` file next to `out` and moved into
  place only after the result has been encoded within its cap, and only if
  the job is still running. So a failed or `cancelled` job never touches
  `out`, and once the job has claimed publication `profile.cancel` no longer
  cancels it. A replaced `out` keeps its mode; a new one is created 0644
  less the daemon's umask, as the CLI creates it.

The three are macOS-only, as are the CLI commands. Off macOS `profile.start`
fails at once (-32603) without starting a job, and the error's `data` is the
object `spectra <kind> --json` prints there, for example on Linux:

```json
{
  "command": "sample",
  "error": "unsupported_platform",
  "feature": "process sampling (sample(1))",
  "os": "linux",
  "message": "process sampling (sample(1)): unsupported on host OS (linux)",
  "hint": "on Linux, `spectra procdiag <pid>` diagnoses a stuck or slow process from /proc"
}
```

Bad parameters are an invalid-params error (-32602). `profile.start` does
not touch the `input` file itself, so a hung mount cannot stall it: an
`input` that is missing, is not a regular file (a FIFO or device fails at
once instead of blocking), or is over 64 MiB fails the job.
