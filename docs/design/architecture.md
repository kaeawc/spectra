# Architecture

Spectra is heading toward a daemon-with-clients model where the same Go
binary acts as either a long-lived collector or as a lightweight client
that queries a local or remote daemon over JSON-RPC.

## Today: single-binary CLI

The current `spectra` binary runs static inspection in a single pass per
invocation:

```
$ spectra /Applications/Claude.app
   │
   ├── Detect()                    # 3-layer framework classification
   ├── populateMetadata()          # Info.plist, codesign, file
   ├── readPrivacyDescriptions()   # Info.plist NS*UsageDescription
   ├── scanDependencies()          # Frameworks/, npm, jars
   ├── scanHelpers()               # XPC, PlugIns, Frameworks/*.app
   ├── scanLoginItems()            # ~/Library + /Library LaunchAgents/Daemons
   ├── scanRunningProcesses()      # ps -axwwo
   ├── scanGrantedPermissions()    # sqlite3 TCC.db
   ├── scanStorage()               # ~/Library size sweep (sparse-aware)
   └── scanNetworkEndpoints()      # opt-in, scans app.asar
```

Each sub-detection is independent. Results accumulate into a single
`detect.Result` struct (see
[reference/result-schema.md](../reference/result-schema.md)).

## Planned: daemon-with-clients

Spectra is shaping into three roles played by the same binary:

```
┌─ Collector (long-lived) ───────────────────────────────────┐
│  spectra serve                                              │
│  ├ Listens on Unix socket and tsnet (Tailscale)            │
│  ├ Caches Detect() results keyed by content hash           │
│  ├ Periodically samples live state (ps, lsof, nettop)      │
│  ├ Writes snapshots to SQLite                              │
│  └ Talks to optional privileged helper for root-only data  │
└────────────────────────────────────────────────────────────┘
        ↑ JSON-RPC over Unix socket / tsnet
┌─ Client (interactive) ─────────────────────────────────────┐
│  spectra list / spectra inspect / spectra connect host     │
│  ├ Renders to terminal (table or JSON)                     │
│  └ Renders to TUI (Bubble Tea)                             │
└────────────────────────────────────────────────────────────┘
        ↑ same RPC surface, talks to local or remote daemon
┌─ Privileged helper (optional, root) ───────────────────────┐
│  Installed by `sudo spectra install-helper`                 │
│  ├ Registered as SMAppService.daemon (macOS 13+)           │
│  ├ Reads system TCC.db, runs fs_usage / powermetrics       │
│  └ Exposes data over a local Unix socket to the daemon     │
└────────────────────────────────────────────────────────────┘
```

The collector is the only role that touches storage and live data
collection. The CLI is a stateless client. The helper is opt-in and
required only for root-grade visibility.

## Why daemon-with-clients (vs CLI-only)

- **Caching pays off across calls.** `Detect()` of an unchanged bundle is
  pure work; a daemon caches it and serves repeat inspections in
  microseconds.
- **Live state needs continuity.** A ring buffer of recent CPU/RSS/network
  samples enables "what was this process doing five minutes ago" without
  the user having had Spectra running at the time.
- **The recommendations engine and issue tracker need persistence.** Both
  imply a single owner of the SQLite database — that's the daemon.
- **Remote portal is the primary use case.** See
  [remote-portal.md](remote-portal.md). A long-lived daemon that's a
  tailnet node makes "inspect a teammate's Mac" a one-line command.

## Why not native macOS GUI from the start

The data layer (collectors, RPC surface, storage) is shared between TUI,
GUI, and remote consumers. Building it once in Go and shipping a Bubble
Tea TUI proves the data model works before investing in SwiftUI. A native
GUI becomes a cheap follow-on once the daemon's RPC surface is stable.

See [design/distribution.md](distribution.md) for why Mac App Store is
incompatible with this architecture.

## RPC protocol

Decision pending. Strong candidates:

- **JSON-RPC 2.0 over HTTP** (carried by `tsnet` for transport): boring,
  portable, debuggable with `curl`. Future SwiftUI client can hit it
  natively.
- **gRPC**: more typed, harder to cross-language without codegen.
- **Custom framed JSON over Unix socket**: simplest, least debuggable.

Leaning toward JSON-RPC over HTTP for protocol stability and ease of
manual debugging. See [design/storage.md](storage.md) for the data the
RPC will be moving.
