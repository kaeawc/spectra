# Swift app inspection

Swift-based macOS apps often look deceptively simple from the bundle
layout alone. Spectra treats Swift as both a framework signal
([../detection/overview.md](../detection/overview.md)) and an inspection
surface: linked Swift runtime libraries, Apple framework usage,
entitlements, privacy declarations, embedded helpers, and app-group
storage together explain what the app is built to do.

This page covers app-level Swift inspection. Swift native modules inside
Electron apps are covered separately in
[../detection/native-modules.md](../detection/native-modules.md).

## When an app is Swift-based

Spectra considers an app Swift-based when `otool -L` on the resolved
application executable shows one of these signals:

| Signal | Meaning |
|---|---|
| `/System/Library/Frameworks/SwiftUI.framework/` | SwiftUI app, or AppKit app with SwiftUI surfaces |
| `libswift*.dylib` | Swift runtime dependency; usually AppKit+Swift when SwiftUI is absent |
| `@rpath/libswift*.dylib` | Bundled Swift runtime, common on older deployment targets |
| `Frameworks/<Name>.framework/<Name>` plus Swift dylibs | Swift code moved into a private framework |

The classifier still reports the UI verdict separately: `SwiftUI`,
`AppKit+Swift`, `MacCatalyst+SwiftUI`, or another stronger framework
verdict when Layer 1 wins. The inspection rows remain useful even when
Swift is only one part of a hybrid app.

## Executable resolution

Swift apps may not put most of their code in `Contents/MacOS/<exe>`.
Before inspecting Swift signals, Spectra resolves the executable the same
way the main detector does:

- Follow tiny shim launchers into
  `Contents/Frameworks/<App> Framework.framework/<App> Framework`.
- Follow tiny wrapper binaries to a larger sibling executable in
  `Contents/MacOS/`.
- Inspect the resolved executable with `otool -L`, `file`, `codesign`,
  and binary content scanning.

This avoids classifying a launcher instead of the app implementation.

## Linked framework profile

Swift inspection records the Apple frameworks that explain app behavior.
The raw source is `otool -L <resolved-executable>`; Spectra normalizes the
paths into framework basenames and filters obvious system noise where a
field only needs third-party frameworks.

Common Swift framework signals:

| Framework | What it suggests |
|---|---|
| `SwiftUI.framework` | Declarative UI layer |
| `Combine.framework` | Reactive streams or Apple platform integration |
| `AppIntents.framework` | Shortcuts, Spotlight actions, or system intents |
| `ScreenCaptureKit.framework` | Screen sharing, recording, or computer-use workflows |
| `AVFoundation.framework` / `AVFAudio.framework` | Camera, media, microphone, or audio processing |
| `CoreBluetooth.framework` | Bluetooth device integration |
| `CoreLocation.framework` | Location access |
| `AuthenticationServices.framework` | Sign in with Apple, passkeys, web auth sessions |
| `Security.framework` / `LocalAuthentication.framework` | Keychain, Secure Enclave, Touch ID, or biometric prompts |
| `Network.framework` | Native networking stack, listeners, or path monitoring |
| `WebKit.framework` | Embedded web views; possible custom WebKit shell |

Framework presence is a capability signal, not proof of runtime behavior.
Spectra pairs it with [security inspection](security.md), TCC grants, and
[running process state](running-processes.md) before drawing conclusions.

## Entitlements and privacy

Swift apps usually interact with macOS through native frameworks, so the
interesting inspection question is whether the framework profile lines up
with declared and granted permissions.

Spectra compares:

- Entitlements from `codesign -d --entitlements :- <app>`.
- Privacy descriptions from `NS*UsageDescription` keys in `Info.plist`.
- Granted TCC services from per-user and system `TCC.db`.
- Hardened runtime, sandbox, Team ID, and Gatekeeper status.

Examples of useful mismatches:

