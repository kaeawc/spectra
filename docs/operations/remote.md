# Remote operation

> **Status: planned.** Captures the design for `spectra connect <host>`.

The remote portal is Spectra's defining workflow: running diagnostic
operations on someone else's Mac to find configuration drift,
performance bottlenecks, and version mismatches.

See [../design/remote-portal.md](../design/remote-portal.md) for the
architecture; this page is the operator-facing reference.

## Connecting

```bash
spectra connect work-mac          # MagicDNS hostname on your tailnet
spectra connect alice@laptop      # via Tailscale ACL identity
spectra connect 100.64.0.5        # raw tailnet IP
```

The remote Mac must:
1. Have `spectra serve --tailscale` running (or set up as a launchd agent).
2. Be on the same tailnet as the client.
3. Be reachable per Tailscale ACLs.

## What you can do

Any RPC method the daemon exposes is available remotely, gated by
Tailscale ACLs. The CLI presents them as flat subcommands:

```bash
spectra connect work-mac inspect /Applications/Slack.app
spectra connect work-mac jvm 4012
spectra connect work-mac jvm threadDump 4012
spectra connect work-mac diff baseline-2024-01-15
spectra connect work-mac snapshot create
```

## Cross-host operations

The killer feature: ops that fan out across multiple tailnet nodes.

```bash
spectra hosts                                # list discovered Spectra daemons
spectra fan inspect /Applications/Slack.app  # inspect Slack on every host
spectra fan jvm                              # JVM snapshot from every host
spectra diff laptop work-mac                 # compare two hosts
```

The client makes parallel RPC calls to each daemon and aggregates
results locally.

## TUI mode

`spectra tui` opens the Bubble Tea TUI against the local daemon.
`spectra tui work-mac` opens it against a remote daemon. Same UI
either way — the data layer doesn't care whether the daemon is on
the same machine.

## Authentication

V1 trusts Tailscale. If a peer can reach the daemon's tsnet listener
per ACLs, it can call any read-only RPC method. State-changing methods
require explicit consent — for example, `jvm.heapDump` writes to the
remote machine's blob cache and is gated.

The Tailscale ACL example for a personal tailnet:

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
consent on the client side and an audit log entry on the daemon.

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

spectra connect alice-laptop jvm threadDump 4012 > intellij-threads.txt
# → captured to local disk for analysis
```

### "Are we on the same JDK?"

```bash
spectra fan jdk list
# → tabulates installed JDKs across all hosts in the tailnet,
#   highlights drift
```

### "What does this app do that mine doesn't?"

```bash
spectra diff me work-mac --filter app=Slack
# → side-by-side metadata, entitlements, granted perms,
#   storage footprint
```

### "Snapshot the whole team's machines as a baseline"

```bash
spectra fan snapshot create --name pre-incident
```

## Implementation order

Same as the daemon (see [daemon.md](daemon.md)) — remote operation is
a property of the daemon's transport layer, not a separate feature
track. Once the daemon listens on `tsnet`, all client subcommands
work remotely.
