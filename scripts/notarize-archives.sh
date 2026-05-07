#!/usr/bin/env bash
set -euo pipefail

# Submit release archives to Apple's notary service and staple the result when
# stapling is supported for the artifact type.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
PROFILE="${NOTARY_PROFILE:-}"

usage() {
    cat <<EOF
Usage: NOTARY_PROFILE=<xcrun-notarytool-profile> $0 [archive ...]

If no archives are passed, all dist/spectra_*_darwin_*.tar.gz files are used.
Create the profile once with:
  xcrun notarytool store-credentials <profile> --apple-id <id> --team-id <team> --password <app-password>
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi

if [[ -z "$PROFILE" ]]; then
    echo "NOTARY_PROFILE is required." >&2
    usage >&2
    exit 2
fi

if [[ "$#" -gt 0 ]]; then
    archives=("$@")
else
    archives=("$DIST_DIR"/spectra_*_darwin_*.tar.gz)
fi

for archive in "${archives[@]}"; do
    if [[ ! -f "$archive" ]]; then
        echo "archive not found: $archive" >&2
        exit 1
    fi
    xcrun notarytool submit "$archive" --keychain-profile "$PROFILE" --wait
done
