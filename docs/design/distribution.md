# Distribution

Each macOS distribution channel imposes a different ceiling on what
Spectra can do. This page captures the analysis behind the current
**source build + optional LaunchDaemon helper** distribution path, the
planned Homebrew/prebuilt release channels, and why the Mac App Store is
out of scope for the full product.

Today the supported path is:

```bash
git clone https://github.com/kaeawc/spectra.git
cd spectra
make build-all
./spectra version
./spectra-helper --version
```

The helper is optional. Users who want root-only telemetry install it
explicitly:

```bash
sudo ./spectra install-helper
./spectra install-helper --status
```

## Capability vs channel matrix

| Capability | MAS | Source build | Homebrew CLI (planned) | Prebuilt archive/cask (planned) |
|---|---|---|---|---|
| Static inspection of `/Applications` | partial | ✓ | ✓ | ✓ |
| Read other users' app data | ✗ (sandbox) | ✓ | ✓ | ✓ |
| Per-user TCC.db | ✗ | ✓ | ✓ | ✓ |
| System TCC.db | ✗ | ✓ via helper | ✓ via helper | ✓ via helper |
| `lsof -i`, `nettop` | ✗ | ✓ | ✓ | ✓ |
| `powermetrics`, system TCC, pf rules | ✗ | ✓ via helper | ✓ via helper | ✓ via helper |
| `fs_usage` | ✗ | planned helper method | planned helper method | planned helper method |
| Listen on TCP / Unix socket | ✗ | ✓ | ✓ | ✓ |
| Embed `tsnet` (Tailscale) | ✗ | ✓ | ✓ | ✓ |
| Install LaunchDaemon | ✗ | ✓ via `sudo spectra install-helper` | ✓ via opt-in command | planned signed helper |
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

## Current source-build channel

Source build is the only supported channel today. It keeps distribution
honest while the product surface is still changing quickly:

- `make build` produces the unprivileged CLI.
- `make build-all` produces both `spectra` and `spectra-helper`.
- `spectra install-helper` copies the helper to
  `/Library/PrivilegedHelperTools/spectra-helper`, writes
  `/Library/LaunchDaemons/dev.spectra.helper.plist`, and loads it with
  `launchctl`.
- `spectra install-helper uninstall` unloads the LaunchDaemon and removes
  the installed helper files.

This is intentionally less polished than a signed release package, but it
matches the implemented code path and keeps root installation explicit.

## Why Homebrew is still the planned primary channel

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

The first Homebrew formula should build the unprivileged CLI and helper
binary, but should not install or start the helper automatically. Root
visibility stays behind the same explicit command:

```bash
sudo spectra install-helper
```

## Why an optional sudo helper

The capabilities that need root (`powermetrics`, system TCC.db, pf
firewall rules, and future `fs_usage`) require a separately installed
privileged helper. The user grants this once, explicitly:

```bash
sudo spectra install-helper
```

The current installer uses a LaunchDaemon plist and root-owned helper
binary. Future signed packaging can move this to `SMAppService.daemon`
or `SMJobBless` once Spectra has release signing, notarization, and
helper identity verification in place. The unprivileged CLI/daemon talks
to the helper over a local Unix socket when it needs root-only data.
Users who don't install the helper still get every capability that
doesn't strictly require root, which is the overwhelming majority of what
Spectra extracts today.

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
