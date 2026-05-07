# Detection overview

Spectra classifies a `.app` bundle's UI framework and language runtime by
running three layers of analysis, strongest signal first. Most apps are
decided by Layer 1 alone; layers 2 and 3 disambiguate edge cases like
"AppKit shell that's actually a Rust binary."

## The three layers

### Layer 1 — Bundle markers

Cheap, decisive. Looks for named directories and files inside
`Contents/`. If a definitive marker is found, classification is high
confidence and layers 2/3 are skipped for the verdict (they still run
for enrichment metadata).

| Framework | Required marker |
|---|---|
| Electron | `Frameworks/Electron Framework.framework/` AND (`Resources/app.asar` OR `Resources/app/`) |
| Flutter | `Frameworks/FlutterMacOS.framework/` |
| Qt | `Frameworks/QtCore.framework/` OR `Resources/qt.conf` |
| React Native | `Frameworks/React.framework/` OR `Frameworks/hermes.framework/` |
| Compose Desktop / KMP | bundled JVM (`runtime/`, `jbr/`, or `jre/`) AND `libskiko-macos-*.dylib` |
| Eclipse RCP | `org.eclipse.osgi*` jar |
| install4j Swing | `i4jruntime.jar` or `.install4j` in bundle |
| NetBeans Platform | `org-netbeans-*.jar` |
| Generic JVM | bundled JVM only |

See [frameworks.md](frameworks.md) for the full signal table with
empirical examples.

### Layer 2 — Linked dylibs

Runs `otool -L` on the main executable and inspects what it dynamically
links. Decides between SwiftUI, AppKit+Swift, AppKit (Obj-C), and
Mac Catalyst:

| Signal | Verdict |
|---|---|
| `/SwiftUI.framework/` linked | SwiftUI |
| `libswift*.dylib` linked but no SwiftUI | AppKit+Swift |
| `/AppKit.framework/` + `/WebKit.framework/`, no Swift dylibs | AppKit+WebKit (Tauri suspect) |
| `/AppKit.framework/`, no Swift, no WebKit | AppKit (Obj-C) |
| Any `/iOSSupport/...` or `/UIKitMacHelper.framework/` | Mac Catalyst |

See [catalyst.md](catalyst.md) for the Catalyst path.
See [../inspection/swift-apps.md](../inspection/swift-apps.md) for the
deeper app-level Swift inspection surface.
See [../inspection/objc-based-app.md](../inspection/objc-based-app.md)
for the AppKit/Objective-C inspection profile.

### Layer 3 — Binary content scanning

Streams the binary searching for ASCII fingerprints that disambiguate
runtimes without dynamic library hints:

| Signal | Verdict |
|---|---|
| `\xff Go buildinf:` magic | Go (overrides any L2 verdict) |
| Combined Rust panic-site strings ≥ 100 hits | Rust (overrides L2 AppKit) |
| `github.com/wailsapp/wails` | Wails (Go + WebKit confirmed) |
| Go buildinf + AppKit+WebKit but no Wails string | Go+WebKit (custom bridge) |

The Rust threshold (100) was chosen empirically: a single bundled Rust
dylib in a non-Rust app produces under 30 hits; native Rust apps
produce hundreds. See
[../inspection/rust-based-apps.md](../inspection/rust-based-apps.md) for
the Rust-specific inspection path.

For Tauri specifically, Layer 2 first records the app as an
`AppKit+WebKit` suspect; Layer 3 promotes that verdict to `Tauri` only
when the main binary has strong Rust evidence. See
[../inspection/tauri.md](../inspection/tauri.md).

## Shim launcher handling

Some apps use a tiny launcher binary (Chrome, Edge, Brave) that loads
its real implementation from `Contents/Frameworks/<App> Framework.framework`.
When the main executable is suspiciously small (< 512 KB) and a
matching framework exists, Spectra follows the framework's binary for
layers 2 and 3 instead.

This is suppressed for Electron (already classified at L1) to avoid
noisy "shim launcher → Electron Framework" signals that don't change
the verdict.

## Wrapper handling

Some apps have a tiny `Wrapper` binary in `Contents/MacOS/` that loads
a sibling binary (e.g. Audacity ships `MacOS/Wrapper` which loads
`MacOS/Audacity`). When the resolved executable is under 256 KB and a
larger sibling exists, Spectra follows the sibling instead.

## Sub-detection: Electron native modules

For confirmed Electron apps, Spectra additionally walks
`Contents/Resources/app.asar.unpacked/**/*.node` and classifies each
native add-on by language:

- **Rust** — link map contains a Cargo target path, OR the `.node` file
  has Rust panic strings.
- **Swift** — link map references `libswift*` or a sidecar named
  `libSwift*.dylib`. Frameworks linked (ScreenCaptureKit, Combine,
  AVFAudio) are surfaced as hints.
- **C++** — links libc++ but no Rust/Swift markers.

This is what surfaces Claude's `claude-native` (Rust) +
`claude-swift/computer_use.node` (Swift, ScreenCaptureKit) hybrid
architecture vs Codex's plain off-the-shelf `node-pty` and
`better-sqlite3` C++ modules.

See [native-modules.md](native-modules.md).

## Confidence levels

- **high** — Layer 1 marker matched, OR Layer 2 had a definitive Swift /
  Catalyst signal, OR Layer 3 confirmed Rust/Go above threshold.
- **medium** — Bare AppKit detected with no further disambiguation
  (correctly applies to old Cocoa apps like Telegram, Sublime Text,
  Audacity, Steam, Alfred).
- **low** — No signal found. Typically empty bundles, Safari (no
  Frameworks/), or shim launchers we couldn't drill through.

## What we deliberately don't try to detect

- **Specific Electron app frameworks** (Forge, electron-builder,
  electron-packager). Not visible from the bundle layout.
- **Framework versions.** Electron, Flutter, Qt, and Tauri versions are
  populated best-effort in `FrameworkVersions` when the bundle exposes
  framework plists or Tauri config metadata.
- **Game engines** (Unity, Unreal, Godot's own classification). Treat
  as "AppKit native" — game apps don't fit the framework taxonomy.
