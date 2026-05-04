# Distribution

Each macOS distribution channel imposes a different ceiling on what
Spectra can do. This page captures the analysis behind why we chose
**Homebrew + optional curl-bash for a privileged helper** and ruled out
the Mac App Store.

## Capability vs channel matrix

| Capability | MAS | Homebrew (CLI) | Homebrew Cask (.app) | curl-bash + sudo |
|---|---|---|---|---|
| Static inspection of `/Applications` | partial | ✓ | ✓ | ✓ |
| Read other users' app data | ✗ (sandbox) | ✓ | ✓ | ✓ |
| Per-user TCC.db | ✗ | ✓ | ✓ | ✓ |
| System TCC.db | ✗ | sudo only | sudo only | ✓ via helper |
| `lsof -i`, `nettop` | ✗ | ✓ | ✓ | ✓ |
| `fs_usage`, `powermetrics`, `pmset` | ✗ | sudo only | sudo only | ✓ via helper |
| Listen on TCP / Unix socket | ✗ | ✓ | ✓ | ✓ |
| Embed `tsnet` (Tailscale) | ✗ | ✓ | ✓ | ✓ |
| Install LaunchDaemon | ✗ | user can do it | via SMAppService | ✓ |
| System Extension (kernel-grade) | ✗ | ✗ | requires Apple entitlement | requires Apple entitlement |

## Why Mac App Store is out

MAS apps **must** be sandboxed (`com.apple.security.app-sandbox`). The
sandbox disqualifies almost everything that makes Spectra interesting:

- File access is restricted; reading `~/Library/Application Support/com.apple.TCC/TCC.db` is impossible.
- Full Disk Access is not a sandbox-compatible entitlement.
- `lsof`, `nettop`, `fs_usage`, `powermetrics`, `pmset -g assertions` cannot run.
- Listening on sockets (the daemon RPC) is blocked.
- `tsnet` cannot be embedded because outbound network from a sandboxed app is gated.
- Privileged helpers (`SMJobBless`, `SMAppService`) are not allowed.

Real precedent: Stats, BetterTouchTool, Bartender, MenuMeters — every
serious AM-adjacent tool ships outside MAS. The few MAS-distributed
"system monitor" apps are toy-grade.

We could ship a stripped MAS variant that does single-app static
inspection of bundles already in `/Applications`. That loses the entire
live-state and remote-debugging story. Two distribution targets isn't
worth the engineering for that capability slice.

## Why Homebrew is the primary channel

- Developer-shaped tools live there. Krit, Tailscale CLI, Datadog Agent,
  1Password CLI all use it.
- Brew installs as the user, so no privilege escalation at install time.
  Users who want root-grade visibility opt in separately.
- The same formula installs to every Mac in a tailnet — important for the
  cross-host remote portal.
- Notarization is required only for the cask (.app) variant; the CLI
  formula can build from source and dodge Gatekeeper.

```bash
# Planned
brew install kaeawc/tap/spectra
```

## Why an optional sudo helper

Homebrew can't install a LaunchDaemon. The capabilities that need root
(`fs_usage`, `powermetrics`, system TCC.db) require a separately
installed privileged helper. The user grants this once, explicitly:

```bash
# Planned
sudo spectra install-helper
```

This installs a notarized helper as a `SMAppService.daemon` (macOS 13+).
The unprivileged CLI/daemon talks to the helper over a local Unix socket
when it needs root-only data. Users who don't install the helper still
get every capability that doesn't strictly require root — which is the
overwhelming majority of what Spectra extracts today.

This split is the same pattern Tailscale uses: unprivileged CLI for most
operations, privileged daemon for the kernel-touching parts.

## What we are deliberately not building

- **Kernel extension / System Extension.** Network-filter and
  Endpoint-Security frameworks unlock packet-level visibility and
  exec/open events but require an Apple-issued entitlement granted on a
  case-by-case basis. Months-long process. Not v1.
- **Single binary that auto-elevates.** Setuid binaries on macOS are a
  trust nightmare and Apple discourages them strongly. The two-tier
  unprivileged-daemon + privileged-helper model is the right shape.
