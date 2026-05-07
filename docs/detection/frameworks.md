# Framework signal table

Empirical signal map for every framework Spectra can detect, with
confirmed examples from real apps. Layer numbers refer to
[overview.md](overview.md).

## Web/JS-based

### Electron
- **Layer 1.** `Frameworks/Electron Framework.framework/` AND
  (`Resources/app.asar` OR `Resources/app/`).
- **Confirmed:** Slack, Discord, Notion, VS Code, Cursor, Claude, Codex,
  Obsidian, 1Password, Figma, Goose, Signal, Antigravity, Motionscribe,
  ComfyUI, Screen Studio, LM Studio.
- **Distinct sub-architectures detectable via native modules:**
  - **Vanilla Electron** (Codex): only off-the-shelf C++ modules
    (node-pty, better-sqlite3).
  - **Hybrid Electron** (Claude): custom Rust + Swift native modules.
  - **Hybrid Electron** (1Password): custom Swift native module.

### Tauri (Rust + WebKit)
- **Layer 2:** AppKit + WebKit linked.
- **Layer 3:** Rust panic strings ≥ 100.
- **Confirmed:** Conductor (204 Rust strings), Polyscope.
- **Inspection details:** [../inspection/tauri.md](../inspection/tauri.md).

### Wails (Go + WebKit)
- **Layer 2:** AppKit + WebKit linked.
- **Layer 3:** Go buildinfo present AND `github.com/wailsapp/wails`
  string present.
- **Confirmed:** none on this dev machine.

### Go+WebKit (custom bridge)
- **Layer 2:** AppKit + WebKit linked.
- **Layer 3:** Go buildinfo present, no Wails marker.
- **Confirmed:** Ollama (custom Go+Cocoa+WebKit binary, not Wails).

### React Native macOS
- **Layer 1:** `Frameworks/React.framework/` or `hermes.framework/`.
- **Confirmed:** none on this dev machine.

### Flutter
- **Layer 1:** `Frameworks/FlutterMacOS.framework/`.
- **Confirmed:** none on this dev machine.

## Native Apple

### SwiftUI
- **Layer 2:** `/System/Library/Frameworks/SwiftUI.framework/` linked.
- **Confirmed:** Keynote, Numbers, Ghostty, Tuple, iTerm (hybrid),
  Muse Hub, ChatGPT (no — see AppKit+Swift), Canary Mail, Airtime,
  InYourFace, MindNode, GarageBand.
- **Inspection:** see [../inspection/swift-apps.md](../inspection/swift-apps.md).

### AppKit + Swift
- **Layer 2:** `libswift*.dylib` linked but no SwiftUI.
- **Confirmed:** ChatGPT, Pages, RemotePlay.
- **Inspection:** see [../inspection/swift-apps.md](../inspection/swift-apps.md).

### AppKit (Obj-C)
- **Layer 2:** AppKit/Cocoa linked, no Swift dylibs.
- **Confirmed:** Telegram, Sublime Text, Steam, Alfred 5, Audacity
  (wxWidgets-based, falls under AppKit), Godot.
- **Inspection:** See
  [../inspection/objc-based-app.md](../inspection/objc-based-app.md).

### Mac Catalyst
- **Layer 2:** Linked path contains `/iOSSupport/...UIKit*.framework/`
  or `/UIKitMacHelper.framework/`.
- **Confirmed (system apps):** Messages, News, Home, Stocks,
  Books (+SwiftUI), Maps (+SwiftUI), Podcasts (+SwiftUI).
- **Confirmed (user apps):** Swift Playground (+SwiftUI).

### MacCatalyst+SwiftUI
- **Layer 2:** Both Catalyst signal AND SwiftUI framework linked.
- The most modern Catalyst pattern — UIKit-on-Mac with SwiftUI on top.

## JVM-based

### Compose Desktop (Kotlin Multiplatform)
- **Layer 1:** Bundled JVM (`runtime/`, `jbr/`, or `jre/`) AND
  `libskiko-macos-*.dylib` somewhere in bundle.
- **Confirmed:** Arbigent, JetBrains Toolbox, Firebender.

### Eclipse RCP
- **Layer 1:** `org.eclipse.osgi*` jar present (with or without bundled
  JVM — some Eclipse apps reference system Java).
- **Confirmed:** Memory Analyzer (210 jars, no embedded JVM),
  JProfiler (with bundled JRE).

### NetBeans Platform
- **Layer 1:** `org-netbeans-*.jar` files present.
- **Confirmed:** VisualVM (138 jars, no embedded JVM).

### install4j Swing
- **Layer 1:** `i4jruntime.jar` or `.install4j` in bundle.
- **Confirmed:** JProfiler.

### Generic JVM (Swing/JavaFX)
- **Layer 1:** Bundled JVM only, no Skiko / Eclipse / NetBeans / install4j.
- Confidence: medium (we don't know the toolkit).

## Other native

### Qt
- **Layer 1:** `Frameworks/QtCore.framework/` or `Resources/qt.conf`.
- **Confirmed:** Parallels Desktop.

### Native (Rust)
- **Layer 3:** Rust panic strings ≥ 100, no AppKit+WebKit (so not Tauri).
- **Confirmed:** Warp (699 strings), Zed (384 strings).

### Native (Go)
- **Layer 3:** Go buildinfo, no AppKit+WebKit.
- **Confirmed:** Docker.

## Edge-case taxonomy

### Shim launchers
- Tiny CFBundleExecutable + matching `<AppName> Framework.framework` →
  follow to the framework binary for L2/L3.
- **Confirmed:** Google Chrome (240 MB framework binary inside).

### Wrappers
- Tiny CFBundleExecutable with a larger sibling binary in `MacOS/`.
- **Confirmed:** Audacity (`MacOS/Wrapper` → `MacOS/Audacity`).

### Honest mediums
- Old Cocoa apps with no clear modern signal classify as bare AppKit at
  medium confidence. This is the right answer — they're real native
  AppKit/Obj-C apps that predate the framework taxonomy we're applying.
- Game engines (Godot) get the same treatment.

### Honest unknowns
- Safari has no `Contents/Frameworks` directory at all. Its real
  implementation lives in system frameworks. Falls through to Unknown.
  This is a uniquely structured system bundle.
