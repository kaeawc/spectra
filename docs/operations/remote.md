# Remote operation

Spectra has an initial remote transport: `spectra serve --tcp ...`
exposes the daemon JSON-RPC API on an explicit TCP listener, and
`spectra connect <host>` calls that daemon. The default daemon remains
local-only on `~/.spectra/sock`; TCP must be opted into.

The remote portal is Spectra's defining workflow: running diagnostic
operations on someone else's Mac to find configuration drift,
performance bottlenecks, and version mismatches.

See [../design/remote-portal.md](../design/remote-portal.md) for the
architecture; this page is the operator-facing reference.

## Connecting

```bash
spectra serve --tcp 127.0.0.1:7878                 # local TCP, useful for smoke tests
spectra serve --tcp 100.64.0.5:7878 --allow-remote # explicit tailnet bind

spectra connect local                              # default Unix socket
spectra connect work-mac                           # TCP port 7878
spectra connect 100.64.0.5:7878                    # raw address
```

The remote Mac must:
1. Have `spectra serve --tcp <addr>:7878 --allow-remote` running.
2. Be reachable through SSH forwarding, a Tailscale interface, or another
   trusted network path.
3. Be protected by network-layer controls. TCP RPC has no Spectra-layer
   authentication yet.

## What you can do

Any RPC method the daemon exposes is available remotely through the
generic `call` form:

```bash
spectra connect work-mac status
spectra connect work-mac call inspect.app '{"path":"/Applications/Slack.app"}'
spectra connect work-mac call jvm.list
spectra connect work-mac call jvm.thread_dump '{"pid":4012}'
spectra connect work-mac call snapshot.create
```

Common typed remote shortcuts are available for frequent read-only calls:

```bash
spectra connect work-mac inspect /Applications/Slack.app
spectra connect work-mac jvm
spectra connect work-mac processes
spectra connect work-mac network
spectra connect work-mac toolchains
```

Use `call` for everything else.

## Cross-host operations

Cross-host fan-out is still planned. The intended shape is:

```bash
spectra hosts                                # list discovered Spectra daemons
spectra fan inspect /Applications/Slack.app  # inspect Slack on every host
spectra fan jvm                              # JVM snapshot from every host
spectra diff laptop work-mac                 # compare two hosts
```

The client makes parallel RPC calls to each daemon and aggregates
results locally.

## TUI mode

Planned: `spectra tui` opens the Bubble Tea TUI against the local daemon.
`spectra tui work-mac` opens it against a remote daemon. Same UI either
way — the data layer doesn't care whether the daemon is on the same
machine.

## Authentication

The current TCP transport trusts the network path. If a peer can reach
the listener, it can call daemon RPC methods. Use loopback, SSH tunnels,
Tailscale ACLs, or firewall rules to limit access.

The planned tsnet mode will make Tailscale identity the default
authorization layer. The Tailscale ACL example for a personal tailnet:

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
spectra connect alice-laptop call jvm.list
# → see all JVMs running, GC stats, heap usage

spectra connect alice-laptop call jvm.thread_dump '{"pid":4012}' > intellij-threads.json
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

The local daemon and explicit TCP transport are implemented. Remaining
remote work:

1. Add tsnet integration so the daemon can join a tailnet without an
   externally managed listener.
2. Add typed `spectra connect <host> <subcommand>` adapters over the
   generic JSON-RPC `call` command.
3. Add host discovery and fan-out commands.
4. Add TUI support against local-or-remote daemon targets.
