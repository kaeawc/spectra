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

## Monitoring and alerts

`daemon run` samples host health every 15 seconds. `--watch-interval 2s` changes
the interval and `--no-notify` disables desktop notifications. The watch uses
one bulk `ps` call per tick for process counts, CPU, RSS, top processes, and process
history. When Java processes are present, one additional `ps` call fetches the
arguments for up to 64 Java PIDs so Gradle and Kotlin daemons can be classified.
It also reads load average, memory pressure and swap, kernel limits,
free space on the data volume, thermal state, and its own CPU time. It does not
scan apps, collect snapshots, or inspect protected paths. On macOS, thermal
state requires one `pmset -g therm` call; on Linux, thermal throttling is not
reported where the kernel has no common direct signal. Limits without a cheap
current-usage counter are reported with a zero limit percentage and do not
trigger an alert. If the daemon averages more than 2% CPU over ten ticks, the
watch doubles its interval up to four times the base; it returns to the base
below 0.5%.

An independent process-spawn probe runs every five seconds, including while a
regular sample is blocked or backed off. On macOS it reads `kern.proc.all` and
on Linux it reads `/proc`; neither backend forks. It counts new PIDs since the
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
them. If the probe's `ps` call fails, that tick is skipped entirely and no alert
is resolved on missing evidence. After five consecutive failed probes the daemon logs a warning (and another every five
further failures, plus an info line on recovery) and appends "[probe unavailable: ...]" to the
detail of every firing sustained alert, persisted immediately. The alerts stay
firing with the same ID, because resolving them as healthy would be a claim
without evidence; the note tells the reader they are stale. The note disappears
when the probe recovers and the alert is re-evaluated. The probe takes its own timestamp when it runs,
because `ps` elapsed times are only meaningful against the snapshot time. The probe runs last in the watch
tick, after alert persistence, notifications and maintenance, so a slow sample
never delays them. A process that `ps` lists but whose `/proc` stat read fails
for a tick keeps its state and previous snapshot rather than resolving; if it
stays unreadable for a whole window (for example `hidepid`), the alert resolves.
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
`load`, `memory`, `limits`, `disk`, `thermal`, `kinds`, `spawn`, and `sustained`; field names are the
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

The watch stores JSON samples for seven days and resolved alerts for 30 days
in the Spectra SQLite database. `SPECTRA_WATCH_DB` overrides the database path
for isolated runs. Process metrics are aggregated each minute. The RPC methods
are `watch.current`, `watch.samples` (`since`, `limit`), `alerts.list` (`state`,
`limit`), `alerts.ack` (`id`), `alerts.subscribe` (`samples`), and
`process.history` (`pid`, `limit`). A subscription sends `alerts.event`
notifications and optionally `watch.sample` notifications until its connection
closes. Slow subscribers can miss events; clients can recover from the stored
history. Desktop notifications are best effort, capped at six per hour and
cooled down per alert key for 15 minutes. LaunchAgent sessions may lack desktop
notification permission.

## Clients

The CLI and stdio MCP server discover the running local daemon through its Unix
socket. They never start it automatically. Run `spectra daemon start` or install
the per-user service before using alert and host-health commands.
`SPECTRA_NO_DAEMON=1` and `--no-daemon` apply to daemon-consuming commands:
`spectra alerts`, `spectra whatswrong` enrichment, and the MCP `host_health`,
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
