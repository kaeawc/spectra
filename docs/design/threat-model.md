# Threat model

Spectra is a local diagnostic tool. It reads system state such as process
tables, application metadata, code-signing information, and optionally a
privileged helper's narrowly scoped telemetry.

## Trust boundaries

```text
Local CLI ── local collectors ── local files and system tools
     │
     ├── per-user daemon over a local Unix socket
     │
     └── optional privileged helper over a local Unix socket
```

The core binary opens no network listeners, includes no remote transport, and
cannot install or update software on another machine. Cross-machine trust,
transport, provisioning, and auditing are responsibilities of Spectra Remote.
The per-user daemon restricts its socket directory and socket permissions,
authenticates each peer UID using the kernel, and rejects other users before
reading requests. A nonblocking lock prevents two servers owning the socket;
the daemon stops if its socket is removed or replaced.

## Primary protections

- Keep diagnostic artifacts and snapshots owner-readable only.
- Scope privileged helper operations to a fixed allowlist and authenticated
  local callers.
- Treat heap dumps, recordings, process command lines, and permission data as
  sensitive local information.
- Preserve an explicit user boundary around destructive diagnostics and helper
  installation.

## Proposed: helper launchd mutation

Status: proposed, not implemented. See
[helper launchd mutation](helper-launchd-mutation.md).

New assets:

- The system launchd domain's enabled/disabled overrides and its loaded
  jobs, including third-party daemons other software depends on.
- Confirmation tokens held in the helper's memory.
- Audit lines recording which daemon was changed, by which uid.

| Threat | Mitigation |
|---|---|
| Caller names an arbitrary path or Apple job | Request carries a label only; the plist path is fixed to `/Library/LaunchDaemons/<label>.plist`; `com.apple.*`, `/System` and Apple-signed code are refused |
| Symlink or swapped file redirects the change | `lstat` + `O_NOFOLLOW` + inode match; programs checked with `EvalSymlinks`; plist must be root-owned and not writable by others |
| Launchd runs a different job by the same label | Duplicate-label scan and `launchctl print system/<label>` path and program cross-check |
| Spoofed ownership of a daemon | Program must sit in a `/Applications` bundle (or `PrivilegedHelperTools` tied by bundle id); both pass strict `codesign` with the same verified team id |
| Replay or prepare-one, execute-another | Single-use, uid- and action-bound token, 60 s expiry, every check re-run and the bound facts compared |
| Argument injection into `launchctl` | Label charset validation; fixed argv to `/bin/launchctl`, no shell |
| Non-admin `_spectra` member disables system daemons | Mutation requires uid 0 or `admin` group membership, checked per call |
| Disabling security software or the helper itself | Helper's own label refused; every change audited; root-owned denylist (`/Library/Application Support/Spectra/helper-denylist.plist`: endpoint security, MDM, backup agents) always refused, and all mutations refused if the file exists but is not root-owned `0644` or stricter |
| Toggling a daemon to cause disruption | 10 mutations per 10 minutes per uid, one mutation in flight |
| Loss of the files that launch a daemon | No delete, move or edit operation exists; `enable` undoes `disable` |

Remaining risk: an admin user, or malware running as one, can disable a
verified third-party daemon that is not on the denylist without the
password prompt `sudo` would ask for. The denylist covers security, MDM and
backup agents by category, but it is only as complete as its entries, and
anyone with `sudo` can edit it; an admin can still run `sudo launchctl`
directly with the commands the refusal prints. `enable` also loads the
daemon, so re-enabling a verified daemon starts it at once. This is a
deliberate, narrow privilege step; the audit log records it, and `enable`
reverses `disable`.
