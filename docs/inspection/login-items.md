# Login items and launchd plists

Spectra enumerates LaunchAgent and LaunchDaemon plists installed on the
system that belong to a given app, regardless of whether they're
currently running. These are the persistent background processes apps
register with `launchd`.

## Locations scanned

| Path | Scope | Runs as |
|---|---|---|
| `~/Library/LaunchAgents` | user | the user |
| `/Library/LaunchAgents` | system | the user (any user that logs in) |
| `/Library/LaunchDaemons` | system | root |

Spectra reports each as user / system / daemon respectively.

## Attribution

A plist is attributed to an app if either condition holds:

1. **The plist filename starts with the bundle's reverse-DNS prefix**
   (first two segments). For `com.docker.docker`, the prefix is
   `com.docker`, and any plist starting with `com.docker.` matches.
2. **The plist's `ProgramArguments` reference the app bundle path**
   inside the converted XML. Catches cases where the launchd label
   doesn't follow the bundle ID convention.

Plist content is read via `plutil -convert xml1 -o -` so binary plists
are handled transparently.

## Sample output

```
Docker
  login items (2): com.docker.socket (daemon), com.docker.vmnetd (daemon)
```

Both Docker daemons are root-running. `vmnetd` provides the bridged
networking; `socket` is the Docker socket bridge.

```
JetBrains Toolbox
  login items (1): com.jetbrains.toolbox
```

User-scope LaunchAgent that auto-starts the toolbox at login.

## Why this matters

- **Persistence audit.** Tells you what a given app has installed that
  outlasts its main process.
- **Privilege awareness.** A `(daemon)` label means the plist runs as
  root — material for trust evaluation.
- **Cleanup hint.** When uninstalling an app, the launchd plists
  often persist after the .app is dragged to Trash. The classic
  zombie-process pattern. This list tells you what to clean up.

## Limitations

- Bundle IDs with only one segment (no dot) can't be prefix-matched and
  fall back to ProgramArguments scanning.
- Apps that install plists under unrelated names (e.g. via a third-party
  installer) and don't reference the app bundle path may be missed.
- The `RunAtLoad` and `KeepAlive` flags from the plists aren't surfaced
  yet — knowing whether a daemon stays alive is part of the picture
  Spectra should eventually expose.

## Implementation reference

`internal/detect/detect.go`:
- `scanLoginItems(appPath, bundleID) []LoginItem`
- `bundleIDPrefix(bundleID) string` — returns the first two reverse-DNS
  segments
- `plistMentionsAppPath(plistPath, appPath) bool` — XML-converted scan
