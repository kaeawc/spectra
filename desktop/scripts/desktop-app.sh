#!/usr/bin/env bash
# Builds the window as a macOS app bundle with Spectra's icon (assets/brand/macos/spectra.icns),
# so the Dock and Finder show the mark: target/release/Spectra.app.
#
#   scripts/desktop-app.sh          # build the bundle and print its path
#   scripts/desktop-app.sh --open   # and open it
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cargo build --release --manifest-path "$root/Cargo.toml" --bin spectra-desktop

release=$root/target/release
app=$release/Spectra.app
version=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$root/Cargo.toml" | head -n 1)
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$release/spectra-desktop" "$app/Contents/MacOS/spectra-desktop"
cp "$root/assets/brand/macos/spectra.icns" "$app/Contents/Resources/spectra.icns"
cat >"$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Spectra</string>
  <key>CFBundleDisplayName</key><string>Spectra</string>
  <key>CFBundleIdentifier</key><string>io.github.kaeawc.SpectraDesktop</string>
  <key>CFBundleExecutable</key><string>spectra-desktop</string>
  <key>CFBundleIconFile</key><string>spectra</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$version</string>
  <key>CFBundleVersion</key><string>$version</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
echo "$app"
if [[ ${1:-} == --open ]]; then
  open "$app"
fi
