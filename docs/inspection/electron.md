# Electron app inspection

Electron apps are easy to classify but easy to under-describe. The
top-level verdict tells us the bundle embeds Chromium and Node.js; the
inspection pass explains how much native surface the app adds, what
helpers it ships, which update channel it uses, and where useful
diagnostic evidence lives inside the bundle.

## Bundle shape

A confirmed Electron app has both:

| Path | Meaning |
|---|---|
| `Contents/Frameworks/Electron Framework.framework/` | Embedded Electron runtime |
| `Contents/Resources/app.asar` or `Contents/Resources/app/` | Application JavaScript payload |

The framework marker is not enough on its own. Spectra requires the app
payload too so a stray framework directory does not classify a partial or
malformed bundle as Electron.

Electron apps usually also ship Chromium helper sub-apps under
`Contents/Frameworks/`. See [helpers-and-xpc.md](helpers-and-xpc.md) for
the helper inventory and process mapping.

## Metadata

Electron-specific metadata comes from the same static sources as native
apps, with one extra framework read:

| Field | Source |
|---|---|
| `UI` | Electron bundle markers |
| `Runtime` | `Node+Chromium` |
| `ElectronVersion` | `Electron Framework.framework/Resources/Info.plist` `CFBundleVersion` |
| `FrameworkVersions["Electron"]` | Same framework plist value |
| `Packaging` | `Squirrel.framework` when present |

`Squirrel.framework` is tracked as packaging metadata, not as an Electron
proof. Some non-Electron apps ship Squirrel, so it must stay independent
from the framework verdict.

## JavaScript payload

Spectra treats the packaged JavaScript as inspectable static data:

1. Prefer `Contents/Resources/app/` when the app ships unpacked sources.
2. Also inspect `Contents/Resources/app.asar` for Electron bundles.
3. Keep extraction read-only; diagnostics should not rewrite or unpack
   application resources in place.

Today this payload is most useful for embedded endpoint discovery. The
network scanner reads Electron payloads because production API hosts,
telemetry endpoints, update URLs, and websocket origins are commonly
present as string literals. See
[network-endpoints.md](network-endpoints.md).

Future enrichment can use the same source boundary to report package
manager metadata, main-process entry points, preload scripts, and coarse
dependency summaries. Those should remain best-effort diagnostics rather
than policy decisions because bundlers routinely rewrite JavaScript.

## Native add-ons

Electron's most important architectural distinction is whether the app is
plain JavaScript plus commodity native dependencies, or a hybrid app with
custom Swift, Rust, or C++ bridges.

For confirmed Electron apps, Spectra walks:

| Path | Contents |
|---|---|
| `Contents/Resources/app.asar.unpacked/**/*.node` | Native add-ons unpacked beside an ASAR |
| `Contents/Resources/app/**/*.node` | Native add-ons in unpacked app payloads |

Each `.node` file is attributed to its owning `node_modules` package when
possible, then classified by linked libraries and binary content. Capability
and risk hints are attached for known packages such as `keytar`, `node-pty`,
`fsevents`, `usb`, and input-monitoring modules.

See [../detection/native-modules.md](../detection/native-modules.md) for
the detailed language rules and examples.

## Helpers and live processes

Electron inherits Chromium's multi-process architecture. A typical app
ships helper sub-apps for renderer, GPU, plugin, and network/service work:

```text
Contents/Frameworks/<App> Helper.app
Contents/Frameworks/<App> Helper (GPU).app
Contents/Frameworks/<App> Helper (Plugin).app
Contents/Frameworks/<App> Helper (Renderer).app
```

Static helper inspection explains what the bundle can launch. Live process
inspection explains what is running now, how many renderers exist, and
which helper command lines are active. This matters when an Electron app
looks idle but has renderer, GPU, network, or utility processes still alive.

See [running-processes.md](running-processes.md) for bundle-scoped process
attribution.

## Security interpretation

Electron inspection should combine several independent signals:

| Signal | Why it matters |
|---|---|
| Entitlements | Sandbox, hardened runtime, JIT allowances, Apple Events |
| TCC grants | Camera, microphone, screen capture, contacts, files |
| Native add-ons | Direct OS access outside normal JavaScript APIs |
| Helper apps | Chromium process topology and code-signing surface |
| Embedded endpoints | Production, telemetry, update, and remote-control hosts |
| Login items | Background launch behavior |

No single signal is enough. For example, `node-pty` is normal for a
terminal-oriented app but high-signal for an app that otherwise should not
spawn shells. ScreenCaptureKit linked from a Swift add-on is expected for a
screen-sharing tool and suspicious for a notes app.

## Output shape

Electron app inspection contributes to these result fields:

| Field | Meaning |
|---|---|
| `UI` | `Electron` |
| `Runtime` | `Node+Chromium` |
| `ElectronVersion` | Embedded Electron runtime version when available |
| `FrameworkVersions` | Includes `Electron` when available |
| `NativeModules` | Classified `.node` add-ons and hints |
| `Helpers` | Helper apps and XPC services |
| `RunningProcesses` | Bundle-attributed live processes |
| `Dependencies.NpmPackages` | Package metadata when dependency scanning can infer it |
| `NetworkEndpoints` | URLs and hosts found in Electron payloads |

## Implementation reference

`internal/detect/detect.go`:

- `detectByBundleMarkers(appPath)` — confirms Electron using framework
  and payload markers.
- `populateMetadata(appPath, exe, *Result)` — reads Electron framework
  version metadata.
- `scanNativeModules(appPath)` — walks Electron native add-ons.
- `scanHelpers(appPath)` — records helper sub-apps and XPC services.
- `scanNetworkEndpoints(appPath, ui)` — includes ASAR payloads for
  Electron apps.
