# Spectra

A diagnostic agent for macOS that combines deep static inspection of installed
apps with live process state, JVM toolchain awareness, and engineer-to-engineer
remote debugging over Tailscale.

Spectra exists to answer questions Activity Monitor structurally cannot:

- "What framework is this app actually built with — Electron, Tauri, Compose
  Desktop, Mac Catalyst, custom Swift+WebKit?"
- "What entitlements has it declared, what permissions has the user granted,
  and which is it actively using right now?"
- "Why is my Mac and my teammate's Mac behaving differently — what JDK
  versions, brew formulae, and toolchain drift exists between them?"
- "What is this app's storage footprint across the eight `~/Library`
  locations apps spread state into — including sparse files like Docker's
  VM disk?"
- "Which engineers on my tailnet have Slack running right now, with how much
  RSS, talking to which hosts?"

## What's Here Today

The current implementation is a CLI (`spectra`) plus an optional daemon
and privileged helper. It does deep single-host inspection of `.app`
bundles, live process/network/storage/toolchain inventory, snapshots,
baseline diffs, recommendations, and JSON-RPC calls to a local or explicit
TCP daemon. See
[detection/overview.md](detection/overview.md) for the framework detection
model, [inspection/](inspection/) for what we extract from each bundle, and
[design/architecture.md](design/architecture.md) for where this is heading.

```
./spectra /Applications/Slack.app           # one app, terse table
./spectra -v /Applications/Claude.app       # full inspection
./spectra --all                             # scan /Applications
./spectra --json --network /Applications/*  # JSON, with embedded URL hosts
./spectra snapshot --baseline pre-incident  # save a baseline
./spectra diff baseline pre-incident live   # compare baseline to now
./spectra serve --tcp 127.0.0.1:7878        # opt-in TCP JSON-RPC
./spectra connect 127.0.0.1:7878            # health check over RPC
```

## What's Planned

- **tsnet remote portal** — `spectra connect work-mac` from your laptop to
  inspect a teammate's machine over the tailnet without manually exposing a
  TCP listener. See
  [design/remote-portal.md](design/remote-portal.md).
- **Remote fan-out** — `spectra fan --hosts ...` runs one typed remote
  call across multiple explicit daemon targets; automatic discovery is
  still planned.
- **TUI client** — Bubble Tea UI against the same local-or-remote daemon
  RPC surface.
- **Release packaging** — Homebrew formula, prebuilt binaries, signing, and
  notarization.

## Distribution

Spectra currently installs from source with an optional `sudo` helper
install for root-only telemetry (system TCC, firewall rules, and
`powermetrics`). Homebrew and prebuilt binaries are planned. The Mac App
Store is incompatible with the live-monitoring features. See
[design/distribution.md](design/distribution.md) for the full analysis.