| Framework signal | Expected companion signal |
|---|---|
| `AVFoundation.framework` or `AVFAudio.framework` | camera / microphone privacy descriptions when capture APIs are used |
| `CoreBluetooth.framework` | Bluetooth privacy description |
| `CoreLocation.framework` | location privacy description |
| `ScreenCaptureKit.framework` | ScreenCapture TCC grant may exist without an `NS*UsageDescription` key |
| `AppIntents.framework` | shortcuts-related behavior, often without a separate entitlement |
| `Network.framework` with `network.server` entitlement | local listener or peer-to-peer feature |

The recommendation engine should avoid treating a linked framework alone
as a defect. Many Apple frameworks are linked by SDK convenience or by
transitive dependencies and may never be exercised.

## Helpers, XPC, and app groups

Swift apps commonly split privileged or long-running work into embedded
targets:

- `Contents/Library/LoginItems/*.app` for launch-at-login agents.
- `Contents/XPCServices/*.xpc` for isolated services.
- `Contents/Library/LaunchServices/*` for helper executables.
- `Contents/PlugIns/*.appex` for extensions.

Each helper has its own `Info.plist`, executable, signature, and
entitlements. Spectra inspects these as separate structural rows in
[helpers-and-xpc.md](helpers-and-xpc.md) and correlates app-group
entitlements with storage under `~/Library/Group Containers/`.

For Swift apps, app groups are often the bridge between the GUI app,
extensions, and login items. A mismatched app group is more interesting
than the presence of a helper by itself.

## Storage and runtime correlation

Static Swift inspection explains what the app can do. Storage and process
inspection explain what it appears to be doing on this host:

- [storage-footprint.md](storage-footprint.md) records app container,
  group container, cache, and log sizes.
- [running-processes.md](running-processes.md) correlates live processes
  back to the bundle ID and computes app uptime.
- [network-endpoints.md](network-endpoints.md) associates listening and
  connected sockets with the app's processes.

This matters for native Swift apps because the bundle often contains very
little obvious third-party structure. The strongest operational signal may
be a long-running helper, a large group container, or a granted TCC
permission that is not obvious from the visible UI.

## Result fields

Swift inspection is exposed as a dedicated `Swift` result section. It is
derived from the same generic collectors used by the rest of app inspection:
linked libraries, entitlements, metadata, storage, TCC, helper, and process
collectors remain shared.

The dedicated section records the Swift-specific signals that would otherwise
be scattered across generic fields:

```go
type SwiftInspection struct {
    RuntimeLibraries []string // libswift*.dylib basenames
    AppleFrameworks  []string // normalized linked Apple frameworks
    UsesSwiftUI      bool
    UsesAppIntents   bool
    UsesScreenCapture bool
    AppGroups        []string
}
```

The surrounding generic fields still provide the operational context:
`UI`, `Runtime`, `Architectures`, `Entitlements`, `PrivacyDescriptions`,
`GrantedPermissions`, `Helpers`, `Dependencies`, and
process/network/storage rows.

Do not add a Swift-only subprocess path unless a generic collector cannot
represent the signal. `otool`, `codesign`, `plutil`, `file`, and TCC reads
should remain shared with the rest of app inspection.

## Implementation reference

Relevant collectors live in `internal/detect/`:

- `Detect()` resolves the executable and runs the three-layer classifier.
- `runOtoolL(exe)` provides the linked-library source for Swift runtime
  and Apple framework inspection.
- `readEntitlements(appPath)` and `readPrivacyDescriptions(appPath)`
  provide declared capability data.
- `scanGrantedPermissions(bundleID)` provides host-granted TCC state.
- Helper, login item, storage, and process collectors provide the
  operational context around the Swift bundle.

## Related docs

- [../detection/frameworks.md](../detection/frameworks.md) — SwiftUI,
  AppKit+Swift, and Catalyst classifier signals
- [security.md](security.md) — entitlements, sandbox, privacy, and TCC
- [helpers-and-xpc.md](helpers-and-xpc.md) — embedded helper inspection
- [storage-footprint.md](storage-footprint.md) — container and group
  container storage
