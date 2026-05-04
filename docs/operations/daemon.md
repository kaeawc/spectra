# Daemon mode

> **Status: planned.** Captures the design for `spectra serve`.

`spectra serve` runs the long-lived collector. CLI invocations like
`spectra list` and `spectra inspect` are clients that talk to a local
or remote daemon over JSON-RPC.

See [../design/architecture.md](../design/architecture.md) for the
overall daemon-with-clients model and
[../design/remote-portal.md](../design/remote-portal.md) for the
Tailscale integration that makes remote operation a first-class case.

## Lifecycle

```bash
spectra serve              # foreground, logs to stdout
spectra serve --daemon     # detached, logs to ~/Library/Logs/Spectra/
```

Or as a launchd-managed agent installed by the installer.

## Listening surfaces

| Surface | Default | Use |
|---|---|---|
| Unix socket | `~/.spectra/sock` | local CLI, local TUI/GUI clients |
| Tailscale `tsnet` | `spectra.<tailnet>.ts.net:7878` | remote clients via Tailscale |
| TCP localhost | _disabled by default_ | explicit `--listen 127.0.0.1:7878` |

The Unix socket is always on. Tailscale and TCP are opt-in via flags.

## RPC surface (sketch)

JSON-RPC 2.0 methods, organized by concern:

- **Inspection** — `inspect.app`, `inspect.app.batch`, `inspect.host`
- **Snapshots** — `snapshot.create`, `snapshot.list`, `snapshot.get`,
  `snapshot.diff`
- **Live state** — `process.list`, `process.tree`, `process.history`
- **JVM** — `jvm.list`, `jvm.inspect`, `jvm.threadDump`, `jvm.heapDump`,
  `jvm.jfr.start/stop/dump`, `jvm.attachAgent`
- **JDK** — `jdk.list`, `jdk.scan`
- **Toolchain** — `toolchain.brew`, `toolchain.runtimes` (mise/asdf/etc.)
- **Network** — `network.connections`, `network.byApp`
- **Storage** — `storage.byApp`, `storage.system`
- **Issues** — `issues.list`, `issues.acknowledge`, `issues.dismiss`
- **Cache** — `cache.stats`, `cache.clear`

The CLI subcommands map 1:1 to RPC methods so a thin client wrapper
is sufficient and the same RPC surface serves a future GUI without
changes.

## Caching

The daemon caches `Detect()` results in the
[blob store](caching.md) and a small in-memory LRU. Repeat calls for
unchanged apps return in microseconds.

## Live data ring buffer

Process-level metrics (CPU%, RSS, network bytes/sec) are sampled at
~1Hz into an in-memory ring buffer. Last 5 minutes per process kept
in RAM; aggregated to 1-minute rows on flush to SQLite.

## Privileged helper

For root-only data (system TCC.db, `fs_usage`, `powermetrics`), the
unprivileged daemon talks to a separately installed privileged
helper over a local Unix socket. See
[../design/distribution.md](../design/distribution.md).

The remote client never talks to the helper directly — the daemon
mediates and applies its own access control.

## Authentication

V1: Tailscale-ACL-only for tsnet listener. The Unix socket is
restricted to the user that runs the daemon (filesystem perms).

Future: per-host bearer tokens issued at install time, for SSH or
non-Tailscale TCP usage.

## Security posture

- Daemon runs as the invoking user, not root.
- The Unix socket is `0600`-permissioned in `~/.spectra/`.
- All RPC calls are read-only by default. State-changing calls
  (`snapshot.create`, `cache.clear`, `issues.acknowledge`,
  `jvm.heapDump`) are routed through an explicit method whitelist.
- The daemon is intended to be a low-privilege observer, not a remote
  shell. Method surface is intentionally narrow; arbitrary
  `exec.Command` is not exposed over RPC.

## Health

Each daemon publishes `/health` returning `{ ok: true, version: ... }`
on its socket and tsnet listener. Used by:

- `spectra status` — local health check.
- TUI client — show connection state.
- Tailscale ACL probes — verify reachability.

## Implementation order

1. RPC dispatcher with handler registration (matches today's
   `Detect()` shape).
2. Unix socket transport.
3. CLI clients dispatch through RPC instead of in-process calls.
4. SQLite snapshots.
5. tsnet integration.
6. Live data ring buffer.
7. Privileged helper handshake.
