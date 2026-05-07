# Threat model

This document tracks Spectra's current security posture and the hardening
still required before a v1 release.

Spectra reads sensitive system state (TCC permissions, code-signing
metadata, process tables), optionally installs a root helper, and can
expose a network-reachable JSON-RPC surface when `spectra serve --tcp`
is explicitly enabled. This is a non-trivial attack surface and warrants
a real threat-model document while the daemon and remote-portal features
continue to mature.

## Assets

What Spectra holds or mediates that's worth protecting:

| Asset | Sensitivity | Where |
|---|---|---|
| TCC permission grants | medium | per-user + system TCC.db (read-only) |
| Process command lines | medium-high | may contain secrets passed via argv |
| Heap dumps (.hprof) | very high | full process memory; secrets, keys, PII |
| JFR recordings | medium | sampled stack frames + GC events |
| Network endpoints embedded in binaries | low | already in the user's bundles |
| App entitlements + signing identities | low (public-by-bundle) | already in `codesign` |
| Snapshot history | medium | growing aggregate of the above |
| TCP RPC listener | medium-high | opt-in `spectra serve --tcp` |
| Tailnet auth identity | medium | embedded `tsnet` state for the daemon |

## Trust boundaries

```
┌──────────────── Local user ────────────────┐
│  CLI invocation                             │
│  TUI                                        │
│  GUI (future)                               │
└─────────────────────┬───────────────────────┘
                      │ Unix socket (0600, user-owned)
┌─────────────────────▼───────────────────────┐
│  Unprivileged daemon                        │
│  Holds: snapshots, blob cache               │
└─────────────────┬─────────────┬─────────────┘
                  │             │
                  │             │ optional TCP JSON-RPC
                  │             ▼
                  │   ┌───────────────────────┐
                  │   │  Remote Spectra peers │
                  │   └───────────────────────┘
                  │
                  │ Unix socket (0660 root:_spectra)
┌─────────────────▼───────────────────────────┐
│  Privileged helper                          │
│  Holds: nothing persistent                  │
│  Reads: system TCC.db, fs_usage, powermetrics│
└─────────────────────────────────────────────┘
```

Each local arrow is a scoped Unix-socket channel. The optional TCP arrow
is scoped only by the network path that exposes it. Each box is
independently reviewable.

## In scope

Spectra is designed to defend against:

1. **Untrusted local users on the same machine.** A non-admin user
   should not be able to read another user's snapshots, query the
   privileged helper, or talk to the daemon's Unix socket.
2. **Untrusted network peers.** TCP RPC is disabled by default and
   loopback-only unless `--allow-remote` is explicitly supplied. When
   exposed remotely, access must be constrained by SSH forwarding,
   Tailscale ACLs, or firewall rules. Embedded `tsnet` exposure relies
   on Tailscale identity and ACLs.
3. **Compromised peers on a trusted network path.** A reachable peer
   should be unable to escalate beyond what the daemon's RPC exposes —
   no arbitrary command execution, no arbitrary file reads, and no
   direct helper socket access.
4. **Replay of captured artifacts.** Heap dumps and JFR recordings are
   stored content-hashed and access-controlled by filesystem
   permissions on the cache directory.
5. **Tampering with the helper IPC.** A non-root local process should
   not be able to inject calls into the helper or impersonate the
   unprivileged daemon to it.

## Out of scope

Spectra does **not** defend against:

1. **A compromised root account.** If the attacker has root on the
   machine, every protection collapses. Spectra is not a hardening
   layer; it's a diagnostic tool.
2. **A malicious user who is the same user Spectra runs as.** The
   user trivially has access to everything Spectra has access to.
3. **Apple-system-level exfiltration.** macOS itself may log,
   transmit, or expose data Spectra reads (e.g. system diagnostic
   logs). Spectra is not a privacy boundary above the OS.
4. **Side-channel attacks** (timing, power, acoustic). Out of scope
   for a Go application that doesn't handle keys.
5. **Supply-chain compromise of the Spectra binary itself.** Mitigated
   by Developer ID code signing, notarization, and reproducible builds
   from a public Git repo, but a determined supply-chain adversary
   isn't in our threat model.
6. **The chosen network tunnel's threat model.** The current TCP
   transport relies on SSH, Tailscale, or firewall controls outside
   Spectra. If that path is exposed to an attacker, Spectra's TCP RPC
   listener is exposed too.

## Specific threats and mitigations

### T1: Local privilege escalation via helper

**Threat:** A non-admin user calls a helper method that performs an
operation as root, escalating privilege.

**Mitigation:**
- Helper Unix socket is `0660 root:_spectra`; the installer provisions
  `_spectra`, adds the invoking user, and the helper assigns socket group
  ownership at startup.
- Helper uses `getpeereid(2)` and passes the calling UID to method
  handlers.
- Helper has a hardcoded method allowlist; no method exposes
  arbitrary execution or file reads.
- Helper observes only — never mutates system state.

**Residual risk:** A local admin (already privileged) can use the
helper as intended. Acceptable; admins already had root.

### T2: Remote command execution via daemon RPC

**Threat:** A network peer crafts an RPC call that causes the
unprivileged daemon to execute attacker-controlled code or read
attacker-chosen files.

**Mitigation:**
- All RPC methods are statically defined; no method accepts a
  command string or a path with shell metacharacters.
