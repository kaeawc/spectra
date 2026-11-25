#!/usr/bin/env bash
set -euo pipefail

# Import a Developer ID Application identity from a .p12 into a temporary
# keychain on a macOS CI runner.
#
# Usage: scripts/import-signing-identity.sh <keychain-path>
#
# Environment:
#   MACOS_SIGNING_P12_BASE64    base64 of the .p12 (certificate and private key)
#   MACOS_SIGNING_P12_PASSWORD  the .p12 export password
#
# Prints the identity's SHA-1 hash, the value codesign --sign takes. Fails
# unless the .p12 holds exactly one valid Developer ID Application identity.
# Remove the keychain afterwards with: security delete-keychain <keychain-path>

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <keychain-path>" >&2
    exit 2
fi
KEYCHAIN="$1"
: "${MACOS_SIGNING_P12_BASE64:?MACOS_SIGNING_P12_BASE64 is required}"
: "${MACOS_SIGNING_P12_PASSWORD:?MACOS_SIGNING_P12_PASSWORD is required}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
p12="$WORK/identity.p12"
(umask 077 && printf '%s' "$MACOS_SIGNING_P12_BASE64" | tr -d '[:space:]' | base64 --decode > "$p12")

# The keychain lives only for this job, so its password is random and unsaved.
kc_password="$(openssl rand -hex 24)"
security create-keychain -p "$kc_password" "$KEYCHAIN"
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$kc_password" "$KEYCHAIN"
security import "$p12" -k "$KEYCHAIN" -f pkcs12 -P "$MACOS_SIGNING_P12_PASSWORD" \
    -T /usr/bin/codesign >/dev/null
security set-key-partition-list -S apple-tool:,apple: -s -k "$kc_password" \
    "$KEYCHAIN" >/dev/null
# Keep the existing search list so system roots and intermediates still resolve.
existing=()
while IFS= read -r line; do
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line#\"}"
    line="${line%\"}"
    [[ -n "$line" ]] && existing+=("$line")
done < <(security list-keychains -d user)
security list-keychains -d user -s "$KEYCHAIN" ${existing[@]+"${existing[@]}"}

# A Developer ID identity is valid only with Apple's intermediate CA, which a
# runner image may lack: import both generations, pinned by SHA-256.
for ca in \
    "DeveloperIDG2CA f16cd3c54c7f83cea4bf1a3e6a0819c8aaa8e4a1528fd144715f350643d2df3a" \
    "DeveloperIDCA 7afc9d01a62f03a2de9637936d4afe68090d2de18d03f29c88cfb0b1ba63587f"; do
    file="${ca%% *}"
    want="${ca#* }"
    if ! curl -fsSL --retry 3 -o "$WORK/$file.cer" "https://www.apple.com/certificateauthority/$file.cer"; then
        echo "warning: could not download $file.cer; relying on the runner's copy" >&2
        continue
    fi
    got="$(shasum -a 256 "$WORK/$file.cer" | awk '{ print $1 }')"
    if [[ "$got" != "$want" ]]; then
        echo "error: $file.cer has SHA-256 $got, expected $want" >&2
        exit 1
    fi
    security import "$WORK/$file.cer" -k "$KEYCHAIN" >/dev/null
done

identities="$(security find-identity -v -p codesigning "$KEYCHAIN" |
    awk '/"Developer ID Application: / { print $2 }')"
count="$(printf '%s' "$identities" | grep -c . || true)"
if [[ "$count" -ne 1 ]]; then
    echo "error: expected one valid Developer ID Application identity in the .p12, found $count" >&2
    security find-identity -v -p codesigning "$KEYCHAIN" >&2 || true
    exit 1
fi
name="$(security find-identity -v -p codesigning "$KEYCHAIN" |
    sed -n 's/.*"\(Developer ID Application: [^"]*\)".*/\1/p')"
echo "Imported $name" >&2
printf '%s\n' "$identities"
