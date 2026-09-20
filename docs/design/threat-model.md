# Threat model

Spectra is a local diagnostic tool. It reads system state such as process
tables, application metadata, code-signing information, and optionally a
privileged helper's narrowly scoped telemetry.

## Trust boundaries

```text
Local CLI ── local collectors ── local files and system tools
     │
     └── optional privileged helper over a local Unix socket
```

The core binary opens no network listeners, includes no remote transport, and
cannot install or update software on another machine. Cross-machine trust,
transport, provisioning, and auditing are responsibilities of Spectra Remote.

## Primary protections

- Keep diagnostic artifacts and snapshots owner-readable only.
- Scope privileged helper operations to a fixed allowlist and authenticated
  local callers.
- Treat heap dumps, recordings, process command lines, and permission data as
  sensitive local information.
- Preserve an explicit user boundary around destructive diagnostics and helper
  installation.
