# Spectra

A local diagnostic tool for macOS that combines deep static inspection of
installed apps with live process state and JVM toolchain awareness.

Spectra exists to answer questions Activity Monitor structurally cannot:

- "What framework is this app actually built with — Electron, Tauri, Compose
  Desktop, Mac Catalyst, custom Swift+WebKit?"
- "What entitlements has it declared, what permissions has the user granted,
  and which is it actively using right now?"
- "What is this app's storage footprint across the eight `~/Library`
  locations apps spread state into — including sparse files like Docker's
  VM disk?"

## What's Here Today

The current implementation is a local CLI (`spectra`) plus an optional
privileged helper. It does deep single-host inspection of `.app` bundles,
live process/network/storage/toolchain inventory, snapshots, baseline diffs,
and recommendations. See
[detection/overview.md](detection/overview.md) for the framework detection
model, [inspection/metadata.md](inspection/metadata.md) for what we
extract from each bundle, and
[design/architecture.md](design/architecture.md) for the local CLI and helper
model.

```
./spectra /Applications/Slack.app           # one app, terse table
./spectra -v /Applications/Claude.app       # full inspection
./spectra --all                             # scan /Applications
./spectra --json --network /Applications/*  # JSON, with embedded URL hosts
./spectra snapshot --baseline pre-incident  # save a baseline
./spectra diff baseline pre-incident live   # compare baseline to now
```

## Implemented And Planned

- **Cross-machine diagnostics** — use the separately distributed Spectra
  Remote project; the core Spectra binary has no listener or remote transport.
- **Release packaging** — Homebrew, prebuilt binaries, signing, and
  notarization are still planned.

## Distribution

Spectra currently installs from source with an optional `sudo` helper install
for root-only telemetry (system TCC, firewall rules, and `powermetrics`).
Homebrew and prebuilt binaries are planned. The Mac App Store is incompatible
with the live-monitoring features. See
[design/distribution.md](design/distribution.md) for the full analysis.
