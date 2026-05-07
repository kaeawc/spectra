#!/usr/bin/env bash
set -euo pipefail

# Build macOS release archives for Spectra.
#
# Outputs:
#   dist/spectra_${VERSION}_darwin_arm64.tar.gz
#   dist/spectra_${VERSION}_darwin_amd64.tar.gz
#   dist/checksums.txt

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"

usage() {
    cat <<EOF
Usage: VERSION=vX.Y.Z $0

Environment:
  VERSION       Release version. Defaults to git describe.
  DIST_DIR      Output directory. Defaults to ./dist.
  CODESIGN_ID   Optional Developer ID Application identity.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi

build_one() {
    local arch="$1"
    local stage="$DIST_DIR/stage/spectra_${VERSION}_darwin_${arch}"
    local archive="$DIST_DIR/spectra_${VERSION}_darwin_${arch}.tar.gz"

    rm -rf "$stage"
    mkdir -p "$stage/bin" "$stage/docs" "$stage/licenses"

    (
        cd "$ROOT"
        GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/spectra" ./cmd/spectra/
        GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/spectra-mcp" ./cmd/spectra-mcp/
        GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/spectra-helper" ./cmd/spectra-helper/
    )

    cp "$ROOT/README.md" "$stage/"
    cp "$ROOT/LICENSE" "$stage/licenses/LICENSE"
    cp "$ROOT/docs/install.md" "$stage/docs/install.md"
    cp "$ROOT/docs/operations/install-services.md" "$stage/docs/install-services.md"
    cp "$ROOT/docs/design/distribution.md" "$stage/docs/distribution.md"
    if [[ -f "$ROOT/agent/spectra-agent.jar" ]]; then
        mkdir -p "$stage/agent"
        cp "$ROOT/agent/spectra-agent.jar" "$stage/agent/"
    fi

    if [[ -n "${CODESIGN_ID:-}" ]]; then
        codesign --force --timestamp --options runtime --sign "$CODESIGN_ID" \
            "$stage/bin/spectra" "$stage/bin/spectra-mcp" "$stage/bin/spectra-helper"
    fi

    tar -C "$DIST_DIR/stage" -czf "$archive" "$(basename "$stage")"
}

rm -rf "$DIST_DIR/stage"
mkdir -p "$DIST_DIR"

build_one arm64
build_one amd64

(
    cd "$DIST_DIR"
    shasum -a 256 spectra_"${VERSION}"_darwin_*.tar.gz > checksums.txt
)

rm -rf "$DIST_DIR/stage"
echo "Wrote release archives to $DIST_DIR"
