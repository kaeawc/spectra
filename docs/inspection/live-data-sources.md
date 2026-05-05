# Live data sources

Reference table of the macOS observability primitives Spectra uses or
intentionally leaves for later. Captures what each gives, what privileges
it needs, what its costs are, and which collector it backs.

This page is the anchor for the live-data roadmap so we don't
accidentally collect the same thing two different ways or pay the
fork cost twice.

## Process state

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `ps -axwwo pid,ppid,pcpu,rss,vsz,uid,user,lstart,command` | full process table | user | ~30ms / call | `process.CollectAll`, snapshots, daemon ring buffer |
| `proc_pidinfo(PROC_PIDTASKINFO)` | per-process thread count | user | microseconds / process | `process.CollectAll`, snapshots |
| `libproc` (cgo) | richer per-process: fd count, region info | user | ~per-process | future replacement for `ps` |
| `top -l 1 -n 10 -o power -stats pid,power,command` | flat per-process energy attribution | user | ~200ms | `power.energy_top_users` |
| `sample <pid> 1` | one-second user-space sample, kernel sample optional | user (own procs); root (others) | ~1s + binding cost | `spectra sample` |

`ps` is what we use today for snapshots, `spectra process`, and the
daemon's ~1Hz metrics sampler. `--deep` process scans add one batched
`lsof -p pid1,pid2,...` call to populate open file descriptor counts,
listening ports, and outbound remote addresses. The natural upgrade path
is expanding the direct `libproc` / `proc_pidinfo` coverage, which would
eliminate more fork cost and add richer per-process detail.

## File and disk activity

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `lsof -p <pid[,pid...]>` | open files + sockets per process | user (own); helper for others | ~50-100ms batched | `process.open_fds`, `process.listening_ports`, `process.outbound_connections` |
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
| `nettop -P -L 2 -x -d -J bytes_in,bytes_out -t external` | per-process network bytes/sec | user (limited) | ~1s sample | `network.process_throughput` |
| `lsof -i -P -n -sTCP:LISTEN` | current listening TCP sockets | user | one-shot | `network.listening_ports` |
| `lsof -i -P -n` | current TCP/UDP sockets | user | one-shot | `network.connections`, `network.byApp`, established count |
| `netstat -an` | system-wide TCP/UDP socket state without PID/app attribution | user | low | fallback when `lsof` unavailable |
| `scutil --proxy` | system proxy config | user | <10ms | `network.proxy_config` |
| `scutil --dns` | DNS resolver config | user | <10ms | `network.dns_servers` |
| `route -n get default` | default route + interface | user | <10ms | `network.default_route_*` |
| `pfctl -s rules` | firewall rules | helper | one-shot | `helper.firewall.rules`, `spectra network firewall` |
| `tcpdump -i <iface>` | raw packet capture | helper | streaming, high | (planned) targeted capture |

The current unprivileged path uses `lsof`, `scutil`, `route`, `ifconfig`,
`nettop`, and `/etc/hosts`. Raw packet capture is reserved for explicit
future live workflows.

## Energy and power

| Source | Output | Privilege | Cost | Used by |
|---|---|---|---|---|
| `pmset -g assertions` | active wake/sleep assertions | user | <10ms | `power.active_assertions` |
| `pmset -g batt` | battery state | user | <10ms | `power.on_battery` |
| `pmset -g therm` | thermal state | user | <10ms | `power.thermal_pressure` |
| `powermetrics --samplers cpu_power,gpu_power,network,disk -n 1` | deeper energy attribution | helper-only | ~1s | `helper.powermetrics.sample` |
| `top -l 1 -n 10 -o power -stats pid,power,command` | flat per-process power column | user | ~200ms | `power.energy_top_users` |

`pmset` plus `top` covers the unprivileged power story. `powermetrics`
provides deeper CPU/GPU/network/disk energy breakdown when the privileged
helper is installed and reachable.

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
| `jfr summary <path>` | JFR recording metadata + event counts | user | low | `jvm.jfr.summary` |
| `jstat -gc <pid>` | GC counters snapshot | same UID | low | `jvm.gc_stats` |

All require a JDK in `$PATH` — see [toolchains.md](toolchains.md) for
JDK discovery. Running JVM inspection records each process's `java.home`,
`java.vendor`, and `java.version`. When `java.home` matches a discovered
JDK path, Spectra also records `jdk_install_id`, `jdk_source`, and
`jdk_path`.

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
| `/Library/Application Support/com.apple.TCC/TCC.db` | system-wide grants | helper-only | ~20ms | `helper.tcc.system.query`; direct scan when readable |
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
lsof -p batch                       50-100ms  (only with process --deep)
lsof -i                             ~100ms
top power sample                    ~200ms
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
