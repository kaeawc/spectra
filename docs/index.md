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

## What's here today

The current implementation is a single-binary CLI (`spectra`) that does deep,
single-host inspection of `.app` bundles. See
[detection/overview.md](detection/overview.md) for the framework detection
model, [inspection/](inspection/) for what we extract from each bundle, and
[design/architecture.md](design/architecture.md) for where this is heading.

```
./spectra /Applications/Slack.app           # one app, terse table
./spectra -v /Applications/Claude.app       # full inspection
./spectra --all                             # scan /Applications
./spectra --json --network /Applications/*  # JSON, with embedded URL hosts
```

## What's planned

- **Daemon mode** — `spectra serve` exposes the inspection RPC over a Unix
  socket and over Tailscale via `tsnet`. See
  [design/architecture.md](design/architecture.md).
- **Remote portal** — `spectra connect work-mac` from your laptop to inspect a
  teammate's machine over the tailnet. See
  [design/remote-portal.md](design/remote-portal.md).
- **JVM inspection** — VisualVM-class introspection of running JVMs and
  installed JDKs. See [inspection/jvm.md](inspection/jvm.md).
- **Cross-host snapshot diff** — "diff my Mac vs your Mac" on apps,
  toolchains, env, and config drift.
- **Recommendations engine** — rules-driven catalog of issues with severity
  and remediation steps. See
  [design/recommendations-engine.md](design/recommendations-engine.md).
- **Persistent storage** — SQLite for relational facts, krit-style sharded
  blob store for heap dumps and JFR recordings. See
  [design/storage.md](design/storage.md).

## Distribution

Spectra ships through Homebrew, with an optional `sudo` step that installs a
privileged helper for root-only telemetry (Full Disk Access, `fs_usage`,
`powermetrics`). The Mac App Store is incompatible with the live-monitoring
features. See [design/distribution.md](design/distribution.md) for the full
analysis.
