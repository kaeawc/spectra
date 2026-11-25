#!/usr/bin/env bash
set -euo pipefail

# Submit the signed darwin binaries in release archives to Apple's notary
# service, and fail unless the submission is Accepted.
#
# notarytool takes only zip, dmg and flat pkg files, so the binaries from all
# given archives go into one zip. Apple records each binary's code directory
# hash, so the tarballs that ship the same binaries are covered. Bare binaries
# cannot be stapled; Gatekeeper checks the ticket online.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
BINARIES="spectra spectra-mcp spectra-helper"

usage() {
    cat <<EOF
Usage: $0 [archive ...]

If no archives are passed, all \$DIST_DIR/spectra_*_darwin_*.tar.gz files are used.

Credentials, one of:
  NOTARY_PROFILE   A keychain profile created once with
                   xcrun notarytool store-credentials <profile> --apple-id <id> --team-id <team>
  NOTARY_KEY_PATH  App Store Connect API key (.p8) file, with
  NOTARY_KEY_ID    its key ID, and
  NOTARY_ISSUER    its issuer ID (team keys only; leave unset for an individual key).

NOTARY_TIMEOUT bounds the wait for Apple's verdict (default 45m).
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi

auth=()
if [[ -n "${NOTARY_PROFILE:-}" ]]; then
    auth=(--keychain-profile "$NOTARY_PROFILE")
elif [[ -n "${NOTARY_KEY_PATH:-}" && -n "${NOTARY_KEY_ID:-}" ]]; then
    auth=(--key "$NOTARY_KEY_PATH" --key-id "$NOTARY_KEY_ID")
    if [[ -n "${NOTARY_ISSUER:-}" ]]; then
        auth+=(--issuer "$NOTARY_ISSUER")
    fi
else
    echo "error: set NOTARY_PROFILE, or NOTARY_KEY_PATH and NOTARY_KEY_ID." >&2
    usage >&2
    exit 2
fi

if [[ "$#" -gt 0 ]]; then
    archives=("$@")
else
    archives=("$DIST_DIR"/spectra_*_darwin_*.tar.gz)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/spectra-notarize"

bins=()
for archive in "${archives[@]}"; do
    if [[ ! -f "$archive" ]]; then
        echo "archive not found: $archive" >&2
        exit 1
    fi
    name="$(basename "$archive" .tar.gz)"
    tar -xzf "$archive" -C "$WORK/spectra-notarize"
    for cmd in $BINARIES; do
        bins+=("$WORK/spectra-notarize/$name/bin/$cmd")
    done
done

# The notary service rejects anything without a Developer ID signature, the
# hardened runtime and a timestamp; say so before uploading.
bash "$ROOT/scripts/verify-darwin-signature.sh" "${bins[@]}"

zip="$WORK/spectra-notarize.zip"
ditto -c -k --keepParent "$WORK/spectra-notarize" "$zip"

# json_field <file> <key> prints a top-level string field of notarytool JSON.
json_field() {
    plutil -extract "$2" raw -o - "$1" 2>/dev/null || true
}

# `submit --wait` exits 0 for an Invalid verdict, so the status field, not the
# exit code, decides. An upload that never got an id is retried.
result="$WORK/submit.json"
id=""
for attempt in 1 2 3; do
    xcrun notarytool submit "$zip" "${auth[@]}" --wait \
        --timeout "${NOTARY_TIMEOUT:-45m}" --output-format json > "$result" || true
    id="$(json_field "$result" id)"
    if [[ -n "$id" ]]; then
        break
    fi
    echo "notarytool submit returned no submission id (attempt $attempt):" >&2
    cat "$result" >&2 || true
    sleep $((attempt * 15))
done
if [[ -z "$id" ]]; then
    echo "error: could not submit to the notary service" >&2
    exit 1
fi

status="$(json_field "$result" status)"
if [[ "$status" != "Accepted" ]]; then
    # A wait that timed out or lost its connection leaves the submission
    # running on Apple's side; ask once more before calling it a failure.
    if xcrun notarytool info "$id" "${auth[@]}" --output-format json > "$WORK/info.json"; then
        status="$(json_field "$WORK/info.json" status)"
    fi
fi
if [[ "$status" != "Accepted" ]]; then
    echo "error: notarization $id finished with status '${status:-unknown}'" >&2
    xcrun notarytool log "$id" "${auth[@]}" >&2 || true
    exit 1
fi
echo "Notarization $id Accepted for ${#bins[@]} binaries."
