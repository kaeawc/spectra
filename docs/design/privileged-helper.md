# Privileged helper

> **Status: planned.** Captures the security boundary and protocol
> between Spectra's unprivileged daemon and its optional root helper.

Most of Spectra works as the user. But three categories of telemetry
require root:

1. **System TCC.db** at `/Library/Application Support/com.apple.TCC/TCC.db`
   — needs Full Disk Access for the helper.
2. **`fs_usage` and `dtrace`-class probes** — kernel tracing,
   root-only.
3. **`powermetrics`** — root-only energy attribution.

Spectra's design splits these out into a separately-installed
LaunchDaemon helper. The unprivileged daemon talks to it over a local
Unix socket when (and only when) it needs root data. Users who don't
install the helper still get every other capability.

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

Modern path: register via `SMAppService.daemon` (macOS 13+) so the
user approves once via Login Items rather than navigating launchctl.
The deprecated `SMJobBless` path is supported as a fallback for older
macOS but new installs go through `SMAppService`.

```bash
sudo spectra uninstall-helper
```

Reverses the steps above. Always available; nothing about the helper
is hard to remove.

## What the helper exposes

The helper's RPC surface is intentionally narrow. It listens on
`/var/run/spectra-helper.sock` (root:wheel `0660`, group `_spectra`
which the user is added to at install time):

| Method | Purpose |
|---|---|
| `helper.tcc.system.query(bundleID)` | Query system TCC.db for granted services |
| `helper.fs_usage.start(filter)` | Begin streaming filesystem activity |
| `helper.fs_usage.stop(handle)` | Stop a started stream |
| `helper.powermetrics.sample(duration)` | One-shot powermetrics output |
| `helper.process.tree()` | Process tree including processes the user can't see (e.g. system daemons) |
| `helper.health()` | Liveness + version |

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

- **Filesystem permissions** (`0660 root:_spectra`) gate which users
  can connect. Joining the `_spectra` group requires admin privilege.
- **Caller credential check** via `getpeereid(2)` on the connected
  socket. The helper logs every call with the calling UID.
- **Method allowlist** is hardcoded in the helper. There is no dynamic
  capability negotiation.
- **Rate limiting** per UID to prevent a compromised unprivileged
  daemon from DOSing the system.

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

## Code layout (planned)

```
cmd/
  spectra/                # CLI client + unprivileged daemon
  spectra-helper/         # privileged helper, separate main package
internal/
  helper/
    rpc.go                # method dispatch
    auth.go               # getpeereid, group membership
    tcc.go                # TCC.db reader
    fsusage.go            # fs_usage streaming
    powermetrics.go       # powermetrics wrapper
  helperclient/           # used by the unprivileged daemon
    client.go
    fallback.go           # graceful "no helper installed" path
```

## Audit log

Every helper call writes a structured line to
`/var/log/spectra-helper.log` with rotation:

```
2026-05-04T18:33:21Z uid=501 method=tcc.system.query bundleID=com.anthropic.claudefordesktop result=ok rows=2
```

Users running with the helper installed can audit what got asked of
it. The unprivileged daemon never writes to this log directly.

## Code signing and notarization

- Helper is signed with the same Developer ID as the main binary,
  hardened runtime enabled.
- `SMAppService.daemon` registration verifies the embedded helper's
  code-signing requirements before loading.
- Notarization staples to both binaries. Without notarization, macOS
  refuses to load the LaunchDaemon at all on machines with default
  Gatekeeper settings.

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
