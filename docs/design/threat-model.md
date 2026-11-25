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

## Helper launchd mutation

Status: implemented (step 1 of issue #600). See
[helper launchd mutation](helper-launchd-mutation.md). The VM integration
test is a manual pre-release step, run in a disposable Tart VM. The
Developer ID test certificate is imported into a keychain only inside that
VM, and the owner keeps the `.p12` on the host
([helper VM integration test](helper-vm-test.md)).

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
| Prepared plan differs from what executes (e.g. daemon loaded after `prepare`, adding a `bootout`) | Token binds the loaded state and the planned argv; any difference at execute refuses `target_changed` and nothing runs |
| Undoing an MDM administrator's decision | Daemons matched by a `com.apple.servicemanagement` managed-preferences rule or flagged managed in BTM are refused; an unreadable management source refuses `management_unknown`; the denylist covers MDM-installed agents these sources miss |
| Program replaced after verification, so launchd runs other code as root | The program and every directory above it must be root-owned, not world-writable and not group-writable (except an `admin`-group directory such as `/Applications`), with no symlink; refused `program_writable` otherwise, at prepare and again at execute. This also protects every later launch, since launchd runs whatever is at that path as root |
| Loss of the files that launch a daemon | No delete, move or edit operation exists; `disable` only adds a launchd override and boots the job out, so the plist, program and bundle are untouched |

`enable` removes the override `disable` added. When the daemon is not
loaded, `enable` also bootstraps (loads) it from the verified plist; an
on-demand daemon is then loaded but not running until triggered. If that
`bootstrap` fails, the daemon stays enabled but stopped. The result reports
"enabled but not loaded", and the daemon next starts at boot or by a
manual `sudo launchctl bootstrap`. `enable` does not restore anything
beyond the override and the loaded state. In particular, it does not
restore a process's runtime state from before `disable` stopped it.

Remaining risk: an admin user, or malware running as one, can disable a
verified third-party daemon that is not on the denylist and not detectably
managed, without the password prompt `sudo` would ask for. The denylist
covers security, MDM and backup agents by category, but it is only as
complete as its entries, and anyone with `sudo` can edit it. Management
detection sees only service-management rules and BTM flags. An admin can
still run `sudo launchctl` directly with the commands the refusal prints.
`enable` also loads a not-loaded daemon, so re-enabling a verified daemon
normally starts it at once if it runs at load. If its bootstrap fails, it stays enabled but
stopped until boot or a manual bootstrap. This is a deliberate, narrow
privilege step, and the audit log records every prepare and execution.

## Helper root process terminate (proposed)

Status: **Proposed — awaiting owner go-ahead** (step 2 of issue #600). No
code exists. See [helper root process terminate](helper-process-terminate.md).

New assets:

- Running root-owned processes of third-party software. Another process may
  depend on one being alive, and its unsaved state is lost when it exits.
- Confirmation tokens for this method, held in the helper's memory in a
  store separate from step 1's.
- Audit lines recording which process was signalled, by which uid.

| Threat | Mitigation |
|---|---|
| Caller signals an arbitrary or system process | The request carries a pid and an observed start time only. Real and effective uid must be 0. The executable (`proc_pidpath`, not argv) must sit in a verified third-party app directly under `/Applications`. `/System`, `/usr`, `/bin`, `/sbin`, `/Library/Apple`, `/Library/PrivilegedHelperTools`, Apple-signed code, system extensions, the helper and its parent are refused. Pids 0 and 1 are rejected as malformed |
| Pid reuse: the user confirms one process and another receives the signal | The pid and start time must match at prepare. The full microsecond start time, the ruid, the euid and the kernel's running CDHash are bound into the token. Identity is re-read after the `codesign` and `csops` calls, at execute, and once more after a kqueue `NOTE_EXIT` filter is attached; that kqueue is polled immediately before `kill(2)`. A two-syscall window remains (see Remaining risk) |
| Process path or name spoofed to look like a trusted vendor's | Identity comes from the kernel (`kinfo_proc`, `proc_pidpath`), not from argv or `comm`. `codesign` dynamic validation (`+<pid>`) must satisfy the bundle's team requirement. The kernel's running CDHash (`csops`) must equal the on-disk executable's |
| Binary replaced after launch, so the running code is not the verified file | The kernel's running CDHash is compared with the file's, and `codesign -dv +<pid>` is never trusted for it. Refused `code_mismatch` |
| Killing security, MDM or backup agents, or their workers | Step 1's root-owned denylist also matches bundle ids, signing identifiers and the label of the nearest ancestor system job, so a worker a denylisted daemon spawned is refused. Unreadable ancestry fails closed. `TeamIDs` ships empty, so team matching protects nothing until an administrator adds entries. Endpoint-security system extensions are outside the allowed paths. The main processes of launchd daemons are refused (`launchd_job`), and stopping a daemon goes through step 1's denylist-checked `disable` |
| A non-admin `_spectra` member probes root processes | Decode, rate limits and the admin check run before any read about the target. Pre-authorization errors carry no target facts |
| Replay, or prepare-one-execute-another | Single-use token bound to uid and every identity fact. 60 s expiry. All checks re-run |
| Signal escalation or flooding | SIGTERM only, sent once, never to a process group. 10 per 10 minutes per uid, counted separately from step 1. One helper mutation in flight, with the mutex shared with step 1 and held only through `kill(2)` |
| Non-admin `_spectra` member signals root processes | uid 0 or `admin` group membership required, checked per call |
| Argument injection | Integer pid and start time with strict decoding. `kill(2)` called directly. `codesign` and `launchctl` run with fixed argv and no shell |
| Secrets leaking through the audit log | The audit line holds identity and outcome only: never argv, environment, command line or token |

Remaining risk: an admin user, or malware running as one, can stop a
verified third-party root process that is not denylisted, without the
password prompt `sudo kill` would ask for. That user could run `sudo kill`
anyway. The effect is limited to one SIGTERM, which a process may handle
or ignore. Stopping the process can still interrupt work, such as an
update or a backup run that is not denylisted. A race spanning two system
calls remains between the kqueue poll and `kill(2)`. If the process exits
and its pid is reused inside that window, the signal reaches the new
process. XNU assigns pids from an increasing counter that any user can
drive to wrap. The public API has no audit-token signal, so this risk is
stated, not closed. Adopting the private `proc_signal_with_audittoken` is
left to the owner. If the process holds no root privilege, or
launchd relaunches it, the method achieves nothing lasting. A relaunch
needs no new privilege, so it is not a risk.
