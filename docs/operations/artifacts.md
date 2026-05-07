# Artifact lifecycle

Spectra can collect diagnostic artifacts that are more sensitive than
ordinary inspection output. Heap dumps, JFR recordings, thread dumps,
process samples, and packet captures can contain credentials, customer
data, source paths, hostnames, private URLs, request payloads, or other
material that should not be shared casually.

This page defines the user-facing lifecycle for those artifacts: when
they are created, where they live, how long they stay around, how to
share them, and how to remove them.

## Artifact classes

| Class | Examples | Sensitivity | Default location |
|---|---|---|---|
| Heap dumps | `.hprof` from `spectra jvm heap-dump` | very high | `~/.spectra/` plus `hprof` cache entries |
| JFR recordings | `.jfr` from `spectra jvm jfr dump` / `stop` | medium-high | `~/.spectra/` plus `jfr` cache entries |
| Thread dumps | `jcmd Thread.print` output | medium-high | `threads` cache entries |
| Process samples | `sample <pid>` output | medium | `samples` cache entries |
| Packet captures | `.pcap` from `spectra network capture` | high | helper-owned capture path; future `netcap` cache entries |
| Detection cache | app detection results | low-medium | `detect` cache entries |

Heap dumps are the most sensitive class because they are full process
memory snapshots. Treat them as equivalent to access to the target
process. JFR files and thread dumps are smaller, but they may still
contain stack frames, class names, file paths, query text, system
properties, environment-derived values, and business identifiers.
Packet captures may include DNS names, IPs, headers, and payload bytes
depending on the protocol and capture filter.

## Creation

Artifacts are created only by explicit diagnostic commands or daemon RPC
methods. Routine app inspection does not create heap dumps, JFR
recordings, or packet captures.

```bash
spectra jvm thread-dump <pid>
spectra jvm heap-dump [--out <path>] <pid>
spectra jvm jfr start <pid> [--name spectra]
spectra jvm jfr dump <pid> [--name spectra] [--out <path>]
spectra jvm jfr stop <pid> [--name spectra] [--out <path>]
spectra sample [--no-cache] [--duration <s>] [--interval <ms>] <pid>
spectra network capture start ...
spectra network capture stop ...
```

For local CLI commands, the person running the command is responsible
for choosing whether the target process may be inspected. For daemon RPC
calls, sensitive write methods require an explicit consent parameter
such as `confirm_sensitive: true`; callers should surface that consent
in their own UI instead of hiding it behind a default.

## Storage

Spectra uses two storage roots:

- `~/.spectra/` for user-visible output paths such as default heap-dump
  and JFR destinations, plus the artifact manifest at
  `~/.spectra/artifacts.json`.
- `~/.cache/spectra/v1/` for content-addressed cache entries managed by
  `spectra cache`.

Both roots are per-user. Spectra does not write shared system-wide
artifact stores for normal CLI or daemon operation. The privileged
helper may create root-only packet-capture files while a capture is
active; those are bounded by helper validation and should be treated as
high-sensitivity artifacts once returned to the user.

The cache layout and implementation details are documented in
[Caching](caching.md). The important operational point is that cache
entries are local files. Filesystem permissions are the primary access
control, and a user who can read the Spectra user's home can read that
user's artifacts.

The manifest records metadata only: artifact kind, sensitivity, source,
command, path, cache kind, PID, size when known, creation time, and small
capture-specific fields such as a JFR recording name or packet-capture
handle. It does not copy artifact contents into the manifest.

## Retention

There is no automatic artifact expiration in the current v1 cache.
Artifacts remain until the user removes them or clears the relevant
cache kind.

Use cache stats before and after long investigations:

```bash
spectra cache stats
spectra cache clear --kind hprof
spectra cache clear --kind jfr
spectra cache clear --kind threads
spectra cache clear --kind samples
spectra cache clear --kind netcap
spectra cache clear
```

