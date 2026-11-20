# Helper launchd mutation (proposed)

!!! warning "Status: proposed"
    This is the design for step 1 of the helper mutation plan in issue #600.
    Nothing here is implemented. No helper code lands until the owner
    approves this design and an independent security review signs off.
    The owner answered four open questions on 2026-11-20 (admin gate,
    `enable` bootstrap, no stop-only method, denylist); those are now part
    of the proposal below. Everything stays proposed.

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
verbs: `enable` also loads the daemon when it is not loaded, so it runs
without a reboot. The bootstrap rules are under
[Enable and bootstrap](#enable-and-bootstrap).

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
  "before": {"loaded": true, "disabled": false, "pid": 412},
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
  "before": {"loaded": true, "disabled": false, "pid": 412},
  "after":  {"loaded": false, "disabled": true, "pid": 0},
  "mismatch": ""
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
   `uninstall.validID`). Refuse any label starting with `com.apple.` (case
   folded) and the helper's own label `dev.spectra.helper`.
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
5. **Duplicate labels.** Scan `/Library/LaunchDaemons` and
   `/System/Library/LaunchDaemons`. Refuse when another plist defines the same
   label, or when a plist cannot be parsed and may define it (the same rule as
   `startup.refuseCopies` and `refuseUnreadable`).
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
7. **Bundle and team id.** The bundle path is not a symlink and not under
   `/System`. Then, with absolute `/usr/bin/codesign`:
   - `codesign --verify --strict <bundle>` must pass;
   - the team id is read with `codesign -dv` only after that passes
     (the `uninstall.readTeamID` rule); ad hoc or missing team ids refuse;
   - `codesign --verify --strict -R='anchor apple generic and
     certificate leaf[subject.OU] = "<TEAM>"'` must pass for both the bundle
     and the program, so the daemon binary carries the bundle's team;
   - `codesign --verify -R='anchor apple'` must **fail** for both. Anything
     Apple-signed is refused, whatever its label.
8. **Launchd's view.** `launchctl print system/<label>`. When the job is
   loaded, its `path` must equal the plist path and its `program` (or first
   argument) the derived program; otherwise launchd is running a different
   job by that name and the helper refuses (`launchd_mismatch`).
   `launchctl print-disabled system` gives the before state.

Only after check 0 and all eight pass does the helper run the planned argv, each as a
fixed slice through the injected `CmdRunner`, never a shell. It then re-reads
`print system` and `print-disabled system` to fill `after` and `mismatch`
exactly as `startup.ChangeResult.verify` does.

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
2. At execute, all checks re-run from scratch, including the denylist, and
   the plist must be the same file `prepare` saw: same device, inode, size,
   mtime and SHA-256, else `target_changed`. The helper bootstraps the path
   it just re-verified from the descriptor it opened, never a path supplied
   by the caller.
3. The helper re-reads `print system/<label>` immediately before
   `bootstrap`. If the job is now loaded (someone else loaded it), the
   bootstrap is skipped and recorded as such. If it is loaded under a
   different plist path or program, the result is `launchd_mismatch` and no
   further command runs.
4. After `bootstrap`, `print system/<label>` must show the job loaded with
   `path` equal to the plist path and program equal to the derived program;
   `print-disabled` must show it enabled. Otherwise `mismatch` is set.

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
  agents; backup agents; plus the helper's own label. No vendor list ships
  in this design; the implementation PR proposes the entries for review.
- **Editing:** only with `sudo`. Spectra never writes the file after
  install, and install does not overwrite an existing, valid file.
- **Integrity:** the helper `lstat`s the directory (a real directory, owned
  by root, not group- or world-writable) and the file (regular, not a
  symlink, owner uid 0, mode `0644` or stricter), opens it `O_NOFOLLOW`
  and compares inode as in check 3. If the file **exists but fails any of
  these checks, or fails to parse**, the helper refuses every mutation
  (`denylist_invalid`) until it is fixed. If the file does not exist, the
  built-in default set (the helper's own label and the `com.apple.`
  prefix) still applies and a warning line is written to the audit log.
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
  SHA-256, program path, bundle path, bundle id, team id, and expiry.
- A token expires 60 seconds after `prepare`. Each uid may hold at most four
  live tokens; a fifth `prepare` evicts the oldest.
- `disable`/`enable` look the token up and **delete it before doing
  anything else**, so it is single use whatever the outcome. Then they
  require the same caller uid and the same action as the method name, re-run
  every check, and refuse with `target_changed` if any bound fact differs.
- A helper restart drops all tokens. Tokens never appear in the audit log;
  the log records the first 8 hex characters of the token's hash to pair a
  `prepare` with its execution.

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

The existing per-request line stays unchanged (no params). Each mutating
call, and each refused `prepare`, adds one line:

