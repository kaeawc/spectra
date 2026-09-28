# Architecture

Spectra is a local macOS and Linux diagnostic CLI. It inspects installed
applications where supported, collects live host state on demand, and stores
local snapshots. Its per-user daemon listens only on a Unix socket. It has no
network listener or embedded remote transport.

```text
spectra CLI
  ├── app inspection and framework classification
  ├── process, JVM, network, storage, power, and toolchain collectors
  ├── local snapshot, baseline, rules, and issue storage
  ├── per-user daemon: local JSON-RPC status and lifecycle
  └── optional local privileged helper over a Unix socket
```

The optional `spectra-helper` is a narrowly scoped local LaunchDaemon for
root-only telemetry. It is not reachable over the network.

Cross-machine diagnostics belong to the separately installed Spectra Remote
agent and controller. Their versioned typed request contract lives in the
`spectra-protocol` module, which has no transport dependencies. Core uses its
shared capabilities type and schema constants; remote transports remain in the
separately installed agent and controller.

## Local collection

Most CLI commands execute their collector directly and render a table or JSON.
Snapshots persist only local state in SQLite. This keeps the main Spectra
binary suitable for environments that prohibit remote-access tools.
The daemon skeleton establishes a shared local backend for future CLI, MCP,
and LSP clients; collection through it and subscriptions are separate phases.
