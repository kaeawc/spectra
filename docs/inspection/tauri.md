# Tauri app inspection

Tauri apps on macOS look like small native AppKit shells that host web UI in
WebKit while running most app logic in Rust. Spectra therefore treats Tauri as
a cross-layer verdict, not a single bundle marker.

## Detection path

Tauri classification requires both of these signals:

| Layer | Signal | Why it matters |
|---|---|---|
| Layer 2 | Main executable links `AppKit.framework` and `WebKit.framework` | The app is a native macOS WebKit host rather than Chromium/Electron |
| Layer 3 | Binary contains at least 100 Rust panic-site strings | The host is a real Rust binary, not a plain Obj-C or Swift WebKit wrapper |

Layer 2 alone produces the intermediate verdict `AppKit+WebKit` with medium
confidence. Layer 3 promotes that verdict to `Tauri`, sets `Runtime` and
`Language` to `Rust`, and raises confidence to high.

This split keeps WebKit wrappers distinct:

- `Tauri` — AppKit + WebKit + strong Rust binary evidence.
- `Wails` — AppKit + WebKit + Go buildinfo + `github.com/wailsapp/wails`.
- `Go+WebKit` — AppKit + WebKit + Go buildinfo, but no Wails marker.
- `WKWebView wrapper` — AppKit + WebKit + bundled `index.html`, with no
  Rust or Go runtime marker.

## Version metadata

Spectra also tries to populate `FrameworkVersions["Tauri"]` from:

- `Contents/Resources/tauri.conf.json`
- `Contents/Resources/tauri.conf.json5`

The version comes from `package.version` first, then top-level `version`.
This is best-effort metadata only. A missing version does not affect the
Tauri verdict, and JSON5 files must be JSON-compatible for the current reader
to parse them.

## Signals

A verbose Tauri result should include evidence similar to:

```text
links AppKit + WebKit (Tauri suspect)
204 Rust panic-site strings
```

The exact Rust count depends on compiler version, optimization, dependencies,
and whether the binary is universal.

## Implementation reference

`internal/detect/detect.go`:

- `classifyByLinkedLibs(exe, *Result)` identifies the `AppKit+WebKit`
  suspect state from `otool -L`.
- `classifyByStrings(exe, *Result)` promotes that suspect state to `Tauri`
  when Rust markers cross the threshold.
- `readTauriVersion(appPath)` extracts best-effort version metadata from
  Tauri config files.

## See also

- [../detection/overview.md](../detection/overview.md) — layer ordering
- [../detection/frameworks.md](../detection/frameworks.md) — framework signal table
- [metadata.md](metadata.md) — bundle and framework version metadata
