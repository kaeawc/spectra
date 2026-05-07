# Result schema

The `detect.Result` struct is the canonical output of one inspection.
JSON output via `--json` serializes this struct directly.

Source of truth: `internal/detect/detect.go`.

## Top-level fields

| Field | Type | Description |
|---|---|---|
| `Path` | string | Absolute path to the `.app` bundle |
| `UI` | string | Framework verdict (Electron, SwiftUI, AppKit, Tauri, …) |
| `Runtime` | string | Runtime classification (Node+Chromium, Swift, Rust, JVM, Go, …) |
| `Language` | string | Best-guess source language |
| `Packaging` | string | Auto-updater / packaging hint (Sparkle, Squirrel, …) |
| `Confidence` | string | high / medium / low |
| `Signals` | []string | Human-readable evidence trail |
| `NativeModules` | []NativeModule | Per-Electron-app native add-on classifications |
| `ObjC` | *ObjCInspection | Per-AppKit Objective-C bundle profile |
| `Rust` | *RustInspection | Rust-specific architecture attribution when Rust is present |

## Metadata fields

| Field | Type | Source |
|---|---|---|
| `BundleID` | string | `Info.plist` `CFBundleIdentifier` |
| `AppVersion` | string | `Info.plist` `CFBundleShortVersionString` |
| `BuildNumber` | string | `Info.plist` `CFBundleVersion` |
| `ElectronVersion` | string | Electron framework's own Info.plist |
| `FrameworkVersions` | map[string]string | Best-effort framework version map, such as Electron, Flutter, Qt, Tauri |
| `Architectures` | []string | `arm64`, `x86_64`, or both |
| `BundleSizeBytes` | int64 | Sparse-file-correct on-disk allocation |
| `TeamID` | string | `codesign -dv` TeamIdentifier |
| `SparkleFeedURL` | string | `Info.plist` `SUFeedURL` |
| `MASReceipt` | bool | `Contents/_MASReceipt/` exists |

## Security fields

| Field | Type | Source |
|---|---|---|
| `HardenedRuntime` | bool | `flags=0x10000(runtime)` from codesign |
| `Sandboxed` | bool | `com.apple.security.app-sandbox` true |
| `Entitlements` | []string | Curated boolean entitlements set true |
| `PrivacyDescriptions` | map[string]string | `NS*UsageDescription` keys + values |
| `GrantedPermissions` | []string | TCC.db `auth_value >= 2` services |
| `GatekeeperStatus` | string | `spctl --assess --type exec`: `accepted`, `rejected`, or empty when unavailable |

## Live + structural fields

| Field | Type | Source |
|---|---|---|
| `Helpers` | *Helpers | Helper apps, XPC services, plugins |
| `LoginItems` | []LoginItem | Attributed launchd plists |
| `RunningProcesses` | []ProcessInfo | Currently-running processes for this bundle |
| `AppStartedAt` | *time.Time | Oldest matching process start time |
| `AppUptimeSeconds` | int64 | Seconds between inspection time and `AppStartedAt` |
| `Storage` | *StorageFootprint | Per-`~/Library`-location size sweep |
| `Dependencies` | *Dependencies | Third-party frameworks, npm packages, jar count |
| `Swift` | *SwiftInspection | Swift runtime libraries, Apple frameworks, app-group signals |
| `NetworkEndpoints` | []string | URL hosts (only when `--network` set) |

## Nested types

### NativeModule

```go
type NativeModule struct {
    Name           string   // e.g. "computer_use.node"
    Path           string   // bundle-relative
    PackageName    string   // npm package name when package.json is present
    PackageVersion string   // npm package version when package.json is present
    Language       string   // Rust, Swift, C++, unknown
    Hints          []string // additional context (linked frameworks, etc.)
    RiskHints      []string // security-sensitive capability patterns to review
}
```

### ObjCInspection

```go
type ObjCInspection struct {
    LinkedFrameworks       []string
    PrincipalClass         string
    MainNibFile            string
    MainStoryboardFile     string
    DocumentTypes          []ObjCDocumentType
    URLSchemes             []string
    Services               []string
    AutomationEntitlements []string
    UpdateMechanism        string
}

type ObjCDocumentType struct {
    Name       string
    Role       string
    Extensions []string
}
```

### RustInspection

```go
type RustInspection struct {
    Kind            string   // native, tauri, electron-native-module, embedded, none
    PrimaryBinary   string   // bundle-relative executable inspected for top-level Rust
    PanicStringHits int      // combined Rust panic-site marker hits
    LinkedFrameworks []string // notable Apple/UI frameworks linked by the primary binary
    NativeModules   []string // bundle-relative Rust Electron native modules
    Sidecars        []string // bundle-relative Rust sidecar dylibs
    FollowUps       []string // suggested next diagnostic surfaces
}
```

### Helpers

```go
type Helpers struct {
    HelperApps  []string // basenames without .app
    XPCServices []string // basenames without .xpc
    Plugins     []string // PlugIns/ contents (full filename)
}
```

### LoginItem

```go
type LoginItem struct {
    Path   string // full plist path
    Label  string // launchd Label
    Scope  string // user | system
    Daemon bool   // /Library/LaunchDaemons
}
```

### ProcessInfo

```go
type ProcessInfo struct {
    PID     int
    RSSKiB  int    // resident set size in kibibytes
    Command string // bundle-relative path
}
```

### StorageFootprint

```go
type StorageFootprint struct {
    ApplicationSupport int64
    Caches             int64
    Containers         int64
    GroupContainers    int64
    HTTPStorages       int64
    WebKit             int64
    Logs               int64
    Preferences        int64
    Total              int64
    Paths              []string // locations actually found
}
```

### Dependencies

```go
type Dependencies struct {
    ThirdPartyFrameworks []string // framework basenames, Apple-filtered
    NPMPackages          []string // top-level dirs in app.asar.unpacked
    JavaJars             int      // count of .jar files anywhere in bundle
}
```

### SwiftInspection

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

## Stability

Field names and JSON tag shape are intended to be stable. Adding new
fields is non-breaking; renaming fields will go through a deprecation
window in the daemon RPC schema once that lands.

## See also

- [../detection/overview.md](../detection/overview.md) — how the verdict
  fields are computed
- [../inspection/node-based-apps.md](../inspection/node-based-apps.md) —
  Node/Electron package, native add-on, and URL-host inspection
- [../inspection/](../inspection/) — per-field deep-dives
