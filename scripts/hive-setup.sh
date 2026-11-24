#!/bin/sh
# Install Spectra into a Hive VM's persistent /workspaces and start the daemon.
#
#   curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/hive-setup.sh \
#     | sh -s -- --version v0.1.3
#
# Safe to run on every boot. Only /workspaces survives when a sleeping VM is
# replaced, so the binaries go there; running processes do not survive, so the
# daemon is started each time (a no-op when it is already up). The download is
# skipped when the installed spectra already reports the requested version.
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

installed_version() {
    [ -x "$BIN/spectra" ] || return 0
    "$BIN/spectra" version 2>/dev/null || true
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

if [ "$have" = "$want" ]; then
    echo "spectra $have already installed in $BIN"
else
    # Run from a checkout, use its install.sh. Piped into sh, $0 is the shell,
    # so fetch the installer at the tag rather than trusting the current dir.
    installer=""
    case "$0" in
        */hive-setup.sh) installer="$(dirname "$0")/install.sh" ;;
    esac
    if [ -z "$installer" ] || [ ! -f "$installer" ]; then
        TMP="$(mktemp -d)"
        trap 'rm -rf "$TMP"' EXIT
        installer="$TMP/install.sh"
        curl -fsSL --proto '=https' --retry 3 -o "$installer" \
            "https://raw.githubusercontent.com/${REPO}/${want}/scripts/install.sh" ||
            die "could not download install.sh for $want"
    fi
    sh "$installer" --version "$want" --prefix "$SPECTRA_HOME"
fi

if [ "$START_DAEMON" = 1 ]; then
    "$BIN/spectra" daemon start
fi

echo "MCP server command: $BIN/spectra-mcp"
