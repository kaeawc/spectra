# Objective-C based app inspection

Objective-C based apps are native Cocoa/AppKit bundles where the main
executable links AppKit but does not link Swift runtime dylibs. Spectra
classifies these as `AppKit (Obj-C)` and treats the result as a real
native Mac app, not an unknown fallback.

This page documents the inspection signals that make that verdict useful
for debugging older Cocoa apps, mixed C/C++ apps using Cocoa shells, and
toolkit apps that ultimately host their UI through AppKit.

## Detection boundary

Objective-C based inspection starts from the Layer 2 framework verdict:

| Signal | Interpretation |
|---|---|
| `/AppKit.framework/` or `/Cocoa.framework/` linked | Native Mac UI surface |
| No `libswift*.dylib` linked | No Swift runtime evidence |
| No `/SwiftUI.framework/` linked | Not SwiftUI-first |
| No `/WebKit.framework/` linked | Not a WebKit shell such as Tauri/Wails/custom Go bridge |
| No Catalyst paths | Not UIKit-on-Mac |

The verdict is intentionally medium confidence. It says "native AppKit
without stronger language/runtime evidence", which is the correct result
for many long-lived Mac apps.

## What Spectra inspects

Objective-C apps are often diagnostic-heavy even when the classifier
surface is small. The useful inspection is a bundle-level profile, not a
single marker.

| Area | Source | Why it matters |
|---|---|---|
| Linked frameworks | `otool -L <executable>` | Shows AppKit, Cocoa, WebKit, Security, CoreData, SQLite, Sparkle, and private toolkit dependencies |
| App delegate / principal class | `Info.plist` `NSPrincipalClass`, `NSMainNibFile`, `NSMainStoryboardFile` | Distinguishes nib/storyboard-era Cocoa apps from custom launcher shells |
| Document types | `Info.plist` `CFBundleDocumentTypes` | Explains Finder integrations and file ownership |
| URL schemes | `Info.plist` `CFBundleURLTypes` | Reveals deep-link and IPC entry points |
| Services | `Info.plist` `NSServices` | Shows macOS Services menu integrations |
| Apple events | Entitlements and usage strings | Explains automation, scripting, and inter-app control needs |
| Helpers and XPC | `Contents/Frameworks/`, `Contents/XPCServices/`, `Contents/PlugIns/` | Finds updater helpers, crash reporters, importers, Quick Look generators, and extensions |
| Persistence | Bundle ID derived paths in `~/Library` | Finds preferences, Application Support, caches, logs, containers, and saved state |
| Update mechanism | `Contents/Frameworks/`, `Info.plist` | Native apps commonly use Sparkle, MAS receipts, or no embedded updater |

These rows overlap with the general metadata, security, helper, and
storage collectors. The `ObjC` result profile assembles the AppKit-native
pieces that are not obvious from the top-level verdict alone.

## Common patterns

### Classic Cocoa app

Signals:

- AppKit or Cocoa linked.
- `NSMainNibFile` or `NSPrincipalClass` present.
- No Swift dylibs.
- Zero or one helper.
- Optional Sparkle framework.

Examples include older productivity tools, utilities, and apps that have
kept a stable Objective-C codebase for years.

### C++ toolkit with AppKit host

Signals:

- AppKit linked because every Mac GUI eventually talks to AppKit.
- Large C++ dependency surface.
- Toolkit-specific frameworks or dylibs.
- No Swift dylibs.

wxWidgets, custom OpenGL/Metal editors, and some game/editor shells can
land here. Spectra reports the AppKit verdict while leaving enough
linked-framework detail to explain that the app is not a plain Cocoa UI.

### Objective-C shell around another runtime

Signals:

- Small launcher executable.
- Larger sibling or framework binary.
- AppKit linked in the launcher.
- Runtime evidence appears only after following the real executable.

The shim and wrapper handling in the classifier exists for this case. If
the real binary contains Go, Rust, JVM, Electron, or WebKit evidence, that
stronger runtime verdict should win.

## Output guidance

Verbose output should keep the framework verdict concise and then expose
the supporting bundle facts:

```text
Alfred 5
  ui: AppKit (Obj-C)  confidence: medium
  id: com.runningwithcrayons.Alfred  version: 5.x
  arch: arm64,x86_64  team: XZZXE9SED4
  packaging: Sparkle
  helpers: Alfred Preferences
```

JSON output uses the canonical `ObjC` field documented in
[`result-schema.md`](../reference/result-schema.md). The profile is
present only for plain AppKit/Objective-C verdicts.

## Limitations

- Objective-C symbols are often stripped in release builds, so method or
  class-name inspection is best-effort enrichment, not a classifier
  requirement.
- AppKit linkage alone does not prove that all UI is hand-written Cocoa.
  Toolkit and game apps still rely on AppKit at the platform boundary.
- Absence of Swift dylibs is not proof that no Swift code exists anywhere
  in the bundle; a plugin or helper may carry Swift independently.
- Private framework names can be useful diagnostically, but Spectra
  reports them as linked dependencies rather than inferring unsupported
  private API behavior.

## Implementation reference

`internal/detect/detect.go`:

- Layer 2 `otool -L` parsing produces the `AppKit (Obj-C)` verdict when
  AppKit/Cocoa is present without Swift, SwiftUI, WebKit, or Catalyst
  signals.
- Shim and wrapper resolution should run before binary-content scanning so
  Objective-C launcher shells do not hide stronger runtime evidence.
- Reuse existing collectors for metadata, security, helpers/XPC, login
  items, running processes, and storage footprint.

Related docs:

- [../detection/overview.md](../detection/overview.md) — classifier layers.
- [../detection/frameworks.md](../detection/frameworks.md) — AppKit
  framework signal table.
- [metadata.md](metadata.md) — bundle plist and architecture fields.
- [helpers-and-xpc.md](helpers-and-xpc.md) — embedded helpers, XPC services,
  and plugins.
