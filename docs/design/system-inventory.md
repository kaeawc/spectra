# System inventory

This is the implemented data model behind every Spectra snapshot. The
recommendations engine, cross-host diff, daemon, and stored snapshot JSON
all read from this shape.

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
  toolchains:  Toolchains           # brew, mise/asdf, language runtimes
  network:     NetworkState         # routes, DNS, proxies, listening ports
  storage:     StorageState         # disk usage, sparse-file accounting
  power:       PowerState           # assertions, energy attribution
  sysctls:     map[string]string    # selected kernel tunables
}
```

Every leaf field is JSON-serializable. The full snapshot is stored as
`snapshots.snapshot_json`; selected rows are also projected into SQLite
tables for summaries and common queries.

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
  architecture        # "arm64" | "amd64"
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
  bsd_name                  # proc_pidinfo on Darwin when visible
  executable_path           # proc_pidpath on Darwin when visible
  cpu_pct                  # over the last sample interval
  rss_kib, vsize_kib
  thread_count             # proc_pidinfo on Darwin when visible
  start_time
  app_path                 # nullable; populated when bundle attribution succeeds
  open_fds                 # via lsof, only when --deep
  listening_ports          # via lsof, only when --deep
  outbound_connections     # via lsof / nettop, only when --deep
}
```

`--deep` is opt-in because the batched `lsof` enrichment adds extra fork
cost.

## JVMInfo

For each Java process at snapshot time:

```
JVMInfo {
  pid
  main_class
  java_home
  jdk_vendor
  jdk_version
  jdk_install_id
  jdk_source
  jdk_path
  vm_args
  vm_flags
  thread_count
  sys_props                            # selected java.* / os.* / user.* keys
}
```

Source: `jps` and `jcmd`. GC snapshots are available through JVM commands
but are not embedded in the snapshot shape today. See
[../inspection/jvm.md](../inspection/jvm.md).

## JDKInstall

Every installed JDK Spectra finds:

```
JDKInstall {
  install_id
  path                       # e.g. ~/.sdkman/candidates/java/21.0.6-tem
  source ∈ {system, brew, sdkman, asdf, mise, coursier, jbr-toolbox, manual}
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
    formulae []{ name, version, installed_via, deprecated, pinned }
    casks    []{ name, version }
    taps     []{ name }
  }
  jdks    []JDKInstall
  python  []{ version, source ∈ {system, brew, uv, pyenv, mise, asdf}, path, active }
  node    []{ version, source ∈ {brew, mise, asdf, fnm, volta, nvm}, active }
  go      []{ version, source ∈ {brew, system, goenv, mise, asdf}, path, active }
  ruby    []{ version, source ∈ {brew, asdf, mise, rbenv}, active }
  rust    []{ toolchain, channel ∈ {stable, beta, nightly, custom}, default }
  jvm_managers ∈ {sdkman, asdf, mise, jenv}
  active_jvm_manager
  build_tools []{ name, version, source, config_path?, user_home? }
  env EnvSnapshot
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
  proxy {
    http, https, socks    # from scutil --proxy
  }
  hosts_overrides []      # only non-default /etc/hosts lines
  vpn_active             # tailscale, cisco anyconnect, openvpn
  vpn_interfaces []
  listening_ports []{ port, proto, local_addr, pid, command, user, app_path }
  established_connections_count  # TCP ESTABLISHED rows only
  process_throughput []{ pid, command, bytes_in_per_sec, bytes_out_per_sec }
}
```

## StorageState

Roll-up of disk usage by category:

```
StorageState {
  volumes []{
    mount_point
    fs_type
    total_bytes, used_bytes, avail_bytes
  }
  user_library_bytes
  app_caches_bytes
  largest_apps []{ path, on_disk_bytes }   # top N
}
```

Sparse-file-aware (`Stat_t.Blocks * 512`) so Docker's container volume
reports actual allocation rather than the multi-TB virtual disk size.

## PowerState

```
PowerState {
  on_battery, battery_pct
  thermal_pressure ∈ {nominal, fair, serious, critical}
  assertions []{
    type, pid, name
  }
  energy_top_users []{ pid, energy_impact, command }
}
```

Source: `pmset -g assertions`, `pmset -g batt`, `pmset -g therm`,
`top -l 1 -o power`. See
[../inspection/live-data-sources.md](../inspection/live-data-sources.md).

## EnvSnapshot

Just the shell-environment bits relevant to runtime behavior:

```
EnvSnapshot {
  shell
  path_dirs []                   # ordered $PATH
  java_home
  go_path, go_root               # nullable
  npm_prefix, pnpm_home          # nullable
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
| `toolchains.jdks` | matched by `(version_major, version_minor, version_patch, vendor)`; same identity at different paths is "compatible," different identities are "drift" |
| `toolchains.brew.formulae` | matched by name; reports added/removed/version-changed |
| `network.listening_ports` | matched by `(port, proto)`; reports added/removed |
| `toolchains.env.path_dirs` | sequence-compared (order matters for shadowing) |
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
| live, scheduled (daemon) | last 100 live snapshots |
| baseline | indefinite |

Configured via `spectra config retention.live = ...` once the daemon
exposes config. Until then, retention is hardcoded.

## See also

- [../operations/snapshots-and-hosts.md](../operations/snapshots-and-hosts.md) —
  host identity, registry lifecycle, baselines, retention, and diff
  workflows
- [storage.md](storage.md) — where snapshots are persisted
- [../inspection/](../inspection/) — per-collector deep dives
- [recommendations-engine.md](recommendations-engine.md) — what fires
  against this data
