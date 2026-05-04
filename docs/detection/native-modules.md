# Electron native module sub-detection

Confirmed Electron apps get an additional pass that walks
`Contents/Resources/app.asar.unpacked/**/*.node` and classifies each
native add-on by source language. This reveals the architectural split
between vanilla Electron apps (only off-the-shelf C++ deps) and hybrid
apps with custom Rust/Swift code.

## Classification rules

Per `.node` file, in order:

1. **Rust signal #1 — Cargo target path.** If the link map contains
   `/target/.../-apple-darwin/release/` or `.../debug/`, classify as
   Rust. The build path leaks the language definitively.
2. **Swift signal.** If the link map references `libswift*.dylib`,
   `Swift.framework`, OR an `@rpath/` sidecar dylib named like
   `libSwift*` / `libClaudeSwift`, classify as Swift. Notable linked
   frameworks (ScreenCaptureKit, AVFAudio, Combine, Metal, AppKit) are
   surfaced as hints.
3. **Rust signal #2 — panic strings.** If the `.node` file or any
   `@rpath`-resolved sidecar has 50+ Rust panic-site strings, classify
   as Rust.
4. **Default — C++.** If `libc++` is linked (the C++ standard library),
   note it. Otherwise unannotated.

The `.dSYM/` directories are skipped (they're debug symbols for the
.node files, not separate modules).

## Empirical examples

### Claude — Rust + Swift hybrid

```
[Rust]  claude-native-binding.node
        cargo target path in link map
[Swift] computer_use.node
        links Swift runtime / Swift sidecar dylib
        uses ScreenCaptureKit
        uses AppKit
[Swift] swift_addon.node
        links Swift runtime / Swift sidecar dylib
        uses ScreenCaptureKit, AVFAudio, Combine, AppKit
[C++]   pty.node
        links libc++
```

The Cargo target path leaked the GitHub Actions build root:
`/Users/runner/work/apps/apps/packages/desktop/claude-native/target/x86_64-apple-darwin/release/deps/libclaude_native.dylib`
— revealing that Anthropic's repo layout is `apps/packages/desktop/claude-native/`
and CI builds on `runner` images.

`computer_use.node` linking ScreenCaptureKit is the binding that powers
Claude's computer-use feature. ScreenCaptureKit is weak-linked so older
macOS versions can still load the rest of the app.

### 1Password — Swift bridge

```
[Swift] index.node
        links Swift runtime / Swift sidecar dylib
        uses AppKit
```

A custom Swift native module presumably for SecureEnclave or Keychain
access.

### Codex — vanilla Electron

```
[C++]   better_sqlite3.node    links libc++
[C++]   pty.node               links libc++
```

Off-the-shelf SQLite binding and node-pty. No custom native code.

## Why this matters

The top-level UI verdict ("Electron") is the same for Codex and Claude,
but the architectural reality is very different. A hybrid Electron app
can do things vanilla Electron cannot — talk to ScreenCaptureKit, run
Rust hot paths, access Apple-private frameworks directly through Swift.
For diagnostic purposes, "Electron with N native modules" is a more
useful classification than "Electron."

## Implementation reference

`internal/detect/detect.go`:
- `scanNativeModules(appPath)` — walks the unpacked node_modules tree.
- `classifyNativeModule(absPath, relPath)` — single-module language
  inference.

## Future ideas

- Surface module versions by reading the package's `package.json` if
  present in the unpacked tree.
- Detect specific high-signal modules by name (e.g. `node-keytar` →
  "uses macOS Keychain"; `node-mac-notifier` → "uses Notification Center")
  and surface that as a capability hint.
- Cross-reference with Electron's known security-sensitive modules to
  flag risk patterns.
