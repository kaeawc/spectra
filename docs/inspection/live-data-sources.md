# Live data sources

Reference table of every macOS observability primitive Spectra will
integrate. Captures what each gives, what privileges it needs, what
its costs are, and which collector it backs.

This page is the anchor for the live-data roadmap so we don't
accidentally collect the same thing two different ways or pay the
fork cost twice.

## Process state

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `ps -axwwo pid,ppid,uid,rss,pcpu,etime,comm` | full process table | user | ~30ms / call | `scanRunningProcesses` (today), daemon ring buffer (planned) |
| `libproc` (cgo) | richer per-process: thread count, fd count, region info | user | ~per-process | future replacement for `ps` |
| `proc_pidinfo` (syscall) | structured per-process, no fork | user | microseconds | future ring buffer |
| `top -l 1 -n 0 -stats pid,rsize,cpu,power` | system-wide energy attribution | user | ~200ms | planned `power` collector |
| `sample <pid> 1` | one-second user-space sample, kernel sample optional | user (own procs); root (others) | ~1s + binding cost | `spectra sample` |

`ps` is what we use today. The natural upgrade path is direct
syscalls via `libproc` — eliminates the fork cost and gives access to
per-process detail (threads, file descriptors, memory regions) that
`ps` can't surface in a single call.

## File and disk activity

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `lsof -p <pid>` | open files + sockets per process | user (own); helper for others | ~50ms per pid | `process.open_fds`, `process.listening_ports` |
| `lsof -i -P -n` | system-wide network connections | user (limited); helper for full | ~100ms | network connections collector |
| `fs_usage -w -f filesys` | live file system trace | helper-only | streaming | `fs_usage` collector via helper |
| `fs_usage -e <pid>` | per-process file activity | helper-only | streaming | targeted file-activity capture |
| `iostat 1` | per-volume IOPS and throughput | user | streaming, low | volume IO ring buffer |
| `du -sk` | directory disk usage | user | seconds for large trees | not used; we walk + `Stat_t.Blocks` instead |

`fs_usage` is the killer: per-process, syscall-level filesystem
events. But it requires root and produces a high-volume stream;
exposing it through the helper requires careful filtering and rate
limiting (see [../design/privileged-helper.md](../design/privileged-helper.md)).

## Network state

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `nettop -P -L 0 -t external` | per-process network bytes/sec | user (limited) | streaming, low | network-by-app live counters |
| `lsof -i -P -n` | current TCP/UDP sockets | user | one-shot | listening ports + connections |
| `netstat -an` | system-wide socket state | user | low | fallback when `lsof` unavailable |
| `scutil --proxy` | system proxy config | user | <10ms | `network.proxy_config` |
| `scutil --dns` | DNS resolver config | user | <10ms | `network.dns_servers` |
| `route -n get default` | default route + interface | user | <10ms | `network.default_route_*` |
| `pfctl -s rules` | firewall rules | helper | one-shot | (planned) fw audit |
| `tcpdump -i <iface>` | raw packet capture | helper | streaming, high | (planned) targeted capture |

We default to `nettop` and `lsof` for the unprivileged daemon. Packet
capture is reserved for explicit request via the helper.

## Energy and power

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `pmset -g assertions` | active wake/sleep assertions | user | <10ms | `power.active_assertions` |
| `pmset -g batt` | battery state | user | <10ms | `power.on_battery` |
| `pmset -g therm` | thermal state | user | <10ms | `power.thermal_pressure` |
| `powermetrics -s tasks -n 1` | per-process energy attribution | helper-only | ~1s | `power.energy_top_users` |
| `top -l 1 -o power` | flat per-process power column | user | ~200ms | fallback when helper absent |

`pmset` covers ~80% of the power story without root. `powermetrics`
provides the deep CPU/GPU/disk energy breakdown but needs the helper.

