# Threat model

> **Status: planned, evolving.** Captures Spectra's intended security
> posture as the daemon, helper, and remote-portal features land.

Spectra reads sensitive system state (TCC permissions, code-signing
metadata, process tables), optionally installs a root helper, and
exposes a network-reachable RPC surface over Tailscale. This is a
non-trivial attack surface and warrants a real threat-model document
even at the planning stage.

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
│  Holds: snapshots, blob cache, tsnet state  │
└─────────────────┬─────────────┬─────────────┘
                  │             │
                  │             │ tsnet (TLS, Tailscale-ACL'd)
                  │             ▼
                  │   ┌───────────────────────┐
                  │   │  Remote Spectra peers │
                  │   └───────────────────────┘
                  │
                  │ Unix socket (root:_spectra, 0660)
┌─────────────────▼───────────────────────────┐
│  Privileged helper                          │
│  Holds: nothing persistent                  │
│  Reads: system TCC.db, fs_usage, powermetrics│
└─────────────────────────────────────────────┘
```

Each arrow is an authenticated, scoped channel. Each box is
independently reviewable.

## In scope

Spectra defends against:

1. **Untrusted local users on the same machine.** A non-admin user
   should not be able to read another user's snapshots, query the
   privileged helper, or talk to the daemon's Unix socket.
2. **Untrusted tailnet peers.** A peer not allowed by Tailscale ACLs
   should be unable to reach the daemon's tsnet listener at all.
3. **Compromised peers within the tailnet.** A trusted peer turned
   adversarial should be unable to escalate beyond what the daemon's
   read-only RPC exposes — no arbitrary command execution, no
   arbitrary file reads, no helper passthrough without consent.
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
6. **Tailscale's threat model.** We rely on Tailscale's identity,
   ACLs, and transport security. If Tailscale is compromised, our
   tailnet exposure is compromised.

## Specific threats and mitigations

### T1: Local privilege escalation via helper

**Threat:** A non-admin user calls a helper method that performs an
operation as root, escalating privilege.

**Mitigation:**
- Helper Unix socket is `0660 root:_spectra`; only `_spectra` group
  members connect.
- Joining `_spectra` requires admin privilege (set during `install-helper`).
- Helper uses `getpeereid(2)` and logs the calling UID on every call.
- Helper has a hardcoded method allowlist; no method exposes
  arbitrary execution or file reads.
- Helper observes only — never mutates system state.

**Residual risk:** A local admin (already privileged) can use the
helper as intended. Acceptable; admins already had root.

### T2: Remote command execution via tsnet RPC

**Threat:** A tailnet peer crafts an RPC call that causes the
unprivileged daemon to execute attacker-controlled code or read
attacker-chosen files.

**Mitigation:**
- All RPC methods are statically defined; no method accepts a
  command string or a path with shell metacharacters.
- `Detect()` and friends accept paths only under bundle-validation
  (must be a real `.app`).
- SQL queries against TCC.db pre-validate the bundle ID against
  `[a-zA-Z0-9._-]+` allowlist (already implemented; see
  `validBundleID`).
- RPC handler dispatch is method-allowlist; unknown methods return
  errors rather than passing through.

**Residual risk:** A bug in input validation. Mitigated by
fuzz-testing the RPC layer (planned) and keeping the surface
intentionally narrow.

### T3: Heap-dump exfiltration

**Threat:** A peer requests a heap dump of a process containing
secrets, then reads the resulting `.hprof` from the blob cache.

**Mitigation:**
- `jvm.heapDump` requires explicit consent on the calling side and an
  audit log entry on the daemon.
- Heap dumps are stored under user-only filesystem permissions
  (`0600`).
- Heap dumps can be marked sensitive in the manifest; the daemon
  refuses to serve them over remote RPC unless explicitly allowed.

**Residual risk:** A peer with consent and ACL access to the daemon
*can* request and read heap dumps — that's the intended capability.
Users delegating diagnostic access to teammates should treat that
delegation as access to live process memory.

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
- tsnet state lives under `~/.spectra/tsnet/`, `0700`.
- A user who can read the file already has full read access to the
  user's home — same residual risk as theft of the user's
  Tailscale-on-Mac state (already a known property of Tailscale).

## Authentication and authorization

| Channel | Authentication | Authorization |
|---|---|---|
| CLI → local daemon (Unix socket) | filesystem perms (`0600`, user-owned) | full RPC surface |
| Daemon → privileged helper (Unix socket) | filesystem perms + `getpeereid` UID check | hardcoded method allowlist |
| Remote client → daemon (tsnet) | Tailscale identity | full read-only surface; state-changing methods require consent flag |
| Daemon → tailnet peers (tsnet) | Tailscale ACL | reciprocal — same posture as inbound |

## Logging

- Helper writes structured calls to `/var/log/spectra-helper.log`
  with rotation. Includes UID, method, args (excluding sensitive
  payloads), result.
- Daemon writes to `~/Library/Logs/Spectra/`. Tailnet RPC calls
  include the calling identity.
- Logs do **not** include heap-dump contents, JFR contents, or
  process command-line arguments (which may contain secrets).

## Hardening checklist

Things Spectra commits to before v1 release:

- [ ] All binaries signed with Developer ID, hardened runtime on.
- [ ] All distributed binaries notarized.
- [ ] Helper installs only via `SMAppService` or `SMJobBless`,
      both of which verify code-signing requirements.
- [ ] Fuzz-test the JSON-RPC dispatcher for malformed payloads.
- [ ] Static analysis (`gosec`) clean.
- [ ] No `os/exec` calls take user-supplied arguments without an
      allowlist.
- [ ] No `database/sql` queries take user-supplied strings without
      parameterization (where supported) or strict allowlist
      (where not, e.g. macOS `sqlite3` CLI).

## Reporting issues

Security issues should be reported privately to the maintainers. See
[../../SECURITY.md](../../SECURITY.md) for the current reporting
policy.

## See also

- [privileged-helper.md](privileged-helper.md) — protocol + boundary
- [distribution.md](distribution.md) — code-signing context
- [remote-portal.md](remote-portal.md) — tailnet exposure model
