# Daemon mode

`spectra serve` runs the local long-lived collector and JSON-RPC server.
It is implemented today for the Unix-socket local workflow and optional
explicit TCP JSON-RPC. Remote tsnet integration remains future work.

Most CLI commands still run in-process. `spectra status`, `spectra metrics`,
and direct JSON-RPC clients use the daemon socket today; the same RPC surface
is intended to back future thin clients and UI clients.

See [../design/architecture.md](../design/architecture.md) for the
overall daemon-with-clients model and
[../design/remote-portal.md](../design/remote-portal.md) for the
Tailscale integration that makes remote operation a first-class case.

## Lifecycle

```bash
spectra serve              # foreground, JSONL log file enabled
spectra serve --sock /tmp/spectra.sock
spectra serve --tcp 127.0.0.1:7878
spectra serve --log-file /tmp/spectra-daemon.jsonl
spectra serve --no-log-file
spectra serve --daemon       # start detached and return
```

By default the CLI runs in the foreground until interrupted. `--daemon`
starts a detached `spectra serve` child with the same serve flags and
returns immediately. Launchd-managed agent packaging remains future
distribution work.

By default, structured daemon lifecycle logs are appended to
`~/Library/Logs/Spectra/daemon.jsonl` with `0600` permissions. Foreground
status lines still go to stderr for interactive use. Use `--log-file` to
choose another JSONL path or `--no-log-file` for short-lived test runs.

## Listening surfaces

| Surface | Default | Use |
|---|---|---|
| Unix socket | `~/.spectra/sock` | local CLI, local TUI/GUI clients |
| TCP localhost | opt-in via `--tcp 127.0.0.1:7878` | explicit local/forwarded JSON-RPC clients |
| TCP remote | opt-in via `--tcp <addr>:7878 --allow-remote` | trusted SSH/Tailscale/firewall paths |
| Tailscale `tsnet` | _not implemented_ | future remote clients via Tailscale |

The Unix socket is created with `0600` permissions and removed on clean
shutdown. TCP RPC has no Spectra-layer authentication; non-loopback
binds require `--allow-remote` and should only be used on a trusted
network path.

## RPC surface

JSON-RPC 2.0 methods, organized by concern:

- **Inspection** — `inspect.app`, `inspect.app.batch`, `inspect.host`
- **Snapshots** — `snapshot.create`, `snapshot.list`, `snapshot.get`,
  `snapshot.diff`, `snapshot.processes`, `snapshot.login_items`,
  `snapshot.granted_perms`, `snapshot.prune`
- **Live state** — `process.live`, `process.history`, `process.list`,
  `process.tree`, `process.sample`
- **Helper** — `helper.health`, `helper.powermetrics.sample`,
  `helper.tcc.system.query`, `helper.firewall.rules`,
  `helper.fs_usage.start`, `helper.fs_usage.stop`
- **JVM** — `jvm.list`, `jvm.inspect`, `jvm.threadDump`, `jvm.heapDump`,
  `jvm.heapHistogram`, `jvm.gcStats`, `jvm.jfr.start`, `jvm.jfr.stop`,
  `jvm.jfr.dump`, `jvm.jfr.summary`
- **JDK** — `jdk.list`, `jdk.scan`
- **Toolchain** — `toolchain.scan`, `toolchain.brew`,
  `toolchain.runtimes`, `toolchain.build_tools`
- **Network** — `network.state`, `network.connections`, `network.byApp`
- **Storage** — `storage.byApp`, `storage.system`
- **Power** — `power.state`
- **Issues** — `issues.list`, `issues.record`, `issues.update`,
  `issues.acknowledge`, `issues.dismiss`, `issues.fix.record`,
  `issues.fix.list`
- **Cache** — `cache.stats`, `cache.clear`
- **Helper proxy** — `helper.health`, `helper.powermetrics.sample`,
  `helper.tcc.system.query`, `helper.firewall.rules`,
  `helper.fs_usage.start`, `helper.fs_usage.stop`

Snake-case and camel-case aliases exist for selected older JVM methods
(`jvm.thread_dump` / `jvm.threadDump`, etc.).

## Caching

The daemon can pass the detect cache store into snapshot creation so repeated
`Detect()` work reuses the [blob store](caching.md). There is no in-memory
LRU today.

## Live data ring buffer

Process-level metrics (CPU%, RSS, virtual size) are sampled at ~1Hz into
an in-memory ring buffer. The last 5 minutes per process are kept in RAM
and flushed as 1-minute aggregates to SQLite. `spectra metrics` reads the
stored rows; the `process.live` RPC returns recent in-memory samples.

The daemon also captures a host-focused live snapshot every minute and prunes
live snapshots to the last 100 rows. Apps, storage, and JVMs are skipped in
that loop to keep the tick cheap.

## Privileged helper

For root-only data, the unprivileged daemon can proxy to a separately
installed privileged helper over a local Unix socket. Implemented helper
proxy methods cover health, system TCC query, and one-shot `powermetrics`
sampling, pf firewall rule reads, and bounded `fs_usage` traces. See
[../design/distribution.md](../design/distribution.md).

The remote client never talks to the helper directly — the daemon
mediates and applies its own access control.

## Authentication

Current local mode relies on Unix socket filesystem permissions. Current
TCP mode relies on the network path that exposes it. Spectra-layer remote
authentication is future work with the remote portal.

## Security posture

- Daemon runs as the invoking user, not root.
- The Unix socket is `0600`-permissioned in `~/.spectra/`.
- All RPC calls are read-only by default. State-changing calls
  (`snapshot.create`, `cache.clear`, `issues.acknowledge`,
  `issues.dismiss`, `issues.fix.record`, `snapshot.prune`, JVM heap/JFR
  capture) are explicit methods. Sensitive artifact writes
  (`jvm.heap_dump`, `jvm.jfr.dump`) also require
  `confirm_sensitive: true` in the request.
- The daemon is intended to be a low-privilege observer, not a remote
  shell. Method surface is intentionally narrow; arbitrary
  `exec.Command` is not exposed over RPC.

## Health

Each daemon exposes a JSON-RPC `health` method returning
`{ ok: true, version: ... }` on the Unix socket and any opted-in TCP
listener. Used by:

- `spectra status` — local health check.
- `spectra connect <target>` — Unix or TCP health check.
- future TUI/GUI clients — show connection state.

## Logs

The daemon writes JSONL records for listener startup, storage readiness,
serving start, and shutdown/error events. These records include socket paths,
TCP listen addresses, database path, version, and listener count. They do not
include heap-dump contents, JFR contents, process command-line arguments, or
RPC payloads.

## Implementation order

Implemented:

1. RPC dispatcher with handler registration.
2. Unix socket transport.
3. SQLite-backed snapshots, issues, process rows, permission rows, and
   metrics rows.
4. Live process metrics ring buffer and periodic live snapshots.
5. Local `spectra status` and `spectra metrics` clients.
6. Privileged helper proxy methods for implemented helper capabilities.
7. Optional TCP JSON-RPC listener, typed `spectra connect <target> ...`
   shortcuts, and the raw `spectra connect <target> call` escape hatch.
8. Explicit-host `spectra fan --hosts ...` fan-out over the same remote
   call surface.
9. `spectra hosts` listing for hosts known from stored snapshots.

Future:

1. CLI-wide RPC dispatch instead of in-process command execution.
2. launchd-managed daemon lifecycle.
3. tsnet listener and Tailscale identity integration.
4. TUI/GUI clients.
