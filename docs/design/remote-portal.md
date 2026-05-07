# Remote portal

Spectra's primary use case is **engineer-to-engineer remote debugging**:
running tool operations over SSH or Tailscale on someone else's Mac to
identify differences and performance bottlenecks. This page captures the
architecture decisions that flow from that requirement.

## What "remote portal" forces

The tool stops being a CLI-with-optional-GUI and becomes a daemon-with-clients:

```
spectra serve            # runs on the target Mac
spectra connect host     # local client, talks to the remote daemon
spectra list             # convenience — same operations against localhost daemon
```

Three consequences:

1. **JSON-over-RPC is the source of truth.** Every collector returns
   structured data; the table printer is one of several views. Already
   how the code is shaped (`detect.Result` + `--json`).
2. **The TUI runs locally and renders remote data.** Bubble Tea on your
   laptop, polling the remote daemon every second over the socket.
   Better than running the TUI inside an SSH session — no terminal-resize
   drama, no latency spikes when network blips.
3. **Tailscale's `tsnet` library is the cheat code.** Embed it and the
   daemon becomes a tailnet node directly — no port forwarding, no
   firewall rules, no `ssh -L`. The spectra binary on the remote Mac
   registers as `work-mac.tailnet-name.ts.net`, and `spectra connect work-mac`
   Just Works with MagicDNS. The daemon can now opt into either explicit TCP
   or embedded tsnet; the client uses the same typed
   `spectra connect <host> ...` shortcuts and raw JSON-RPC
   `spectra connect <host> call ...` form for both.

## Authentication

Three options, ranked by simplicity:

1. **Pure Tailscale identity.** The daemon trusts any
   peer the tailnet ACLs let through. Defer access control to Tailscale.
   Right for personal/team tailnets.
2. **Per-host tokens** layered on top: daemon issues a token at install
   time, client stores it. More work, but works off-tailnet too.
3. **mTLS via Tailscale-issued certs.** Cleanest but more code.

The implemented `tsnet` mode uses Tailscale identity and ACLs, with
optional Spectra-side allowlists for login names and node names. Per-host
tokens remain future work for non-Tailscale remote exposure.

## The killer feature: cross-host correlation

Once each Mac is running the daemon, queries can fan out. Explicit-host
fan-out and Tailscale daemon probing are implemented:

```bash
spectra fan --hosts laptop,work-mac inspect /Applications/Slack.app
# both daemons inspect Slack and return one JSON envelope
```

The broader grouped-inventory workflow remains planned:

```bash
spectra list --all-hosts
# every Mac running spectra reports its app inventory
# aggregated and grouped, with version drift highlighted
```

```bash
spectra diff laptop work-mac
# both daemons return their snapshots, client computes the diff
# "Slack 4.47.72 (laptop) vs 4.46.31 (work-mac)"
# "JDK 21.0.6-tem on laptop, JDK 17.0.10-zulu on work-mac"
```

Activity Monitor cannot tell either of those stories because it's
per-machine by construction.

## Architecture decisions

1. **Authentication model.** Tailscale ACLs are the primary remote access
   control in `tsnet` mode, with optional `WhoIs`-based allowlists. Explicit
   TCP still relies on the trusted network path.
2. **On-demand plus ring-buffer collection.** Most collectors run on demand
   for RPC calls. The daemon also keeps a short process metrics ring buffer
   and writes aggregate history to SQLite, as covered by
   [storage.md](storage.md).
3. **Daemon-side analysis.** Binary scanning, snapshot creation, rules
   evaluation, issue persistence, and helper mediation happen daemon-side.
   Clients render typed responses or call raw JSON-RPC methods.
4. **Privileged helper coupling.** The unprivileged daemon talks to the
   privileged helper over a local Unix socket when it needs root-only
   data. The remote client never talks to the helper directly — the
   daemon mediates. See [distribution.md](distribution.md) for why this
   split is necessary.

## Build order

1. Refactor `Detect()` and live collectors behind an `Inspector` interface
   so the same code path serves CLI and daemon.
2. `spectra serve` over a local Unix socket first. Validates the RPC shape
   without touching networking. **Implemented.**
3. Add explicit TCP JSON-RPC transport plus typed `spectra connect <host>
   ...` shortcuts. **Implemented; authentication is still delegated to the
   network path.**
4. Add explicit-host `spectra fan --hosts ...` fan-out over the typed
   connect surface. **Implemented.**
5. Add stored `spectra hosts` listing for machines seen through snapshots
   plus Tailscale daemon probing through `--discover-daemons`.
   **Implemented.**
6. Add `tsnet` integration. Daemon becomes a tailnet node. **Implemented.**
7. TUI client. Bubble Tea, talks to local-or-remote daemon identically.
   **Planned.**
8. Privileged helper as `spectra install-helper` subcommand. Same binary
   ships the helper; SMAppService-registered LaunchDaemon.
   **Implemented.**
9. Ring buffer + history for replay (requires SQLite from
   [storage.md](storage.md)).
   **Implemented for process metrics.**
10. Native GUI after the TUI proves the data model. **Planned.**
