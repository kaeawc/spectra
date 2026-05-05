# System inventory

> **Status: planned.** This is the data model behind every Spectra
> snapshot. The recommendations engine, cross-host diff, and remote
> portal all read from this shape.

A snapshot is a structured capture of one host at one point in time.
Cross-host diff (`spectra diff laptop work-mac`) compares two snapshots.
Baselines are frozen reference snapshots. The recommendations engine
fires CEL rules against snapshot fields. All of these features need a
single coherent inventory schema.

## Top-level shape

```
Snapshot {
  id, host_id, taken_at, kind ∈ {live, baseline}
  host:        HostInfo
  apps:        []AppInfo            # via Detect()
  processes:   []ProcessInfo        # ps + lsof at capture time
  jvms:        []JVMInfo            # running Java processes
  jdks:        []JDKInstall         # installed JDK toolchains
  toolchains:  Toolchains           # brew, mise/asdf, language runtimes
  network:     NetworkState         # routes, DNS, proxies, listening ports
  storage:     StorageState         # disk usage, sparse-file accounting
  power:       PowerState           # assertions, energy attribution
  env:         EnvSnapshot          # shell config relevant to runtime
  sysctls:     map[string]string    # selected kernel tunables
}
```

Every leaf field is JSON-serializable. The on-wire format and the
SQLite row format are the same shape with `_id` references replacing
embedded objects.

## HostInfo

```
HostInfo {
  hostname, machine_uuid
  os_name = "macOS"
  os_version          # "14.4"
  os_build            # "23E214"
  cpu_brand           # "Apple M3 Max"
  cpu_cores           # physical cores
  ram_bytes
  arch                # "arm64" | "x86_64"
  uptime_seconds
  spectra_version
}
```

Source: `sysctl`, `system_profiler`, `os.Hostname`, `runtime.GOOS`.

## AppInfo

The `detect.Result` shape we already produce — see
[../reference/result-schema.md](../reference/result-schema.md). One
row per `.app` per snapshot.

## ProcessInfo

Live process state captured via `ps -axwwo` at snapshot time:

```
ProcessInfo {
  pid, ppid
  uid, user
  command, full_command_line
  cpu_pct                  # over the last sample interval
  rss_kib, vsize_kib
  thread_count
  start_time
  app_path                 # nullable; populated when bundle attribution succeeds
  open_fds                 # via lsof, only when --deep
  listening_ports          # via lsof, only when --deep
  outbound_connections     # via lsof / nettop, only when --deep
}
```

`--deep` is opt-in because per-process `lsof` adds seconds to a snapshot.

## JVMInfo

For each Java process at snapshot time:

```
JVMInfo {
  pid                                  # links to ProcessInfo
  jdk_install_id                       # links to JDKInstall
  vm_args, system_properties           # jcmd VM.command_line / VM.system_properties
  loaded_class_count, loaded_classes_bytes
  thread_count, daemon_thread_count, blocked_thread_count
  heap {
    used_bytes, committed_bytes, max_bytes
    young_gen_used, old_gen_used
    last_gc_at, last_gc_kind, gc_time_pct
  }
  recent_thread_dump_blob_id           # nullable; references blob store
  recent_jfr_blob_id                   # nullable
}
```

Source: `jcmd`, `jstat`. See
[../inspection/jvm.md](../inspection/jvm.md).

## JDKInstall

Every installed JDK Spectra finds:

```
JDKInstall {
  install_id
  path                       # e.g. ~/.sdkman/candidates/java/21.0.6-tem
  source ∈ {system, brew, sdkman, asdf, mise, jbr-toolbox, manual}
  version_major, version_minor, version_patch
  vendor                     # "Eclipse Adoptium", "Azul Zulu", "JetBrains"
  release_string             # raw "21.0.6+7-LTS"
}
```

Source: scan known install paths + parse `release` file. See
[../inspection/toolchains.md](../inspection/toolchains.md).

## Toolchains

Aggregates language and build-tool installations:

```
Toolchains {
  brew {
    formulae []{ name, version, installed_via, deprecated }
    casks    []{ name, version }
    taps     []{ name }
  }
  python  []{ version, source ∈ {system, brew, uv, pyenv}, path }
  node    []{ version, source ∈ {brew, mise, asdf, fnm, volta, nvm}, active }
  go      []{ version, source ∈ {brew, system}, path }
  ruby    []{ version, source ∈ {brew, asdf, rbenv}, active }
  rust    []{ toolchain, channel ∈ {stable, beta, nightly}, default }
  jvm_managers ∈ {sdkman, asdf, mise, jenv}
  build_tools []{ name, version, source, config_path? }
}
```

