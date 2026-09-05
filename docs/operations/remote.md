# Remote operation

Spectra has two remote transports. `spectra serve --tcp ...` exposes the
daemon JSON-RPC API on an explicit TCP listener. `spectra serve --tsnet`
embeds Tailscale's `tsnet` library so the daemon joins the tailnet as its
own managed node. The default daemon remains local-only on
`~/.spectra/sock`; remote listeners must be opted into.

The remote portal is Spectra's defining workflow: running diagnostic
operations on someone else's Mac to find configuration drift,
performance bottlenecks, and version mismatches.

See [../design/remote-portal.md](../design/remote-portal.md) for the
architecture; this page is the operator-facing reference.

## Connecting

```bash
spectra serve --tcp 127.0.0.1:7878                 # local TCP, useful for smoke tests
spectra serve --tcp 100.64.0.5:7878 --allow-remote # explicit tailnet bind
spectra serve --tsnet --tsnet-hostname work-mac    # managed tailnet node
spectra serve --nebula --nebula-config ~/.nebula/config.yaml  # embedded Nebula node

spectra connect local                              # default Unix socket
spectra connect work-mac                           # TCP port 7878, including MagicDNS
spectra connect 100.64.0.5:7878                    # raw address
spectra connect 192.168.100.11:7878                # Nebula overlay address
```

For explicit TCP, the remote Mac must:
1. Have `spectra serve --tcp <addr>:7878 --allow-remote` running.
2. Be reachable through SSH forwarding, a Tailscale interface, or another
   trusted network path.
3. Be protected by network-layer controls. TCP RPC has no Spectra-layer
   authentication yet.

For managed tailnet mode, the remote Mac must run `spectra serve --tsnet`.
The daemon stores its node state in `~/.spectra/tsnet` by default. First
run enrollment uses `TS_AUTHKEY` if set; otherwise the daemon writes a
Tailscale login URL to its log or stderr. Once enrolled, peers allowed by
Tailscale ACLs can connect through the MagicDNS name and the same
`spectra connect` commands.

To narrow access inside an already-permitted tailnet, add a Spectra-side
allowlist:

```bash
spectra serve --tsnet --tsnet-allow-logins alice@example.com,bob@example.com
spectra serve --tsnet --tsnet-allow-nodes alice-mac.tailnet.ts.net
```

Both managed overlay modes share one mesh abstraction, so they behave the
same way: an embedded node joins the overlay in-process, the daemon listens
on `:7878` there, and an optional Spectra-side allowlist gates peers by the
identity the overlay attests. Run either, or both at once.

For embedded Nebula mode, the remote Mac must run
`spectra serve --nebula --nebula-config <path>` pointing at a standard
nebula `config.yaml` (pki paths, `static_host_map`, lighthouse, firewall —
the same file the standalone `nebula` daemon would use). The node runs
in-process with a userspace network stack, so it needs no root and creates
no tun device, and the overlay can reach only what the daemon listens on —
nothing else on the machine. Certificates are provisioned externally with
`nebula-cert` against your mesh CA.

Nebula peers are identified by their signed certificate, so the allowlist
gates on cert names and groups instead of Tailscale logins:

```bash
spectra serve --nebula --nebula-config ~/.nebula/config.yaml \
  --nebula-allow-groups engineers
spectra serve --nebula --nebula-config ~/.nebula/config.yaml \
  --nebula-allow-names alice-mac,bob-mac
```

The nebula firewall rules inside `config.yaml` still apply first; the
Spectra allowlist narrows further. `health` reports each enabled overlay
under its provider name (`tsnet`, `nebula`) with its listen address and
overlay IPs.

## What you can do

Any RPC method the daemon exposes is available remotely through the
generic `call` form:

```bash
spectra connect work-mac status
spectra connect work-mac inspect /Applications/Slack.app
spectra connect work-mac jvm
spectra connect work-mac jvm-threads 4012
spectra connect work-mac snapshot
```

Typed remote shortcuts cover the common local-debugging workflows:

