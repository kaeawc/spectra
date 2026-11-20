# App uninstall

`spectra uninstall` removes a macOS app and what it left behind in two steps:
a read-only `plan`, and an `apply` that acts only on a saved plan.

```bash
spectra uninstall plan /Applications/Foo.app            # review
spectra uninstall plan -json com.foo.app > plan.json    # by bundle id
spectra uninstall apply --plan plan.json --dry-run
spectra uninstall apply --plan plan.json --quit
```

## What is matched

A path is included only when something concrete ties it to the app:

| Reason | Rule |
| --- | --- |
| `bundle_id` | Named exactly after the bundle id: `Application Support/<id>`, `Caches/<id>`, `Preferences/<id>.plist`, `Preferences/ByHost/<id>.<host-uuid>.plist`, `Containers/<id>`, `Saved Application State/<id>.savedState`, `HTTPStorages/<id>`, `WebKit/<id>`, `Logs/<id>`, `Cookies/<id>.binarycookies`, `Application Scripts/<id>`, in `~/Library` and (support, caches, preferences, logs) `/Library`. Also a launchd job whose `Label` is the bundle id or whose `AssociatedBundleIdentifiers` lists it. |
| `helper_of_bundle` | The same locations for helper bundle ids declared inside the app (`Contents/Frameworks`, `Library/LoginItems`, `XPCServices`, `PlugIns`, `Library/SystemExtensions`) that extend the main id, such as `com.tinyspeck.slackmacgap.helper`. A launchd job or `/Library/PrivilegedHelperTools/<label>` whose label is such an id, an `SMPrivilegedExecutables` key, or a tool in `Contents/Library/LaunchServices`. |
| `launchd_program_in_bundle` | A plist in `~/Library/LaunchAgents`, `/Library/LaunchAgents` or `/Library/LaunchDaemons` whose `Program` (or first `ProgramArguments` entry) is inside the bundle. A privileged helper that job runs is included too. |
| `team_id`, `entitlement_group` | `~/Library/Group Containers/<group>` for each group in the app's `com.apple.security.application-groups` entitlement. |
| `receipt` | A `pkgutil` receipt whose file list contains the bundle, its `/var/db/receipts` entries, and the plain files (never directories) it installed outside the bundle. |
| `name` | A folder in `~/Library/Application Support`, `Caches` or `Logs` named exactly after the app, only when no other installed app uses that name and the name is not generic (`Google`, `Microsoft`, `Apple`, `Adobe`, `Utilities`, `Helper`, `Update`, `Updater` and similar). |

Every id used as a file name (bundle id, helper id, launchd label, group id,
receipt id) must be reverse-DNS: at least two non-empty dot-separated segments
of letters, digits, `.`, `-` and `_`, at most 200 characters. An app whose
bundle id is `.`, `..`, `a/b` or `com..x` is refused. Every derived path must
also sit directly in the location it was joined onto, and no item may be a
location itself or contain one (`~`, `~/Library`, `~/Library/Caches`,
`/Library`, `/Applications`, `/`, ...). Both checks run when planning and again
in `apply`, so a hand-edited plan cannot widen what is touched.

Bundle ids are compared ignoring case everywhere, because the default APFS
volume is case-insensitive: `Caches/com.Foo.App` and `Caches/com.foo.app` are
the same folder.

## Why it is conservative

Nothing is matched by substring or a shared reverse-DNS prefix: a plan for
`com.foo.app` does not include `com.foo.application`, a plan for `Code` does
not include `Xcode` or `Visual Studio Code` leftovers, and a launchd job that
merely shares a vendor prefix is not claimed.

An app can declare anything in its own Info.plist and entitlements, so a
declaration is trusted only when it points back at the app itself:

- a declared launchd label, privileged helper or `AssociatedBundleIdentifiers`
  entry is the app's own only when it is `<bundle id>` or `<bundle id>.*`;
  a job whose `Program` runs from inside the bundle is the app's own whatever
  its label;
- an app group is the app's own only when it is `group.<bundle id>[.*]`, or
  `<TEAMID>.*` for the app's verified team id. A team id counts only when
  `codesign --verify --strict <bundle>` passes and `codesign -dv` reports a
  non-ad-hoc `TeamIdentifier`; a modified, ad-hoc or unsigned bundle has no
  team id, so its team groups are shared. `--strict` also rejects a bundle
  that carries Finder metadata or other extended attributes ("resource fork,
  Finder information, or similar detritus not allowed"), which is common for
  apps copied out of a download; such a bundle has no team id either, and its
  team groups are report-only. This is deliberate: Spectra does not strip
  attributes or relax the check to recover a team id.

An id can also be Apple's or another app's. So, in every mode:

- any `com.apple.*` id (the main id, a helper id, a declared label, or a
  `group.com.apple.*` group) is report-only unless the bundle satisfies
  `codesign --verify --strict -R='anchor apple'`. Apple's own apps under
  `/System` stay refused as system apps; an Apple app in `/Applications`
  that passes may proceed. Apple's App Store apps (Keynote, Pages, Numbers,
  Xcode and the like) are signed through the App Store, fail `anchor apple`,
  and so stay report-only; this is a deliberate conservative choice, not a
  bug.
