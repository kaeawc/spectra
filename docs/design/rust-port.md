# Rust port

**Status:** approved 2026-11-27, wave 1 in progress.

Spectra moves from Go to Rust, and spectra-desktop (the Rune desktop app) moves into this repo
under `desktop/`. Go stays the shipped build until the Rust output is byte-identical to Go's
JSON. One cutover PR then removes Go, and a tag-triggered workflow publishes the CLI crates to
crates.io once the owner confirms the crate names and the first version.

## Rules for the port

- **Byte parity.** For the same inputs, Rust's stdout, stderr shape and exit code equal Go's.
  The JSON contract ([result schema](../reference/result-schema.md)) is read by name by waaagh's
  `spectra-contract.tsv`, spectra-desktop and MCP clients, so field names, order, `omitempty`
  behaviour, `null` versus `[]`, float text and timestamps must match Go's `encoding/json`.
- **Host-OS aware.** `spectra_base::hostos::Kind` selects binaries, paths and parsers per OS;
  macOS-only features return `Unsupported` elsewhere. Prefer a runtime `Kind` match over
  `#[cfg]` where the code is pure Rust, so Linux paths stay testable on macOS.
- **Testability.** Time, env, subprocess and filesystem go through the traits in
  `spectra-base`. `clippy.toml` denies the direct std calls everywhere else.
- **Unsafe code** is forbidden in every crate except `kaeawc-spectra-sys`, which holds the Mach,
  sysctl and IOReport FFI.
- **Nothing publishes** to crates.io without the owner's explicit yes on names and version. The
  workspace is `publish = false` until then.

## Workspace

One repo, two Cargo workspaces. The root workspace holds the CLI, daemon, helper and MCP server
under `crates/`; `desktop/` is its own workspace because Rune is a large git dependency with a
GPU stack. Package names carry the `kaeawc-spectra` prefix (`spectra` is taken on crates.io);
library names are `spectra_*`, and crates refer to each other through `spectra-*` aliases in
`[workspace.dependencies]`, so a prefix change touches only manifests.

| Package | Holds |
| --- | --- |
| `spectra-protocol` | Capabilities contract, error codes, framing, limits |
| `kaeawc-spectra-sys` | All `unsafe` FFI |
| `kaeawc-spectra-base` | Seams: `hostos`, `Runner`, `Clock`, `Env`, `Fs`, atomic writes, the Go-compatible JSON writer |
| `kaeawc-spectra-host` | Cross-platform collectors (memory, processes, network, storage, toolchain, load...) |
| `kaeawc-spectra-macos` | `.app` inspection, launchd, TCC and macOS tool wrappers |
| `kaeawc-spectra-jvm` | JVM, heap, GC log, attach; embeds `agent/spectra-agent.jar` |
| `kaeawc-spectra-store` | SQLite store (same schema), snapshots, cache, diff, metrics |
| `kaeawc-spectra-rules` | CEL and YAML rules, issues, whatswrong, playbooks, fleet |
| `kaeawc-spectra-daemon` | Unix-socket JSON-RPC server and typed client, hostwatch, peer credentials |
| `kaeawc-spectra-helper` | Privileged helper binary, kept small |
| `kaeawc-spectra` | Command layer (library) and the `spectra` binary |
| `kaeawc-spectra-mcp` | MCP server binary and its tools |
| `spectra-desktop` (`desktop/`) | Rune desktop app; never published (git dependencies) |

The desktop calls the command layer directly instead of parsing `spectra --json` output, and
the daemon's typed client instead of hand-written JSON-RPC.

## Build conventions

Taken from [rust-build](https://github.com/kaeawc/rust-build): toolchain pinned in
`rust-toolchain.toml` (1.99.1, the same as Rune); all dependencies in `[workspace.dependencies]`
with default features off; `deny.toml` allows one version of each crate and bans aws-lc and
OpenSSL; `dev`, `release`, `dist` and `ci` profiles; `cargo c`, `cargo t` and `cargo lint`
aliases; bacon; nextest; doctests off. Argument parsing stays Go-`flag` compatible (single-dash
long flags), so `clap` is not used.

```bash
cargo c          # check --all-targets
cargo t          # nextest run (or: make rust-test)
cargo lint       # clippy --all-targets -D warnings
cargo fmt --all
cargo deny check
make rust-ci     # all of the above, like the Rust CI workflow
```

## Waves

| Wave | Contents | Gate |
| --- | --- | --- |
| 1 | Workspace, conventions, Rust CI; Go-compatible JSON writer; seams; protocol and FFI crates; parity harness | JSON writer and protocol vectors pass |
| 2 | Import spectra-desktop with its history; desktop CI and release; archive the old repo | Desktop tests pass in Spectra CI |
| 3 | `capabilities`, `version`, `help`; command layer; first direct desktop call | `capabilities` matches Go byte for byte |
| 4–7 | In parallel: host collectors; macOS inspection; JVM and crash; store and rules. Each ends by moving the desktop's reads to direct calls | All waaagh contract rows pass on Rust |
| 8 | Daemon, hostwatch, alerts, jobs; privileged helper | Go/Rust cross-wire tests and the Tart VM suite pass |
| 9 | MCP server and database inspection | All four parity layers at 100% |
| 10 | Cutover: release builds Rust, Go deleted, docs and Homebrew updated | Owner's yes on crate names and version |
| 11 | First crates.io release; signed desktop binaries on the same GitHub release | |

## Parity proof

Recorded cases live in `contract/cases/<kind>/<name>/`. A Go test writes each one from the
Go build with `-contract-update` (helpers in `internal/contracttest`) and otherwise fails when
the checked-in case no longer matches Go; the Rust crate `spectra-contract` replays the same
inputs (recorded subprocess calls, environment, clock, filesystem tree) and asserts the same
stdout bytes and exit code. `contracttest.Recorder` records a case's subprocess calls once on
a real host.

```bash
go test ./cmd/spectra/ -run TestContract -contract-update   # rewrite cases from Go
cargo t -p spectra-contract                                 # check Rust against them
```

The layers:

1. **Replay fixtures, byte-equal:** recorded subprocess calls, a fake filesystem root, a fixed
   clock and env; Go (test-only hook) and Rust run the same fixture and must print the same
   bytes.
2. **Live shape diff** on the macOS and Linux CI runners: same key paths, JSON types, and
   `null` versus absent.
3. **Outside consumers:** waaagh's contract rows, the desktop's fixtures, and MCP
   `initialize` and `tools/list`.
4. **Cross-wire:** Go client against the Rust daemon and the reverse; the helper VM suite.
