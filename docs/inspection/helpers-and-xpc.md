# Helpers, XPC services, and plugins

Apps embed sub-bundles for several reasons: Chromium-style multi-process
architectures, sandboxed background tasks, system extensions. Spectra
enumerates all of them.

## Categories

| Path | Type | Purpose |
|---|---|---|
| `Contents/Frameworks/<Name>.app/` | Helper sub-app | Multi-process architecture (Chromium, Electron, Chrome) |
| `Contents/XPCServices/<Name>.xpc/` | XPC service | Sandboxed background task with IPC |
| `Contents/PlugIns/<Name>.appex` | App Extension | Shipped extensions (Share, Calendar, Notification Center) |
| `Contents/PlugIns/<Name>.plugin` | Bundle plugin | App-defined plugin format (Xcode, etc.) |
| `Contents/PlugIns/<Name>.ideplugin` | IDE plugin | Xcode-specific |

Spectra reports the basename (without the type suffix) for helpers and
XPC services; plugins are reported with their full filename because the
suffix carries meaning.

## Sample output

### Electron's four-helper pattern

```
Claude
  helpers (4): Claude Helper, Claude Helper (GPU),
               Claude Helper (Plugin), Claude Helper (Renderer)
```

This is the standard Chromium multi-process layout that Electron
inherits. Each helper has different entitlements (the GPU helper allows
JIT, the renderer is more restricted). All Electron apps ship this
pattern; Spectra surfaces it consistently.

### Apps with custom XPC services

```
Tuple
  xpc services (3): tuple-log, tuple-webcam-capturer, window-veil
  plugins (1): TupleCalendar.appex
```

Tuple ships purpose-built XPC services for logging, webcam capture, and
the screen-darkening overlay. The Calendar app extension adds Tuple
events to the system Calendar.

```
1Password
  helpers (4): 1Password Helper, 1Password Helper (GPU),
               1Password Helper (Plugin), 1Password Helper (Renderer)
  xpc services (1): OP Updater Service
```

1Password's electron helpers plus a dedicated updater XPC.

### Xcode's plugin sprawl

```
Xcode
  plugins: AppShortcutsEditor.framework, DVTCorePlistStructDefs.dvtplugin,
           DebugHelperSupportUI.ideplugin, DebuggerKit.framework,
           DebuggerLLDB.framework, DebuggerLLDBService.ideplugin, ...
```

Xcode's `PlugIns/` is dozens of debugger and IDE-component bundles.

### Single-tile Dock plugin

```
Ghostty
  plugins (1): DockTilePlugin.plugin
```

The Dock-tile plugin shows up while the app is dragged out of the dock.

## Why this matters

- **Helper count is a quick architectural fingerprint.** Vanilla
  Electron = 4 helpers. Tauri = 0. Native AppKit = 0–1 (e.g. for the
  occasional crash-reporter helper).
- **XPC services reveal capability boundaries.** A custom `*Updater`
  XPC means the app does background updates outside the main process.
  A `*WebcamCapture` XPC tells you what device APIs the app uses.
- **Plugins reveal extensibility.** A non-Apple `.appex` extension
  shows the app has hooks into system surfaces (Calendar, Share menu,
  Notification Center).

## Implementation reference

`internal/detect/detect.go`:
- `scanHelpers(appPath) *Helpers`
- Strips known suffixes (`.app`, `.xpc`) from helper/XPC names; keeps
  full filename for plugins.
- Sorted output for stable JSON.