- when Spotlight does not answer (see below), every `com.apple.*` item is
  report-only, even for a bundle that passes `anchor apple`: a copy of a
  system app elsewhere on disk may be the real owner of that data.
- a bundle id of fewer than three segments (`com.google`) is a likely vendor
  prefix: its id-derived items are report-only unless the app has a verified
  team id.

Everything else is reported with `shared: true` and never acted on:

- a declared label, helper or group outside the rules above (for example
  Docker's `com.docker.vmnetd` daemon, or `group.com.docker` for
  `com.docker.docker`);
- a group container another installed app's entitlements also list, judged
  across `/Applications`, `/Applications/Utilities`, `~/Applications`,
  `/System/Applications`, `/System/Library/CoreServices` and
  `/System/Library/CoreServices/Applications` (where Archive Utility and
  similar system apps live), plus one folder below each
  (`/Applications/<Vendor>/Foo.app`) and a top-level symlinked `.app` such as
  `/Applications/Safari.app`; a symlink that resolves to the planned bundle,
  or a path equal to it ignoring case, is the same app, not a second copy;
- a `<TEAMID>.*` group container this app's entitlements do not list;
- every id-derived item when another installed app has the same bundle id,
  ignoring case (a second copy in `~/Downloads`, or `com.Foo.App` next to
  `com.foo.app`), or an id under this app's id (`com.google.Chrome` for an
  app whose id is `com.google`); the note names the other app;
- an item whose helper id or declared label is another installed app's
  bundle id.

Other installed apps are found in the folders above and, read-only, with
`mdfind "kMDItemCFBundleIdentifier == '<id>'cd"` (and `'<id>.*'`, plus each
helper id and label) under a 5-second deadline. The same query also asks for
`com.apple.finder` as a canary: `mdfind` exits 0 with no hits when indexing
is off, so Spotlight counts as having answered only when Finder is among the
hits. Any hit outside the planned bundle (ignoring case, after resolving
symlinks) and the Trash counts as installed. If Spotlight does not answer, the
plan carries a warning, only the folder scan is used, every `com.apple.*`
item is report-only, and `--leftovers` refuses outright.

An item is never planned, and a plan that lists one is rejected, when its
location folder (`~/Library/Caches`, `Application Support`, `Containers` and
so on) is a symbolic link, or when its parent folder resolves somewhere else
through a symbolic link higher up (a symlinked `~/Library`, say). The plan
lists each such item under its warnings.

Files a receipt installed (`receipt_file`) are always report-only, with a
`pkgutil --file-info` command to check them: another installer may have
replaced the file since (a Homebrew symlink at the same path, say).

Name-only matches are `confidence: low` and `apply` skips them unless
`--include-low-confidence` is passed.

## What apply does

`apply` re-reads the bundle and rebuilds the matched set before touching
anything. The plan is rejected outright when its identity no longer matches the
bundle on disk, it was built for another user, an id or path fails the checks
above, a path is listed more than once (under any kind or reason, ignoring
case), or any item that still exists is not in the app's matched set. Per
item, a path that is gone is skipped (`gone`), and a change of kind, reason,
owner, file type or size class (more than 1 MiB and more than half), or an
item the plan reported as shared, is skipped as drift.

If the plan's bundle no longer exists, or is now a different app, `apply`
refuses. With `--leftovers` it acts only on the exact `<location>/<bundle id>`
paths in the plan (no helper ids, ByHost files, launchd jobs, groups, receipts
or name matches; those are skipped), and refuses outright for a `com.apple.*`
id, an id of fewer than three segments, an id that any installed app still
has or sits under, or when Spotlight does not answer. `--leftovers` is refused
while the bundle is still installed.

Then, in order:

1. If the bundle will not be moved (it is root-owned, as App Store apps often
   are, drifted, or was taken out of the plan), apply stops before changing
   anything. `--data-only` moves the data anyway and lists the bundle under
   `needs_root`.