## JVM observation

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `jps -l` | running JVM PIDs + main classes | user | ~50ms | JVM discovery |
| `jcmd <pid> VM.system_properties` | system properties | same UID as target JVM | <100ms | `jvm.system_properties` |
| `jcmd <pid> VM.command_line` | full launch command line | same UID | <100ms | `jvm.vm_args` |
| `jcmd <pid> Thread.print` | thread dump | same UID | ~200ms-1s | `jvm.threadDump` artifact |
| `jcmd <pid> GC.class_histogram` | heap class histogram | same UID | ~500ms-5s | `jvm.heap` summary |
| `jcmd <pid> GC.heap_dump <path>` | full heap dump | same UID | seconds-minutes, GBs | `jvm.heapDump` artifact |
| `jcmd <pid> JFR.start name=spectra` | start JFR recording | same UID | low | `jvm.jfr.start` |
| `jcmd <pid> JFR.dump name=spectra filename=...` | stop+dump JFR | same UID | low | `jvm.jfr.dump` |
| `jstat -gc <pid> 1000` | GC counters polling | same UID | streaming, low | `jvm.gc` ring buffer |

All require a JDK in `$PATH` — see [toolchains.md](toolchains.md) for
JDK discovery. Spectra picks the JDK most-recently used to launch the
target JVM (each process's `java.home` system property tells us which).

## Code-signing and entitlements

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `codesign -dv <app>` | signing identity + flags | user | ~100ms | `TeamID`, `HardenedRuntime` |
| `codesign -d --entitlements :- <app>` | entitlements XML | user | ~100ms | `Entitlements` |
| `spctl --assess --type exec <app>` | Gatekeeper status | user | ~100ms | `GatekeeperStatus` |

## Permissions

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `~/Library/Application Support/com.apple.TCC/TCC.db` | per-user grants | user | ~20ms | `GrantedPermissions` (today) |
| `/Library/Application Support/com.apple.TCC/TCC.db` | system-wide grants | helper-only | ~20ms | `GrantedPermissions` system rows (planned) |
| `tccutil reset <service>` | reset specific permission | user (own); helper for system | <100ms | not used; mutation only |

## App bundle structure

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `plutil -extract <key> raw -o - <plist>` | single Info.plist key | user | ~10ms | metadata fields |
| `plutil -convert xml1 -o - <plist>` | full plist as XML | user | ~10ms | `PrivacyDescriptions` |
| `otool -L <binary>` | linked dylibs | user | ~30ms | Layer 2 detection |
| `file <binary>` | architectures | user | ~10ms | `Architectures` |

These are the macOS-only utilities Spectra leans on for static
inspection. All preinstalled — no install-time dependencies.

## System tunables

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `sysctl -n <key>` | single tunable | user | <10ms | `sysctls` map (allowlisted keys) |
| `system_profiler SPHardwareDataType` | hardware specs | user | ~500ms | `HostInfo.cpu_brand`, `ram_bytes` |
| `sw_vers` | macOS version + build | user | <10ms | `HostInfo.os_*` |

`sw_vers` is faster than `system_profiler SPSoftwareDataType` for the
specific os_version + os_build fields.

## What we deliberately don't use

- **`dtrace` directly.** Powerful but System Integrity Protection
  blocks it on most user binaries. `fs_usage` (which uses dtrace
  under the hood for kernel-only types) is our ceiling.
- **`Endpoint Security` framework.** Requires an Apple-issued
  entitlement we don't have. v2+.
- **`spindump`.** Useful for hangs but high-overhead and produces
  user-readable text rather than structured data. May add later for
  targeted "this app is stuck" workflows.
- **`atos`** for symbolicating samples. Will integrate when we ship
  the sampler-to-flamegraph pipeline.
- **`leaks` and `heap`.** Spectra's heap-dump path goes through JVM's
  `jcmd` for Java processes; native-process heap analysis isn't on
  the roadmap.

## Cost model

Per-snapshot cost summary for a typical Mac with ~100 apps installed,
~200 processes running, no helper-required collectors:

```
ps                                 30ms
lsof (per app, only when matched)   N × 50ms  (skipped without --deep)
nettop -L 0 (5s sample)             5s   (only when --deep or live)
codesign + plutil per app           100ms × (apps inspected)
TCC.db query per bundle             20ms
JVM jcmd per Java pid               300ms × (Java pids)
sysctl batch                        50ms
```

Live ring-buffer collector at 1Hz: ~30ms of CPU per second per host,
dominated by `ps` parsing. Acceptable.

## See also

- [../design/system-inventory.md](../design/system-inventory.md) —
  what these sources roll up into
- [../design/privileged-helper.md](../design/privileged-helper.md) —
  which sources require root and how the helper exposes them
- [../inspection/](../inspection/) — per-collector deep dives
