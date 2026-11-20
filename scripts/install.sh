#!/bin/sh
# Install a Spectra release archive for this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/install.sh | sh -s -- --version v0.1.0 --prefix /usr/local
#
# Downloads spectra_<tag>_<os>_<arch>.tar.gz and checksums.txt from the
# GitHub release, verifies the archive's SHA-256, and installs spectra,
# spectra-mcp and spectra-helper into <prefix>/bin. The helper is copied but
# not enabled; run `sudo spectra install-helper` for that.
#
# Options (environment variable in parentheses):
#   --version TAG   release tag, or "latest" (SPECTRA_VERSION; default latest)
#   --prefix DIR    install prefix (SPECTRA_PREFIX; default $HOME/.local)
#   --base-url URL  release download base, <url>/<tag>/<file>
#                   (SPECTRA_BASE_URL; default GitHub releases; file:// works)
#
# Under GitHub Actions the bin directory is appended to $GITHUB_PATH.
set -eu

REPO="kaeawc/spectra"
VERSION="${SPECTRA_VERSION:-latest}"
PREFIX="${SPECTRA_PREFIX:-${HOME}/.local}"
BASE_URL="${SPECTRA_BASE_URL:-https://github.com/${REPO}/releases/download}"

die() {
    echo "spectra install: $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
        --version=*) VERSION="${1#*=}"; shift ;;
        --prefix) [ $# -ge 2 ] || die "--prefix needs a value"; PREFIX="$2"; shift 2 ;;
        --prefix=*) PREFIX="${1#*=}"; shift ;;
        --base-url) [ $# -ge 2 ] || die "--base-url needs a value"; BASE_URL="$2"; shift 2 ;;
        --base-url=*) BASE_URL="${1#*=}"; shift ;;
        -h|--help) echo "usage: install.sh [--version TAG|latest] [--prefix DIR] [--base-url URL]"; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

detect_os() {
    case "$(uname -s)" in
        Darwin) echo darwin ;;
        Linux) echo linux ;;
        *) die "unsupported OS: $(uname -s) (Spectra ships macOS and Linux builds)" ;;
    esac
}

detect_arch() {
    arch="$(uname -m)"
    # A Rosetta shell reports x86_64 on Apple silicon; prefer the native build.
    if [ "$arch" = x86_64 ] && [ "$(uname -s)" = Darwin ] &&
        [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
        arch=arm64
    fi
    case "$arch" in
        x86_64|amd64) echo amd64 ;;
        arm64|aarch64) echo arm64 ;;
        *) die "unsupported architecture: $arch" ;;
    esac
}

# fetch <url> <dest>
fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 3 -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        case "$1" in
            file://*) cp "${1#file://}" "$2" ;;
            *) wget -q -O "$2" "$1" ;;
        esac
    else
        die "curl or wget is required"
    fi
}

resolve_latest() {
    command -v curl >/dev/null 2>&1 || die "resolving the latest release needs curl; pass --version"
    url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")" ||
        die "could not resolve the latest release"
    tag="${url##*/}"
    case "$tag" in
        v[0-9]*) echo "$tag" ;;
        *) die "no published release found (resolved to $url)" ;;
    esac
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        die "sha256sum or shasum is required to verify the download"
    fi
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
if [ "$VERSION" = latest ]; then
    VERSION="$(resolve_latest)"
fi
case "$VERSION" in
    v[0-9]*) ;;
    *) die "version must be a tag like v0.1.0, got '$VERSION'" ;;
esac

NAME="spectra_${VERSION}_${OS}_${ARCH}"
ARCHIVE="${NAME}.tar.gz"
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t spectra-install)"
trap 'rm -rf "$TMP"' EXIT INT TERM

echo "Downloading ${ARCHIVE}"
fetch "${BASE_URL}/${VERSION}/${ARCHIVE}" "$TMP/$ARCHIVE" || die "download failed: ${BASE_URL}/${VERSION}/${ARCHIVE}"
fetch "${BASE_URL}/${VERSION}/checksums.txt" "$TMP/checksums.txt" || die "download failed: ${BASE_URL}/${VERSION}/checksums.txt"

want="$(awk -v name="$ARCHIVE" '$2 == name || $2 == "*" name { print $1 }' "$TMP/checksums.txt")"
[ -n "$want" ] || die "checksums.txt has no entry for $ARCHIVE"
got="$(sha256_of "$TMP/$ARCHIVE")"
[ "$want" = "$got" ] || die "checksum mismatch for $ARCHIVE: expected $want, got $got"
echo "Verified SHA-256 ${got}"

tar -xzf "$TMP/$ARCHIVE" -C "$TMP"
[ -x "$TMP/$NAME/bin/spectra" ] || die "archive does not contain $NAME/bin/spectra"

mkdir -p "$PREFIX/bin"
for bin in spectra spectra-mcp spectra-helper; do
    if [ -f "$TMP/$NAME/bin/$bin" ]; then
        cp "$TMP/$NAME/bin/$bin" "$PREFIX/bin/$bin.tmp.$$"
        chmod 0755 "$PREFIX/bin/$bin.tmp.$$"
        mv -f "$PREFIX/bin/$bin.tmp.$$" "$PREFIX/bin/$bin"
    fi
done

echo "Installed $("$PREFIX/bin/spectra" version) to $PREFIX/bin"
if [ -n "${GITHUB_PATH:-}" ]; then
    echo "$PREFIX/bin" >> "$GITHUB_PATH"
fi
case ":${PATH}:" in
    *":$PREFIX/bin:"*) ;;
    *) [ -n "${GITHUB_PATH:-}" ] || echo "Add $PREFIX/bin to PATH to use spectra." ;;
esac