2. If any process runs from the bundle, apply refuses. With `--quit` it sends
   SIGTERM to each one after checking its pid and start time again, waits up to
   10 seconds, and aborts if any remain; no launch agent has been booted out
   and nothing has moved at that point. It never sends SIGKILL.
3. It creates a new folder `~/.Trash/<bundle id> uninstalled <date time>` with
   `mkdir`, which fails if anything already has that name (then it tries
   ` 2`, ` 3` and so on), and renames the bundle into it. This proves the
   bundle can move before launchd is touched: if the move fails (App
   Management, an immutable flag), apply stops there with no agent booted out
   and nothing else moved, unless `--data-only` is passed.
4. It runs `launchctl bootout gui/<uid>/<label>` for your own launch agents in
   the plan whose label is the bundle id or under it, never for a
   `com.apple.*` label. A job that only runs from the bundle under another
   label has its plist moved but is not booted out (the same rule decides
   whether a `needs_root` command includes a bootout). A bootout that fails
   for any reason other than the job not being loaded ("No such process",
   "Could not find specified service") is reported with `failed: true`, that
   agent's plist is left in place (skipped as `not moved: launchctl bootout
   failed`), and apply exits 3. It then checks the
   processes again, from the old path and from the bundle's place in the
   Trash. If the app started again in between (a `KeepAlive` agent, say), it
   reports the new processes under `relaunched` and stops with only the
   bundle moved.
5. It renames each of your remaining items into the same folder. Nothing
   already in the Trash can be overwritten. A symlink is moved as the link;
   its target is not touched. Nothing is deleted, copied or followed; a move
   across filesystems fails and is reported.

`--dry-run` performs every check and prints the same result without sending
signals, booting out jobs, creating the Trash folder or moving files. `apply`
refuses to run as root.

## What needs root

Items owned by another user or root (an App Store bundle, `/Library` support
files, launch daemons, privileged helpers, receipts) are listed under
`needs_root` with a command you can review and run yourself, for example:

```bash
sudo mv -n -- '/Library/Application Support/com.foo.app' '/Users/me/.Trash/'
sudo pkgutil --forget 'us.zoom.pkg.videomeeting'
```

A receipt's `.bom` and `.plist` share one `pkgutil --forget` command, shown
once. Spectra never runs these.

## Finishing after a partial failure

`apply` never undoes a move, so a failed run can leave part of the plan done.
What to do depends on where it stopped:

- **The quit timed out, the bundle would not move, or the plan was
  rejected.** Nothing was booted out or moved (with `--data-only`, the data
  may have moved but the bundle stayed). Fix the cause and run
  `spectra uninstall plan` again for a fresh plan, then `apply` it.
- **The app started again after the bootout (`relaunched`).** The bundle is
  already in the Trash folder `apply` printed, and nothing else moved. Quit
  the app, move the bundle back from that Trash folder to its old path, then
  make a fresh plan and apply it. This is the only way to finish the launch
  agents, privileged helpers, groups, ByHost files and receipts: they are
  matched through the bundle, so they need a plan made while the bundle
  exists.
- **Some items failed to move** (`move failed:` in `skipped`, exit code 3).
  The bundle and the other items are in the Trash. Fix the cause (a
  cross-volume path, a permission) and move those items yourself, or use
  `--leftovers` as below.
- **The bundle is gone and only data remains.** `apply --plan plan.json
  --leftovers` moves only the exact `<location>/<bundle id>` folders and files
  still in the plan (`Application Support/<id>`, `Caches/<id>`,
  `Preferences/<id>.plist` and the like). It does not cover helper ids,
  `ByHost` preferences, launch agents or daemons, privileged helpers, group
  containers, receipts or name matches; those are listed as skipped, and you
  check and remove them yourself (a launch agent still loaded needs
  `launchctl bootout gui/<uid>/<label>` first). It refuses for `com.apple.*`
  ids, ids of fewer than three segments, ids another installed app has or
  sits under, and when Spotlight does not answer.

## Limitations

- Login items registered through Background Task Management with no plist on
  disk, and symlinked launchd plists, are not inspected.
- A `.app` path that is a symbolic link (such as `/Applications/Safari.app`,
  which points into the system volume) is refused with the link's target; plan
  the target instead if it is not a system app.
- Permissions are judged from mode bits; ACLs and SIP/App Management
  protection can still make a move fail, which is reported per item.
- Other installed apps are found by listing the folders above (and one level
  below them) and through Spotlight. An app outside those folders on a volume
  Spotlight does not index is not seen; a copy in the Trash is ignored.
- Receipts are found with `pkgutil --file-info` on the bundle; an installer
  whose receipt does not resolve that way is not matched.
