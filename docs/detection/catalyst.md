# Mac Catalyst detection

Mac Catalyst apps are iOS UIKit apps running on macOS via Apple's
bridging layer. They link UIKit out of `/System/iOSSupport/` rather
than `/System/Library/Frameworks/`, and the `UIKitMacHelper.framework`
provides the AppKit bridge.

## Signal

Layer 2 detector inspects `otool -L` output. Catalyst is identified
when any linked path contains either:

- `/iOSSupport/` (iOS frameworks bridged onto Mac)
- `/UIKitMacHelper.framework/` (the bridging helper)

This signal is unambiguous — these paths never appear in plain AppKit
or SwiftUI macOS apps.

## Variants

| Combined signal | Verdict |
|---|---|
| Catalyst + SwiftUI linked | `MacCatalyst+SwiftUI` |
| Catalyst, no SwiftUI | `MacCatalyst` |

The hybrid case (modern Catalyst + SwiftUI for new UI) is increasingly
common.

## Empirical examples

| App | Verdict | Notes |
|---|---|---|
| Messages | MacCatalyst | Apple system app, brought from iOS |
| News | MacCatalyst | iPad-bridged |
| Home | MacCatalyst | iPad-bridged |
| Stocks | MacCatalyst | uses `_AppIntents_UIKit` private framework |
| Maps | MacCatalyst+SwiftUI | hybrid, modern |
| Books | MacCatalyst+SwiftUI | hybrid |
| Podcasts | MacCatalyst+SwiftUI | hybrid |
| Swift Playground | MacCatalyst+SwiftUI | iPad app on Mac |

## Why classify separately

A Catalyst app's developer experience and runtime behavior differ
substantially from a native AppKit or SwiftUI app:

- Different debugging story (Xcode treats them as iOS targets).
- UIKit gestures translated to AppKit at runtime.
- Many AppKit APIs unavailable; some Catalyst-specific bridging APIs
  required for menu bar / window-management integration.
- Asset catalogs use the iOS image-scale conventions.

For diagnostics, conflating Catalyst with native AppKit hides these
real differences. Spectra's separate verdict is honest about what's
running.

## Implementation reference

`internal/detect/detect.go`:
- `classifyByLinkedLibs` — Catalyst short-circuit takes precedence over
  the SwiftUI/AppKit branches once `hasCatalyst` matches.

## See also

- [overview.md](overview.md) — the three-layer detection model
- [frameworks.md](frameworks.md) — full signal table
