# Node-based app inspection

Node-based macOS apps usually ship JavaScript bundles, npm dependencies,
native add-ons, and a runtime wrapper. Spectra inspects those bundle
artifacts without executing app code. Today this mostly means Electron,
but the same model also applies to Node payloads embedded in other
wrappers.

## What it finds today

For confirmed Electron apps, Spectra can report:

| Signal | Source | Output |
|---|---|---|
| Node runtime wrapper | `Contents/Frameworks/Electron Framework.framework/` plus `Resources/app.asar` or `Resources/app/` | `UI=Electron`, `Runtime=Node+Chromium` |
| Electron version | Electron framework `Info.plist` | `ElectronVersion`, `FrameworkVersions["Electron"]` |
| npm package inventory | top-level directories under unpacked `node_modules` payloads | `Dependencies.NPMPackages` |
| Native add-ons | `.node` files under unpacked `node_modules` trees | `NativeModules` |
| Embedded URL hosts | main executable and `Contents/Resources/app.asar` when `--network` is set | `NetworkEndpoints` |
| Helper layout | helper `.app` bundles under `Contents/Frameworks/` | `Helpers.HelperApps` |

These signals answer different questions. The framework verdict says
"this is Electron." The package and native-module passes say what kind
of Node app it is: a mostly stock JavaScript shell, a terminal app with
`node-pty`, a credential-touching app with `keytar`, or a hybrid app
with Swift/Rust native bridges.

## Package inventory

Spectra treats npm package inventory as a dependency summary, not as a
lockfile audit. It records package names that are visible in unpacked
payloads and keeps the scan bounded:

- `Contents/Resources/app.asar.unpacked/node_modules/<package>/`
- `Contents/Resources/app.asar.unpacked/node_modules/@scope/<package>/`
The inventory is intentionally shallow. Top-level packages are useful
for quickly spotting major capabilities (`node-pty`, `better-sqlite3`,
`keytar`, `playwright`, `puppeteer`, `sharp`) without expanding every
transitive dependency into noisy output.

## Native add-ons

Node native add-ons are the highest-signal part of Node app inspection.
They are Mach-O dynamic libraries loaded by Node, conventionally named
with the `.node` extension. Spectra walks unpacked package payloads,
finds each `.node` file, attributes it back to its owning npm package
when possible, and classifies the native implementation language.

See [../detection/native-modules.md](../detection/native-modules.md)
for the full classifier. In short:

- Rust is detected from leaked Cargo target paths or Rust panic-site
  strings.
- Swift is detected from Swift runtime links or Swift sidecar dylibs.
- C++ is inferred from `libc++` links when no stronger language signal
  is present.
- Known packages add capability and risk hints, such as pseudoterminal,
  Keychain, global input, USB/HID, serial, filesystem event, and privacy
  permission access.

## Network literals

When `--network` is set, Electron app inspection also scans
`Contents/Resources/app.asar` for URL host literals. This is not live
traffic capture; it is a static reference inventory. It is useful for
finding telemetry providers, OAuth hosts, staging endpoints, embedded
documentation links, and supply-chain surprises in bundled JavaScript.

See [network-endpoints.md](network-endpoints.md) for details and
limitations.

## Current limits

- `app.asar` is scanned for URL literals, but not unpacked into a full
  JavaScript module graph.
- Package inventory is shallow and best-effort. It does not replace
  `package-lock.json`, `pnpm-lock.yaml`, or SBOM analysis.
- npm package inventory only covers `app.asar.unpacked/node_modules`
  today. Native modules also include unpacked `Contents/Resources/app`
  payloads.
- Arbitrary embedded Node runtimes may need additional
  wrapper-specific paths.
- Spectra does not execute Node, load app JavaScript, or evaluate dynamic
  imports. All signals come from bundle files.

## Implementation reference

`internal/detect/detect.go`:

- `scanDependencies(appPath)` — collects third-party frameworks, npm
  packages, and jar counts.
- `scanNativeModules(appPath)` — walks unpacked Node native add-ons.
- `scanNetworkEndpoints(appPath, exe)` — scans executable and Electron
  `app.asar` URL literals when network scanning is enabled.

## See also

- [../detection/overview.md](../detection/overview.md) — framework and
  runtime classification
- [../detection/native-modules.md](../detection/native-modules.md) —
  `.node` add-on classification
- [network-endpoints.md](network-endpoints.md) — static URL host
  extraction
- [helpers-and-xpc.md](helpers-and-xpc.md) — Electron helper app layout
