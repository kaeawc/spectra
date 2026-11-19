# CLI

`spectra` performs diagnostics on the local Mac.

```bash
spectra /Applications/Slack.app
spectra --json /Applications/Slack.app
spectra snapshot --baseline pre-incident
spectra diff baseline pre-incident live
spectra jvm
spectra network
spectra toolchain
```

The principal commands are `inspect`, `list`, `snapshot`, `diff`, `jvm`,
`network`, `power`, `storage`, `process`, `toolchain`, `rules`, `issues`, `alerts`,
`whatswrong`, and `playbook`. Run `spectra help` for the installed command list.

`spectra du [--top N] [--depth N] [--min-size 100M] [--cross-mounts] [--json] <path>`
lists the largest directories (cumulative) and files under a path by allocated
size, flagging sparse files with their apparent size. It stays on one
filesystem by default, never follows symlinks, counts hard links once, skips
already-visited directories, lists skipped mounts in `skipped`, and prints a
partial result (`partial: true`) on interrupt. Paths are reported under the
path as typed.

`spectra daemon start|stop|status|install|uninstall|print-plist|logs` manages
the local per-user daemon. `spectra daemon run` runs it in the foreground.

`spectra alerts [--state firing|resolved|all] [--limit N] [--json]` lists alerts.
`spectra alerts ack <id> [--json]` acknowledges one. `spectra alerts health
[--json]` summarizes load, memory, disk, processes, and active alerts.
`spectra alerts watch [--samples] [--json]` streams daemon notifications.
These commands require a running daemon. `spectra whatswrong [--json]` keeps its
local ranking and adds active daemon alerts and a five-minute trend when available.
Both commands accept `--no-daemon`, and `SPECTRA_NO_DAEMON=1` disables daemon
access globally.

`whatswrong` also sums CPU per process family and reports multi-process
simulator/emulator families at 100% CPU or more (browser, build and Xcode
families need a higher bar) as "`<family>` uses N% of machine CPU", ranked above
single-process findings. Each names the family (for iOS simulators, the device
UDID), its process count, the age of its oldest member, and its heaviest
members; `--json` adds a `families` array. See
[Running processes](../inspection/running-processes.md#process-families).

`spectra lineage [--json] <pid>` answers "who started this, and what did it
start?": the ancestor chain nearest-first up to pid 1 (user, start time,
command) and the descendant tree with per-node RSS and CPU plus subtree totals.
Nodes are flagged `zombie` (inferred, since no process state is collected:
`<defunct>`, or on Linux a bracketed empty-cmdline process with no memory whose
parent is not a kernel thread) and the target `reparented` (PPID 1 and non-root; a
heuristic, as per-user launchd agents also match). Exits 2 on bad usage and 1
for an unknown pid.

`spectra whois [--json] [--sudo] [--duration 3s] <pid>` (macOS only) attributes a
busy XPC/Mach service to the processes driving it. It samples the service, reads
the client PID from its `com.apple.NSXPCConnection.user.<service>.<pid>` (or
`.user.anonymous.<pid>`) dispatch queue names, and resolves each client to its
command line, parent chain, and launching tool (for example `xcodebuild`) with
its share of the sampled work. When the queue names carry no PID it falls back
to `lsmp` port ownership, which needs root. Root-owned services such as
`diagnosticd`, `logd`, and `mds_stores` can only be sampled with `--sudo`, which
runs `sample` and `lsmp` through `sudo -n`; run `sudo -v` first so it does not
need to prompt.

`spectra owner [--json] <target>` answers "who has this?" for a port (`:8080`,
`tcp:8080`, `udp:53`), a file path, or a pid. Ports list the processes listening
on or connected from that local port; paths list processes holding the file open
(with fd mode), as their cwd, or as their executable; a pid shows its user,
executable and start time. Every owner includes its parent chain up to pid 1.
macOS uses `lsof`, Linux uses `ss` and `/proc`. Without root only your own
processes are visible, and the output says the result may be incomplete. Exit
codes: 0 owner found, 1 error, 2 usage, 3 no owner found.

`spectra whatswrong` adds the same attribution, such as `OSLogService (17874)
busy, driven by CtrlProxyUITests-Runner (17649), launched by xcodebuild (17491)`,
when the busiest process is over 80% CPU.

