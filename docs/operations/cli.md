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

`spectra network top [--duration 10s] [--interval 1s] [--top 10] [--pid N] [--json]`
answers "who is using the network right now?". On macOS each interval is one
`nettop -P -x -d -L 2 -s <secs> -t external` run, whose delta block counts
what every flow moved in it, including flows that opened and closed inside it;
nettop's delay is whole seconds, so `--interval` and `--duration` are rounded
up to whole seconds, and an interval cut short by Ctrl-C is dropped. On Linux
it diffs per-socket TCP counters from `ss -tinpeH` (`bytes_received` in,
`bytes_acked` out, keyed by the socket's `sk:` cookie, or by local/remote
address when ss prints none) sampled on fixed deadlines; a socket first seen
after the baseline sample counts its whole counter, since ss counters start at
0 when the socket opens. Each ss poll is bounded by 10s and ends at once on
Ctrl-C, and `--interval` must be at least 100ms on Linux (exit 2 otherwise). It
prints one line per interval with
the total in/out rate, and ends with a table ranking processes by bytes moved:
bytes in/out, average and peak rates, user, working directory, and up to 3
remote endpoints (on Linux the remotes whose sockets moved the most bytes
inside the window, including sockets that closed before it ended,
`remotes_basis` `window-bytes`;
on macOS one connection listing taken at the end of the window, `remotes_basis`
`end-snapshot`, shown under a "REMOTES (open at end of window)" header, since a
connection that closed earlier is not listed). Rates use the actual time each interval took, not the nominal
interval. The summary also lists processes that started using the network
(their first interval with traffic came after the first measured interval) and
processes that stopped (their last came before the last measured interval); a
failed interval is not evidence either way. Loopback traffic is not counted; on Linux only TCP is counted and
other users' sockets need root. `--pid` limits the output to that process and
its descendants: the per-interval lines count the descendants alive at the
start, the summary also those alive at the end. Ctrl-C ends early and still
prints the summary with the actual duration; if Ctrl-C comes before the first
interval completes, it prints an empty zero-duration summary marked interrupted
and exits 0. A host where nothing moved (or,
unprivileged, nothing is visible) reports "no process moved any bytes" and
exits 0. `--json` emits one object with `duration_ms`, `interval_ms`,
`samples`, `basis` (`nettop` or `ss-tcp`), `remotes_basis`, `totals`, `processes`
(each with `started`/`stopped` flags), `started`,
`stopped` and `limitations`. Exit codes: 0 ok, 1 runtime or output error
(a missing sampling tool, or no interval measured because the tool failed on
every sample or, on Linux, on every sample after the baseline), 2 usage error.

`spectra du [--top N] [--depth N] [--min-size 100M] [--cross-mounts] [--json] <path>`
lists the largest directories (cumulative) and files under a path by allocated
size, flagging sparse files with their apparent size. It stays on one
filesystem by default, never follows symlinks, counts hard links once, skips
already-visited directories, lists skipped mounts in `skipped`, and prints a
partial result (`partial: true`) on interrupt. Paths are reported under the
path as typed.

