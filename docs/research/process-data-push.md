# Process data: polling cost and push APIs

Research note, 2026-11-25. It answers two questions: how Spectra gets its
data today and what that costs, and which macOS APIs could push process and
system events to Spectra instead of Spectra polling for them. Status:
**research**, not a design. Each recommended step is tracked as its own issue
(see [Recommendation](#recommendation)), and steps that touch the privileged
helper or Endpoint Security are design-only until the owner approves them under
the helper ground rules in
[#600](https://github.com/kaeawc/spectra/issues/600).

The measurements are reproducible with
`scripts/research/measure-collection.sh`
(command and daemon cost) and
`scripts/research/procpush`
(a standalone probe of the macOS primitives, in its own Go module so it never
ships in a Spectra build).

## Summary

- Nothing in Spectra is event driven today. Every live reading is a poll, and
  on macOS the hot polls fork `ps` and `pmset`.
- On macOS the idle daemon spends about **5.5 ms of CPU per second**, and about
  half of that is the two `ps` execs it makes every 15 s tick. A `ps` call
  costs 18 to 25 ms of CPU; the `kern.proc.all` sysctl that already backs the
  spawn probe returns the same process table in **0.07 ms**, about 250 times
  cheaper. Forking under heavy load at background priority is also what makes
  collection overrun, the problem behind
  [#708](https://github.com/kaeawc/spectra/issues/708).
- macOS has **no unprivileged system-wide "process started" push**. The only
  system-wide push is Endpoint Security, which needs root, Full Disk Access
  and (for a native client) an Apple-granted entitlement plus an app-like
  bundle with a provisioning profile. Apple's `eslogger` CLI exposes the same
  events without our own entitlement, but still needs root and Full Disk
  Access.
- Without privileges, kqueue `EVFILT_PROC` gets close: a non-root process may
  watch **every** pid (root's and other users' included) for exit, fork and
  exec. Measured on macOS 26: 471 of 471 pids registered, under 1 µs each. A
  fork on a watched pid triggers a cheap `kern.proc.all` rescan that finds the
  new child. In a test with 200 short-lived children, the fork-triggered
  rescan saw **200 of 200**; 250 ms polling, the `spectra spawns` default,
  saw **1**.
- Recommended order: drop `ps` from the daemon's per-tick path (small, safe,
  biggest steady-state win); then an event-driven process table on kqueue with
  a slow reconcile poll; then cheap pushes for thermal, power, crash reports
  and app installs; and only then, with the owner's go-ahead, a helper-backed
  Endpoint Security stream.

## Today's data paths

All four entry points share the collector packages, so a data source costs the
same wherever it is called from. What differs is cadence.

| Entry point | How it collects | Cadence |
|---|---|---|
| CLI (`cmd/spectra`) | Calls collectors directly, or asks the daemon first for `alerts`, `whatswrong`, `load`, `idle` and `orphans` | On demand |
| Daemon (`spectra daemon run`) | `internal/hostwatch` loops plus on-demand RPC collectors with TTL caches (`internal/daemonhost`, `internal/daemonprocess`) | 15 s watch tick, 5 s spawn probe, plus on-demand calls |
| MCP server (`cmd/spectra-mcp`) | Same collectors as the CLI; follows the daemon's `watch.subscribe` stream for live resources, or runs a local `hostwatch.Collector` when no daemon is up and `spectra://host/health` is subscribed | On demand; subscribed health updates at most every 10 s |
| Privileged helper (`cmd/spectra-helper`) | Allowlisted root commands: `ps`, `powermetrics`, `fs_usage`, `tcpdump`, `pfctl`, system TCC reads | On demand only, per request |

### Collection mechanisms

- **Exec through `proc.Runner`** (`internal/proc`) is the default: `ps`,
  `pmset`, `lsof`, `nettop`, `top`, `scutil`, `route`, `diskutil`, `codesign`,
  `plutil`, `spctl`, `launchctl`, `jps`/`jcmd`/`jstat`, `brew`, `log`, `sample`,
  `spindump`, `system_profiler`, and `ss`/`df` on Linux. The default timeout is
  two minutes.
- **Direct sysctl from Go** (`golang.org/x/sys/unix`, no cgo):
  `kern.proc.all` (spawn probe), `kern.procargs2` (argv), `vm.loadavg`,
  `kern.memorystatus_vm_pressure_level`, kernel limits, `statfs`,
  `getrusage`.
- **cgo, Darwin only, four files**: `proc_pidinfo(PROC_PIDTASKALLINFO)` and
  `proc_pidpath` for thread counts and paths (`internal/process/thread_darwin.go`),
  `proc_pid_rusage` for energy (`internal/sysinfo/rusage_darwin.go`),
  `host_statistics64` and `vm.swapusage` (`internal/memstate`), and IOReport
  SoC power (`internal/sysinfo/power_ioreport_darwin_arm64.go`).
- **File reads**: `/proc` and `/sys` on Linux, `~/Library/Logs/DiagnosticReports`,
  launchd plist directories, app bundles, toolchain directories.
- **Events**: none. There is no kqueue, FSEvents, inotify, dispatch source or
  notify(3) use anywhere. `spectra fswatch` and `spectra spawns` both poll and
  diff, and say so in their output.

### What reads what, and how often

| Data | macOS | Linux | When |
|---|---|---|---|
| Process table, daemon tick | `ps -Ao pid=,ppid=,rss=,%cpu=,comm=` + `ps -o pid=,args= -p <java pids>` | same `ps -eo ...` | Every 15 s (`--watch-interval`), backed off up to 4x when the daemon itself is busy |
| Sustained-CPU probe | `ps ... pid=,ppid=,%cpu=,etime=,comm=` | same `ps`, plus `/proc/<pid>/stat` | Every watch tick |
| Spawn probe and spawn origins | `kern.proc.all` sysctl; `lsof -p` for cwd of new parents | `/proc` walk | Every 5 s |
| Thermal | `pmset -g therm` | `/sys/class/thermal` | Every watch tick |
| Load, memory pressure, swap, limits, disk free | sysctl, `host_statistics64`, `statfs` | `/proc`, `statfs` | Every watch tick |
| `process.list` / `spectra process` | `ps -axwwo ...lstart,command`, `ps -axwwo pid=,comm=`, libproc detail | `/proc` | On demand; 2 s daemon cache |
| `spectra spawns` | `ps` (through `process.CollectAll`) | `/proc` | Every 250 ms for 10 s by default |
| Memory, thermal, power, network, whatswrong RPCs | `pmset`, `top`, `lsof`, `nettop`, `scutil`, `route` | `/proc`, `ss` | On demand; 2 to 15 s caches, stale-while-revalidate for power and whatswrong |
| JVM history | `jps`, `jcmd`, `jstat` | same | Off by default; every 5 min when enabled |
| Gatekeeper refresh | `spctl --assess` per app | n/a | Off by default; hourly when enabled |
| Crash reports | Directory scan of DiagnosticReports | core files | On demand |
| Apps and bundles | `plutil`, `codesign`, `otool`, `file`, `spctl` | unsupported | On demand (`inspect`, `snapshot`) |
| launchd and startup items | plist scans, `launchctl print`, `launchctl print-disabled` | systemd units | On demand |
| Toolchains | `brew`, `which`, directory scans | `which`, directory scans | On demand |

Daemon wiring is in `cmd/spectra/daemon.go`. The watch loop is
`internal/hostwatch/service.go`, the bulk sample is
`internal/hostwatch/sample.go` (`collectPS`), the sustained probe is
`internal/hostwatch/sustained_probe.go`, and the spawn probe is
`internal/hostwatch/spawn.go` with per-OS backends in `spawn_darwin.go` and
`spawn_linux.go`. The launchd job runs at `ProcessType Background`,
`Nice 10`, `LowPriorityIO`; the systemd unit at `Nice=10`, `CPUWeight=20`,
`IOSchedulingClass=idle`.

### The overrun fallback (#708)

When a host is saturated (load averages in the hundreds during emulator and
Xcode builds), the daemon's background-priority collection could take longer
than its 15 s budget and fail. PR
[#718](https://github.com/kaeawc/spectra/pull/718) made `memory.current`,
`thermal.current`, `power.current`, `network.current`, `whatswrong.current`
and `process.list` wait at most 3 s and then answer with the last good
reading, marked `stale: true` with `sampled_at`, while the collection keeps
running and refills the cache. That fixed the symptom (no 15 s hang, no
error). It does not make readings fresh under load. The cause is that each
reading forks several short-lived tools, and every fork, exec, dyld load and
pipe read is scheduled at background priority behind the load. In-process
sysctl and libproc calls avoid all of that.

## Measurements

### Hosts

- **macOS**: GitHub-hosted `macos-26-arm64` runner, macOS 26.7.2 (25G332),
  Apple M2 Pro (virtual), 5 CPUs, 14 GiB, about 470 processes. **SIP is
  disabled on this image**, so the DTrace and `eslogger` results below say
  what the API does, not what a user's Mac allows.
- **Linux**: Hive VM, Ubuntu 22.04, kernel 6.1, 4 CPUs, about 115 processes.

Times are the median of three runs and include child processes. Exec counts
come from `strace -f` on Linux (exact) and from PATH shims on macOS (a lower
bound: a tool run by absolute path, such as `/bin/launchctl`, is not counted).

### Primitive cost on macOS

| Primitive | Wall per call | CPU per call | Notes |
|---|---|---|---|
| `kern.proc.all` sysctl (470 procs) | 0.07 ms p50, 0.16 ms p95 | 0.11 ms | pid, ppid, uid, comm, start time |
| `ps -Ao pid=,ppid=,rss=,%cpu=,comm=` | 19 ms p50, 21 ms p95 | 18 ms (child) | daemon tick, sustained probe |
| `ps -axwwo ...,lstart=,command=` | 31 ms p50, 48 ms p95 | 25 ms (child) | `spectra process` |
| `proc_pidinfo(PROC_PIDTASKINFO)` | pending (queued macOS run) | | RSS and CPU time; whether non-root can read other users' pids decides how step 1 handles them |
| `proc_pid_rusage(RUSAGE_INFO_V4)` | pending (queued macOS run) | | footprint, energy; same visibility question |
| kqueue `EVFILT_PROC` registration | 0.5 µs per pid | | NOTE_EXIT, NOTE_FORK, NOTE_EXEC |

`ps` against `kern.proc.all` under CPU load at background priority (`taskpolicy -b`, like the LaunchAgent's `ProcessType Background`) is measured by the same probe (`-cost-only`); that run is still queued, so this note gives no number for it yet.

### Daemon, idle, default 15 s tick

| Host | CPU over 120 s | CPU per second | Execs | Exec mix |
|---|---|---|---|---|
| macOS | 0.66 s | 5.5 ms | 25 (12.5/min) | `ps` 16, `pmset` 8, `lsof` 1 |
| Linux | 0.49 s self + child `ps` | 4.1 ms self | 2 per tick | `ps` only |

On macOS, 16 `ps` calls at about 20 ms of CPU each are roughly 0.33 s, half
of the daemon's total. On Linux the daemon also makes about 1,080 syscalls per
second at idle, most of them `openat`/`read`/`fcntl`/`close`/`epoll_ctl` from
the 5 s `/proc` walk: Go's `os.Open` registers each file with the network
poller, which costs several syscalls per file.

### Commands

macOS, `SPECTRA_NO_DAEMON=1`:

| Command | Wall s | User s | Sys s | Execs | Exec mix |
|---|---|---|---|---|---|
| `spectra process` | 0.068 | 0.026 | 0.045 | 2 | `ps` 2 |
| `spectra process --deep` | 0.200 | 0.103 | 0.095 | 3 | `ps` 2, `lsof` 1 |
| `spectra system` | 0.010 | 0.005 | 0.004 | 0 | |
| `spectra memory` | 0.012 | 0.006 | 0.006 | 0 | |
| `spectra thermal` | 0.025 | 0.011 | 0.012 | 1 | `pmset` 1 |
| `spectra power` | 1.191 | 0.038 | 0.159 | 4 | `pmset` 3, `top` 1 |
| `spectra network` | 1.169 | 0.317 | 1.057 | 7 | `scutil` 2, `lsof` 2, `route`, `nettop`, `ifconfig` |
| `spectra storage` | 0.302 | 0.294 | 0.424 | 12 | `diskutil` 10, `mount`, `df` |
| `spectra toolchain` | 1.492 | 2.442 | 0.456 | 10 | `which` 5, `brew` 3, `xcrun`, `bazel` |
| `spectra jvm` | 0.143 | 0.104 | 0.024 | 1 | `jps` |
| `spectra services` | 0.049 | 0.027 | 0.021 | 0 | |
| `spectra startup` | 0.076 | 0.052 | 0.035 | 0 | |
| `spectra whatswrong` | 1.166 | 0.056 | 0.186 | 8 | `pmset` 3, `sysctl` 2, `ps` 2, `top` |
| `spectra load` | 0.125 | 0.037 | 0.080 | 5 | `ps` 2, `vm_stat`, `sysctl`, `lsof` |
| `spectra snapshot --json` | 117 | 136 | 310 | 155+ | `codesign` 35, `plutil` 34, `ps` 25, `sysctl` 15, `diskutil` 10 |

Linux, same flags (115 processes):

| Command | Wall s | Execs | Syscalls | Exec mix |
|---|---|---|---|---|
| `spectra process` | 0.043 | 0 | 6,520 | |
| `spectra network` | 0.062 | 3 | 6,142 | `ss` 3 |
| `spectra toolchain` | 0.029 | 6 | 3,434 | `which` 5, `make` |
| `spectra snapshot --json` | 0.098 | 19 | 31,266 | `sysctl` 6, `which` 5, `ss` 3, `ps` 2 |
| `spectra load` | 0.577 | 0 | 14,522 | (0.5 s is a deliberate sampling window) |
| other read commands | 0.02 to 0.05 | 0 to 1 | 600 to 6,600 | |

Observations:

- The one-second commands (`power`, `network`, `whatswrong`) are dominated
  by sampling windows (`top -l 2 -s 1`, `nettop -L 2`), not by Spectra.
- `snapshot` on macOS is dominated by app-bundle inspection (`codesign`,
  `plutil` per app) and calls `ps` 25 times. It runs on demand, not on the
  daemon tick, so it is out of scope for push APIs, but the repeated `ps`
  calls are worth folding into one process table.
- `spectra spawns` on macOS forks `ps` four times a second by default, about
  80 to 100 ms of CPU per second while it runs, and still misses every
  process shorter than 250 ms.

### Push primitives on macOS

| Probe | Non-root | Root |
|---|---|---|
| `EVFILT_PROC` `NOTE_EXIT\|NOTE_FORK\|NOTE_EXEC` on every pid | 471/471 ok (own 255, root 145, other users 70) | 472/472 ok |
| `EVFILT_PROC` with `NOTE_EXITSTATUS` | own pids only; root and other users `EACCES` | all but one ok |
| 200 short-lived `/usr/bin/true` children: fork-triggered `kern.proc.all` rescan | 200/200 seen, 200 fork events, 29 ms of rescans, 43 ms observer CPU | 200/200 |
| same workload, 250 ms `kern.proc.all` poll | 1/200 seen | 0/200 |
| `eslogger exec fork exit` | n/a (needs root) | ran on the runner: 317 events and 827 KB of JSON (about 2.6 KB per event) over an 11 s window that included 200 `/usr/bin/true` runs; the per-type breakdown and idle rate are pending |
| `dtrace -n proc:::exec-success` | n/a | works on this runner because SIP is disabled; blocked on a default Mac |

The `NOTE_EXITSTATUS` result matches XNU's `filt_procattach`, which only
permission-checks a registration that asks for the exit status (the caller
must be the parent or able to signal the target). Plain `NOTE_EXIT`,
`NOTE_FORK` and `NOTE_EXEC` only need the target to be visible.

## Push and event APIs on macOS

| API | Pushes | Privilege and entitlement | Go route | SIP, notarization, packaging | Volume and overhead |
|---|---|---|---|---|---|
| kqueue `EVFILT_PROC` | exit, fork, exec, signal for **known pids**; no child pid, no system-wide new-process event (`NOTE_TRACK` removed in 10.5) | none; exit status of others needs parent or `cansignal` | pure Go (`x/sys/unix.Kevent`) | none | one knote per pid, under 1 µs to register; one event per fork/exec/exit of a watched pid |
| `DISPATCH_SOURCE_TYPE_PROC` | same as above, via libdispatch | none | cgo (blocks); no reason over raw kqueue | none | same |
| Endpoint Security (`es_new_client`, `NOTIFY_EXEC/FORK/EXIT`) | **system-wide** exec, fork, exit and many more, with audit token, path, argv, env, code-signing info | `com.apple.developer.endpoint-security.client` (Apple grant), root, Full Disk Access (`ES_NEW_CLIENT_RESULT_ERR_NOT_PERMITTED`) | cgo or a Swift/ObjC shim | provisioning profile, so an app-like bundle; launchd daemon or System Extension; a bare Homebrew CLI cannot be a client | high on a dev box; mute by path; slow handlers back up the queue |
| `eslogger` (macOS 13+) | same NOTIFY events as ES, as JSON lines | root, and Full Disk Access for the responsible process; no entitlement of our own | subprocess from the root helper, parse NDJSON | Apple's binary; schema "not stable" per its man page | about 2.6 KB of JSON per event on the runner |
| NSWorkspace `didLaunch`/`didTerminateApplication`, `runningApplications` KVO | launch and exit of **GUI apps** (notifications skip LSUIElement and background apps; KVO covers them) | logged-in session | cgo/ObjC with a run loop (AppKit) in the LaunchAgent | none | tiny |
| `DISPATCH_SOURCE_TYPE_MEMORYPRESSURE` | normal/warn/critical transitions | none | cgo (blocks) | none | about zero |
| `kern.memorystatus_vm_pressure_level` | nothing (level 1, 2 or 4) | none | pure Go (already used) | none | trivial poll |
| `proc_pid_rusage`, `proc_pidinfo` | nothing (per-pid counters to diff) | see the visibility row above | cgo (already used) or purego | none | per-pid syscall |
| `OSLogStore.local()` | nothing (query) | admin account plus `com.apple.logging.local-store` | Swift/ObjC | entitlement | n/a |
| `log stream --predicate ... --style ndjson` | matching log entries | none (private fields redacted) | subprocess (already used by `logs`) | none | each stream makes `logd`/`diagnosticd` evaluate the predicate; Spectra's own sustained-CPU alert names `diagnosticd` as a cost of log streaming |
| FSEvents (`FSEventStreamCreate`) | recursive, coalesced path changes; `MustScanSubDirs` on drops | none (TCC-protected folders aside) | cgo (`fsnotify/fsevents`) or purego | none | low, latency-coalesced |
| kqueue `EVFILT_VNODE` (what `fsnotify` uses on macOS) | per-fd writes, renames, deletes | none | pure Go | none | one fd per watched file or directory; fine for a few flat directories |
| notify(3) keys (`kOSThermalNotificationPressureLevelName`, `kIOPSNotifyPowerSource`, `kNotifySCNetworkChange`) | state-change tokens; `notify_get_state` reads the current level | none | cgo (`notify_register_file_descriptor` delivers on an fd, no run loop) | none | about zero |
| `IORegisterForSystemPower` | sleep and wake (must acknowledge, or sleep waits 30 s) | none | cgo with a CFRunLoop thread | none | about zero |
| `NSProcessInfo.thermalStateDidChangeNotification` | coarse thermal state | none | ObjC | none | coarser than the notify key |
| `SMAppService` | nothing; status is a property, no change notification | app bundle | ObjC | bundle | n/a |
| kdebug / `ktrace` / `fs_usage` | syscall and filesystem trace | root; one kdebug client at a time (conflicts with Instruments) | subprocess (already used by the helper's `fs_usage`) | private library | very high |
| DTrace | arbitrary probes | root, and SIP relaxed (`csrutil enable --without dtrace`) | subprocess | blocked on default Macs | high |

### Fit with the privileged helper and threat model

- **Native Endpoint Security client.** Spectra ships two Developer ID signed,
  notarized Mach-O binaries through Homebrew. An ES client needs an Apple
  entitlement grant (a request form and review, historically slow), a
  provisioning profile, and therefore an app-like bundle. It would also need
  a new install path (`SMAppService.daemon` or a System Extension) and a
  heavier approval flow. [Privileged helper](../design/privileged-helper.md)
  already defers System Extensions to "v2+", and nothing measured here changes
  that.
- **`eslogger` from the helper.** The helper already runs as root and asks for
  Full Disk Access at install, so it meets `eslogger`'s requirements without
  our own entitlement. When launchd starts the helper, it is the responsible
  process, so its FDA grant should cover a child `eslogger`; that must be
  checked on a real Mac. It would be the helper's first streaming,
  system-wide observation method, and it moves every user's exec argv across
  the helper boundary. Under the helper rules it needs a threat-model update
  (data minimization: filter to the caller's uid and drop environments; a
  bounded duration; rate limits), a security review and the owner's
  go-ahead. Apple also warns that `eslogger`'s JSON schema is not stable.
- **Everything else** in the recommendation runs unprivileged inside the
  per-user daemon and needs no helper change.

### Linux counterparts

The Linux daemon already reads `/proc`. Linux equivalents exist if parity is
wanted later: the netlink proc connector (system-wide fork/exec/exit, needs
`CAP_NET_ADMIN`), `pidfd_open` plus poll for exits of known pids
(unprivileged), and inotify for crash directories. They are not part of this
recommendation.

## Recommendation

Ranked by benefit against cost and risk. Each step is a separate issue.

| # | Step | Benefit | Cost and risk | Issue |
|---|---|---|---|---|
| 1 | Daemon on macOS: build the per-tick process sample and sustained probe from `kern.proc.all` plus libproc instead of `ps` | Removes 2 forks per tick, about half the idle daemon CPU; collection stops depending on fork latency under load (#708) | Small; libproc and sysctl are already used. CPU% must come from CPU-time deltas, as the Linux probe already does | [#745](https://github.com/kaeawc/spectra/issues/745) |
| 2 | Daemon on Linux: build the same readings from `/proc` instead of `ps`, and read `/proc` files without the Go poller | Removes 2 forks per tick and most idle syscalls | Small | [#746](https://github.com/kaeawc/spectra/issues/746) |
| 3 | `spectra spawns` and `process.CollectAll` on macOS: use `kern.proc.all` for the pid/ppid/start table instead of `ps` | 250x cheaper per poll, so a tighter interval becomes affordable | Small | [#747](https://github.com/kaeawc/spectra/issues/747) |
| 4 | Event-driven process table on macOS: seed from `kern.proc.all`, watch every pid with `EVFILT_PROC` (`NOTE_EXIT\|NOTE_FORK\|NOTE_EXEC`), rescan on fork/exec with a rate cap, reconcile with the existing slow poll | Exact exit times; catches short-lived children and parents that polling misses (spawn origins, `orphans`, `spawns`, `spawn.rate`) | Medium; pure Go, unprivileged. Fork storms must be rate-capped, and a kqueue fallback to polling is needed | [#748](https://github.com/kaeawc/spectra/issues/748) |
| 5 | Thermal and power by notify(3): read the `kOSThermalNotificationPressureLevelName` key with `notify_get_state` instead of `pmset -g therm` per tick, and use power-source and network-change tokens to invalidate the `power.current` and `network.current` caches | Removes the per-tick `pmset` fork; fresher cached answers | Small to medium; cgo (already used on Darwin). The pressure level is not the same signal as `pmset`'s CPU speed limit, so the mapping needs a real Mac | [#749](https://github.com/kaeawc/spectra/issues/749) |
| 6 | Crash reports and app installs by kqueue `EVFILT_VNODE`: watch the DiagnosticReports directories and `/Applications` | New crash reports and app installs reach alerts and caches without scans | Small; pure Go, a handful of fds | [#750](https://github.com/kaeawc/spectra/issues/750) |
| 7 | Helper `eslogger` stream: a bounded, observe-only helper method that relays exec/fork/exit for the caller's uid | System-wide exec with argv and signing info, nothing missed | Medium to high: helper change, threat-model update, schema instability. **Needs the owner's go-ahead (#600); design only** | [#751](https://github.com/kaeawc/spectra/issues/751) |
| 8 | Native Endpoint Security client | Same as 7, with a stable API and muting | High: Apple entitlement, provisioning profile, app bundle, new install flow. **Needs the owner and Apple; decision only** | [#752](https://github.com/kaeawc/spectra/issues/752) |

Not recommended:

- **NSWorkspace notifications.** They cover GUI apps only, which kqueue
  already covers, and they need an AppKit run loop in the daemon.
- **`OSLogStore`.** It needs an entitlement.
- **Long-running `log stream`.** It puts its cost on `logd` and `diagnosticd`.
  Keep it for bounded, explicit queries.
- **kdebug and DTrace.** They need root, kdebug allows only one client at a
  time, and SIP blocks DTrace.
- **The memory-pressure dispatch source.** The sysctl poll is already cheap.
  Revisit it only if a pressure spike between ticks matters.
- **`IORegisterForSystemPower`.** Suspend gaps are already detected from tick
  timing.

### Shape of step 4

- **Seed.** One `kern.proc.all` call. Register `EVFILT_PROC` with
  `EV_ADD|EV_CLEAR` and `NOTE_EXIT|NOTE_FORK|NOTE_EXEC` for every pid, in
  batches through one `kevent` call. `ESRCH` means the process already exited.
- **Fork or exec event.** Mark the table dirty. Run one `kern.proc.all`
  rescan, coalesced and capped (for example at most every 20 ms and 50 per
  second). Register the new pids, and record each one's parent at first
  sight. That is the moment `spectra orphans` needs, before reparenting.
- **Exit event.** Record the exact exit time. Drop the knote, which the kernel
  removes on exit anyway.
- **Reconcile.** Keep the existing 5 s probe as a reconcile pass. It catches
  any pid that forked and exited between a fork event and the rescan. It also
  covers processes forked by unwatched parents, which can't exist after the
  seed, because every process is someone's child.
- **Budget.** At the measured 0.1 ms per rescan, the cap bounds observer CPU
  at about 5 ms per second during a fork storm.
- **Fallback.** If kqueue registration fails, fall back to polling.
- **Linux.** Keeps polling.

## Unverified without a real Mac

The CI runner is a virtual Mac with SIP disabled and an image that may hold
Full Disk Access for its agent. These need a stock, SIP-enabled Mac:

- `EVFILT_PROC` registration on other users' and root pids with SIP on. XNU's
  code path does not consult SIP, but that has not been run on a stock Mac.
- Whether `eslogger` started by the root helper is covered by the helper's Full
  Disk Access grant, and its event volume on a developer machine during a
  build. The runner numbers are for an idle CI VM.
- That DTrace is blocked. Expected from Apple's SIP documentation, not
  measured.
- The literal value of `kOSThermalNotificationPressureLevelName` (reported as `com.apple.system.thermalpressurelevel`; the header exports only the symbol), and how its level maps to `pmset -g therm`'s
  speed limit on Apple Silicon and Intel.
- `ps` against sysctl wall time under the real #708 load (load average in the
  hundreds on a laptop). The runner's synthetic load is a proxy.

## Sources

Apple documentation and headers:

- kqueue(2) man page: <https://keith.github.io/xcode-man-pages/kqueue.2.html>
- XNU `bsd/sys/event.h` (EVFILT_PROC notes, NOTE_TRACK removal): <https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/event.h>
- XNU `bsd/kern/kern_event.c` (`filt_procattach` permission check): <https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_event.c>
- XNU memorystatus notifications: <https://github.com/apple-oss-distributions/xnu/blob/main/doc/vm/memorystatus_notify.md>
- Endpoint Security framework: <https://developer.apple.com/documentation/endpointsecurity>
- `es_new_client` results: <https://developer.apple.com/documentation/endpointsecurity/client>
- ES entitlement: <https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.endpoint-security.client>
- eslogger(1): <https://keith.github.io/xcode-man-pages/eslogger.1.html>
- NSWorkspace `didLaunchApplicationNotification`: <https://developer.apple.com/documentation/appkit/nsworkspace/didlaunchapplicationnotification>
- `OSLogStore.local()`: <https://developer.apple.com/documentation/oslog/oslogstore/local()>
- `FSEventStreamCreate`: <https://developer.apple.com/documentation/coreservices/1443980-fseventstreamcreate>
- FSEvents programming guide: <https://developer.apple.com/library/archive/documentation/Darwin/Conceptual/FSEvents_ProgGuide/UsingtheFSEventsFramework/UsingtheFSEventsFramework.html>
- `SMAppService`: <https://developer.apple.com/documentation/servicemanagement/smappservice>
- Libnotify `notify_keys.h` (`kNotifySCNetworkChange`): <https://github.com/apple-oss-distributions/Libnotify/blob/main/notify_keys.h>
- Libc `libkern/OSThermalNotification.h` (`kOSThermalNotificationPressureLevelName`, pressure levels): <https://github.com/apple-oss-distributions/Libc/blob/main/include/libkern/OSThermalNotification.h>
- IOKit `IOPowerSources.h` (`kIOPSNotifyPowerSource` and friends): <https://github.com/apple-oss-distributions/IOKitUser/blob/main/ps.subproj/IOPowerSources.h>
- ktrace(1): <https://keith.github.io/xcode-man-pages/ktrace.1.html>

WWDC and Apple forums:

- WWDC19 702, System Extensions and DriverKit: <https://developer.apple.com/videos/play/wwdc2019/702/>
- WWDC20 10159, Build an Endpoint Security app: <https://developer.apple.com/videos/play/wwdc2020/10159/>
- WWDC22 110345, What's new in Endpoint Security (introduces eslogger): <https://developer.apple.com/videos/play/wwdc2022/110345>
- ES client as a launchd daemon, packaging with a provisioning profile: <https://developer.apple.com/forums/thread/683451>, <https://developer.apple.com/forums/thread/133222>, <https://developer.apple.com/forums/thread/660294>

Projects:

- osquery process auditing with EndpointSecurity: <https://osquery.readthedocs.io/en/stable/deployment/process-auditing/>, <https://blog.trailofbits.com/2021/11/10/announcing-osquery-5-now-with-endpointsecurity-on-macos/>
- Santa (ES System Extension): <https://northpole.dev/deployment/troubleshooting/>
- Objective-See ProcessMonitor: <https://github.com/objective-see/ProcessMonitor>
- `golang.org/x/sys/unix` on Darwin: <https://pkg.go.dev/golang.org/x/sys/unix?GOOS=darwin>
- fsnotify (kqueue on macOS) and fsevents: <https://pkg.go.dev/github.com/fsnotify/fsnotify>, <https://github.com/fsnotify/fsevents>
- purego (calling libSystem without cgo): <https://github.com/ebitengine/purego>
- gopsutil Darwin process collector: <https://github.com/shirou/gopsutil/blob/master/process/process_darwin.go>
- *The Art of Mac Malware*, vol. 2, ch. 6, log monitoring cost: <https://taomm.org/vol2/pdfs/CH%206%20Log%20Monitoring.pdf>

## See also

- [Live data sources](../inspection/live-data-sources.md): the per-source
  table this research extends.
- [Background service](../operations/daemon.md): daemon cadences and alerts.
- [Privileged helper](../design/privileged-helper.md) and
  [Threat model](../design/threat-model.md): the boundary any Endpoint
  Security work must respect.
