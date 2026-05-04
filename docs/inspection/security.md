# Security inspection

Spectra surfaces three layers of an app's security posture: code
signing, hardened runtime / sandbox, and the entitlements vs granted
permissions split.

## Code signing

Pulled from `codesign -dv <app>`:

- **Team ID** (`TeamIdentifier=`). Apple Developer team identifier.
  System apps return `not set`, which Spectra treats as empty rather
  than displaying it.
- **Mac App Store** distribution flagged separately by the presence of
  `Contents/_MASReceipt/`.

## Hardened runtime

Detected from the `flags=0x10000(runtime)` substring in
`codesign -dv` output. Required for Developer ID notarization. Apps
without it can't pass Gatekeeper on macOS 10.15+ and are a meaningful
security regression.

## Sandbox

Detected from the entitlement
`com.apple.security.app-sandbox` being set to `true` in the bundle's
embedded entitlements plist. Mac App Store apps must be sandboxed;
Developer ID apps may opt in.

## Declared entitlements

Spectra parses the entitlements via
`codesign -d --entitlements :- <app>` (XML plist on stdout) and
extracts a curated allowlist of boolean keys set to true:

- `app-sandbox`
- `network.client` / `network.server`
- `device.camera` / `device.audio-input` / `device.bluetooth` / `device.usb`
- `personal-information.location`
- `cs.allow-jit` / `cs.disable-library-validation` / `cs.allow-unsigned-executable-memory`
- `automation.apple-events`
- `virtualization`

Reported with the `com.apple.security.` prefix stripped.

## Declared privacy descriptions

Pulled from `NS*UsageDescription` keys in `Info.plist`. These are the
strings macOS shows in permission prompts. Apps cannot ask for
permissions whose description they haven't declared — so this captures
what the app is *willing to ask for*.

Example:

```
privacy declared: AudioCapture, BluetoothAlways, BluetoothPeripheral,
                  Camera, Microphone, SpeechRecognition
```

## Granted privacy permissions

The major reveal: pulled from the TCC (Transparency, Consent, and
Control) database. Spectra queries both:

- `~/Library/Application Support/com.apple.TCC/TCC.db` (per-user;
  readable as the user)
- `/Library/Application Support/com.apple.TCC/TCC.db` (system; requires
  Full Disk Access — silent skip if denied)

Query pattern:

```sql
SELECT service FROM access WHERE client = '<bundle-id>' AND auth_value >= 2;
```

`auth_value`:
- `0` — denied
- `2` — allowed
- `3` — limited / always-allow
- `4` — allowed but only when in front

Service names get the `kTCCService` prefix stripped for display.

### SQL safety

macOS's `sqlite3` CLI doesn't accept bind variables on the command
line, so the bundle ID is interpolated into the query string. Spectra
guards this with `validBundleID` — an allowlist of `[a-zA-Z0-9._-]+`
characters that matches the reverse-DNS bundle ID format. Anything
outside that charset is rejected.

## Declared vs granted: the gap

The interesting signal is the gap between what an app declares and what
the user has actually granted:

```
Claude
  declared: Camera, Microphone, Bluetooth, SpeechRecognition
  granted:  Accessibility, ScreenCapture
```

Claude has been granted ScreenCapture (for computer-use) without the
declaration showing it; macOS has a generic prompt for ScreenCaptureKit
that doesn't require an `NSScreenCaptureUsageDescription`.

```
Slack
  declared: Bluetooth, Camera, DownloadsFolder, Microphone
  granted:  Camera, Microphone, ScreenCapture, SystemPolicyDownloadsFolder
```

Slack has ScreenCapture granted that wasn't in its declared list — this
is the exact pattern the recommendations engine
([../design/recommendations-engine.md](../design/recommendations-engine.md))
will flag in the future as a permission-vs-declaration mismatch.

## Implementation reference

`internal/detect/detect.go`:
- `readSigning(appPath)` — team ID + hardened runtime
- `readEntitlements(appPath)` — declared entitlements
- `readPrivacyDescriptions(appPath)` — `NS*UsageDescription`
- `scanGrantedPermissions(bundleID)` — TCC reads
- `validBundleID(s)` — SQL safety gate
