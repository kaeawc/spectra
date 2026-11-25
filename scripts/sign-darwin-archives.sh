#!/usr/bin/env bash
set -euo pipefail

# Developer ID sign the darwin binaries inside release archives, in place.
#
# Usage: CODESIGN_ID=<identity> scripts/sign-darwin-archives.sh <tag> <dist_dir>
#
# Unpacks dist/spectra_<tag>_darwin_{arm64,amd64}.tar.gz, signs spectra,
# spectra-mcp and spectra-helper with the hardened runtime and a secure
# timestamp, verifies them with scripts/verify-darwin-signature.sh, repacks
# each archive with the same layout, and rewrites checksums.txt. The Linux
# archives are left alone.
#
# Environment:
#   CODESIGN_ID        Developer ID Application identity: its SHA-1 hash or
#                      common name, as `security find-identity` prints it.
#   CODESIGN_KEYCHAIN  Optional keychain that holds the identity.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARIES="spectra spectra-mcp spectra-helper"

if [[ $# -ne 2 ]]; then
    echo "Usage: CODESIGN_ID=<identity> $0 <tag> <dist_dir>" >&2
    exit 2
fi
TAG="$1"
DIST_DIR="$(cd "$2" && pwd)"
if [[ -z "${CODESIGN_ID:-}" ]]; then
    echo "error: CODESIGN_ID is not set" >&2
    exit 2
fi

keychain_args=()
if [[ -n "${CODESIGN_KEYCHAIN:-}" ]]; then
    keychain_args=(--keychain "$CODESIGN_KEYCHAIN")
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

for arch in arm64 amd64; do
    name="spectra_${TAG}_darwin_${arch}"
    archive="$DIST_DIR/$name.tar.gz"
    if [[ ! -f "$archive" ]]; then
        echo "error: $archive not found" >&2
        exit 1
    fi
    mkdir -p "$WORK/$arch"
    tar -xzf "$archive" -C "$WORK/$arch"
    stage="$WORK/$arch/$name"
    paths=()
    for cmd in $BINARIES; do
        paths+=("$stage/bin/$cmd")
    done
    codesign --force --timestamp --options runtime --sign "$CODESIGN_ID" \
        ${keychain_args[@]+"${keychain_args[@]}"} "${paths[@]}"
    bash "$ROOT/scripts/verify-darwin-signature.sh" "${paths[@]}"
    # COPYFILE_DISABLE keeps macOS tar from adding AppleDouble ._ entries.
    COPYFILE_DISABLE=1 tar -C "$WORK/$arch" -czf "$archive.tmp" "$name"
    mv "$archive.tmp" "$archive"
    echo "Signed $name"
done

(
    cd "$DIST_DIR"
    shasum -a 256 spectra_"${TAG}"_*.tar.gz > checksums.txt
)
echo "Rewrote $DIST_DIR/checksums.txt"