`spectra thermal [--json] [--watch 30s [--interval 2s]]` reports a `nominal`,
`warm` or `throttled` verdict plus the single reading that decided it: macOS
parses `pmset -g therm` (CPU speed/scheduler limits and warning levels), Linux
reads `/sys` thermal zones (placeholder trips and disabled zones ignored),
Processor/powerclamp cooling devices, a fan at its maximum state, and
`thermal_throttle` counters. A cpufreq cap is only a note unless a zone is also
warm; idle fans and firmware cooling offsets are notes too. `--watch` reports the worst verdict seen and any counter increase.
Exit codes: 0 nominal, 3 warm, 4 throttled, 1 error, 2 usage.

`spectra impact [--interval 1s] [--top N] [--json]` (macOS only) samples per-pid
rusage and ranks processes by a documented energy impact score with a
per-component breakdown. `--from-json file|-` scores supplied records instead;
see [Power and thermal inspection](../inspection/power-thermal.md#energy-impact-score).

On Linux, `spectra procdiag <pid>` diagnoses a stuck or slow process from `/proc`
and `spectra pressure [--pid N]` shows pressure-stall and cgroup v2 accounting;
see [Linux process diagnosis](../inspection/linux-process-diagnosis.md).

`spectra spawns [--interval 250ms] [--duration 10s] [--pid N] [--json]` traces
process starts and exits live by polling the process table, to catch
short-lived children that a one-shot `spectra process --tree` misses. Each
event carries the pid, ppid, parent chain, command and (on exit) lifetime;
`--pid` scopes the trace to a process and its descendants. It ends with
per-parent and per-command spawn counts, the shortest-lived processes and
respawn loops (the same command started 3+ times under one parent); the
by-parent and by-command lists are cut to the top 10 and say how many distinct
entries there were. `--interval` must not exceed `--duration`; a final
collection runs at the deadline. Lifetimes are approximate (`lived ~0.2s`;
JSON `lifetime_ms` with `lifetime_max_error_ms`). An unknown `--pid` exits 1;
if the scoped process exits mid-trace its tracked descendants stay in scope.
Polling misses processes that live for less than the interval, and a pid
reused within the same second is not distinguished.

Some commands read macOS-only sources. On Linux they exit `1` with an
"unsupported on host OS (linux)" error instead of returning empty results:
`spectra logs` (unified log), `spectra updates` (install log and history),
`spectra schedule` (launchd; use cron or a systemd timer to run
`spectra snapshot`), and `spectra crash` readiness. `spectra crash
list|signatures|oom` need `--dir` with copied `.ips` reports there, while
`crash inspect|resource|state` parse given files on any host.
`spectra timeline` shows process starts only on Linux.

`spectra load` answers "can this machine take another job?" in one call. It
exits `0` (admit), `3` (hold: 1-minute load at or above
`--max-load-per-cpu` x CPUs, available memory below `--min-free-gb` GiB, or
memory that could not be read: it fails closed) or `4`
(backoff: memory below `--backoff-free-gb` GiB), and attributes CPU and RSS to
processes, families and git worktrees (`--root DIR`, repeatable, names a
worktree directory for the `roots` cause). `--json` emits stable snake_case
fields including `elapsed_ms` and `cpu_window_ms`. On Linux, per-process CPU
is measured over a short window (`--sample`, default `500ms`) from two
`/proc/<pid>/stat` readings, matched by pid and start time, so a busy-now
process outranks one that was only busy earlier; `--sample 0` falls back to
whole-lifetime averages. `cpu_basis` says what the CPU figures mean:
`window` (Linux delta over `cpu_window_ms`), `recent` (macOS, where `ps`
reports a decaying recent average; `cpu_window_ms` is `0`) or `lifetime`
(Linux with `--sample 0`, or nothing could be diffed; `cpu_window_ms` is
`0`). A process that started during the window is scored on its CPU over the
window. The text output has a `cpu` line saying which basis applied.

Spectra has no `serve`, `connect`, `fan`, `hosts`, or `install-daemon`
commands. Cross-machine operations use the separately distributed Spectra
Remote project.
