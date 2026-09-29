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

Warnings include load above 1.5 times CPU count for two samples, rising load,
memory pressure for two samples, swap growth above 1 GB in five minutes,
resource limits above 70%, less than 20 GB free, thermal throttling for two
samples, a process kind above 600% CPU for three samples, and high Gradle,
simulator, emulator, or Codex plus Claude counts. Critical alerts cover load
above three times CPU count for two samples, critical memory pressure, limits
above 90%, and less than 5 GB free. Alerts clear after two healthy evaluations.

Thresholds can be overridden in `<daemon directory>/watch.yml`. Sections are
`load`, `memory`, `limits`, `disk`, `thermal`, `kinds`, and `spawn`; field names are the
snake case names in the defaults, such as `load.warn_multiple` or
`disk.critical_gb`. The `spawn` keys are `interval` (duration, minimum `1s`),
`rate_warn`, `rate_critical`, `uid_warn_pct`, `uid_critical_pct`,
`total_warn_pct`, and `total_critical_pct`. An unknown YAML key logs an error and keeps all defaults.
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
