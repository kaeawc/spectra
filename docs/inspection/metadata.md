# Bundle metadata

The cheap-to-extract structural facts about an app: bundle ID,
versions, architectures, on-disk size, packaging, MAS receipt.

## Sources

| Field | Source |
|---|---|
| `BundleID` | `Info.plist` `CFBundleIdentifier` |
| `AppVersion` | `Info.plist` `CFBundleShortVersionString` |
| `BuildNumber` | `Info.plist` `CFBundleVersion` |
| `ElectronVersion` | Electron Framework's own `Info.plist` `CFBundleVersion` |
| `Architectures` | `file <executable>` parsed for `arm64` / `x86_64` |
| `BundleSizeBytes` | walk over bundle, sparse-aware (see [storage-footprint.md](storage-footprint.md)) |
| `TeamID` | `codesign -dv` |
| `SparkleFeedURL` | `Info.plist` `SUFeedURL` |
| `MASReceipt` | `Contents/_MASReceipt/` directory exists |
| `Packaging` | derived from frameworks present (Sparkle, Squirrel) |

## plutil for plist extraction

All `Info.plist` reads go through `plutil`:

```bash
plutil -extract <key> raw -o - <path>            # single key, raw
plutil -convert xml1 -o - <path>                 # full plist as XML
```

`raw` mode returns just the value with no XML wrapping — ideal for
single-key reads. The XML conversion is used when a regex needs to
match many keys at once (e.g. `NS*UsageDescription`).

## Architectures

Spectra parses `file <exe>` output rather than reading the Mach-O
header directly. `file` already understands universal binaries
(reports both architectures on separate lines) and is fast enough
that the fork cost is irrelevant.

```
arch: arm64,x86_64       # universal
arch: arm64              # Apple Silicon only (newer apps)
arch: x86_64             # Intel only (old / abandoned apps)
```

## Packaging

Inferred from the frameworks present in `Contents/Frameworks/`:

- `Sparkle.framework` → Sparkle (the de-facto auto-updater for native
  Mac apps).
- `Squirrel.framework` → Squirrel (the auto-updater bundled with
  electron-updater; also used by some non-Electron apps like Ollama).
- Neither → unmanaged updates, or distributed only through the Mac
  App Store.

Note: Squirrel.framework alone doesn't imply Electron — Ollama ships it
without Electron Framework. Spectra tracks them as independent signals.

## Sample output (verbose)

```
Claude
  id: com.anthropic.claudefordesktop  version: 1.5354.0
  arch: arm64,x86_64  size: 690 MB
  electron: 41.3.0
  team: Q6L2SF6YDW
```

## Implementation reference

`internal/detect/detect.go`:
- `populateMetadata(appPath, exe, *Result)` is the single entry point.
- `readPlistString(path, key)` — `plutil -extract` wrapper.
- `readArchitectures(exe)` — `file` parser.
- `bundleSize(path)` — recursive walk with `diskBytes`.
