#!/bin/sh
# Install Spectra into a Hive VM's persistent /workspaces and start the daemon.
#
#   curl -fsSL -o /tmp/hive-setup.sh \
#     https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/hive-setup.sh &&
#     sh /tmp/hive-setup.sh --version v0.1.3
#
# Safe to run on every boot. Only /workspaces survives when a sleeping VM is
# replaced, so the binaries go there; running processes do not survive, so the
# daemon is started each time (a no-op when it is already up). The download is
# skipped when the installed spectra and spectra-mcp both report the requested
# version. The installer itself is always fetched at that tag, so the tag pins
# both the binaries and the install.sh that verifies them.
#
# Register the MCP server once, as a stdio server with the command
# <home>/bin/spectra-mcp and no arguments.
#
# Options (environment variable in parentheses):
#   --version TAG   release tag, or "latest" (SPECTRA_VERSION; default latest).
#                   Pin a tag so every VM runs the same build.
#   --home DIR      install prefix (SPECTRA_HOME; default /workspaces/.spectra)
#   --no-daemon     install only; do not start the daemon
#   --base-url URL  release download base, passed to install.sh
#                   (SPECTRA_BASE_URL; file:// works)
set -eu

REPO="kaeawc/spectra"
VERSION="${SPECTRA_VERSION:-latest}"
SPECTRA_HOME="${SPECTRA_HOME:-/workspaces/.spectra}"
START_DAEMON=1

die() {
    echo "spectra hive-setup: $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
        --version=*) VERSION="${1#*=}"; shift ;;
        --home) [ $# -ge 2 ] || die "--home needs a value"; SPECTRA_HOME="$2"; shift 2 ;;
        --home=*) SPECTRA_HOME="${1#*=}"; shift ;;
        --no-daemon) START_DAEMON=0; shift ;;
        --base-url) [ $# -ge 2 ] || die "--base-url needs a value"; SPECTRA_BASE_URL="$2"; export SPECTRA_BASE_URL; shift 2 ;;
        --base-url=*) SPECTRA_BASE_URL="${1#*=}"; export SPECTRA_BASE_URL; shift ;;
        -h|--help) echo "usage: hive-setup.sh [--version TAG|latest] [--home DIR] [--no-daemon] [--base-url URL]"; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

BIN="$SPECTRA_HOME/bin"

# installed_version prints the tag only when both binaries report it, so a
# half-finished install is redone.
installed_version() {
    [ -x "$BIN/spectra" ] && [ -x "$BIN/spectra-mcp" ] || return 0
    cli="$("$BIN/spectra" version 2>/dev/null)" || return 0
    mcp="$("$BIN/spectra-mcp" --version 2>/dev/null)" || return 0
    [ "$mcp" = "spectra-mcp $cli" ] && echo "$cli"
    return 0
}

resolve_latest() {
    url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest" 2>/dev/null)" || return 1
    tag="${url##*/}"
    case "$tag" in
        v[0-9]*) echo "$tag" ;;
        *) return 1 ;;
    esac
}

have="$(installed_version)"
want="$VERSION"
if [ "$want" = latest ]; then
    if ! want="$(resolve_latest)"; then
        # Offline boot: keep whatever is installed rather than failing.
        [ -n "$have" ] || die "could not resolve the latest release and nothing is installed"
        echo "Could not resolve the latest release; keeping spectra $have"
        want="$have"
    fi
fi
case "$want" in
    v[0-9]*) ;;
    *) die "version must be a tag like v0.1.3, got '$want'" ;;
esac
case "${want#v}" in
    *[!0-9A-Za-z.+-]*) die "version contains characters outside [0-9A-Za-z.+-]: '$want'" ;;
esac

if [ "$have" = "$want" ]; then
    echo "spectra $have already installed in $BIN"
else
    TMP="$(mktemp -d)"
    trap 'rm -rf "$TMP"' EXIT
    curl -fsSL --proto '=https' --retry 3 -o "$TMP/install.sh" \
        "https://raw.githubusercontent.com/${REPO}/${want}/scripts/install.sh" ||
        die "could not download install.sh for $want"
    sh "$TMP/install.sh" --version "$want" --prefix "$SPECTRA_HOME"
fi

if [ "$START_DAEMON" = 1 ]; then
    "$BIN/spectra" daemon start
fi

echo "MCP server command: $BIN/spectra-mcp"
