#!/usr/bin/env bash
set -euo pipefail

# Render the Homebrew formula for a release from the archive checksums.
#
# Usage: scripts/release-formula.sh <tag> <dist_dir> [out]
#
# Reads <dist_dir>/checksums.txt (written by scripts/dist.sh) and fills the at-sign
# placeholders in packaging/homebrew/spectra.rb. Writes to [out], or
# stdout when omitted. Fails if any archive checksum is missing or any
# placeholder survives, so a partial formula is never published.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="${SPECTRA_FORMULA_TEMPLATE:-$ROOT/packaging/homebrew/spectra.rb}"

if [[ $# -lt 2 || $# -gt 3 ]]; then
    echo "Usage: $0 <tag> <dist_dir> [out]" >&2
    exit 2
fi

TAG="$1"
DIST_DIR="$2"
OUT="${3:-}"
CHECKSUMS="$DIST_DIR/checksums.txt"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    echo "error: tag must be vX.Y.Z or vX.Y.Z-<pre>, got '$TAG'" >&2
    exit 1
fi
if [[ ! -f "$CHECKSUMS" ]]; then
    echo "error: $CHECKSUMS not found" >&2
    exit 1
fi

# checksum_for <goos> <arch> prints the SHA-256 recorded for that archive.
checksum_for() {
    local name="spectra_${TAG}_$1_$2.tar.gz"
    local sum
    sum="$(awk -v name="$name" '$2 == name || $2 == "*" name { print $1 }' "$CHECKSUMS")"
    if [[ ! "$sum" =~ ^[0-9a-f]{64}$ ]]; then
        echo "error: no SHA-256 for $name in $CHECKSUMS" >&2
        return 1
    fi
    printf '%s' "$sum"
}

darwin_arm64="$(checksum_for darwin arm64)"
darwin_amd64="$(checksum_for darwin amd64)"
linux_arm64="$(checksum_for linux arm64)"
linux_amd64="$(checksum_for linux amd64)"

rendered="$(sed \
    -e "s/@VERSION@/${TAG#v}/g" \
    -e "s/@TAG@/${TAG}/g" \
    -e "s/@SHA256_DARWIN_ARM64@/${darwin_arm64}/g" \
    -e "s/@SHA256_DARWIN_AMD64@/${darwin_amd64}/g" \
    -e "s/@SHA256_LINUX_ARM64@/${linux_arm64}/g" \
    -e "s/@SHA256_LINUX_AMD64@/${linux_amd64}/g" \
    "$TEMPLATE")"

if grep -Eq '@[A-Z0-9_]+@' <<<"$rendered"; then
    echo "error: unreplaced placeholders remain in rendered formula" >&2
    exit 1
fi

if [[ -n "$OUT" ]]; then
    printf '%s\n' "$rendered" > "$OUT"
else
    printf '%s\n' "$rendered"
fi