`spectra fswatch <path> [--duration 30s] [--interval 1s] [--top 10] [--max-entries N] [--holders-timeout 10s] [--json]`
answers "what keeps writing here?" by snapshotting the tree under a path each
interval and streaming created, modified and removed events, each tagged with
the entry kind (`file`, `dir`, `symlink`, `other`); a path that changes kind is
reported as a `removed` of the old kind followed by a `created` of the new one.
It then summarizes counts by kind and the busiest files and directories, naming
the processes that hold each busiest surviving regular file open when the watch
ends (symlinks are never looked up). Holder lookup covers at most the top 5
files and shares one `--holders-timeout` budget (`0` disables it); when owner
could only see part of the system, or the budget ran out, the output says so and
JSON sets `summary.holders_incomplete`. It is a polling watcher: files that
appear and vanish between polls are missed and several writes within one
interval count once. It never follows symlinks. Past `--max-entries` (default
200000) the first entries in tree order stay tracked and changes beyond the cap
are not seen; this is reported once with the tracked count. Unreadable
subdirectories (including ones that list but whose entries cannot be stat'ed)
are reported once as warnings (`warnings` in JSON) and produce no removals; a
`..` after a symlinked component is applied to the resolved path, as the
filesystem does, and a holder lookup that ignores its timeout is abandoned and
reported as incomplete; a poll where the root cannot be read is skipped and counted in
`polls_failed`, and a root that no longer exists ends the watch with
`ended_reason`. Ctrl-C abandons any scan in progress and prints the summary with
the actual elapsed time; a second Ctrl-C terminates immediately. `--json` emits
one object at the end.

`spectra daemon start|stop|status|install|uninstall|print-plist|logs` manages
the local per-user daemon. `spectra daemon run` runs it in the foreground.

`spectra process --classify [--json]` adds `role` (`app`, `app_helper`,
`system`, `shell`, `terminal`, `ai_agent`, `cli`), `runtime` (`electron`,
`native`, `rust`, `go`, `jvm`, `node`, `bun`, `deno`, `python`, `ruby`,
`shell`, `unknown`), `app` (`{name, bundle_path, bundle_id}` of the outermost
owning `.app`), `signing` (macOS only: `{status, team_id, hardened_runtime}`,
status `apple`, `developer_id`, `adhoc`, `unsigned` or `unknown`) and
`evidence` to every listed process; the table gains `ROLE`, `RUNTIME` and, on
macOS, `SIGN` columns. Runtime comes from the executable name, the owning
bundle (Electron Framework, a bundled JBR) and then the binary itself (Go
build info, Bun's segment, rustc markers); signing is one `codesign -dv` per
unique executable. Results are cached per executable by (path, size, mtime)
in the `classify` cache kind, so only the first run is slow. Without
`--classify` the output and cost of `spectra process` are unchanged. The rules
and precedence are in
[Running processes](../inspection/running-processes.md#process-classification).

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

`spectra memtrace <pid> [--tree] [--duration 60s] [--interval 2s] [--json]`
samples one process's resident memory over time and says whether it is
growing. It prints one line per sample, then the sample count, min, max,
first, last and net change (as facts only), a growth rate in KiB/minute and a
verdict. The growth rate is the Theil–Sen slope (median of pairwise slopes), so
a single spike or where the endpoints land does not move it. The trend change
(slope × observed span) is compared with max(5% of the median RSS, 1 MiB):
`growing`, `shrinking` or `steady`. A series with two or more full cycles of
swings of at least 10% of the median is a `sawtooth`; its troughs are fitted
the same way, so a leak under garbage collection reports `growing` with
`pattern: "sawtooth"` ("growing under a sawtooth"). With fewer than 3 troughs
the all-sample trend decides instead, and the trough-to-trough and
peak-to-peak changes must agree with its sign. `unreadable` means every
reading was 0 and `insufficient` means fewer than 5 samples, so `--duration`
must be at least 4 intervals (a hint is printed when it is not). Polls run at
fixed deadlines; a collection that overruns one skips it, and JSON reports
`polls_failed` and `polls_skipped` (text shows them when non-zero). A start
time that drifts by up to 1 second is the same process. `--tree` also sums the
whole descendant subtree (and its process count) per sample, gives it its own
verdict, and lists the top 5 descendants by RSS growth. An unknown pid exits 1; if the process exits
mid-trace, or its pid is reused by another process, the trace stops and
reports what was collected with exit 0. A target that has exited but is not yet reaped (a
zombie) counts as exited and its zero reading is not recorded; with `--tree`, zombie descendants
are excluded from the subtree sum, count and growers. JSON `summary.exit_reason` is `gone` or
`zombie` when exited. Ctrl-C ends early and still prints the
summary. `--json` emits one object with `samples`, `summary` and `growers`.

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

`spectra orphans [--root DIR]... [--min-age 1m] [--pids] [--json]` finds
leftover processes from worktrees and finished jobs: parent pid 1, a readable
working directory under a root (default: the current directory), and older than
`--min-age`. Spectra itself and its ancestors are excluded. PPID 1 is a
heuristic: it also covers per-user launchd/systemd services, tools that
daemonize on purpose, and anything started with nohup/disown. The list is
evidence, not a kill list, and a candidate with descendants may be the parent
of live work. Each candidate reports pid, user, age, RSS, CPU, cwd, command
name, full command line (truncated to 120 characters in text, complete as
`command_line` in JSON), descendant count and subtree RSS. `cwd_missing` is set
only when the directory does not exist (including a Linux `(deleted)` cwd);
`cwd_unreadable` marks any other stat failure such as a permission error or an
unmounted volume. Output is grouped by worktree like `spectra load`; group
RSS is the sum of subtree RSS ("incl. descendants"). A process whose cwd is
inside a `.../worktrees/<name>` directory that lost its `.git` is grouped under
that directory with `worktree_removed: true`. Known daemonize-by-design tools
(tmux, screen, adb, emulator, qemu-system-*, caffeinate, Gradle and Kotlin
daemons, ssh-agent, gpg-agent, dockerd, containerd, colima, lima*, syncthing)
are flagged `daemon_by_design: true`, shown in a trailing "Probably intentional
(daemonize by design)" section, and excluded from `--pids` and the exit code.
`--pids` prints a space-separated pid line of the remaining candidates for you
to review; the command never kills anything. Processes with an unreadable
working directory are skipped and counted. Exit codes: `0` none found, `3`
non-daemon orphans found, `1` runtime or output error, `2` usage error.

Spectra has no `serve`, `connect`, `fan`, `hosts`, or `install-daemon`
commands. Cross-machine operations use the separately distributed Spectra
Remote project.