Manually chosen output files under `~/.spectra/` or another `--out`
path are not removed by `spectra cache clear`; delete those files
directly when the investigation is done.

Recommended retention defaults:

| Artifact | Recommended maximum |
|---|---|
| Heap dumps | delete as soon as analysis is complete |
| Packet captures | delete as soon as analysis is complete |
| JFR recordings | keep only for the active incident or performance investigation |
| Thread dumps / samples | keep only while the corresponding issue is open |
| Detection cache | safe to keep; clear when troubleshooting cache behavior |

## Sharing

Share artifacts only through channels approved for the data they may
contain. Do not attach heap dumps, packet captures, or JFR files to
public issues, public pull requests, chat rooms, or unencrypted email.

Before sharing, prefer a derived summary over the raw artifact:

- `spectra jvm heap-histogram <pid>` instead of a full heap dump.
- `spectra jvm jfr summary <recording.jfr>` instead of the full JFR.
- `spectra network capture summarize <pcap-path>` instead of the raw
  PCAP.
- A short excerpt from a thread dump instead of the full dump.

If a raw artifact is necessary, record:

- who requested it,
- which process or interface was captured,
- when it was captured,
- where it was sent,
- when it should be deleted.

The future remote portal should expose this as an artifact manifest
rather than relying on ad hoc notes. The daemon and CLI already write
local manifest records; portal UI and policy controls should build on
that record instead of inventing a second lifecycle model.

## Deletion

For cached artifacts, use `spectra cache clear --kind <kind>` when the
artifact class is no longer needed. For explicit output files, delete
the path printed by the command.

Suggested cleanup after a sensitive JVM investigation:

```bash
spectra cache clear --kind hprof
spectra cache clear --kind jfr
spectra cache clear --kind threads
rm -f ~/.spectra/*.hprof ~/.spectra/*.jfr
spectra cache stats
```

Suggested cleanup after packet capture work:

```bash
spectra cache clear --kind netcap
rm -f /var/tmp/spectra-netcap/*/*.pcap
spectra cache stats
```

Only remove packet captures from helper-managed paths that belong to
your investigation. On shared systems, coordinate with the machine owner
before removing files under `/var/tmp/spectra-netcap/`.

## Redaction limits

Spectra summaries are designed to avoid retaining packet payload bodies
and full artifact contents, but summaries are not guaranteed to be
anonymous. Hostnames, URLs, class names, bundle IDs, process names, stack
frames, and file paths can all identify systems or users.

Do not assume that a derived summary is safe for public posting without
review.

## Remote access

Remote debugging over TCP or Tailscale delegates meaningful diagnostic
power to the peer. A peer who can request heap dumps, JFR dumps, process
samples, or packet captures may obtain sensitive data from the machine.

Remote clients should:

- require explicit consent for each high-sensitivity artifact,
- show the destination path and estimated scope before capture,
- avoid serving raw artifacts by default,
- prefer summaries and bounded excerpts,
- record artifact creation in the daemon audit log or future artifact
  manifest.

This complements the security posture in the
[Threat model](../design/threat-model.md).

## Incident handling

If a sensitive artifact was shared with the wrong audience:

1. Remove the artifact from the shared location.
2. Clear the relevant Spectra cache kind.
3. Delete explicit output files under `~/.spectra/` or the supplied
   `--out` path.
4. Rotate credentials that may have been present in process memory,
   command output, network traffic, or system properties.
5. Preserve only the minimum metadata needed to investigate the leak:
   timestamp, command, process, destination, and recipient list.

Heap-dump exposure should be treated as possible secret exposure for the
target process.

## See also

- [Caching](caching.md) — cache layout, stats, and clear commands.
- [JVM inspection](../inspection/jvm.md) — JVM artifact commands.
- [Live data sources](../inspection/live-data-sources.md) — command
  inventory and capture cost.
- [Threat model](../design/threat-model.md) — sensitive assets and
  remote-debugging risk.
