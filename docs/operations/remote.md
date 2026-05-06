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

spectra connect local                              # default Unix socket
spectra connect work-mac                           # TCP port 7878, including MagicDNS
spectra connect 100.64.0.5:7878                    # raw address
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
spectra connect work-mac network-by-app /Applications/Slack.app
spectra connect work-mac metrics
spectra connect work-mac metrics 4012 60
spectra connect work-mac storage
spectra connect work-mac storage /Applications/Slack.app
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

## Cross-host operations

Cross-host fan-out is implemented with `spectra fan --hosts`.
When `--hosts` is omitted, `spectra fan` can merge:
- hosts from local `spectra hosts` data (`~/.spectra/spectra.db`), and
- optional tailscale peers (`spectra fan --discover`) from `tailscale status --json`.

This currently supports a manual discovery opt-in path while we migrate toward
fully managed tsnet fan-out:

```bash
spectra hosts
spectra hosts --discover
spectra fan --hosts work-mac,alice-laptop status
spectra fan --hosts work-mac,alice-laptop inspect /Applications/Slack.app
spectra fan inspect /Applications/Slack.app
spectra fan --hosts work-mac,alice-laptop jvm
spectra fan --hosts work-mac,alice-laptop network-by-app /Applications/Slack.app
spectra fan --discover status
```

The client makes parallel RPC calls to each daemon and aggregates
results locally into one JSON envelope. The remaining intended shape is:

```bash
spectra hosts                                # include discovered Spectra hosts
spectra hosts --probe                         # report reachable hosts
spectra fan inspect /Applications/Slack.app  # inspect Slack on every discovered host
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
layer. The Tailscale ACL example for a personal tailnet:

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
`confirm_sensitive: true`.

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

The local daemon, explicit TCP transport, and embedded tsnet listener are
implemented. Remaining remote work:

1. Add managed tsnet host discovery so `spectra hosts` includes reachable daemons
   and `spectra fan` can run without an explicit `--hosts` list.
2. Add TUI support against local-or-remote daemon targets.
