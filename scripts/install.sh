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
# Signature verification: when the release also carries spectra-release.json
# and spectra-release.json.sig, and an openssl with Ed25519 support is on PATH,
# the manifest's Ed25519 signature is checked against the release public key
# and the archive's SHA-256 must match the signed manifest. The key is fetched
# over HTTPS from the pinned tag path
#   https://raw.githubusercontent.com/kaeawc/spectra/<tag>/packaging/release/spectra-release.pub
# (override: SPECTRA_RELEASE_PUBKEY_URL; file:// works). A key fetched at the
# tag is only as trustworthy as the repository tag itself, but it is separate
# from the release assets, so tampered assets alone cannot pass. When any of
# that material or openssl support is missing, the checksum check still runs
# and a warning says the download was not signature-verified. A signature that
# is present but does not verify is a hard failure.
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
PREFIX="${SPECTRA_PREFIX:-}"
BASE_URL="${SPECTRA_BASE_URL:-https://github.com/${REPO}/releases/download}"
PUBKEY_URL_OVERRIDE="${SPECTRA_RELEASE_PUBKEY_URL:-}"

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

if [ -z "$PREFIX" ]; then
    [ -n "${HOME:-}" ] || die "HOME is not set; pass --prefix DIR or set SPECTRA_PREFIX"
    PREFIX="${HOME}/.local"
fi

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
        curl -fsSL --proto-redir '=https' --retry 3 -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        case "$1" in
            file://*) cp "${1#file://}" "$2" ;;
            *) wget -q --https-only -O "$2" "$1" ;;
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

# ed25519_ok: succeeds when openssl can verify a raw Ed25519 signature. Checked
# against RFC 8032 test vector 2 (the one-byte message 0x72) so LibreSSL, which
# lacks pkeyutl -rawin Ed25519, is detected instead of failing mid-install.
# Not vector 1: OpenSSL 3.0.x (Ubuntu 22.04) cannot -rawin an empty message
# ("Could not allocate 0 bytes"), which would skip verification there.
ed25519_ok() {
    command -v openssl >/dev/null 2>&1 || return 1
    printf '%s' 302a300506032b65700321003d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c |
        xxd_r > "$TMP/selftest.pub.der" || return 1
    printf '%s' 92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00 |
        xxd_r > "$TMP/selftest.sig" || return 1
    printf 'r' > "$TMP/selftest.msg"
    openssl pkeyutl -verify -rawin -pubin -inkey "$TMP/selftest.pub.der" -keyform DER \
        -sigfile "$TMP/selftest.sig" -in "$TMP/selftest.msg" >/dev/null 2>&1
}

# xxd_r: hex on stdin to binary on stdout, without xxd.
xxd_r() {
    hex="$(cat)"
    while [ -n "$hex" ]; do
        rest="${hex#??}"
        # shellcheck disable=SC2059
        printf "\\$(printf '%03o' "0x${hex%"$rest"}")"
        hex="$rest"
    done
}

# json_str <key> reads a string field from compact or pretty JSON on stdin.
json_str() {
    tr -d '\r\n' | sed -n 's/.*"'"$1"'" *: *"\([^"]*\)".*/\1/p'
}

