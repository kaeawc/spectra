# Architecture

Spectra is a local macOS diagnostic CLI. It inspects installed applications,
collects live host state on demand, and stores local snapshots. It does not
listen on network sockets or embed a remote transport.

```text
spectra CLI
  ├── app inspection and framework classification
  ├── process, JVM, network, storage, power, and toolchain collectors
  ├── local snapshot, baseline, rules, and issue storage
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
