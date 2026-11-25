# Helper launchd mutation

!!! note "Status: implemented"
    This is step 1 of the helper mutation plan in issue #600, approved by
    the owner on 2026-11-20 and implemented in `internal/helper`
    (`launchd*.go`), `internal/helperclient` and `spectra startup
    disable|enable --scope system`. The owner's answers (admin gate,
    `enable` bootstrap, no stop-only method, denylist, VM fixture signing)
    are part of the design below. The VM integration test is a manual
    pre-release step (see [testing](#testing) and
    [helper VM integration test](helper-vm-test.md)). Where this page was
    ambiguous the implementation chose the more restrictive reading; those
    choices are listed under [implementation notes](#implementation-notes).

The privileged helper gains one narrow ability: disable (and stop) or
re-enable a **system-domain launchd daemon** whose plist sits in
`/Library/LaunchDaemons` and whose program belongs to a **verified
third-party bundle**. Today `spectra startup disable --scope system` refuses
and prints `sudo launchctl ...` commands. With this method it can do the same
change through the helper, after checks that the helper runs itself.

The helper never trusts the caller's description of the target. The request
carries a label and nothing else. The helper works out the plist, program,
bundle and team id from the filesystem and `codesign`, and refuses anything
that does not match.

## Methods

Three methods. Each does one thing.

| Method | Mutates | Purpose |
|---|---|---|
| `helper.launchd.system.prepare` | no | Re-derive and verify the target, return what would run and a single-use token |
| `helper.launchd.system.disable` | yes | `launchctl disable system/<label>`, then `launchctl bootout system/<label>` when loaded |
| `helper.launchd.system.enable` | yes | `launchctl enable system/<label>`, then `launchctl bootstrap system <plist>` when the job is not loaded |

`disable` and `enable` match the user-scope `spectra startup disable/enable`
verbs: `enable` also loads (bootstraps) the daemon when it is not loaded,
without a reboot. Loading does not guarantee a process starts: a job with
`RunAtLoad` or `KeepAlive` starts at once, while an on-demand job stays
loaded but not running until a socket, Mach port or event triggers it. The
result reports `loaded` and `running` separately. The bootstrap rules are
under [Enable and bootstrap](#enable-and-bootstrap).

### Request and response

```json
// helper.launchd.system.prepare
{"label": "com.example.agentd", "action": "disable"}

// result
{
  "label": "com.example.agentd",
  "action": "disable",
  "refused": false,
  "token": "b64url-128-bit",
  "expires_at": "2026-11-20T17:00:30Z",
  "target": {
    "plist_path": "/Library/LaunchDaemons/com.example.agentd.plist",
    "plist_sha256": "…",
    "program": "/Applications/Example.app/Contents/Library/LaunchServices/agentd",
    "bundle_path": "/Applications/Example.app",
    "bundle_id": "com.example.app",
    "team_id": "ABCDE12345"
  },
  "before": {"loaded": true, "running": true, "disabled": false, "pid": 412},
  "planned": [["/bin/launchctl", "disable", "system/com.example.agentd"],
              ["/bin/launchctl", "bootout", "system/com.example.agentd"]]
}

// helper.launchd.system.disable | helper.launchd.system.enable
{"token": "b64url-128-bit"}

// result: the same shape as startup.ChangeResult, plus target and via
{
  "action": "disable", "label": "com.example.agentd", "domain": "system",
  "via": "helper", "refused": false, "target": {…},
  "commands": [{"argv": [...], "exit_code": 0}, {"argv": [...], "exit_code": 0}],
  "pending": false,
  "before": {"loaded": true, "running": true, "disabled": false, "pid": 412},
  "after":  {"loaded": false, "running": false, "disabled": true, "pid": 0},
  "mismatch": "", "outcome": "ok"
}
```

A refusal is a normal result with `refused: true`, a `reason` code and a
`message`, plus the `suggested_commands` the CLI already prints. JSON-RPC
errors are kept for malformed params, rate limits and internal faults. The
mutating methods take only the token: the label and action come from the
token's stored binding, so a client cannot prepare one target and execute
another.

## What the helper re-derives

Every numbered check below runs in `prepare` and again, from scratch, in `disable`/`enable`.
Any check failing means refusal and no `launchctl` call.

0. **Denylist.** Before anything else, the root-owned
   [denylist](#denylist) is loaded and checked; see that section for the
   file's own integrity checks.
1. **Label.** At most 200 bytes, reverse-DNS with at least two non-empty
   segments, only characters `bundleid.Valid` allows (the same rule as
   `uninstall.validID`). Refuse `com.apple` itself and any label starting
   with `com.apple.` (both case folded) with `apple_label`, and the helper's own label
   `dev.spectra.helper` with `denylisted` (a built-in entry). A label that
   fails the shape rule is malformed params (JSON-RPC `-32602`).
2. **Plist path.** Exactly `/Library/LaunchDaemons/<label>.plist`, joined
   with the `idPath` rule so the result sits directly in that directory. No
   other directory is searched. A label whose file has another name is
   refused (`label_file_mismatch`).
3. **Directory and file.** `lstat` on `/Library/LaunchDaemons`: a real
   directory, owned by root, not group- or world-writable. `lstat` on the
   plist: a regular file (not a symlink), owner uid 0, group 0, not group- or
   world-writable, at most 1 MiB. Open with `O_NOFOLLOW`, `fstat` the
   descriptor, and require the same device and inode as the `lstat`.
4. **Plist contents.** Parse the bytes read from that descriptor with
   `internal/plistread`. `Label` must equal the requested label. A plist with
   `BundleProgram` (an `SMAppService` job) is refused; those are out of scope.
   The SHA-256 of the bytes is part of the token binding.
5. **Duplicate labels.** Scan every entry of `/Library/LaunchDaemons` and
   `/System/Library/LaunchDaemons`, the two directories whose jobs load into
   the `system` domain. Only **candidate job plists** are read: entries
   whose name ends in `.plist` and does not start with `.`. Other entries
   (`.DS_Store`, a README) are ignored and never block a change. A
   candidate that is not a regular file (a directory or symlink named
   `x.plist`) counts as unreadable and non-inert below.
   Refuse with `duplicate_label` when another plist
   defines the same label (the `startup.refuseCopies` rule). Then apply a
   **system-domain unreadable rule**. `startup.refuseUnreadable` is not
   reused, because it skips every entry outside the `gui` domain and would
   therefore ignore all of these plists. The helper's own rule takes every
   candidate in those two directories other than the target plist, and for each
   one that cannot be opened, stat'd or parsed as a plist (too large,
   malformed, not a regular file, permission error):
   - if a partial scan read its `Label` as the requested label, refuse with
     `duplicate_label`;
   - if its label is unknown and the file is not inert (inert means empty,
     or a dict with no `Label`, as in `PlistError.inert`), refuse with
     `unreadable_plist` and list the paths;
   - otherwise skip it.

   A read error on either directory itself refuses with `unreadable_plist`.
   The helper runs as root, so permission errors should not happen here; when
   one does, something unusual is going on and the helper does not guess.
6. **Program.** `Program`, else `ProgramArguments[0]`. Must be absolute and
   clean, and `EvalSymlinks(program) == program` (no symlink anywhere in the
   path). Must be a regular file not under `/System`, `/usr` (including
   `/usr/local`), `/bin`, `/sbin` or `/Library/Apple`. It must be one of:
   - inside an `.app` bundle directly under `/Applications`; the owning
     bundle is the outermost `.app` on the path; or
   - directly in `/Library/PrivilegedHelperTools` (root-owned, not
     writable by others); the owning bundle is the app under `/Applications`
     named by the plist's `AssociatedBundleIdentifiers`, matched by bundle id.

   Anything else is refused (`program_not_in_bundle`).

   **The program stays protected until launch.** launchd runs whatever is
   at the program path as root, on this launch and on every later one, so
   the helper requires that no one but root can replace it. The program
   file and every ancestor directory up to `/` must be owned by uid 0,
   contain no symlink, be not world-writable, and be not group-writable,
   except a directory whose group is `admin` (gid 80), such as
   `/Applications` (`root:admin 0775`): an admin can already act as root.
   The program file itself may never be group-writable. Anything else is
   refused with `program_writable`, for `disable` and `enable` alike, at
   `prepare` and again at execute. An app dragged into `/Applications` by a
   user is owned by that user and is therefore refused; that is deliberate,
   since the user could swap the binary that `enable` would have launchd
   run as root.
7. **Bundle and team id.** The bundle path is not a symlink and not under
   `/System`. Then, with absolute `/usr/bin/codesign`:
   - `codesign --verify --strict <bundle>` must pass;
   - the team id is read with `codesign -dv` only after that passes
     (the `uninstall.readTeamID` rule); ad hoc or missing team ids refuse;
   - `codesign --verify --strict -R='anchor apple generic and
     certificate leaf[subject.OU] = "<TEAM>"'` must pass for both the bundle
     and the program, so the daemon binary carries the bundle's team;
   - `codesign --verify -R='anchor apple'` must **fail** for both, with
     exit 3 (requirement not met). Exit 0 is `apple_signed`; any other exit
     is no verdict and refuses `unsigned`. Anything Apple-signed is
     refused, whatever its label.
   - A `--strict` rejection whose only complaint is Finder metadata
     ("detritus") is retried once without `--strict`, which still checks
     code and resources (the `internal/codesignout` rule the uninstaller
     uses). Any other complaint refuses.
8. **Launchd's view.** `launchctl print system/<label>`. When the job is
   loaded, its `path` must equal the plist path and its `program` (or first
   argument) the derived program; otherwise launchd is running a different
   job by that name and the helper refuses (`launchd_mismatch`).
   `launchctl print-disabled system` gives the before state.
9. **Configuration-profile management.** Runs after check 8 and before any
   `launchctl` mutation. See
   [Managed daemons](#managed-daemons) for the sources and the rules.

Only after check 0 and all nine pass does the helper run the planned argv, each as a
fixed slice through the injected `CmdRunner`, never a shell. It then re-reads
`print system` and `print-disabled system` to fill `after` and `mismatch`
exactly as `startup.ChangeResult.verify` does.

## Managed daemons

A daemon that a configuration profile manages is out of scope. Its
administrator decided its state, and the helper must not undo that decision
on behalf of a local admin. The helper runs as root and reads two sources.
Neither source needs a prompt.

1. **Service-management payloads.** These are the managed-preferences files
   a `com.apple.servicemanagement` profile payload installs:
   `/Library/Managed Preferences/com.apple.servicemanagement.plist`, plus
   `/Library/Managed Preferences/<user>/com.apple.servicemanagement.plist`
   for each per-user directory. Per-user rules do not bind the `system`
   domain, but they are checked anyway, conservatively. Each file is checked
   like the denylist: a regular file, not a symlink, owner root, opened
   `O_NOFOLLOW`. It is parsed with `internal/plistread`. Every entry of its
   `Rules` array is matched against the target. `RuleType` `Label` and
   `LabelPrefix` are matched against the label. `BundleIdentifier` and
   `BundleIdentifierPrefix` are matched against the bundle id from check 7
   and the plist's `AssociatedBundleIdentifiers`. `TeamIdentifier` is
   matched against the team id from check 7. Any match refuses with
   `managed`, and the message names the file and the rule.
2. **Background Task Management.** The helper runs
   `/usr/bin/sfltool dumpbtm` as root, which does not prompt as root. It
   parses the output with the `startup` BTM parser, extended to keep the
   `Flags` field, which today's `applyBTMField` drops. It then selects the
   records whose `URL` is the target plist, whose `Identifier` equals the
   label (case folded), or whose `Identifier` ends in `.<label>`. If a selected record carries a flag word that marks it as
   managed, the helper refuses with `managed`. The same applies when a
   selected record carries a flag word outside the set that the
   implementation PR pins from `sfltool` fixtures for each supported macOS
   version. The pinned set is `legacy`, `curated` and `sandboxed`; any
   other word, including `managed`, refuses `managed`. A selected record
   with no `Flags` line refuses `management_unknown`.
   Disposition is not used as a management signal. `enabled`/`disabled` is
   ordinary state, and `allowed` is the user's choice.

The helper refuses with `management_unknown`, before any `launchctl` call,
in these cases:

- a service-management file exists but fails its integrity checks or does
  not parse;
- `/Library/Managed Preferences` exists but cannot be read;
- `sfltool dumpbtm` exits non-zero, cannot be parsed (no `Records for
  UID` header), or is larger than 16 MiB;
- a service-management rule has a `RuleType` the helper does not know.

The helper does not refuse in these cases:

- no service-management file exists;
- BTM parses and has no record for the target;
- BTM has a record for the target that carries only known, unmanaged flags.

The normal third-party daemon is in that last group, so it can still be
disabled. The rule is conservative only when a source exists and cannot be
read.

What this detects: daemons named, by label, bundle id or team id, in a
service-management managed login items payload; and daemons that BTM itself
flags as managed or with an unrecognised flag.

What this does not detect: a daemon that an MDM server installed (for
example with an `InstallApplication` package) without any
service-management rule covering it; restrictions an MDM enforces only
server-side; and profiles that are pending or were removed after the
helper's read. Those daemons look ordinary to the helper. The
[denylist](#denylist) is the second layer for them. It names MDM,
enrollment and endpoint-security agents by label and team id, whatever
installed them.

Both sources are re-read at execute along with every other check. A daemon
that became managed between `prepare` and execute is refused with
`managed`.

## Operations

Allowed, with `/bin/launchctl` and nothing else:

- `disable system/<label>` — persistent override; launchd will not load it.
- `bootout system/<label>` — only after a successful `disable`, only when
  loaded. A bootout still in progress sets `pending`.
- `enable system/<label>` — removes the override.
- `bootstrap system <plist_path>` — only inside `enable`, only when the job
  is not loaded, and only with the plist path derived and bound at `prepare`.

Never: deleting, moving, renaming or editing any file; `bootstrap` outside
`enable`, `load`, `unload`, `kickstart`, `kill`, `remove`; any domain other
than `system`.

### Enable and bootstrap

Decided by the owner (2026-11-20): `enable` matches user-scope
`spectra startup enable`, so an enabled daemon is also started.

1. `prepare` for `enable` records whether the job is loaded
   (`launchctl print system/<label>`). The planned argv list shows the
   `bootstrap` line only when it is not loaded.
2. At execute, all checks re-run from scratch, including the denylist and
   the management check. The plist must be the same file `prepare` saw
   (same device, inode, size, mtime and SHA-256), the program must be the
   same file (same device, inode, size, mtime and code directory hash), and
   the loaded state and
   planned argv must equal the bound ones, else `target_changed`. The
   helper bootstraps the path it just re-verified from the descriptor it
   opened, never a path supplied by the caller.
3. Between those checks and `bootstrap` the helper runs `enable`, and
   another process could load the job in that window. To cover it, the
   helper re-reads `print system/<label>` immediately before `bootstrap`.
   If the job is now loaded from the bound plist and program, the bootstrap
   is skipped and the result records it as skipped. That is one confirmed
   step left out, never an extra one. If the job is loaded under a different
   plist path or program, the result is `launchd_mismatch` and no further
   command runs. The same rule holds for `disable`: a `bootout` that was not
   in the plan is never added. If the job loads after the re-check, the
   result says "disabled but running".
4. After `bootstrap`, `print system/<label>` must show the job loaded with
   `path` equal to the plist path and program equal to the derived program;
   `print-disabled` must show it enabled. Otherwise `mismatch` is set. A
   loaded job that has no process yet (an on-demand daemon) is a success,
   reported as loaded and not running.

Failure handling:

- `enable` fails: nothing else runs; `outcome: error`.
- `enable` succeeds, `bootstrap` fails: the override stays removed (no
  automatic re-disable), `outcome: error`, `pending: false`, and the result
  says "enabled but not loaded", with argv, exit code, stderr and the
  suggestion `sudo launchctl bootstrap system <plist>` or a reboot.
- Bootstrap succeeds but the re-read disagrees: reported as `mismatch`,
  exit 1. The helper does not `bootout` to undo it; the user is shown the
  manual command.

## Denylist

Decided by the owner (2026-11-20): the helper ships with a root-owned
denylist it always refuses, so an admin session cannot silently disable
security tooling through this method.

- **Path:** `/Library/Application Support/Spectra/helper-denylist.plist`.
  `sudo spectra install-helper` creates the directory `root:wheel` `0755`
  and writes the default file `root:wheel` `0644`. The directory is
  system-wide, so only root can change it, and it keeps policy out of
  `/etc` (managed by OS updates) and out of any user home. It sits next to
  other per-product system data; the helper verifies it rather than trusting
  the location.
- **Format:** an XML property list parsed with `internal/plistread`, with
  two string arrays and XML comments for the reason behind each entry:

  ```xml
  <dict>
    <key>Labels</key>
    <array>
      <!-- endpoint security: exact label or trailing-* prefix -->
      <string>com.example.edr.agentd</string>
      <string>com.example.mdm.*</string>
    </array>
    <key>TeamIDs</key>
    <array>
      <!-- backup agents: every daemon signed by this team -->
      <string>ABCDE12345</string>
    </array>
  </dict>
  ```

  Labels match case-folded, exactly, or by prefix when the entry ends in
  `*`. A team id entry matches the team id the helper derived in check 7.
  Unknown keys are ignored; a malformed entry makes the file invalid (below).
- **Default contents, by category:** endpoint detection and response
  agents; mobile device management and device-management enrollment
  agents; backup agents; plus the helper's own label. The entries are the
  label prefixes compiled into the helper as `DefaultDenylistPlist`.
  `TeamIDs` ships empty: no vendor team id has been verified against the
  vendors' own signed releases, and a guessed id would refuse an unrelated
  vendor or give false assurance.
- **Editing:** only with `sudo`. Spectra never writes the file after
  install, and install does not overwrite an existing, valid file.
- **Integrity:** the helper `lstat`s the directory (a real directory, owned
  by root, not group- or world-writable) and the file (regular, not a
  symlink, owner uid 0, mode `0644` or stricter), opens it `O_NOFOLLOW`
  and compares inode as in check 3. If the file **exists but fails any of
  these checks, or fails to parse**, the helper refuses every mutation
  (`denylist_invalid`) until it is fixed. If the file (or its directory)
  does not exist, the compiled-in default entries (`DefaultDenylistPlist`,
  the same file `install-helper` writes) apply, along with the built-ins
  (the helper's own label, `com.apple` and the `com.apple.` prefix), and a
  warning line is written to the audit log. A refusal from the fallback
  names "the built-in default denylist" as its source.
- **Scope:** the denylist applies to `prepare`, `disable` and `enable`;
  a listed daemon cannot be changed through the helper in either direction.
- **Reporting:** a match is a normal refusal with `reason: denylisted`, the
  matched entry and its kind (`label` or `team_id`) in the message, and the
  CLI's usual manual `sudo launchctl` commands, so an administrator can still
  act deliberately. The audit log records the refused `prepare` with the
  matched entry. An invalid file reports `denylist_invalid` with the failed
  check (owner, mode, symlink or parse error) and the path.

## Confirmation token and replay protection

- `prepare` creates 128 random bits from `crypto/rand` (an injected reader
  in tests) and returns them base64url-encoded.
- The helper keeps, in memory only, the token's SHA-256 mapped to a binding:
  caller uid, action, label, plist path, device, inode, size, mtime,
  SHA-256, program path, the program file's device, inode, size and mtime,
  the program's code directory hash (`CDHash=` from
  `codesign -dv --verbose=4`, read after the signature verified; best effort,
  bound as empty when codesign does not report one), bundle path, bundle
  id, team id, expiry, the
  daemon's **loaded state at prepare** (`loaded`, plus `path` and program
  when loaded), and the exact **planned argv list** returned to the caller.
- At execute the helper recomputes the loaded state and the planned argv
  list from scratch. If either differs from the binding, it refuses with
  `target_changed` and runs nothing. This covers a `disable` prepared
  against an unloaded daemon that is loaded by execute (a `bootout` would be
  added), and an `enable` prepared against an unloaded daemon that is
  loaded by execute (the `bootstrap` would be dropped). The CLI then tells
  the user to run the command again, which makes a new `prepare` and shows
  the new plan. The helper never runs an argv that was not in the plan
  the user confirmed.
- A token expires 60 seconds after `prepare`. Each uid may hold at most four
  live tokens; a fifth `prepare` evicts the oldest.
- `disable`/`enable` look the token up and **delete it before doing
  anything else**, so it is single use whatever the outcome. Then they
  require the same caller uid and the same action as the method name, re-run
  every check, and refuse with `target_changed` if any bound fact differs.
- A helper restart drops all tokens. Tokens never appear in the audit log.
  Each successful `prepare` and each execution record includes the first 8
  hex characters of the token's SHA-256 (`token_hash`), so an execution
  record can be paired with its prepare record (see [audit log](#audit-log)).

The token stops a captured or repeated request from acting twice and ties the
user's confirmation to the exact target they saw. It is not proof that a
human confirmed: a hostile client can call both methods back to back. The
security controls are the caller checks and target verification.

## Caller authentication

- Peer uid from `peercred.PeerUID`, as today, with the socket still
  `root:_spectra` `0660`.
- **New (decided 2026-11-20):** the mutating methods and `prepare` also
  require the caller to be uid 0 or a member of the `admin` group,
  re-checked on every call. Being in
  `_spectra` was granted for telemetry; changing system daemons needs the
  same standing as the `sudo` it replaces.
- Not exposed over the per-user daemon, the MCP server or Spectra Remote.
  Only the local CLI calls it.

## Rate limits

- The existing 120 requests per minute per uid still applies.
- `prepare`: 30 per minute per uid.
- `disable` + `enable` together: 10 per 10 minutes per uid, and one mutation
  in flight across the whole helper (a mutex), so toggling cannot thrash a
  daemon.

## Audit log

The existing per-request line stays unchanged (no params). Every
`prepare`, successful or refused, and every mutating call adds one line.
A successful `prepare` line has `event: launchd.system.prepare`, the
action, the label, the target fields, the `before` state, the planned
argv and `token_hash`, and `outcome: ok`. A refused `prepare` has no
token, so it has no `token_hash`, and carries `outcome: refused` with the
`reason`. A mutating call looks like this:

```json
{"time":"2026-11-20T17:00:12Z","uid":501,"event":"launchd.system.disable",
 "token_hash":"3f9a0c1e","label":"com.example.agentd",
 "plist":"/Library/LaunchDaemons/com.example.agentd.plist","plist_sha256":"…",
 "program":"/Applications/Example.app/Contents/Library/LaunchServices/agentd",
 "bundle_id":"com.example.app","team_id":"ABCDE12345",
 "commands":[{"argv":["/bin/launchctl","disable","system/com.example.agentd"],"exit_code":0},
             {"argv":["/bin/launchctl","bootout","system/com.example.agentd"],"exit_code":0}],
 "outcome":"ok","mismatch":""}
```

`token_hash` is the first 8 hex characters of SHA-256(token), never the
token. An execution with an unknown or expired token still logs the hash
of what it received. `outcome` is `ok`, `refused` (with `reason`),
`mismatch` or `error`. The log keeps its newsyslog rotation.

## CLI

```text
spectra startup disable <label> --scope system [--yes] [--json]
spectra startup enable  <label> --scope system [--yes] [--json]
```

0. Without `--scope system` nothing here applies. User-scope requests keep
   today's behaviour: the CLI changes the user's own `gui/<uid>` domain
   directly, without root and without the helper.
1. With `--scope system` and no helper installed (or the socket refuses the
   connection), behaviour is today's system-scope refusal: exit 3 and the
   `sudo launchctl` commands, now with a line saying
   `sudo spectra install-helper` enables this. An installed helper that
   predates these methods answers JSON-RPC `-32601` (method not found); the
   CLI treats that the same way, with a line saying to update the helper
   (`spectra install-helper`) instead.
2. With `--scope system` and the helper, call `prepare`. A refusal prints the helper's reason and
   the manual commands; exit 3.
3. Print the target (plist, program, bundle, team id, before state, planned
   commands) and ask `Disable com.example.agentd for all users? [y/N]` on a
   TTY. Without a TTY, `--yes` is required; otherwise refuse, exit 3, token
   unused.
4. Call `disable`/`enable` with the token and print the result in the same
   table as a user-scope change, with `via: spectra-helper`. `--json`
   prints the result object as is.

Exit codes are unchanged: 0 ok, 1 error or re-read mismatch, 2 usage,
3 refused. Running the CLI as root still goes through the helper so there
is one audited path.

## Failure modes

| Situation | Reason / error | What the user sees |
|---|---|---|
| Helper not installed or socket refused | — | Today's refusal plus install hint; exit 3 |
| Caller not admin | `not_admin` | "changing system daemons needs an administrator account"; exit 3 |
| No plist at the fixed path for the label | `unknown_label` | The path; exit 3 |
| Plist uses `BundleProgram` (an `SMAppService` job) | `bundle_program` | The plist; exit 3 |
| Program or an ancestor directory is not root-owned, or is writable by others | `program_writable` | The path, owner and mode; exit 3 |
| Daemon on the denylist | `denylisted` | Matched entry and manual commands; exit 3 |
| Denylist exists but not root-owned, not `0644` or stricter, or unparseable | `denylist_invalid` | Failed check and path; all mutations refused; exit 3 |
| `enable` succeeded, `bootstrap` failed | `outcome: error` | "enabled but not loaded", argv, stderr, manual bootstrap command; exit 1 |
| Apple label, Apple-signed, under `/System` | `apple_label`, `apple_signed`, `system_path` | Refusal with manual commands; exit 3 |
| Plist or program is a symlink | `symlink` | Path named; exit 3 |
| Plist not root-owned or writable by others | `plist_permissions` | Owner and mode shown; exit 3 |
| File name differs from label, duplicate or unreadable plist | `label_file_mismatch`, `duplicate_label`, `unreadable_plist` | Plists listed; exit 3 |
| Program outside a bundle, unsigned, wrong team | `program_not_in_bundle`, `unsigned`, `team_mismatch` | Program, bundle and team ids shown; exit 3 |
| Loaded job differs from the plist | `launchd_mismatch` | Both paths shown; exit 3 |
| Daemon covered by a service-management rule or flagged managed in BTM | `managed` | Source file or BTM record and the matching rule; exit 3 |
| A management source exists but cannot be read or parsed | `management_unknown` | The source and the error; exit 3 |
| Token unknown, expired, other uid, or target changed (including loaded state or planned argv) | `token_invalid`, `token_expired`, `target_changed` | "run the command again"; exit 3 |
| Rate limit | JSON-RPC `-32001` | "too many changes; try again in N minutes"; exit 1 |
| `launchctl` non-zero | `outcome: error` | argv, exit code and stderr; exit 1 |
| After state disagrees | `mismatch` | the mismatch sentence; exit 1 |

A `disable` whose `bootout` fails leaves the job disabled but running; the
result says so and suggests a reboot or `sudo launchctl bootout`.

## Testing

- **Unit (no root, any OS):** the checks run against an injected filesystem
  (`Lstat`, `Open` with `O_NOFOLLOW`, `Fstat`, `ReadDir`, `EvalSymlinks`),
  an injected `CmdRunner` that records argv and scripts `codesign` and
  `launchctl` output, an injected clock and random reader, and an injected
  group lookup. One table test per refusal reason, each asserting no
  `launchctl` mutation argv was recorded.
- **Token tests:** reuse, expiry, wrong uid, wrong action, eviction at five,
  target changed between calls (plist bytes, inode, program path, program bytes, inode or CDHash, team id), and
  loaded state changed between calls (unloaded at prepare and loaded at
  execute, for both `disable` and `enable`) refusing `target_changed` with
  no argv recorded.
- **Dispatcher tests:** the new rate limits, the mutation mutex, and the
  audit line's fields: a successful `prepare` line and its execution line
  carry the same `token_hash`, and the token itself never appears.
- **Duplicate and unreadable tests:** an unreadable, non-inert plist in
  `/Library/LaunchDaemons` or `/System/Library/LaunchDaemons` refuses
  `unreadable_plist`; inert ones are skipped; a partially read duplicate
  label refuses `duplicate_label`.
- **Management tests:** a service-management rule matching by each
  `RuleType`; a BTM record flagged managed; an unknown BTM flag word; a
  symlinked or malformed payload file and a failing `sfltool` each refuse
  `management_unknown`; no payload file and an unmanaged BTM record pass.
- **Fuzz:** label validation and plist parsing.
- **CLI tests:** helper absent, refusal, no TTY without `--yes`, success and
  mismatch output, all against a fake helper client.
Implemented as table and unit tests in `internal/helper/launchd_test.go`
(fakes in `launchd_fake_test.go`), `cmd/spectra/startup_system_test.go`,
`internal/helperclient/launchd_test.go` and
`internal/startup/export_test.go`, including `FuzzLaunchdParams` for the
request decoder. The VM test below is scripted but not run by CI
(`internal/helper/launchd_vm_test.go`).

- **VM-only integration (manual pre-release step):** build tag `helper_vm`
  plus `SPECTRA_VM_TEST=1`. It runs only inside a disposable macOS VM, never
  on CI hosts or developer machines. The owner runs it before each release
  that changes the helper, with `scripts/helper-vm-test.sh` in a disposable
  Tart VM (decided 2026-11-21), and posts the summary on the release's
  tracking issue. The procedure, the tests and the certificate handling are
  in [helper VM integration test](helper-vm-test.md). It serves the helper
  code in-process (a fresh dispatcher on a private socket, not the
  installed `spectra-helper` service) and installs a fixture app with a
  trivial daemon. It then checks disable, enable, a replayed token, and refusals for a
  `com.apple.*` label, a symlinked plist, a non-root plist and a
  service-management rule. Enable is checked to bootstrap an unloaded
  fixture daemon. The fixture is signed with a **Developer ID test
  certificate** imported, for that run only, into a keychain inside the VM.
  An ad-hoc signature is not enough: check 7 requires a verified non-Apple
  team id, and an ad-hoc signature has none. The owner keeps the `.p12` on
  the host and never uses that certificate to sign anything shipped;
  deleting the VM removes only the guest copies.
- **Denylist tests:** label, prefix and team id matches; file absent, wrong
  owner, mode `0664`, symlink and malformed content each refuse (or fall back,
  when absent) with the right reason; no mutation argv recorded.
- **Bootstrap tests:** skipped when already loaded; plist changed between
  `prepare` and execute; `enable` ok then `bootstrap` fails; post-bootstrap
  mismatch.

## Implementation notes

Choices the implementation made where this page left room, each the more
restrictive reading:

- The admin check runs `/usr/bin/dsmemberutil checkmembership -u <uid> -G
  admin` and fails closed on any output it does not recognise.
- The mutating methods take the token out of the store before the rate
  limit, the admin check and every other step; a rate-limited or refused
  execute still uses up its token.
- Every execute counts toward the 10-per-10-minutes limit, refused or not,
  and a second execute while one is in flight is a `-32001` error.
- A program in `/Library/PrivilegedHelperTools` must be owned by exactly
  one app directly in `/Applications`; two matching apps refuse as
  ambiguous.
- A per-user entry of `/Library/Managed Preferences` that is a symlink
  refuses `management_unknown`; payload files use the denylist's file
  rule (root-owned, mode `0644` or stricter).
- A BTM record is selected by an `Identifier` equal to the label (case
  folded), an `Identifier` ending in `.<label>`, or its file URL; any record
  so selected must pass the flag rule.
- The denylist's directory must also resolve without symlinks. The default
  file `install-helper` writes lists the helper's label and label prefixes
  of common endpoint-security, device-management and backup agents, for
  review, and the helper applies the same entries when the file is absent;
  `TeamIDs` ships empty.
- Commands run with a fixed environment (`PATH=/usr/bin:/bin:/usr/sbin:/sbin`,
  `LC_ALL=C`) and a 60-second timeout each; a whole prepare or execute is
  bounded at 150 seconds.

## Out of scope

- Deleting, moving or editing plists, programs or bundles (later steps of
  #600, each with its own design).
- A stop-only method (`bootout` without the persistent `disable`). Decided
  2026-11-20: step 1 has none; `disable` is the only way to stop a daemon.
- `kickstart`, `load` and starting jobs, except the `bootstrap` that
  `enable` performs on an unloaded daemon.
- `/Library/LaunchAgents`, other users' `gui/<uid>` domains, and the
  user's own domain (the CLI already changes that without root).
- `SMAppService` jobs (`BundleProgram`) and BTM Login Items. Check 4
  refuses `BundleProgram` jobs.
- Configuration-profile managed daemons. Check 9 refuses them, within the
  limits stated under [managed daemons](#managed-daemons).
- Programs outside an `.app` or `/Library/PrivilegedHelperTools`, such as
  Homebrew services under `/usr/local` or `/opt/homebrew`.
- Signalling processes (step 2,
  [proposed separately](helper-process-terminate.md)), batch changes, and
  any caller besides the local CLI (no MCP, per-user daemon or Remote
  exposure).
- Linux: the methods return `hostos.Unsupported` off Darwin; systemd units
  get their own design.

## Open questions for the owner

None remain open. Answered 2026-11-20 and folded into the design above: the `admin` gate
([caller authentication](#caller-authentication)), `enable` bootstrapping
([enable and bootstrap](#enable-and-bootstrap)), no stop-only method
([out of scope](#out-of-scope)), and the [denylist](#denylist).

1. *Resolved as proposed:* which signing identity signs the VM fixture
   daemon, and where does that VM test run? A Developer ID test certificate
   imported only into the disposable test VM (the owner keeps the `.p12`
   on the host). Ad-hoc signing cannot pass
   check 7. The owner runs the test by hand inside that VM before each
   helper release (see [testing](#testing)). The VM is a disposable Tart
   clone on the owner's Mac (decided 2026-11-21, #650).
