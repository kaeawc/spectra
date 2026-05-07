# Glossary

Terms used across Spectra's docs and code. Cross-referenced to the
canonical doc when one exists.

## Spectra-specific

**Agent.** Generic term we avoid in favor of the more precise
[*daemon*](#daemon) and [*helper*](#privileged-helper). The
"spectra-agent" is specifically the Java agent JAR loaded into a
running JVM via the Attach API for layer-2 JVM inspection — see
[../inspection/jvm.md](../inspection/jvm.md).

**Baseline.** A frozen reference [*snapshot*](#snapshot) used as the
"known good" comparison point for [*diff*](#diff). Created via
`spectra snapshot create --baseline <name>`. Immutable; not subject
to live-snapshot retention.

**Blob store.** The on-disk store for large artifacts (heap dumps,
JFR recordings, thread dumps) that don't belong in
[*SQLite*](#sqlite). Two-level hash-sharded layout under
`~/.cache/spectra/v1/{kind}/{hash[:2]}/{hash[2:]}.bin`. See
[../design/storage.md](../design/storage.md) and
[../operations/artifacts.md](../operations/artifacts.md).

**Catalog.** The set of [*rules*](#rule) the
[*recommendations engine*](#recommendations-engine) evaluates. Ships
with the binary, can be extended by project-local
`spectra.yml` overrides or remote rule sources.

**Collector.** A function that observes one slice of system state and
returns a structured result. `Detect`, `scanRunningProcesses`,
`scanGrantedPermissions`, etc. are collectors. The daemon is a
composition of collectors.

**Confidence (high / medium / low).** Spectra's classifier output
quality marker. *High* means a definitive signal matched (e.g. an
Electron framework directory). *Medium* means the closest matching
signal was non-specific (e.g. AppKit linked but no framework
markers). *Low* means we couldn't classify and reported what we
found.

**Daemon.** The long-lived [*unprivileged*](#unprivileged-daemon) Go
process invoked via `spectra serve`. Owns [*SQLite*](#sqlite),
[*blob store*](#blob-store), [*tsnet*](#tsnet) state, and lives in
the user's home. Distinguished from the
[*privileged helper*](#privileged-helper).

**Detect / Detection.** The static-inspection layer that classifies
a `.app` bundle's framework. Returns `detect.Result`. See
[../detection/overview.md](../detection/overview.md).

**Diff.** A structural comparison between two snapshots — typically
"my Mac vs your Mac" or "this Mac now vs this Mac last Tuesday." See
[../design/system-inventory.md#diff-semantics](../design/system-inventory.md#diff-semantics).

**Drift.** A diff result indicating two hosts have meaningfully
different state in a category. Toolchain drift (different JDK
versions), formula drift (different brew versions), config drift
(different sysctls). The recommendations engine often fires on
drift.

**Granted permissions.** The privacy services TCC has actually
authorized for a bundle. Distinct from
[*declared*](#declared-privacy) — a permission can be granted only
if it's been declared. See
[../inspection/security.md](../inspection/security.md).

**Hardened runtime.** macOS code-signing flag (`flags=0x10000(runtime)`)
required for Developer ID notarization. Disables certain runtime
capabilities (loading unsigned dylibs, JIT without explicit
entitlement). Required for any Spectra-distributed binary.

**Helper.** Short for [*privileged helper*](#privileged-helper).

**Inspection.** The live-state half of Spectra's data collection,
distinguished from the static [*detection*](#detect--detection)
half. Process state, network state, granted permissions, etc.

**Issue.** An open finding emitted by the
[*recommendations engine*](#recommendations-engine). Persists across
snapshots; same finding seen on Monday and Tuesday is one issue with
two observations. Status moves through
`open → acknowledged → fixed → closed` (or `dismissed`). See
[../design/recommendations-engine.md](../design/recommendations-engine.md).

**JDK install.** A discovered Java Development Kit on the host.
Spectra enumerates from system, brew, SDKMAN, asdf, mise, JBR
toolbox, and manual install paths. See
[../inspection/toolchains.md](../inspection/toolchains.md).

**JFR.** Java Flight Recorder. Built into modern OpenJDK; records
profiling and runtime events to a binary `.jfr` file. Spectra
captures these via `jcmd JFR.start/dump` and stores them in the
blob store.

**JVM info.** Per-running-Java-process state (heap, threads, GC,
JDK attribution). Each `JVMInfo` row in a snapshot links to a
[*JDK install*](#jdk-install) and a process. See
[../inspection/jvm.md](../inspection/jvm.md).

**Layer (1 / 2 / 3).** The detection model's three-pass structure:
bundle markers, linked dylibs, binary content scanning. Strongest
signal first. See [../detection/overview.md](../detection/overview.md).

**Login item.** A LaunchAgent or LaunchDaemon plist installed on the
system that belongs to a given app. Spectra enumerates them by
filename prefix (matching the bundle ID's reverse-DNS prefix) or
by ProgramArguments path matching. See
[../inspection/login-items.md](../inspection/login-items.md).

**Native module.** An `.node` file under
`Contents/Resources/app.asar.unpacked/` in an Electron app — a
custom-compiled bridge between Node.js and the OS. Spectra
classifies each by language (Rust / Swift / C++). See
[../detection/native-modules.md](../detection/native-modules.md).

**Privileged helper.** The optional root-running LaunchDaemon
installed via `sudo spectra install-helper`. Exposes a narrow RPC
surface to the unprivileged daemon over a local Unix socket. See
[../design/privileged-helper.md](../design/privileged-helper.md).

**Recommendations engine.** The CEL-rules-driven evaluation layer
that fires against [*snapshot*](#snapshot) data and produces
[*issues*](#issue). See
[../design/recommendations-engine.md](../design/recommendations-engine.md).

**Result.** The output of `Detect()` for one app — see
[result-schema.md](result-schema.md). Synonym in casual usage:
"detection record."

**Rule.** A single declarative match expression in CEL with
metadata (id, severity, message, fix). Lives in the
[*catalog*](#catalog).

**Sandbox.** The `com.apple.security.app-sandbox` entitlement
flag. Mandatory for Mac App Store apps; optional for Developer ID
apps. Spectra cannot ship as a sandboxed app — see
[../design/distribution.md](../design/distribution.md).

**Sample / sampling.** Capturing a stack trace from a running
process, typically at sub-second intervals, to identify hot code
paths. macOS provides `sample <pid>`. Spectra stores sample output
in the [*blob store*](#blob-store) keyed by `(pid, timestamp)`.

**Shim launcher.** A small main executable that loads its real
implementation from a sibling framework (Chrome, Edge, Brave). When
encountered, Spectra follows the framework binary for layer-2/3
classification. See
[../detection/overview.md#shim-launcher-handling](../detection/overview.md#shim-launcher-handling).

**Snapshot.** A timestamped capture of one host's state. Lives in
[*SQLite*](#sqlite); refers to artifacts in the
[*blob store*](#blob-store). See
[../design/system-inventory.md](../design/system-inventory.md).

**SQLite.** Spectra's relational store for structured snapshot
data. One database per host. WAL mode. Cross-host diff is a client-
side correlation across each daemon's local database. See
[../design/storage.md](../design/storage.md).

**Sub-detection.** A secondary classifier that runs after the main
[*detection*](#detect--detection) verdict has been reached. Today
this is just the [*native module*](#native-module) classifier for
Electron apps; future sub-detections may include JVM
fingerprinting, browser identity (Chromium vs Edge vs Brave), etc.

**System inventory.** The full structured shape of one
[*snapshot*](#snapshot) — apps, processes, JVMs, JDKs, toolchains,
network state, storage, power, env. See
[../design/system-inventory.md](../design/system-inventory.md).

**TCC.** Transparency, Consent, and Control — Apple's privacy
permission system. The TCC.db SQLite database holds grant decisions
per `(service, client)` pair. Spectra reads it to populate
[*granted permissions*](#granted-permissions).

**Tier ("user" / "system" / "daemon").** For login items: a
LaunchAgent in `~/Library` runs as the user; one in `/Library/LaunchAgents`
runs as the user (any user); a LaunchDaemon in `/Library/LaunchDaemons`
runs as root.

**tsnet.** Tailscale's Go library that lets a process join the
tailnet directly as a node, without requiring `tailscaled`.
Spectra's daemon embeds it so remote-portal connections work without
port forwarding. See
[../design/remote-portal.md](../design/remote-portal.md).

**Unprivileged daemon.** The user-running [*daemon*](#daemon)
process. Distinguished from the
[*privileged helper*](#privileged-helper).

## macOS-native terms (non-Spectra)

**`.app` bundle.** A directory ending in `.app` that macOS treats as
a runnable program. Has a fixed structure under `Contents/`
(`Info.plist`, `MacOS/<binary>`, `Resources/`, `Frameworks/`, etc.).

**`@rpath`.** Runtime search path token used in Mach-O linking; lets
a binary reference a sibling library by relative location. Spectra
parses these to follow native module sidecars.

**Catalyst (Mac Catalyst).** Apple's bridging layer that runs iOS
UIKit apps on macOS. Identified by paths under
`/System/iOSSupport/`. See
[../detection/catalyst.md](../detection/catalyst.md).

**CFBundleExecutable / CFBundleIdentifier.** `Info.plist` keys: the
main executable filename and the reverse-DNS bundle identifier.
Spectra uses both extensively.

**CGo.** Go's foreign-function interface for calling C. Spectra
deliberately uses **no CGo** today, which keeps cross-compilation
trivial and binaries small. The pure-Go `modernc.org/sqlite` driver
is chosen for the same reason. See
[../design/storage.md](../design/storage.md).

**Codesign.** macOS's code-signing tool. Spectra parses
`codesign -dv` output for team identifier and hardened-runtime flag.

**Declared privacy.** The set of `NS*UsageDescription` keys an app
declares in `Info.plist`. macOS shows these strings during permission
prompts. Apps cannot ask for permissions they haven't declared.

**Entitlement.** A capability declared in the app's signed
entitlements plist. Read via `codesign -d --entitlements :-`. See
[../inspection/security.md](../inspection/security.md).

**Helper sub-app.** An `.app` directory inside `Contents/Frameworks/`
of a parent bundle. Standard Chromium / Electron pattern: GPU,
Renderer, Plugin, main helper.

**LaunchAgent / LaunchDaemon.** launchd-managed background processes.
Agents run as the user; Daemons run as root. See
[../inspection/login-items.md](../inspection/login-items.md).

**Mach-O.** macOS executable file format. Multi-arch ("universal")
binaries pack one Mach-O per architecture into a fat header.

**MASReceipt.** `_MASReceipt` directory inside `Contents/`,
indicating Mac App Store distribution.

**plutil.** macOS plist utility. Spectra uses it to extract
`Info.plist` keys (fast) and to convert binary plists to XML for
regex scanning (when single-key reads aren't sufficient).

**SMAppService.daemon.** The macOS-13+ API for registering a
LaunchDaemon embedded in an app. Replaces the deprecated
SMJobBless. Spectra uses it for the
[*privileged helper*](#privileged-helper) install.

**Sparkle.** The de-facto auto-update framework for native macOS apps.
Spectra detects it as a [*packaging*](#packaging) hint.

**Squirrel.** The Electron auto-updater. Spectra detects it as a
[*packaging*](#packaging) hint. Note: Squirrel.framework alone
doesn't imply Electron — some non-Electron apps (Ollama) ship it
standalone.

**Stat_t.Blocks.** The `struct stat` field that gives the number of
512-byte blocks actually allocated to a file. Spectra uses
`Blocks * 512` to report sparse-file-correct sizes. See
[../inspection/storage-footprint.md](../inspection/storage-footprint.md).

**Universal binary.** A Mach-O containing multiple architecture
slices (typically `arm64` + `x86_64`). Spectra reports both.

**XPC service.** A sub-bundle under `Contents/XPCServices/<name>.xpc`
that runs as a separate process for IPC. Common for sandboxed
background tasks. See
[../inspection/helpers-and-xpc.md](../inspection/helpers-and-xpc.md).

## See also

- [result-schema.md](result-schema.md) — every field of `detect.Result`
- [../detection/overview.md](../detection/overview.md) — the
  classifier model
- [../design/system-inventory.md](../design/system-inventory.md) —
  the snapshot data model
