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

V1 will be Tailscale-ACL-only.

## The killer feature: cross-host correlation

Once each Mac is running the daemon, queries can fan out. Explicit-host
fan-out is implemented today; tailnet discovery is the planned next step:

```bash
spectra fan --hosts laptop,work-mac inspect /Applications/Slack.app
# both daemons inspect Slack and return one JSON envelope
```

The intended discovered-host workflow remains:

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

## Open architecture questions

These will get pinned down before the first daemon commit:

1. **Authentication model.** Pure Tailscale ACL is leaning, but per-host
   tokens may be needed for non-Tailscale SSH usage.
2. **On-demand vs always-on collection.** If `spectra connect host` only
   spins up data collection when a client connects, idle cost is ~zero.
   If the daemon keeps a ring buffer of the last hour, scrollback and
   replay become possible. The latter requires real persistence —
   already covered by [storage.md](storage.md).
3. **What runs where.** Does the daemon do all analysis (binary
   scanning, rules engine evaluation), or does it stream raw observations
   and the client computes? Daemon-side keeps clients dumb and lets
   results cache across multiple connecting clients. Almost certainly
   daemon-side, lazy, with a small in-memory LRU of recent `Detect()`
   results.
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
5. Add stored `spectra hosts` listing for machines seen through snapshots.
   **Implemented; live daemon discovery is still planned.**
6. Add `tsnet` integration. Daemon becomes a tailnet node. **Implemented.**
7. TUI client. Bubble Tea, talks to local-or-remote daemon identically.
8. Privileged helper as `spectra install-helper` subcommand. Same binary
   ships the helper; SMAppService-registered LaunchDaemon.
9. Ring buffer + history for replay (requires SQLite from
   [storage.md](storage.md)).
10. Native GUI after the TUI proves the data model.
