#!/usr/bin/env bash
set -euo pipefail

# Build release archives for Spectra (macOS and Linux).
#
# Outputs:
#   dist/spectra_${VERSION}_darwin_arm64.tar.gz
#   dist/spectra_${VERSION}_darwin_amd64.tar.gz
#   dist/spectra_${VERSION}_linux_amd64.tar.gz
#   dist/spectra_${VERSION}_linux_arm64.tar.gz
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
  CODESIGN_ID   Optional Developer ID Application identity (darwin only).
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi

# build_one <goos> <arch>
build_one() {
    local goos="$1"
    local arch="$2"
    local stage="$DIST_DIR/stage/spectra_${VERSION}_${goos}_${arch}"
    local archive="$DIST_DIR/spectra_${VERSION}_${goos}_${arch}.tar.gz"

    rm -rf "$stage"
    mkdir -p "$stage/bin" "$stage/docs" "$stage/licenses"

    # Linux ships a static, pure-Go binary (CGO off; the cgo libproc thread
    # collector is darwin-only and the Linux path reads procfs). Darwin keeps
    # the toolchain default so its native cgo thread collector links.
    local cgo_env=()
    if [[ "$goos" == "linux" ]]; then
        cgo_env=(CGO_ENABLED=0)
    fi

    (
        cd "$ROOT"
        env "${cgo_env[@]}" GOOS="$goos" GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/spectra" ./cmd/spectra/
        env "${cgo_env[@]}" GOOS="$goos" GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/spectra-mcp" ./cmd/spectra-mcp/
        env "${cgo_env[@]}" GOOS="$goos" GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/spectra-helper" ./cmd/spectra-helper/
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
    # Linux archives ship the systemd unit templates.
    if [[ "$goos" == "linux" && -d "$ROOT/packaging/systemd" ]]; then
        mkdir -p "$stage/systemd"
        cp "$ROOT"/packaging/systemd/*.service "$stage/systemd/"
    fi

    if [[ "$goos" == "darwin" && -n "${CODESIGN_ID:-}" ]]; then
        codesign --force --timestamp --options runtime --sign "$CODESIGN_ID" \
            "$stage/bin/spectra" "$stage/bin/spectra-mcp" "$stage/bin/spectra-helper"
    fi

    tar -C "$DIST_DIR/stage" -czf "$archive" "$(basename "$stage")"
}

rm -rf "$DIST_DIR/stage"
mkdir -p "$DIST_DIR"

build_one darwin arm64
build_one darwin amd64
build_one linux amd64
build_one linux arm64

(
    cd "$DIST_DIR"
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 spectra_"${VERSION}"_*.tar.gz > checksums.txt
    else
        sha256sum spectra_"${VERSION}"_*.tar.gz > checksums.txt
    fi
)

rm -rf "$DIST_DIR/stage"
echo "Wrote release archives to $DIST_DIR"
