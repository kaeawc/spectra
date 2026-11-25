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
  CODESIGN_ID   Optional Developer ID Application identity in a local
                keychain (darwin only). The release workflow signs in its
                codesign job instead (scripts/sign-darwin-archives.sh).
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
    mkdir -p "$stage/bin" "$stage/docs"

    # Linux ships a static, pure-Go binary (CGO off; the cgo libproc thread
    # collector is darwin-only and the Linux path reads procfs). Darwin forces
    # cgo on: Go disables it by default when cross-compiling, so the amd64
    # build on an arm64 runner would otherwise silently drop the libproc,
    # memstate and rusage collectors. Apple clang targets either arch.
    local cgo=1
    if [[ "$goos" == "linux" ]]; then
        cgo=0
    fi

    local cmd
    for cmd in spectra spectra-mcp spectra-helper; do
        (
            cd "$ROOT"
            CGO_ENABLED="$cgo" GOOS="$goos" GOARCH="$arch" \
                go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/$cmd" "./cmd/$cmd/"
        )
        if ! go version -m "$stage/bin/$cmd" | grep -q "CGO_ENABLED=$cgo\$"; then
            echo "error: $goos/$arch $cmd was not built with CGO_ENABLED=$cgo" >&2
            exit 1
        fi
    done

    cp "$ROOT/README.md" "$stage/"
    cp "$ROOT/LICENSE" "$stage/LICENSE"
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
        bash "$ROOT/scripts/verify-darwin-signature.sh" \
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
