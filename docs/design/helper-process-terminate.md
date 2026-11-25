# Helper root process terminate

!!! warning "Status: Proposed — awaiting owner go-ahead"
    This is the design for **step 2** of the helper mutation plan in issue
    #600: "signal a root-owned process with the same pid + start-time
    identity check the desktop uses". It is docs only. No helper code exists
    for it, and none will be written until the owner approves this page.
    The independent security review of this design and how each finding
    was resolved are under [security review](#security-review). Decisions
    the owner still has to make are under
    [open questions for the owner](#open-questions-for-the-owner).

The privileged helper would gain one more narrow ability: send **SIGTERM**,
once, to one **running root-owned process** whose executable belongs to a
**verified third-party bundle**. That process must be the exact process the
user saw: the same pid, the same start time, and the same running code.

Today a user cannot stop such a process without `sudo kill`. `spectra
uninstall apply --quit` re-checks each process's pid and start time, then
runs `kill -TERM` as the user (`internal/uninstall/apply.go`). For a
root-owned process that fails with `EPERM` and the apply aborts. No
`sudo kill` command is suggested. This method gives that path a narrow,
audited, admin-only way through the helper.

As in [step 1](helper-launchd-mutation.md), the helper never trusts the
caller's description of the target. The request carries a pid and the
start time the caller observed, and nothing else. The helper reads the
process's credentials, start time, executable and running code signature
from the kernel and `codesign`, and refuses anything that does not match.

## Methods

Two methods. One does not mutate; the other does one thing.

| Method | Mutates | Purpose |
|---|---|---|
| `helper.process.root.prepare` | no | Re-derive and verify the process, return what would happen and a single-use token |
| `helper.process.root.terminate` | yes | Send `SIGTERM` to the prepared process, once, then report whether it exited |

There is **no SIGKILL**, no other signal, and no escalation. A process
that ignores or handles SIGTERM keeps running, and the result says so (see
[open question 1](#open-questions-for-the-owner)).

Unlike step 1's `disable`/`enable` pair, this step has no compensating
operation: a delivered signal cannot be undone. A process that something
relaunches comes back on its own. The helper refuses the case where
launchd would do that, because terminating such a process does nothing
lasting. See check 8.

### Request and response

```json
// helper.process.root.prepare
{"pid": 4182, "start_time": 1795595200}

// result
{
  "pid": 4182,
  "refused": false,
  "token": "b64url-128-bit",
  "expires_at": "2026-11-25T17:00:30Z",
  "target": {
    "pid": 4182,
    "start_time": 1795595200,
    "start_time_precise": "2026-11-25T08:26:40.123456Z",
    "ppid": 1,
    "uid": 0,
    "ruid": 0,
    "executable": "/Applications/Example.app/Contents/Library/LaunchServices/updater",
    "bundle_path": "/Applications/Example.app",
    "bundle_id": "com.example.app",
    "signing_id": "com.example.updater",
    "team_id": "ABCDE12345",
    "cdhash": "…"
  },
  "planned": {"signal": "SIGTERM", "wait_seconds": 10}
}

// helper.process.root.terminate
{"token": "b64url-128-bit"}

// result
{
  "pid": 4182, "via": "spectra-helper", "refused": false,
  "target": {…},
  "signal": "SIGTERM", "delivered": true, "exited": true,
  "waited_ms": 840, "outcome": "ok"
}
```

`start_time` is whole Unix seconds in the request and in `target`. That
is what the CLI and the uninstall plan already record (`ps -o lstart`,
one-second precision, `internal/process`). The helper compares it with the
kernel's start time truncated to seconds. It then binds the kernel's full
microsecond start time (`start_time_precise`) into the token.

A refusal is a normal result with `refused: true`, a `reason` code, a
`message`, and `suggested_commands` (`sudo kill -TERM <pid>`), as in step
1. JSON-RPC errors are kept for malformed params (`-32602`), rate limits
(`-32001`) and internal faults. `terminate` takes only the token. The pid
and the identity come from the token's stored binding, so a client cannot
prepare one process and signal another.

## What the helper re-derives

**Order.** Both methods run the same order. First they decode the params
strictly, then apply the rate limits, then the admin check. Only then
does the helper read anything about the target pid. A `not_admin`
refusal, a malformed request or a rate-limit error carries no fact about
the target: not whether the pid exists, nor its owner, team, denylist
match or launchd label. A non-admin `_spectra` member gets no oracle for
root processes, and cannot make root run `codesign` (security review
SR-4). This is the order step 1's `prepare` and `execute` already use.

Every numbered check runs in `prepare` and again, from scratch, in
`terminate`. If any check fails, the helper refuses and sends no signal.

0. **Denylist.** The root-owned [denylist](helper-launchd-mutation.md#denylist)
   from step 1 is loaded and integrity-checked as in step 1. If it is
   invalid, every mutation is refused (`denylist_invalid`). Matching rules
   for processes are in check 7.
1. **Pid.** An integer with `1 < pid <= 99999`. 99999 is the XNU `PID_MAX`.
   `start_time` must be positive. Anything else, including pid 0 and pid 1
   (`launchd`), is malformed params (`-32602`). The helper's own pid and its
   parent's pid are refused `self`.
2. **Identity.** The helper reads `sysctl kern.proc.pid.<pid>`
   (`kinfo_proc`, as `internal/hostwatch` already does). This gives the
   start time to the microsecond (`p_starttime`), the real and effective
   uid, the ppid, and the process state.
   - No such process: refused `gone`.
   - A zombie (`SZOMB`): refused `gone`.
   - A start time whose seconds differ from the request: refused
     `identity_mismatch`. Some other process now has that pid.

   This is the uninstall rule (`sameProcess`), with the kernel's value in
   place of `ps`.
3. **Ownership.** The real uid **and** the effective uid must both be 0.
   - A non-root caller's own process (real uid equal to the caller's, which
     is not 0): refused `not_root`, with `kill -TERM <pid>`. The caller
     needs no helper for it. A root caller has no such case. Every process
     it targets is root's, and it always goes through the helper.
   - Another non-root user's process: refused `other_user`. That is out of
     scope.
   - A setuid-root process started by a user (euid 0, ruid not 0): refused
     `other_user`. Its real owner is that user.
4. **Executable.** The helper reads `proc_pidpath(pid)`. This is the vnode
   path of the running image. It is not argv, which the process controls.
   - The path must be absolute and clean, with
     `EvalSymlinks(path) == path`. It must be a regular file.
   - It must not be under `/System`, `/usr`, `/bin`, `/sbin` or
     `/Library/Apple`, compared case-folded (step 1's `systemPrefixes`).
   - It must be inside an `.app` directly under `/Applications`. The owning
     bundle is the outermost `.app` on the path, as in step 1 check 6. The
     innermost `.app` on the path is also recorded, if it differs, for the
     denylist (check 7).
   - Prefixes are compared case-sensitively, as step 1's `appProgram`
     does. If `proc_pidpath` returns another casing, for example
     `/applications/…` on case-insensitive APFS, the result is a refusal,
     never a looser match.

   Anything else is refused `not_in_bundle`. That covers system
   extensions under `/Library/SystemExtensions`, Homebrew services,
   anything in a user's home, and `/Library/PrivilegedHelperTools`.
   Step 1 links a tool there to its app through the job plist's
   `AssociatedBundleIdentifiers`, and a bare process has no plist, so no
   owner can be derived for it. Those tools are out of scope for this step
   (security review SR-3; [open question 6](#open-questions-for-the-owner)).
   The helper's own binary lives there, so this rule also refuses it.

   No ancestor-ownership rule applies, unlike step 1. Step 1 needed one
   because launchd would *run* the file as root in the future. Here, nothing
   new runs. Check 5 ties the running image to the verified file.
5. **Running code.** This check runs after check 6, which supplies the
   team. It uses two sources, because neither is enough alone.
   - **Dynamic validation:** `/usr/bin/codesign --verify
     -R='anchor apple generic and certificate leaf[subject.OU] = "<TEAM>"'
     +<pid>` must pass. With a pid argument, codesign validates the
     *running* process: the kernel's code-signing state, including
     `CS_VALID`, plus the requirement. The team comes from check 6.
     `codesign --verify -R='anchor apple' +<pid>` must fail with exit 3,
     as in step 1.
   - **Kernel CDHash:** the running image's code directory hash and status
     come straight from the kernel with `csops(pid, CS_OPS_CDHASH)` and
     `csops(pid, CS_OPS_STATUS)`. The status must have `CS_VALID` set and
     `CS_PLATFORM_BINARY` clear. The CDHash must equal the on-disk
     executable's `CDHash` (`codesign -dv --verbose=4 <path>`).
     `codesign -dv +<pid>` is **not** used for this. Display mode on a pid
     resolves the process to its executable path and may report the
     *file's* signature. After an update replaced the file, that would
     compare the new file with itself (security review SR-1).
   - A mismatch refuses `code_mismatch`. That covers a binary replaced
     after launch, the usual atomic-rename update, and a process whose
     signature went invalid.
   - `csops` is exported by libSystem but declared only in a private
     header (`<sys/codesign.h>`). The implementation PR declares the
     prototype itself and calls it through cgo, as `internal/process`
     already does for `proc_pidpath`. Any failure of `csops` refuses
     `code_mismatch`; the helper never falls back to `codesign -dv +<pid>`.

   A pid lookup can race with pid reuse. To cover this, check 2 runs again
   **after** the `codesign` and `csops` calls, and its start time and uids
   must not have changed. Otherwise the result is `identity_mismatch`.
6. **Bundle and team id.** The owning bundle gets the same checks as step
   1 check 7: strict `codesign --verify` of the bundle and the executable
   file, the team id read only after that passes, ad-hoc signatures and
   missing team ids refused, Apple-signed code refused (`apple_signed`),
   and the detritus retry.
7. **Denylist match.** The process is refused `denylisted` when any of
   these matches:
   - its team id is in `TeamIDs`;
   - an id matches a `Labels` entry (case-folded, exact, or by
     trailing-`*` prefix). The ids checked are the outermost and innermost
     bundle ids, the running code's signing identifier, and the label of
     the **ancestor launchd job** (check 8).

   The ancestor job is what protects a worker that a denylisted daemon
   spawned. A worker's signing identifier is often a bare file name such as
   `falcond`, which no reverse-DNS entry can match. Its parent job's label,
   such as `com.crowdstrike.falcond`, does match (security review SR-2).

   The default entries name endpoint security, MDM and backup vendors by
   reverse-DNS prefix. Those prefixes match the vendors' launchd labels,
   bundle ids and signing identifiers, so step 1's file protects their
   processes with no new keys (see
   [open question 3](#open-questions-for-the-owner)). `TeamIDs` ships
   **empty**, as in step 1, so team matching protects nothing until an
   administrator adds entries. The built-in entries also refuse the
   helper's own code: signing identifier `dev.spectra.helper`.
8. **Launchd job and ancestor job.** The helper parses the `services`
   block of `launchctl print system`, which lists every loaded system
   job's label and pid.
   - If the pid is a job's main process, the result is `launchd_job`. The
     message names the label and suggests `spectra startup disable <label>
     --scope system` (step 1). Launchd owns that process's lifecycle,
     whether it is `KeepAlive` or on demand, and step 1's denylist-checked
     `disable` is the supported way to stop a daemon.
   - Otherwise the helper walks the ppid chain upward through `kinfo_proc`,
     at most 64 steps, to the nearest ancestor that is a system job's main
     pid. That job's label feeds the denylist in check 7. If the chain
     reaches pid 1 without a job, there is no ancestor job, as for a
     process reparented to launchd. If any step cannot be read, or the
     chain exceeds 64 steps, the result is `ancestry_unknown` (fail closed).
   - If the `services` block cannot be found or parsed, the result is
     `launchd_unknown`. `launchctl print` output is not a stable API, so the
     implementation PR pins it with fixtures for each supported macOS
     version, as step 1 did for `sfltool`.

   A process the job *spawned*, for example an updater child, is not the
   job's main pid. It is in scope unless its ancestor job is denylisted.
   Such a child may be relaunched by its own parent. That is the parent's
   choice, needs no privilege, and is reported by `exited`.
9. **Bound facts.** The running image is bound by its kernel CDHash from
   check 5. The path's device, inode, size and mtime from `lstat` are bound
   too, so a file changed between `prepare` and `terminate` refuses
   `target_changed`. Check 5's comparison is what ties the running process
   to that file.

Only after checks 0 to 9 pass does the helper:

1. register a kqueue `EVFILT_PROC` / `NOTE_EXIT` filter on the pid. That
   attaches to the process itself, not to the number;
2. re-read identity (check 2) one last time;
3. poll that kqueue with a zero timeout. If the process has exited since
   step 1, the result is `gone`;
4. call `kill(pid, SIGTERM)` directly, as a syscall (`syscall.Kill`). It
   never runs `/bin/kill`, a shell, or `kill` from `PATH`;
5. release the mutation mutex, then wait on the same kqueue for up to
   `wait_seconds` (10) for `NOTE_EXIT`, and write the audit line.

The helper sends exactly one signal. It never repeats the signal, never
escalates, and never signals the process group.

## Confirmation token and replay protection

The token mechanism is step 1's
([details](helper-launchd-mutation.md#confirmation-token-and-replay-protection)),
in its own store, so a launchd token is never valid here:

- 128 random bits, base64url-encoded, kept in memory as SHA-256. It
  expires after 60 seconds. Each uid may hold at most four live tokens.
  `terminate` deletes the token before doing anything else. That includes
  the rate limit and the mutex, so a `-32001` busy or rate-limited
  `terminate` also uses up its token, as in step 1. The CLI then says to
  run the command again.
- The binding holds:
  - the caller's uid;
  - the pid, the full microsecond start time, the ruid and the euid;
  - the executable path, with its device, inode, size and mtime;
  - the on-disk `CDHash` and the kernel's running `CDHash`;
  - the ancestor job's label, or none;
  - the bundle path, bundle id, signing identifier and team id;
  - the planned signal and wait.
- At `terminate` every check re-runs. Any difference in a bound fact
  refuses `target_changed`, or `gone` when the process has exited. The
  helper never signals a process the user did not confirm.

Remaining race: between the zero-timeout kqueue poll and `kill(2)`, the
verified process could exit, and the kernel could give its pid to a new
process. The window is two adjacent system calls. XNU hands out pids from
an increasing counter. It wraps after `PID_MAX` and skips pids still in
use. After a wrap, reuse needs only the counter to reach this pid again,
not a full cycle, and any local user can force a wrap with a burst of
forks. The race is therefore stated here, not claimed closed (security
review SR-5).

The public API has no handle for signalling one specific process. The
implementation PR should evaluate `proc_signal_with_audittoken` (libproc
SPI). It signals a process named by audit token, which carries a
per-process version and fails on a reused pid, so it would close the race.
Adopting it needs the owner's agreement, because it is private
([open question 7](#open-questions-for-the-owner)).

## Caller authentication

Unchanged from step 1:

- peer uid from `peercred.PeerUID`;
- the caller must be uid 0 or in the `admin` group (`dsmemberutil`),
  checked on every call;
- only the local CLI calls these methods; they are not exposed over the
  per-user daemon, MCP or Spectra Remote.

## Rate limits

- The existing 120 requests per minute per uid still applies.
- `prepare`: 30 per minute per uid.
- `terminate`: 10 per 10 minutes per uid. Every execute counts, refused or
  not. The counter is separate from step 1's `disable`/`enable` counter,
  so an admin can make at most 20 helper mutations of both kinds per 10
  minutes.
- Step 1's mutation mutex is **shared**, so only one helper mutation of
  either kind runs at a time. A second one gets `-32001`. `terminate`
  holds the mutex through the checks and `kill(2)` only, not through the
  10 s wait, so a slow-exiting process does not block step 1.

## Audit log

The audit log uses one line per `prepare` and per `terminate`, in step 1's
format. The events are `process.root.prepare` and `process.root.terminate`.

```json
{"time":"2026-11-25T17:00:12Z","uid":501,"event":"process.root.terminate",
 "token_hash":"3f9a0c1e","pid":4182,"start_time":"2026-11-25T08:26:40.123456Z",
 "executable":"/Applications/Example.app/Contents/Library/LaunchServices/updater",
 "bundle_id":"com.example.app","signing_id":"com.example.updater",
 "team_id":"ABCDE12345","cdhash":"…","signal":"SIGTERM",
 "delivered":true,"exited":true,"outcome":"ok"}
```

The log never contains a token, argv, the environment, or the process's
command line. The command line can hold secrets, and the threat model
treats it as sensitive.

## CLI

```text
spectra process terminate <pid> [--yes] [--json]
```

1. The CLI reads the process from its own process table (`internal/process`,
   which uses `ps lstart`) to get the start time. If it cannot see the pid,
   it exits 3.
2. If the caller is not root and owns the process, the CLI signals it
   directly, without the helper, after the same pid and start-time
   re-check that uninstall uses. A root caller always goes through the
   helper.
3. For a root-owned process, the CLI calls `prepare`. If the helper is
   missing, or predates the method (`-32601`), the CLI prints `sudo kill
   -TERM <pid>` and an install or update hint, then exits 3. A refusal
   prints the reason and the manual command, then exits 3.
4. The CLI prints the verified target: pid, start time, executable, bundle,
   team and signing identifier. It asks `Send SIGTERM to <exe> (pid N, root)?
   [y/N]` on a TTY. Without a TTY, `--yes` is required.
5. It calls `terminate` and prints `exited` or `still running after 10s`.

Exit codes follow step 1: 0 means sent and exited, 1 means an error or
still running, 2 is a usage error, 3 is refused. Running the CLI as root
still goes through the helper, so there is one audited path.

Wiring `uninstall apply --quit` to route root-owned processes through this
method is a separate PR ([open question 4](#open-questions-for-the-owner)).

## Failure modes

| Situation | Reason | What the user sees |
|---|---|---|
| Helper missing, or older than this method | — | `sudo kill -TERM <pid>` plus install or update hint; exit 3 |
| Caller not admin | `not_admin` | "signalling root processes needs an administrator account"; exit 3 |
| Pid gone, a zombie, or exited before execute | `gone` | "process N is no longer running"; exit 3 |
| Start time differs | `identity_mismatch` | Both start times; exit 3 |
| Caller's own process | `not_root` | `kill -TERM <pid>`; exit 3 (the CLI handles this before calling the helper) |
| Another user's process, or setuid from a user | `other_user` | uid and ruid; exit 3 |
| Pid is the helper or its parent | `self` | exit 3 |
| Executable outside a verified bundle, or in a system path | `not_in_bundle`, `system_path`, `symlink` | The path; exit 3 |
| Running code differs from the file, or is invalid | `code_mismatch` | Both CDHashes; exit 3 |
| Unsigned, wrong team, Apple-signed | `unsigned`, `team_mismatch`, `apple_signed` | Team and identifiers; exit 3 |
| Denylisted | `denylisted`, `denylist_invalid` | The matched entry and its kind; exit 3 |
| Main pid of a loaded system launchd job | `launchd_job` | The label and `spectra startup disable <label> --scope system`; exit 3 |
| Ancestry cannot be read, or `launchctl print system` cannot be parsed | `ancestry_unknown`, `launchd_unknown` | What could not be read; exit 3 |
| Token unknown, expired, another uid, or a target fact changed | `token_invalid`, `token_expired`, `target_changed` | "run the command again"; exit 3 |
| Rate limited, or another mutation in flight | JSON-RPC `-32001` | "try again in N minutes"; exit 1 |
| `kill(2)` fails (`ESRCH` just after the last check) | `outcome: error` | "the process exited before the signal"; exit 1 |
| Signal delivered, process still running after 10 s | `outcome: still_running` | "still running; it may be handling SIGTERM"; exit 1 |

## Testing (for the implementation PR)

- **Unit tests, no root, any OS.** These use an injected process-info
  source (`kinfo_proc`, `proc_pidpath`), an injected signaller, injected
  filesystem, clock and random source, and a `CmdRunner` that scripts
  `codesign` (file and `+pid`) and `launchctl print system`.
  - One table test per refusal reason. Each asserts that no signal was
    sent.
  - Pid reuse between prepare and execute, and between the codesign calls
    and the last identity re-read.
  - A zombie process.
  - A running CDHash that differs from the file's, and a failing `csops`.
  - A child of a denylisted job, refused by the ancestor job's label.
  - An unreadable link in the ppid chain.
  - A `not_admin` caller, with no process-info, `csops` or runner calls.
  - Token reuse, expiry, wrong uid and eviction.
  - The shared mutation mutex with step 1.
- **Audit tests.** Prepare and execute lines pair by `token_hash`. The
  token and the command line never appear.
- **Fuzz test.** The params decoder.
- **CLI tests.** Helper absent, refusal, no TTY without `--yes`, exited,
  and still running, all against a fake helper client.
- **VM integration.** Extends the `helper_vm` suite and runs in the same
  disposable Tart VM ([helper VM integration test](helper-vm-test.md)).
  - The test starts the signed fixture executable as root, as a child of
    the test, not as a launchd job.
  - Then: prepare, terminate, and check the process exited.
  - A fixture that ignores SIGTERM reports `still_running` and gets no
    SIGKILL.
  - The fixture binary is replaced on disk, by atomic rename, while it
    runs, and the result is `code_mismatch`.
  - Refusals: the fixture's daemon as a loaded launchd job
    (`launchd_job`); an Apple process (`apple_signed` or `system_path`);
    the caller's own process (`not_root`); a replayed token.

## Out of scope

- SIGKILL, SIGHUP, SIGSTOP and any signal other than SIGTERM; signalling a
  process group; repeated signals.
- Processes of non-root users other than the caller, and setuid processes
  a user started.
- Processes outside a verified third-party app in `/Applications`: system
  extensions, `/Library/PrivilegedHelperTools` tools, Homebrew services,
  and anything Apple-signed. A script run by an interpreter is refused
  because its executable is the interpreter. If a verified process loaded
  other code (a plug-in or an injected library), the method still verifies
  only the host executable. That is acceptable for a method that can only
  send SIGTERM.
- The main process of a loaded launchd job. Use step 1's `disable`.
- Batch or "all processes of a bundle" requests. The CLI may loop, but each
  process needs its own prepare, token and confirmation.
- Exposure to the per-user daemon, MCP or Remote.
- Linux: the methods return `hostos.Unsupported` off Darwin.

## Security review

The review was run independently on 2026-11-25, by a separate reviewer
that had not written this page. It covered this page and the threat-model
section against `internal/helper` (step 1) and the ground rules in #600.
Its findings, and how each was resolved, are below.

No high-severity finding. Every finding changed this page.

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| SR-1 | medium | `codesign -dv +<pid>` may report the on-disk file's signature, not the running image's. After an atomic-rename update, the "running CDHash equals file CDHash" check would compare the new file with itself. | Check 5 takes the running CDHash and `CS_VALID` from the kernel (`csops`), and keeps `codesign --verify +<pid>` for dynamic validation. It never uses `-dv +<pid>`. A VM test replaces the binary while the fixture runs and expects `code_mismatch`. |
| SR-2 | medium | Workers spawned by a denylisted daemon are unprotected. Their signing ids are often bare names that no reverse-DNS entry matches, and default `TeamIDs` is empty. | Check 8 walks the ppid chain to the nearest system job, and check 7 matches that job's label. The chain fails closed (`ancestry_unknown`). The innermost bundle id is matched too. The threat model states that `TeamIDs` ships empty. |
| SR-3 | medium | Owner lookup for `/Library/PrivilegedHelperTools` was undefined: step 1 derives it from a job plist, which a bare process lacks. | Dropped from this step. Only executables in `/Applications/*.app` are in scope. [Open question 6](#open-questions-for-the-owner) asks whether to add these tools later. |
| SR-4 | medium | The admin check's position was not stated. If target reads ran first, a non-admin `_spectra` member would get an oracle for root-process facts and a way to make root run `codesign`. | The order is now explicit: decode, rate limit, `authorize`, then target reads. Pre-authorization errors carry no target facts. A test asserts that `not_admin` makes no process-info or runner calls. |
| SR-5 | low | The pid-reuse argument was overstated: after a wrap, reuse needs only the counter to reach the pid, and any user can force a wrap. Better primitives exist. | The race text is corrected. A kqueue `NOTE_EXIT` filter is registered before the last identity read and polled immediately before `kill(2)`. `proc_signal_with_audittoken` is left to the owner as [open question 7](#open-questions-for-the-owner). |
| SR-6 | low | Contradiction for a root caller: "own process → `not_root`" against "root always uses the helper". | `not_root` applies only to a non-root caller's own process. A root caller always goes through the helper. |
| SR-7 | low | The shared mutex was held through slow `codesign` runs and the 10 s wait. A busy `-32001` burns the token. The rate-limit sharing was unstated. | The mutex is released after `kill(2)`. The burned token is documented. The `terminate` counter is separate from step 1's (at most 20 mutations per 10 minutes). |
| SR-8 | low | Edge cases: path casing on case-insensitive APFS, hard links, interpreters and injected libraries, and the instability of `launchctl print`. | Prefixes match case-sensitively and fail closed. Interpreters are refused through the path rule. Loaded code is stated as host-only verification. An unparseable `services` block refuses `launchd_unknown`, with output pinned by fixtures. A hard-linked name cannot cross vendors, because the executable's team must equal the bundle's. |
| SR-9 | info | Doc errors: the example start time did not match, `start_time` types were inconsistent, the token-store wording conflicted, pid 1 was described as both refused and malformed, the relaunch rationale was too broad, and the allowlist was implicit. | All fixed. The allowlist is stated under [allowlisted targets](#allowlisted-targets). |

The reviewer also checked these and found them sound:

- the request carries only a pid and start time, and `terminate` only a
  token;
- the single-use, uid-bound token in its own store;
- comparing start times at one-second precision, consistent with `ps
  lstart` and uninstall's `sameProcess`;
- requiring both ruid and euid to be 0, which excludes setuid processes a
  user started;
- a single `syscall.Kill` with a positive pid, never a process group;
- team id validation before it is used in a `-R` requirement;
- fixed argv and environment with no shell;
- audit lines without argv, environment or token.

## Allowlisted targets

Issue #600 asks for allowlisted targets per method. For this method the
allowlist is one path class: a running executable inside an `.app`
directly under `/Applications`. That executable's team must match its
bundle's and be verified, and the running code must match the file. On
top of that come exclusions: the root-owned denylist (also matched through
the ancestor job), launchd main processes, and the helper itself. Nothing
outside that path class can be signalled, whatever the denylist says.

## Open questions for the owner

1. **SIGTERM only.** Proposed: yes. SIGKILL, if ever wanted, would be its
   own later step with its own review.
2. **Refuse launchd main processes.** Proposed: yes, and send the user to
   step 1's `disable` (check 8). The alternative allows them when the job
   has no `KeepAlive`, which needs plist parsing of the job.
3. **Reuse step 1's denylist with no new keys.** Proposed: yes. `Labels`
   entries would also match bundle ids and signing identifiers, and
   `TeamIDs` would match the process's team. The alternative is a separate
   `Processes` key.
4. **CLI surface.** Proposed: `spectra process terminate <pid>` in this
   step's PR. Routing `uninstall apply --quit` through it comes in a
   follow-up PR, so uninstall no longer aborts with `EPERM` on a root
   helper process.
5. **Wait window.** Proposed: 10 s, matching uninstall's `quitWait`. The
   helper waits, not the CLI, so the audit line records `exited`.
6. **`/Library/PrivilegedHelperTools`.** Proposed: out of scope for this
   step, because a bare process gives no way to derive which app owns such
   a tool (SR-3). A later step could resolve the owner through the
   ancestor job's plist and step 1's `AssociatedBundleIdentifiers` rule.
7. **Private APIs.** `csops` (check 5) is required. It is a libSystem
   export with a private header, used the same way by many security
   tools. `proc_signal_with_audittoken` is optional, and would close the
   remaining pid-reuse race. Proposed: use `csops`, and adopt
   `proc_signal_with_audittoken` only if the implementation PR confirms it
   on every supported macOS version. Otherwise keep the kqueue mitigation
   and the stated residual race.
