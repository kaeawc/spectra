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
  `<TEAMID>.*` for the app's team id as `codesign` reports it (an ad-hoc or
  unsigned app has no team id, so its team groups are shared).

Everything else is reported with `shared: true` and never acted on:

- a declared label, helper or group outside the rules above (for example
  Docker's `com.docker.vmnetd` daemon, or `group.com.docker` for
  `com.docker.docker`);
- a group container another installed app's entitlements also list, judged
  across `/Applications`, `/Applications/Utilities`, `~/Applications`,
  `/System/Applications` and `/System/Applications/Utilities`;
- a `<TEAMID>.*` group container this app's entitlements do not list;
- every id-derived item when another installed app in those folders has the
  same bundle id, ignoring case (a second copy in `~/Downloads`, or
  `com.Foo.App` next to `com.foo.app`); the note names the other app.

Files a receipt installed (`receipt_file`) are always report-only, with a
`pkgutil --file-info` command to check them: another installer may have
replaced the file since (a Homebrew symlink at the same path, say).

Name-only matches are `confidence: low` and `apply` skips them unless
`--include-low-confidence` is passed.

## What apply does

`apply` re-reads the bundle and rebuilds the matched set before touching
anything. The plan is rejected outright when its identity no longer matches the
bundle on disk, it was built for another user, an id or path fails the checks
above, or any item that still exists is not in the app's matched set. Per
item, a path that is gone is skipped (`gone`), and a change of kind, reason,
owner, file type or size class (more than 1 MiB and more than half) is skipped
as drift.

If the plan's bundle no longer exists, or is now a different app, `apply`
refuses. With `--leftovers` it acts only on the exact `<location>/<bundle id>`
paths in the plan (no helper ids, ByHost files, launchd jobs, groups, receipts
or name matches; those are skipped), and refuses outright for a `com.apple.*`
id or an id that any installed app still has. `--leftovers` is refused while
the bundle is still installed.

Then, in order:

1. If the bundle will not be moved (it is root-owned, as App Store apps often
   are, drifted, or was taken out of the plan), apply stops before changing
   anything. `--data-only` moves the data anyway and lists the bundle under
   `needs_root`.
2. If any process runs from the bundle, apply refuses. With `--quit` it sends
   SIGTERM to each one after checking its pid and start time again, waits up to
   10 seconds, and aborts if any remain; no launch agent has been booted out
   and nothing has moved at that point. It never sends SIGKILL.
3. Once the app has quit, it runs `launchctl bootout gui/<uid>/<label>` for
   your own launch agents in the plan, then checks the processes again. If the
   app started again in between (a `KeepAlive` agent relaunching it, say), it
   reports the new processes under `relaunched` and stops without moving
   anything; run `apply --quit` again.
4. It creates a new folder `~/.Trash/<bundle id> uninstalled <date time>` with
   `mkdir`, which fails if anything already has that name (then it tries
   ` 2`, ` 3` and so on), and renames the bundle and then each of your items
   into it. Nothing already in the Trash can be overwritten. A symlink is moved
   as the link; its target is not touched. Nothing is deleted, copied or
   followed; a move across filesystems fails and is reported. If the bundle
   move itself fails (App Management, an immutable flag), the data stays where
   it is unless `--data-only` is passed.

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

## Limitations

- Login items registered through Background Task Management with no plist on
  disk, and symlinked launchd plists, are not inspected.
- A `.app` path that is a symbolic link (such as `/Applications/Safari.app`,
  which points into the system volume) is refused with the link's target; plan
  the target instead if it is not a system app.
- Permissions are judged from mode bits; ACLs and SIP/App Management
  protection can still make a move fail, which is reported per item.
- Other installed apps are found by listing the folders above directly, not
  through Spotlight; an app elsewhere is not seen.
- Receipts are found with `pkgutil --file-info` on the bundle; an installer
  whose receipt does not resolve that way is not matched.