```bash
spectra connect work-mac host
spectra connect work-mac inspect /Applications/Slack.app
spectra connect work-mac jvm
spectra connect work-mac jvm-gc 4012
spectra connect work-mac jvm-heap 4012
spectra connect work-mac processes
spectra connect work-mac process-tree /Applications/Slack.app
spectra connect work-mac network
spectra connect work-mac connections
spectra connect work-mac diff snap-before snap-after
spectra connect work-mac network-by-app /Applications/Slack.app
spectra connect work-mac metrics
spectra connect work-mac metrics 4012 60
spectra connect work-mac storage
spectra connect work-mac storage /Applications/Slack.app
spectra connect work-mac network firewall
spectra connect work-mac power
spectra connect work-mac rules
spectra connect work-mac issues check
spectra connect work-mac cache
spectra connect work-mac cache clear detect
spectra connect work-mac issues local-machine
spectra connect work-mac issues update issue-123 fixed
spectra connect work-mac jvm-jfr-start 4012
spectra connect work-mac jvm-jfr-dump 4012 /tmp/recording.jfr
spectra connect work-mac jvm-jfr-stop 4012 /tmp/recording.jfr
spectra connect work-mac jvm-heap-dump 4012
spectra connect work-mac jvm-heap-dump 4012 /tmp/heap.hprof
spectra connect work-mac snapshot list
spectra connect work-mac snapshot diff snap-before snap-after
spectra connect work-mac toolchains
spectra connect work-mac jdk
spectra connect work-mac brew
```

Use `call` for less common methods such as direct JDK calls.

The same typed surface is also available as a top-level client flag when you
want the normal command shape:

```bash
spectra --remote work-mac jvm
spectra --remote work-mac jvm thread-dump 4012
spectra --remote work-mac inspect /Applications/Slack.app
spectra --target local network connections
```

Top-level remote dispatch returns JSON, just like `connect` and `fan`.

## Cross-host operations

Cross-host fan-out is implemented with `spectra fan --hosts`.
When `--hosts` is omitted, `spectra fan` can merge:
- hosts from local `spectra hosts` data (`~/.spectra/spectra.db`), and
- optional tailscale peers (`spectra fan --discover`) from `tailscale status --json`.

This supports both raw Tailscale peer merge and managed Spectra daemon
discovery. Use `--discover` to include all Tailscale peers from
`tailscale status --json`; use `--discover-daemons` to probe those peers and
keep only reachable Spectra daemons:

```bash
spectra hosts
spectra hosts --discover
spectra fan --hosts work-mac,alice-laptop status
spectra fan --hosts work-mac,alice-laptop inspect /Applications/Slack.app
spectra fan inspect /Applications/Slack.app
spectra fan --hosts work-mac,alice-laptop jvm
spectra fan --hosts work-mac,alice-laptop network-by-app /Applications/Slack.app
spectra fan --discover status
spectra fan --discover-daemons status
```

### Discovery source

`--discover` and `--discover-daemons` default to `--discover-via auto`, which
attempts every source and merges the results: a Tailscale-only host, a
Nebula-only host, and a host on both all discover cleanly, and a source that is
absent (no `tailscale`, no Nebula certs) is skipped rather than treated as an
error. Pass `--discover-via tailscale` or `--discover-via nebula` to force a
single source.

- **Tailscale** reads peers from `tailscale status --json`.
- **Nebula** has no live peer list, so the source of truth is the signed host
  certs: Spectra enumerates `~/.nebula` (override with `SPECTRA_NEBULA_CERTS`),
  runs `nebula-cert print -json` on each `*.crt`, and returns their overlay IPs.
  Because Nebula has no MagicDNS, discovery yields IPs rather than names.

`--discover-daemons` then narrows the merged set to hosts actually running a
Spectra daemon.

```bash
spectra hosts --discover                        # auto: Tailscale + Nebula
spectra hosts --discover-daemons --discover-via nebula
spectra fan --discover-via tailscale jvm
```

The client makes parallel RPC calls to each daemon and aggregates
results locally into one JSON envelope. The remaining intended shape is:

```bash
spectra hosts --discover-daemons             # include reachable Spectra daemons
spectra hosts --probe                        # report reachable hosts
spectra fan --discover-daemons inspect /Applications/Slack.app
spectra diff laptop work-mac                 # compare two hosts
```

## TUI mode

Planned: `spectra tui` opens the Bubble Tea TUI against the local daemon.
`spectra tui work-mac` opens it against a remote daemon. Same UI either
way — the data layer doesn't care whether the daemon is on the same
machine.

## Authentication

The current TCP transport trusts the network path. If a peer can reach
the listener, it can call daemon RPC methods. Use loopback, SSH tunnels,
Tailscale ACLs, or firewall rules to limit access.

Managed `tsnet` mode makes Tailscale identity the default authorization
layer. Embedded Nebula mode makes the mesh CA the authorization layer:
only hosts holding a certificate signed by your CA can reach the overlay
at all, the nebula firewall rules in `config.yaml` gate the port, and
`--nebula-allow-names` / `--nebula-allow-groups` narrow further by
cert-attested identity. The Tailscale ACL example for a personal tailnet:

```hujson
{
  "acls": [
    { "action": "accept", "src": ["autogroup:owner"], "dst": ["*:7878"] }
  ]
}
```

For a team tailnet, restrict by tag:

```hujson
{
  "tagOwners": { "tag:engineer": ["alice@example.com", "bob@example.com"] },
  "acls": [
    { "action": "accept", "src": ["tag:engineer"], "dst": ["tag:engineer:7878"] }
  ]
}
```

## Privacy and consent

The remote daemon is **read-only by default**. State-changing
operations (snapshots, heap dumps, JFR recordings) require explicit
consent on the client side and an audit log entry on the daemon. The
daemon rejects sensitive artifact writes unless the request includes
`confirm_sensitive: true` under the default `--artifact-policy=confirm`.
Operators can start the daemon with `--artifact-policy=deny` to make
remote artifact capture unavailable, or `--artifact-policy=allow` for
trusted automation. Clients can read the active setting through
`health` or `artifact.policy`.

The daemon does not expose:
- Arbitrary file reads outside Spectra-managed paths.
- Arbitrary command execution.
- Process memory contents (heap dumps go through `jcmd` which
  produces a sanitized .hprof, but they're still sensitive — gated
  behind explicit consent).

Operators planning to use Spectra in a multi-user / shared-tailnet
context should review what the daemon exposes; the documentation tracks
the full RPC surface.

## Common workflows

### "Why is my teammate's IDE slow?"

```bash
spectra connect alice-laptop jvm
# → see all JVMs running, GC stats, heap usage

spectra connect alice-laptop jvm-explain 4012
# → get interpreted JVM args, metaspace, code cache, soft-reference, and NMT findings

spectra connect alice-laptop jvm-vm-memory 4012
# → inspect metaspace, native memory tracking, classloaders, and code cache

spectra connect alice-laptop jvm-jmx-start-local 4012
# → enable the target JVM's local JMX connector for MBean-capable tools

spectra connect alice-laptop jvm-flamegraph 4012 /tmp/intellij-profile.html
# → capture an async-profiler flamegraph on the remote host

spectra connect alice-laptop jvm-threads 4012 > intellij-threads.json
# → captured to local disk for analysis
```

### "Are we on the same JDK?"

```bash
spectra fan --hosts alice-laptop,bob-laptop jdk
# → returns one JDK inventory per host for drift comparison
```

### "What does this app do that mine doesn't?"

```bash
spectra diff me work-mac --filter app=Slack
# → side-by-side metadata, entitlements, granted perms,
#   storage footprint
```

### "Snapshot the whole team's machines as a baseline"

```bash
spectra fan --hosts alice-laptop,bob-laptop snapshot
```

## Implementation order

The local daemon, explicit TCP transport, embedded tsnet listener, and
managed Tailscale daemon discovery are implemented. Remaining remote work:

1. Add TUI support against local-or-remote daemon targets.