- `Detect()` and friends accept paths only under bundle-validation
  (must be a real `.app`).
- SQL queries against TCC.db pre-validate the bundle ID against
  `[a-zA-Z0-9._-]+` allowlist in both the detector and privileged
  helper paths (see `internal/bundleid.Valid`).
- RPC handler dispatch is method-allowlist; unknown methods return
  errors rather than passing through.
- Subprocess use behind daemon/helper RPC is fixed per method: command
  names are not accepted from RPC params; PIDs and durations are numeric;
  `fs_usage` modes and TCC bundle IDs are allowlisted before reaching
  `os/exec`.
- Helper installation is the only root `sudo` path, and it rejects command
  names outside the fixed installer utility allowlist.
- TCP listen is opt-in. Non-loopback binds require `--allow-remote`,
  which prints an explicit warning that Spectra does not add its own
  authentication layer.

**Residual risk:** A bug in input validation. Mitigated by native Go fuzz
coverage for RPC request handling (`FuzzHandle`) and keeping the surface
intentionally narrow.

### T3: Heap-dump exfiltration

**Threat:** A peer requests a heap dump of a process containing
secrets, then reads the resulting `.hprof` from the blob cache.

**Mitigation:**
- Local CLI heap dumps default to `~/.spectra/`, created with user-only
  directory permissions.
- Daemon RPC requires `confirm_sensitive: true` before `jvm.heap_dump`
  or `jvm.jfr.dump` writes a sensitive artifact.
- Sensitive artifact manifests and remote-serving restrictions are
  planned hardening items.
- The privileged helper enforces a per-UID request limit so a compromised
  unprivileged daemon cannot hammer root-only commands indefinitely.

**Residual risk:** A peer with network access to daemon RPC can request
heap dumps. Users delegating diagnostic access to teammates should treat
that delegation as access to live process memory.

### T4: Snapshot leak across users

**Threat:** Snapshots taken by user A are read by user B.

**Mitigation:**
- Snapshots are stored in `~/.spectra/` (the running user's home).
- The daemon runs as the user; cannot read another user's home.
- The helper does not return snapshot data; it only returns the
  raw queries it's asked.

### T5: Tampering with cached artifacts

**Threat:** A local attacker swaps a heap dump under
`~/.cache/spectra/v1/hprof/...` for a malicious file, hoping to feed
attacker-controlled data into a downstream parser.

**Mitigation:**
- Cache filenames are content-addressed by SHA-256 of the file's own
  content. Mismatch fails verification.
- Cache directory is `0700` (user-only).

### T6: tsnet credential theft

**Threat:** A local attacker steals the daemon's tsnet state directory
and impersonates the daemon as that tailnet node.

**Mitigation:**
- tsnet state lives under `~/.spectra/tsnet/`, `0700`, unless the user
  explicitly supplies `--tsnet-state-dir`.
- A user who can read the file already has full read access to the
  user's home — same residual risk as theft of the user's
  Tailscale-on-Mac state (already a known property of Tailscale).

## Authentication and authorization

| Channel | Authentication | Authorization |
|---|---|---|
| CLI → local daemon (Unix socket) | filesystem perms (`0600`, user-owned) | full RPC surface |
| Daemon → privileged helper (Unix socket) | filesystem perms + `getpeereid` UID check | hardcoded method allowlist |
| Remote client → daemon (TCP, opt-in) | external network controls only | full RPC surface |
| Remote client → daemon (tsnet) | Tailscale identity | full read-only surface; state-changing methods require consent flag |

## Logging

- Helper stderr is written by launchd to `/var/log/spectra-helper.log`.
  The helper emits structured per-call JSON audit records there. The
  helper installer provisions a dedicated `newsyslog` rotation policy.
- Daemon lifecycle logs are written as JSONL to
  `~/Library/Logs/Spectra/daemon.jsonl` by default, with `0600`
  permissions. Foreground status lines still go to stderr for interactive
  use.
- Logs do **not** include heap-dump contents, JFR contents, or
  process command-line arguments (which may contain secrets).

## Hardening checklist

Things Spectra commits to before v1 release:

- [ ] All binaries signed with Developer ID, hardened runtime on.
- [ ] All distributed binaries notarized.
- [x] Helper install path provisions `_spectra` and sets socket group
      ownership, or moves to `SMAppService` / `SMJobBless` with
      code-signing requirement checks.
- [x] Fuzz-test the JSON-RPC dispatcher for malformed payloads.
- [x] Static analysis (`gosec`) runs in CI.
- [x] No `os/exec` calls take user-supplied arguments without an
      allowlist.
- [x] No `database/sql` queries take user-supplied strings without
      parameterization (where supported) or strict allowlist
      (where not, e.g. macOS `sqlite3` CLI).

## Reporting issues

Security issues should be reported privately to the maintainers. See
the [security policy](https://github.com/kaeawc/spectra/security/policy)
for the current reporting process.

## See also

- [privileged-helper.md](privileged-helper.md) — protocol + boundary
- [distribution.md](distribution.md) — code-signing context
- [remote-portal.md](remote-portal.md) — tailnet exposure model
- [Artifact lifecycle](../operations/artifacts.md) — retention, sharing,
  and deletion guidance for sensitive artifacts
