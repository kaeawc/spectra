# Rust-based app inspection

Rust shows up in macOS app bundles in two different ways:

- the primary app executable is Rust, as with native Rust apps such as
  Warp or Zed;
- a higher-level shell embeds Rust native code, as with Electron apps
  that ship `.node` add-ons backed by Cargo-built libraries.

Spectra treats those as separate questions. The top-level detection
verdict answers "what is this app built on?" while inspection answers
"where is Rust present, and what does that imply for debugging?"

## Top-level Rust apps

Rust app detection is a Layer 3 binary-content scan. Spectra streams the
resolved executable and counts Rust panic-site strings. A count of 100 or
more classifies the runtime as Rust when no stronger bundle marker has
already identified a higher-level framework.

This catches apps whose macOS surface still looks like AppKit from
`otool -L`, because Rust GUI stacks commonly bridge through Cocoa,
WebKit, Metal, or custom Objective-C shims.

| Signal | Interpretation |
|---|---|
| Rust panic-site strings >= 100 | primary executable is likely Rust |
| AppKit linked, no WebKit | native Rust app using Cocoa/AppKit glue |
| AppKit + WebKit linked, Rust strings >= 100 | Tauri-style Rust + WebKit app |
| tiny launcher binary | follow the larger framework or sibling binary before scanning |

The threshold is intentionally conservative. A single bundled Rust dylib
inside an otherwise non-Rust app can leak panic strings, but empirical
fixtures stayed well below 100 hits. Native Rust apps usually produce
hundreds.

## Tauri distinction

Tauri is not reported as plain native Rust. Spectra first looks for the
AppKit + WebKit link pattern, then uses the Rust string scan to confirm
that the WebKit shell is backed by Rust.

That distinction matters operationally:

- Tauri failures often involve the WebView boundary, app-local protocol
  handling, entitlements, or frontend bundle state.
- Native Rust failures often involve the primary Mach-O, embedded assets,
  GPU/windowing libraries, or native storage paths.

## Embedded Rust in Electron

For confirmed Electron apps, Rust inspection moves to the native-module
sub-detection. Spectra walks
`Contents/Resources/app.asar.unpacked/**/*.node` and classifies each
native add-on independently.

Rust native modules are identified by either:

- Cargo target paths in the link map; or
- Rust panic-site strings in the `.node` binary or an `@rpath` sidecar.

This keeps the top-level verdict honest. An Electron app with one Rust
native module is still Electron, but the inspection result can show that
performance-sensitive or privileged behavior lives in Rust.

See [../detection/native-modules.md](../detection/native-modules.md) for
the native-module classifier.

## What to inspect next

Rust identification should guide the next diagnostic pass rather than end
the investigation.

| Finding | Follow-up |
|---|---|
| native Rust app | inspect code-signing, entitlements, helpers, storage, and network endpoints |
| Tauri app | inspect WebKit usage, custom protocols, local resources, and network endpoints |
| Electron app with Rust modules | inspect each `.node` module, sidecar dylibs, and linked Apple frameworks |
| universal Rust binary | compare arm64 and x86_64 slices when failures reproduce only under Rosetta |

The most useful companion pages are:

- [security.md](security.md) for sandboxing, hardened runtime, and
  entitlement context;
- [helpers-and-xpc.md](helpers-and-xpc.md) for privileged helpers and
  XPC services;
- [network-endpoints.md](network-endpoints.md) for static network hints;
- [storage-footprint.md](storage-footprint.md) for cache and data layout.

## Current limits

Spectra does not decode Rust crate metadata, Cargo dependency graphs, or
symbol names beyond the lightweight fingerprints above. Stripped release
binaries often hide meaningful names, and Rust's static linking means
there is no equivalent of "list dynamic framework versions" for most
crate-level dependencies.

For now, the inspection target is architectural attribution: identify
where Rust is present, distinguish native Rust from Tauri and Electron
hybrids, then route the engineer to the right macOS-level probes.