# verify_signature <archive> <archive-sha256>
verify_signature() {
    v_archive="$1"
    v_sum="$2"
    warn="WARNING: the download was NOT signature-verified (SHA-256 against checksums.txt only)"
    if ! fetch "${BASE_URL}/${VERSION}/spectra-release.json" "$TMP/manifest.json" 2>/dev/null ||
        ! fetch "${BASE_URL}/${VERSION}/spectra-release.json.sig" "$TMP/manifest.sig.json" 2>/dev/null; then
        echo "$warn: release has no spectra-release.json / .sig" >&2
        return 0
    fi
    pub_url="${PUBKEY_URL_OVERRIDE:-https://raw.githubusercontent.com/${REPO}/${VERSION}/packaging/release/spectra-release.pub}"
    if ! fetch "$pub_url" "$TMP/release.pub" 2>/dev/null; then
        echo "$warn: could not fetch the release public key from $pub_url" >&2
        return 0
    fi
    if ! ed25519_ok; then
        echo "$warn: openssl with Ed25519 support (OpenSSL 1.1.1+ or 3.x, not LibreSSL) is required" >&2
        return 0
    fi

    pub_b64="$(tr -d '\r\n ' < "$TMP/release.pub")"
    case "$pub_b64" in
        ed25519:*) pub_b64="${pub_b64#ed25519:}" ;;
        *) die "release public key is not in ed25519:<base64> form" ;;
    esac
    printf '%s' "$pub_b64" | base64 -d > "$TMP/release.pub.raw" 2>/dev/null ||
        printf '%s' "$pub_b64" | base64 -D > "$TMP/release.pub.raw" 2>/dev/null ||
        die "release public key is not valid base64"
    [ "$(wc -c < "$TMP/release.pub.raw" | tr -d ' ')" = 32 ] || die "release public key is not 32 bytes"
    { printf '\060\052\060\005\006\003\053\145\160\003\041\000'; cat "$TMP/release.pub.raw"; } > "$TMP/release.pub.der"

    sig_b64="$(json_str signature < "$TMP/manifest.sig.json")"
    [ -n "$sig_b64" ] || die "signature file has no signature field"
    printf '%s' "$sig_b64" | base64 -d > "$TMP/manifest.sig" 2>/dev/null ||
        printf '%s' "$sig_b64" | base64 -D > "$TMP/manifest.sig" 2>/dev/null ||
        die "signature is not valid base64"
    # The signed message is a domain prefix followed by the exact manifest bytes.
    { printf 'spectra-release-manifest-v1\n'; cat "$TMP/manifest.json"; } > "$TMP/manifest.msg"
    openssl pkeyutl -verify -rawin -pubin -inkey "$TMP/release.pub.der" -keyform DER \
        -sigfile "$TMP/manifest.sig" -in "$TMP/manifest.msg" >/dev/null 2>&1 ||
        die "signature verification FAILED for spectra-release.json; refusing to install"

    m_version="$(json_str version < "$TMP/manifest.json")"
    [ "$m_version" = "$VERSION" ] || die "signed manifest is for '$m_version', not $VERSION"
    # One artifact object per line, then pick the entry for this archive.
    m_sum="$(tr -d '\r\n' < "$TMP/manifest.json" | sed 's/},[ ]*{/}\
{/g' | grep "\"path\" *: *\"$v_archive\"" | json_str sha256)"
    [ -n "$m_sum" ] || die "signed manifest has no entry for $v_archive"
    [ "$m_sum" = "$v_sum" ] || die "archive SHA-256 $v_sum does not match signed manifest $m_sum"
    echo "Verified Ed25519 release signature and manifest digest"
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
case "${VERSION#v}" in
    *[!0-9A-Za-z.+-]*) die "version contains characters outside [0-9A-Za-z.+-]: '$VERSION'" ;;
esac

NAME="spectra_${VERSION}_${OS}_${ARCH}"
ARCHIVE="${NAME}.tar.gz"
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t spectra-install)"
trap 'rm -rf "$TMP"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "Downloading ${ARCHIVE}"
fetch "${BASE_URL}/${VERSION}/${ARCHIVE}" "$TMP/$ARCHIVE" || die "download failed: ${BASE_URL}/${VERSION}/${ARCHIVE}"
fetch "${BASE_URL}/${VERSION}/checksums.txt" "$TMP/checksums.txt" || die "download failed: ${BASE_URL}/${VERSION}/checksums.txt"

want="$(awk -v name="$ARCHIVE" '$2 == name || $2 == "*" name { print $1 }' "$TMP/checksums.txt")"
[ -n "$want" ] || die "checksums.txt has no entry for $ARCHIVE"
got="$(sha256_of "$TMP/$ARCHIVE")"
[ "$want" = "$got" ] || die "checksum mismatch for $ARCHIVE: expected $want, got $got"
echo "Verified SHA-256 ${got}"

verify_signature "$ARCHIVE" "$got"

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
