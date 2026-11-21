# Security inspection

Spectra surfaces four layers of an app's security posture: code
signing, Gatekeeper trust assessment, hardened runtime / sandbox, and
the entitlements vs granted permissions split.

## Code signing

Pulled from `codesign -dv <app>`:

- **Team ID** (`TeamIdentifier=`). Apple Developer team identifier.
  System apps return `not set`, which Spectra treats as empty rather
  than displaying it.
- **Mac App Store** distribution flagged separately by the presence of
  `Contents/_MASReceipt/`.

## Hardened runtime

Detected from the `runtime` entry in the CodeDirectory `flags=` list of
`codesign -dv` output (for example `flags=0x10000(runtime)` or
`flags=0x12a00(kill,restrict,library-validation,runtime)`). Required for Developer ID notarization. Apps
without it can't pass Gatekeeper on macOS 10.15+ and are a meaningful
security regression.

## Gatekeeper assessment

Spectra reports Gatekeeper in two clearly separated ways.

**Signature inference (default).** The `codesign -dv --verbose=4` call
Spectra already makes (~30 ms) names the authority chain and whether a
notarization ticket is stapled. From that alone Spectra fills
`GatekeeperLikely` / `GatekeeperLikelySource` and sets
`GatekeeperMethod` to `signature`:

| Signature | `GatekeeperLikely` | `GatekeeperLikelySource` |
|---|---|---|
| `Developer ID Application:` → `Developer ID Certification Authority` → `Apple Root CA`, ticket stapled | `accepted` | `Notarized Developer ID` |
| `Apple Mac OS Application Signing` → `Apple Worldwide Developer Relations Certification Authority` → `Apple Root CA` | `accepted` | `Mac App Store` |
| `Software Signing` / `macOS Software Signing` → `Apple Code Signing Certification Authority` → `Apple Root CA` | `accepted` | `Apple System` |
| Developer ID without a stapled ticket, ad-hoc, unsigned, any other chain | empty | empty |

A chain that predicts acceptance is then confirmed with a plain
`codesign --verify <app>` (no `--deep`, no `--strict`; a Finder-metadata
detritus complaint alone still counts as intact). It hashes every sealed
resource, so it costs ~0.05 s for Chrome and ~8 s for Xcode; the outcome
is cached on disk (the `seal` cache kind, 30 days) keyed by bundle path
and CDHash, so each app version pays once. A broken seal leaves
`GatekeeperLikely` empty and sets `GatekeeperLikelyReason` to
`resource seal invalid` (or `signature invalid: <reason>`), shown as
"likely not accepted: resource seal invalid (from signature; not
assessed)". A check that times out (30 s) or is canceled is not cached
and reads `seal not checked: <why>`, shown as "unknown".

The inference never predicts `rejected` and never populates
`GatekeeperStatus`, so it cannot fire `app-gatekeeper-rejected`, which
needs `--assess-gatekeeper`. It does not check revocation or nested code;
it is a prediction, shown as "likely accepted (…, from signature; not
assessed)".

**Real assessment (opt in).** `--assess-gatekeeper` on `spectra inspect`,
`spectra snapshot`, and `spectra rules` (and `assess_gatekeeper` on the
MCP `inspect_app` tool) runs `spctl --assess -v --type exec <app>`. The
`-v` is required: without it `spctl` prints nothing for an accepted
bundle. It is opt in because syspolicyd serializes assessments and can
spend minutes on per-nested-binary notarization lookups (Chrome, Xcode),
so Spectra runs one assessment at a time per process, under a 15 s
timeout (a caller canceled while queued or running gets
`GatekeeperError` `canceled`). The coarse verdict lands in `GatekeeperStatus` (`accepted` or
`rejected`) with `GatekeeperMethod` `spctl`; the `source=` line, when
present, is `GatekeeperSource`. A timeout or an unassessable bundle (for
example a damaged signature) leaves `GatekeeperStatus` empty and records
why in `GatekeeperError`.

Real verdicts are cached in-process for 24 h keyed by bundle path and the
main executable's `CDHash`, so a long-running daemon or MCP server does not
re-ask syspolicyd. The on-disk detect cache never stores a spctl
verdict or a timeout. Its key is content-only, so a cache hit re-reads the
signature for the requested path (one `codesign -dv`) before predicting,
and is re-assessed (through the in-process cache) whenever an assessment
is requested.

This is separate from code signing metadata. A bundle can expose a Team
ID and entitlements but still be rejected by Gatekeeper because it is
not notarized, is damaged, or fails policy checks on the current host.

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
guards this with `internal/bundleid.Valid` — an allowlist of
`[a-zA-Z0-9._-]+` characters that matches the reverse-DNS bundle ID
format. Anything outside that charset is rejected before the detector
or privileged helper builds the query.

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
flags today as `permission-mismatch` when the granted TCC service maps to
a required `NS*UsageDescription` key that is missing from `Info.plist`.
Services that macOS grants through generic system prompts, such as
Accessibility or ScreenCapture, are intentionally ignored by that rule
until there is a stable declaration key to compare against.

## Implementation reference

`internal/detect/detect.go`:
- `readSigning(appPath)` — team ID, hardened runtime, authority chain,
  stapled ticket, CDHash
- `internal/detect/gatekeeper.go` — `inferGatekeeper` (signature
  prediction) and `GatekeeperAssessor` (bounded, serialized, CDHash-cached
  `spctl --assess`)
- `readEntitlements(appPath)` — declared entitlements
- `readPrivacyDescriptions(appPath)` — `NS*UsageDescription`
- `scanGrantedPermissions(bundleID)` — TCC reads
- `internal/bundleid.Valid(s)` — SQL safety gate
