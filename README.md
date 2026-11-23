# Spectra

A local diagnostic tool for macOS that combines deep static inspection of
installed apps with live process state and JVM toolchain awareness.

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

Today: a Go CLI, a local per-user daemon, and an optional local privileged helper. Spectra does
deep `.app` inspection, live process/network/storage/power inventory,
JVM and toolchain diagnostics, SQLite-backed snapshots and diffs,
recommendation rules, and issue tracking. Cross-machine operation lives in
the separately distributed Spectra Remote project.

The daemon currently exposes status and lifecycle over a Unix socket; it has
no network listener. Run `spectra daemon start` and `spectra daemon status` to
inspect it. CLI and MCP diagnostic data integration is planned separately.

Implemented code with passing tests is treated as complete in the docs.
Code whose tests are failing or absent is documented as partial until the
test suite catches up.

## Install

Prebuilt binaries for macOS (arm64, amd64) and Linux (amd64, arm64), with
SHA-256 verification:

```bash
curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/install.sh | sh
```

Pin a release with `sh -s -- --version vX.Y.Z`; in GitHub Actions the bin
directory is added to `PATH` automatically. Homebrew:
`brew install kaeawc/tap/spectra`. See [docs/install.md](docs/install.md) and
[Install release binaries](docs/operations/install-binaries.md).

## Linux and cloud agent VMs

The Linux builds are static (no cgo, no macOS frameworks), so they run on a
bare amd64 or arm64 VM with no GUI. A setup script or VM snapshot can install
them without prompts:

```bash
curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/install.sh \
  | sh -s -- --version v0.1.1 --prefix /usr/local   # or the default ~/.local, then add ~/.local/bin to PATH
spectra version && spectra-mcp --version
```

Register the MCP server over stdio, for example in a repository's `.mcp.json`:

```json
{"mcpServers": {"spectra": {"command": "spectra-mcp", "args": []}}}
```

`spectra-mcp` writes only JSON-RPC to stdout (logs go to stderr) and exits
when stdin closes. Everything an agent asks about the host works on Linux
from `/proc`, `/sys`, cgroups, `ip`/`ss` and `df`: load and its cause
(`spectra load --json --root <worktrees>`), processes and their trees
(`process`, `lineage`), leftover processes (`orphans`, `idle`), who holds a
port or directory (`owner`), short-lived spawns (`spawns`), JVMs and Gradle
daemons (`jvm`, needs a JDK's `jcmd`/`jps` on PATH), PSI and cgroup limits
(`pressure`), network, storage and kernel limits. `spectra capabilities
--json` lists the machine-readable commands this build supports.
`.app` inspection, the TCC database, launchd, unified logs and Time Machine
are macOS-only and report that they are unsupported on Linux. `alerts` and
`host_health` read the optional per-user daemon: `spectra daemon start` runs
it without systemd; otherwise those tools return `daemon_unavailable`.

## Documentation

Full living docs at [`docs/`](docs/index.md):

- [Quickstart](docs/quickstart.md) — common commands and outputs
- [Architecture](docs/design/architecture.md) — local CLI, daemon, and privileged helper
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

- macOS or Linux. Spectra is host-OS-aware: on Linux it uses native
  sources (`/etc/os-release`, `/proc`, `/sys`, `ip`/`ss`, `df`) and installs
  the optional helper via the platform-specific installer. App-bundle inspection is macOS-only — it
  shells out to `plutil`, `otool`, `codesign`, `file`, and `sqlite3`, and is
  refused with a clear message on Linux.
- Go 1.26+ for source builds

## License

MIT.
