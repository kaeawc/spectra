# Privileged helper

Spectra has an optional root helper for telemetry that user-mode Spectra
cannot collect. The current implementation is a separate `spectra-helper`
binary installed as a LaunchDaemon and reached over a local Unix socket.

Most of Spectra works as the user. But three categories of telemetry
require root:

1. **System TCC.db** at `/Library/Application Support/com.apple.TCC/TCC.db`
   — needs Full Disk Access for the helper.
2. **`fs_usage` and `dtrace`-class probes** — kernel tracing,
   root-only.
3. **`powermetrics`** — root-only energy attribution.

Spectra splits these out into a separately-installed LaunchDaemon helper.
The unprivileged daemon talks to it over a local Unix socket when (and only
when) it needs root data. Users who don't install the helper still get every
other capability.

## Why two processes

A single binary that auto-elevates would either be setuid (a trust
nightmare on macOS, strongly discouraged) or would prompt for sudo
on every invocation (hostile UX). The right pattern on macOS is:

- An **unprivileged** long-running daemon that owns user-scoped data.
- A **privileged** long-running helper that owns root-scoped data,
  installed once with a single password prompt.
- A clearly-defined IPC boundary between them so the unprivileged tier
  can be reviewed independently and the privileged tier stays small.

This is the pattern Tailscale, Docker Desktop, 1Password, and the
macOS system itself (`launchd`, `coreduetd`, etc.) all use.

## Installation

```bash
sudo spectra install-helper
```

What this does:

1. Copies the `spectra-helper` binary to `/Library/PrivilegedHelperTools/`.
2. Installs a launchd plist at `/Library/LaunchDaemons/dev.spectra.helper.plist`.
3. Loads the daemon (`launchctl load -w`).
4. Prompts the user to grant Full Disk Access to the helper in System
   Settings → Privacy & Security → Full Disk Access.

`SMAppService.daemon` / `SMJobBless` packaging is future distribution work.
The current installer is intentionally explicit and shell-based.

```bash
sudo spectra uninstall-helper
```

Reverses the steps above. Always available; nothing about the helper
is hard to remove.

## What the helper exposes

The helper's RPC surface is intentionally narrow. It listens on
`/var/run/spectra-helper.sock` with `0660` permissions:

| Method | Purpose |
|---|---|
| `helper.tcc.system.query(bundleID)` | Query system TCC.db for granted services |
| `helper.powermetrics.sample(duration)` | One-shot powermetrics output |
| `helper.process.tree()` | Process tree including processes the user can't see (e.g. system daemons) |
| `helper.health()` | Liveness + version |

Planned but not implemented yet:

- `helper.fs_usage.start(filter)`
- `helper.fs_usage.stop(handle)`

Notably absent:

- **No arbitrary file reads.** The helper never accepts an arbitrary
  path. Each method has a fixed source.
- **No arbitrary command execution.** Each method invokes exactly one
  pre-defined system command with structured arguments.
- **No mutation methods.** The helper observes; it does not change
  system state.

## Wire protocol

JSON-RPC 2.0 over Unix socket, framed with length-prefix headers:

```
<8-byte big-endian length><JSON-RPC payload>
```

Why not gRPC: too much code for a tiny surface. Why not raw JSON
streams: framing makes recovery from partial reads trivial.

## Authentication and authorization

- **Filesystem permissions** on `/var/run/spectra-helper.sock` gate which
  users can connect. The installer creates an `_spectra` group, adds the
  invoking user, and the helper assigns the socket to `root:_spectra`
  with `0660` permissions at startup.
- **Caller credential check** via `getpeereid(2)` on the connected
  socket is implemented and passed to method handlers.
- **Method allowlist** is hardcoded in the helper. There is no dynamic
  capability negotiation.
- **Rate limiting** is enforced per caller UID in the helper dispatcher.
  The installed helper allows 120 requests per minute per UID before
  returning a JSON-RPC rate-limit error.

## What the unprivileged daemon does without the helper

Everything currently implemented today, plus:

- Per-user TCC.db (works as the user, no helper needed).
- `lsof` / `nettop` (work without root, just don't see system-only
  processes).
- `pmset -g assertions` (works without root for user-scope assertions).
- `ps`, `sample <pid>` for processes the user owns.
- All static inspection.

The helper is strictly additive. It unlocks specific telemetry; it
doesn't gate the core experience.

## Security boundary

```
Unprivileged daemon (your user)              Privileged helper (root)
└── reads ~/Library/...                      └── reads /Library/...
└── runs ps/lsof/jcmd as user                └── runs fs_usage/powermetrics
└── writes ~/.spectra/                       └── writes nothing user-visible
└── exposes RPC over tsnet/Unix sock         └── exposes RPC over local sock only
                                                └── never reachable from the network
                ↓ JSON-RPC over Unix socket ↑
                  caller-authenticated, method allowlisted
```

The helper is **not** reachable over Tailscale. Remote clients always
go through the unprivileged daemon; if they need root data, the
unprivileged daemon mediates with the helper locally and applies its
own access control on the remote-facing side.

## Code layout

```
cmd/
  spectra/                # CLI client + unprivileged daemon
  spectra-helper/         # privileged helper, separate main package
internal/
  helper/
    dispatcher.go         # method dispatch
    framing.go            # length-prefixed JSON-RPC framing
    peeruid_darwin.go     # getpeereid
    methods.go            # TCC, powermetrics, process tree
  helperclient/           # used by the unprivileged daemon
    client.go
    fallback.go           # graceful "no helper installed" path
```

## Audit log

The LaunchDaemon plist sends stderr to `/var/log/spectra-helper.log`.
The helper writes one JSON object per handled request to stderr, so the
LaunchDaemon log captures caller UID, method, status, duration, and error
summary without recording request parameters.

```
{"time":"2026-05-05T17:33:21Z","uid":501,"method":"helper.tcc.system.query","ok":true,"duration_ms":4}
{"time":"2026-05-05T17:33:24Z","uid":501,"method":"helper.unknown","ok":false,"duration_ms":0,"error":"method not found"}
```

Users running with the helper installed can audit what got asked of
it. The unprivileged daemon never writes to this log directly.
`spectra install-helper` also installs
`/etc/newsyslog.d/spectra-helper.conf`, which keeps seven compressed
rotations and rolls the helper audit log after it reaches 1 MiB.

## Code signing and notarization

- Release builds should sign the helper with the same Developer ID as
  the main binary, hardened runtime enabled.
- Future `SMAppService.daemon` registration should verify the embedded
  helper's code-signing requirements before loading.
- Notarization should staple both binaries for end-user distribution.

## Why not a System Extension instead

System Extensions (NetworkExtension, EndpointSecurity) provide
kernel-grade visibility — packet inspection, exec/open events at the
kernel boundary — that even the privileged helper can't reach. But:

- They require an Apple-issued specific entitlement
  (`com.apple.developer.endpoint-security.client`) granted on a
  case-by-case basis after review.
- Months-long approval process.
- Heavier user-facing approval (a kernel extension–style prompt).
- Different debugging/distribution story.

A System Extension is a v2+ option once the unprivileged + helper
tier proves the product. Until then, the helper covers ~95% of what
deep diagnostics actually need.

## See also

- [distribution.md](distribution.md) — why this two-tier split is
  required by macOS distribution rules
- [threat-model.md](threat-model.md) — what the helper protects
  against and what's still in scope
- [architecture.md](architecture.md) — where the helper sits in the
  full daemon-client picture
