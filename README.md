# Spectra

A diagnostic agent for macOS that combines deep static inspection of installed
apps with live process state, JVM toolchain awareness, and engineer-to-engineer
remote debugging over Tailscale.

```bash
git clone https://github.com/kaeawc/spectra.git
cd spectra
make build
./spectra /Applications/Slack.app
```

```
APP                           UI                        RUNTIME         PACKAGING   CONFIDENCE
------------------------------------------------------------------------------------------------
Slack                         Electron                  Node+Chromium               high
```

## What it does

Spectra answers questions Activity Monitor structurally cannot:

- What framework is this app built with — Electron, Tauri, Compose Desktop,
  Mac Catalyst, custom Swift+WebKit?
- What entitlements has it declared, what permissions has the user granted,
  and which is it actively using right now?
- What is its real on-disk storage footprint, accounting for sparse files
  like Docker's VM disk?
- What hosts does its code reference?

The full inspection picks up: bundle ID, app version, Electron version,
architectures, code-sign team, hardened runtime, sandbox status, declared
entitlements, declared privacy purposes, granted privacy permissions
(from TCC.db), third-party frameworks, embedded npm packages, helper apps,
XPC services, plugins, login items, running processes with RSS, and the
storage footprint across eight `~/Library` locations. With `--network`,
also extracts every URL host referenced in the binary and `app.asar`.

## Status

Today: a single-binary CLI that does deep, single-host static inspection
of `.app` bundles. 17 tests, ~10s to scan all 100 apps in `/Applications`.

Heading toward: a daemon-with-clients architecture that exposes the same
inspection over JSON-RPC, talks to peers over Tailscale via `tsnet`,
adds VisualVM-class JVM introspection, and runs a CEL-based
recommendations engine over snapshots stored in SQLite.

## Documentation

Full living docs at [`docs/`](docs/index.md):

- [Quickstart](docs/quickstart.md) — common commands and outputs
- [Architecture](docs/design/architecture.md) — where this is heading
- [Distribution](docs/design/distribution.md) — why MAS is out, why Homebrew
- [Storage stack](docs/design/storage.md) — SQLite + sharded blob store
- [Detection model](docs/detection/overview.md) — the three-layer classifier
- [Result schema](docs/reference/result-schema.md) — JSON output contract

Local docs preview:

```bash
make docs-install   # mkdocs + lychee
make docs-serve     # http://127.0.0.1:8080
```

## Requirements

- macOS (detection shells out to `plutil`, `otool`, `codesign`, `file`,
  `sqlite3` — all preinstalled)
- Go 1.26+ for source builds

## License

MIT.