```json
{"time":"2026-11-20T17:00:12Z","uid":501,"event":"launchd.system.disable",
 "token":"3f9a0c1e","label":"com.example.agentd",
 "plist":"/Library/LaunchDaemons/com.example.agentd.plist","plist_sha256":"…",
 "program":"/Applications/Example.app/Contents/Library/LaunchServices/agentd",
 "bundle_id":"com.example.app","team_id":"ABCDE12345",
 "commands":[{"argv":["/bin/launchctl","disable","system/com.example.agentd"],"exit_code":0},
             {"argv":["/bin/launchctl","bootout","system/com.example.agentd"],"exit_code":0}],
 "outcome":"ok","mismatch":""}
```

`outcome` is `ok`, `refused` (with `reason`), `mismatch` or `error`. The
log keeps its newsyslog rotation.

## CLI

```text
spectra startup disable <label> --scope system [--yes] [--json]
spectra startup enable  <label> --scope system [--yes] [--json]
```

1. Without the helper, or without `--scope system`, behaviour is today's:
   refuse with exit 3 and print the `sudo launchctl` commands, now with a
   line saying `sudo spectra install-helper` enables this.
2. With the helper, call `prepare`. A refusal prints the helper's reason and
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
| Daemon on the denylist | `denylisted` | Matched entry and manual commands; exit 3 |
| Denylist exists but not root-owned, not `0644` or stricter, or unparseable | `denylist_invalid` | Failed check and path; all mutations refused; exit 3 |
| `enable` succeeded, `bootstrap` failed | `outcome: error` | "enabled but not loaded", argv, stderr, manual bootstrap command; exit 1 |
| Apple label, Apple-signed, under `/System` | `apple_label`, `apple_signed`, `system_path` | Refusal with manual commands; exit 3 |
| Plist or program is a symlink | `symlink` | Path named; exit 3 |
| Plist not root-owned or writable by others | `plist_permissions` | Owner and mode shown; exit 3 |
| File name differs from label, duplicate or unreadable plist | `label_file_mismatch`, `duplicate_label`, `unreadable_plist` | Plists listed; exit 3 |
| Program outside a bundle, unsigned, wrong team | `program_not_in_bundle`, `unsigned`, `team_mismatch` | Program, bundle and team ids shown; exit 3 |
| Loaded job differs from the plist | `launchd_mismatch` | Both paths shown; exit 3 |
| Token unknown, expired, other uid, or target changed | `token_invalid`, `token_expired`, `target_changed` | "run the command again"; exit 3 |
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
  target changed between calls (plist bytes, inode, program, team id).
- **Dispatcher tests:** the new rate limits, the mutation mutex, and the audit
  line's fields (and that the token itself never appears).
- **Fuzz:** label validation and plist parsing.
- **CLI tests:** helper absent, refusal, no TTY without `--yes`, success and
  mismatch output, all against a fake helper client.
- **VM-only integration:** build tag `helper_vm` plus `SPECTRA_VM_TEST=1`,
  run only inside a disposable macOS VM, never on CI hosts or developer
  machines. It installs the helper and a Developer ID-signed fixture app with
  a trivial daemon, then checks disable, enable, a replayed token, and
  refusals for a `com.apple.*` label, a symlinked plist and a non-root plist.
  Enable is checked to bootstrap an unloaded fixture daemon.
- **Denylist tests:** label, prefix and team id matches; file absent, wrong
  owner, mode `0664`, symlink and malformed content each refuse (or fall back,
  when absent) with the right reason; no mutation argv recorded.
- **Bootstrap tests:** skipped when already loaded; plist changed between
  `prepare` and execute; `enable` ok then `bootstrap` fails; post-bootstrap
  mismatch.

## Out of scope

- Deleting, moving or editing plists, programs or bundles (later steps of
  #600, each with its own design).
- A stop-only method (`bootout` without the persistent `disable`). Decided
  2026-11-20: step 1 has none; `disable` is the only way to stop a daemon.
- `kickstart`, `load` and starting jobs, except the `bootstrap` that
  `enable` performs on an unloaded daemon.
- `/Library/LaunchAgents`, other users' `gui/<uid>` domains, and the
  user's own domain (the CLI already changes that without root).
- `SMAppService` jobs (`BundleProgram`), BTM Login Items, and
  configuration-profile managed items.
- Programs outside an `.app` or `/Library/PrivilegedHelperTools`, such as
  Homebrew services under `/usr/local` or `/opt/homebrew`.
- Signalling processes, batch changes, and any caller besides the local
  CLI (no MCP, per-user daemon or Remote exposure).
- Linux: the methods return `hostos.Unsupported` off Darwin; systemd units
  get their own design.

## Open questions for the owner

Answered 2026-11-20 and folded into the design above: the `admin` gate
([caller authentication](#caller-authentication)), `enable` bootstrapping
([enable and bootstrap](#enable-and-bootstrap)), no stop-only method
([out of scope](#out-of-scope)), and the [denylist](#denylist).

1. Which signing identity signs the VM fixture daemon, and where does that
   VM test run?