Source: each subsystem reports independently. See
[../inspection/toolchains.md](../inspection/toolchains.md).

## NetworkState

Snapshot of routes, DNS, proxies, listening ports:

```
NetworkState {
  default_route_iface, default_route_gw
  dns_servers []
  proxy_config {
    http, https, socks    # from scutil --proxy
  }
  /etc/hosts entries []   # only non-default lines
  vpn_active             # tailscale, cisco anyconnect, openvpn
  listening_ports []{ port, proto, pid, app_path }
  established_connections_count
}
```

## StorageState

Roll-up of disk usage by category:

```
StorageState {
  volumes []{
    mount_point
    fs_type, fs_format
    total_bytes, used_bytes, available_bytes
  }
  user_library_bytes        # sum of ~/Library by category
  app_caches_total_bytes
  largest_apps []{ path, on_disk_bytes }   # top N
}
```

Sparse-file-aware (`Stat_t.Blocks * 512`) so Docker's container volume
reports actual allocation rather than the multi-TB virtual disk size.

## PowerState

```
PowerState {
  on_battery, battery_pct, time_remaining
  thermal_pressure ∈ {nominal, fair, serious, critical}
  active_assertions []{
    type ∈ {PreventUserIdleSleep, PreventDisplaySleep, NoIdleSleepAssertion, …}
    pid, app_path
    name, created_at
  }
  energy_top_users []{ pid, app_path, energy_impact_pct }
}
```

Source: `pmset -g assertions`, `pmset -g batt`, `pmset -g therm`,
`top -l 1 -o power`. See
[../inspection/live-data-sources.md](../inspection/live-data-sources.md).

## EnvSnapshot

Just the shell-environment bits relevant to runtime behavior:

```
EnvSnapshot {
  shell ∈ {bash, zsh, fish}
  path_dirs []                   # ordered $PATH, deduped
  java_home, javac_home          # nullable
  go_path, goroot                # nullable
  npm_prefix, pnpm_home          # nullable
  active_runtime_overrides {     # mise/asdf shims taking precedence
    node, python, ruby, java
  }
  proxy_env_vars                 # http_proxy, no_proxy, etc.
}
```

We deliberately do **not** snapshot the full `os.Environ()` — too much
PII and secret material. Only the keys above.

## sysctls

Selected kernel tunables that materially affect runtime behavior:

```
sysctls {
  "kern.maxfiles"
  "kern.maxproc"
  "kern.ipc.maxsockbuf"
  "kern.boottime"
  "vm.memory_pressure"
  "hw.ncpu"
  "hw.memsize"
}
```

A small allowlist. Adding to this is a deliberate decision; we don't
dump the entire `sysctl -a` because it's massive and noisy.

## Diff semantics

`spectra diff <a> <b>` computes a structural diff. Per category, the
equality rule is category-specific:

| Category | Equality rule |
|---|---|
| `host` | exact match per field |
| `apps` | matched by bundle ID; reports added/removed/version-changed |
| `processes` | not directly diffed (snapshot-time noise); aggregates compared instead |
| `jdks` | matched by `(version_major, version_minor, version_patch, vendor)`; same identity at different paths is "compatible," different identities are "drift" |
| `toolchains.brew.formulae` | matched by name; reports added/removed/version-changed |
| `network.listening_ports` | matched by `(port, proto)`; reports added/removed |
| `env.path_dirs` | sequence-compared (order matters for shadowing) |
| `sysctls` | exact match per key |

## Baseline lifecycle

```
spectra snapshot create --baseline pre-incident   # freeze
spectra diff baseline pre-incident live           # diff a frozen baseline against the current state
spectra baseline list / spectra baseline drop <id>
```

Baselines are immutable. Their `kind = baseline` flag prevents
automatic eviction; live snapshots are subject to retention policy.

## Retention

| Snapshot kind | Default retention |
|---|---|
| live, on-demand | last 100 |
| live, scheduled (daemon) | last 24h at 1m granularity, last 30d at 1h granularity |
| baseline | indefinite |

Configured via `spectra config retention.live = ...` once the daemon
exposes config. Until then, retention is hardcoded.

## See also

- [storage.md](storage.md) — where snapshots are persisted
- [../inspection/](../inspection/) — per-collector deep dives
- [recommendations-engine.md](recommendations-engine.md) — what fires
  against this data
