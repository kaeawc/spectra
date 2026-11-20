# Startup items

`spectra startup` answers "what launches at login or boot, who put it there,
and is it running?" with one row per item. It is macOS only; on Linux it
exits with an unsupported-OS error.

```bash
spectra startup                      # same as: spectra startup list
spectra startup list --json --scope user
spectra startup disable com.example.agent
spectra startup enable com.example.agent --json
```

## Sources

Each row lists the sources that contributed to it, and the report's
`sources` array says whether each source could be read.

| Source | What it gives | Command or path |
|---|---|---|
| `launchd_plist` | label, program (`Program` or `ProgramArguments[0]`), trigger, plist owner | `~/Library/LaunchAgents`, `/Library/LaunchAgents`, `/Library/LaunchDaemons`; `/System/Library/LaunchAgents` and `LaunchDaemons` only with `--include-apple` |
| `launchctl_print_disabled` | enabled/disabled overrides (`disabled`/`enabled`, or `true`/`false` on older macOS) | `launchctl print-disabled gui/<uid>` and `launchctl print-disabled system` |
| `launchctl_print` | loaded, running, pid, last exit | `launchctl print gui/<uid>` and `launchctl print system` |
| `btm_store` | Background Task Management items: what System Settings > General > Login Items shows, read without root | the world-readable `/private/var/db/com.apple.backgroundtaskmanagement/BackgroundItems-v16.btm`; only store version 16 is decoded |
| `btm` | the same items, as the fallback when the store cannot be used and Spectra runs as root; otherwise reported as `needs_root` | `sfltool dumpbtm`, as root only |
| `owner` | owning app and team id | the `.app` containing the program, the plist's `AssociatedBundleIdentifiers`, an app whose bundle id equals or is a dotted prefix of the label, or BTM's parent app; team id from `codesign -dv` |

Loaded state comes from one `launchctl print <domain>` per domain: its
services table lists every loaded job with its pid. A per-label
`launchctl print gui/<uid>/<label>` would cost one call per item, and
`launchctl list` only covers the caller's own domain.

`trigger` is the strongest start condition: `keep_alive`, `login`
(`RunAtLoad` in an agent), `boot` (`RunAtLoad` in a daemon), `interval`
(`StartInterval`), `calendar` (`StartCalendarInterval`), or `on_demand`
(none of these: started by a socket, Mach port, watched path or by hand).
`triggers` lists every condition that applies. `disabled` is the launchctl
override when there is one, else the plist `Disabled` key, else BTM's
disposition (`disabled_by` says which). A malformed plist, or one without a
`Label`, is listed under `errors` and skipped.

## Scopes

- `user`: `~/Library/LaunchAgents` and per-user BTM items. The user can
  change these without root.
- `system`: `/Library/LaunchAgents` (loaded into every user's GUI domain),
  `/Library/LaunchDaemons` (the `system` domain), and BTM daemons. Changing
  these needs root.

`--scope user|system|all` filters the list (default `all`).

## Disable and enable

`spectra startup disable <label>` runs `launchctl disable gui/<uid>/<label>`
and then, if the job is loaded, `launchctl bootout gui/<uid>/<label>`, which
stops it. A bootout that finds the job already gone (`No such process`) is
treated as done; one that reports `Operation now in progress` is reported as
`pending`, since launchd may still be stopping the job.
`spectra startup enable <label>` runs `launchctl enable gui/<uid>/<label>`
and then, if the job is not loaded, `launchctl bootstrap gui/<uid> <plist>`.
Bootstrap loads the job and starts it only when the plist has `RunAtLoad`, so
enable undoes a disable for interval, calendar and on-demand jobs as well.
Both re-read the state afterwards, also when a command failed; `--json`
reports each command's argv, exit code and stderr with the item before and
after, and `mismatch` when the re-read state is not what was asked. Under
`sudo`, both act on the invoking user's (`SUDO_UID`) GUI domain.

Disable and enable always read every plist directory, `/System/Library`
included, whatever `--include-apple` says for `list`. Spectra changes
user-scope launchd items only, and refuses (exit `3`, no command run) with a
reason:

| Reason | When | What to do |
|---|---|---|
| `apple_label` | the label starts with `com.apple.` | the exact `launchctl` command is printed for you to run if you are sure |
| `apple_plist` | a plist with that label is under `/System/Library`, for example a user copy of an Apple job in `~/Library/LaunchAgents` | the paths are printed; change it yourself if you are sure |
| `duplicate_label` | more than one plist defines the label; launchd loads one job per label, so Spectra cannot tell which one a change would act on | the paths are printed; remove or rename the extra plist |
| `system_scope` | the item is in `/Library/LaunchAgents` or `/Library/LaunchDaemons` | the exact `launchctl ...` commands are printed (with `sudo` for `/Library/LaunchDaemons` when not already root; an override in your GUI domain needs no root); the [privileged helper](../design/privileged-helper.md) is observe-only by design |
| `btm_only` | a BTM login item with no launchd plist (an app opened at login, or an SMAppService job inside an app bundle) | change it in System Settings > General > Login Items: `open "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"` |
| `unknown_label` | no item has that label; it may be a Login Items entry when the `btm`/`btm_store` source could not be read | `spectra startup list` shows the labels; the Login Items pane is suggested |
| `unreadable_plist` | a plist that could not be parsed might define the label, so Spectra cannot rule out a duplicate | the unreadable paths are listed in the result's `unreadable_plists` field; fix or remove them |
| `unsupported_scope` | `--scope` is not `user` | |

Exit codes: `0` ok, `1` error (a failed `launchctl` command, or a re-read
state that does not match the change), `2` usage, `3` refused.

## Background Task Management limits

- Login Items are read without root from the BTM store file, decoded as an
  NSKeyedArchiver binary plist (`btm_store` source). Only store version 16
  is decoded; the newest `BackgroundItems-v<N>.btm` is used. The input is
  vetted before decoding (binary plist header, bounded object count) and the
  decode is time-bounded. If the store is missing, of another version,
  malformed, or too slow to decode, Spectra falls back to `needs_root`
  for the `btm` source and lists launchd plists only.
- `sfltool dumpbtm` asks for administrator authorization in a dialog when it
  is not run as root, and blocks until the dialog is answered. Spectra
  therefore runs it only as root, and only when the store cannot be used.
  Under `sudo`, Spectra reports the invoking user's (`SUDO_UID`) session.
- The store lists only users whose generated UUID Spectra can resolve (the
  caller's own, root, nobody); other users' records are skipped.
- Without BTM, apps that open at login through `SMAppService` and helper jobs
  whose plists live inside an app bundle are not listed; launchd plists in
  the three directories above are always listed.
- `sfltool dumpbtm` output is undocumented. Spectra keys only on the record
  headers, `#N:` item markers and `Key: value` lines, keeps launchd agents
  and daemons, login items and apps registered to open at login, and drops
  developer groups, disabled parent apps, and Quick Look, Spotlight and dock
  tile plug-ins. Items recorded for other users are skipped.
- A BTM legacy agent or daemon and its launchd plist are one row, matched by
  plist path and then by label.
- Two plists that define the same label in the same domain are two rows, each
  with `duplicate_of` naming the other plist; the table prints a
  `duplicate label` line under each.
