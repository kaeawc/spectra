#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTOCOL_TOOL="${SPECTRA_RELEASE_TOOL:-}"

usage() {
    echo "Usage: $0 <version> <dist_dir>" >&2
}

if [[ $# -ne 2 ]]; then
    usage
    exit 2
fi

VERSION="$1"
DIST_DIR="$(cd "$2" && pwd)"
PUBLIC_KEY_FILE="$ROOT/packaging/release/spectra-release.pub"

if [[ ! -s "$PUBLIC_KEY_FILE" ]]; then
    echo "error: release trust root is missing or empty: $PUBLIC_KEY_FILE; generate and commit packaging/release/spectra-release.pub before releasing" >&2
    exit 1
fi
if [[ -z "${SPECTRA_RELEASE_ED25519_KEY:-}" ]]; then
    echo "error: SPECTRA_RELEASE_ED25519_KEY is unset or empty; provide the base64 Ed25519 seed to sign the release" >&2
    exit 1
fi

archives=("$DIST_DIR"/*.tar.gz)
if [[ ! -f "${archives[0]}" ]]; then
    echo "error: no .tar.gz release archives found in $DIST_DIR" >&2
    exit 1
fi

run_tool() {
	if [[ -z "$PROTOCOL_TOOL" ]]; then
		go tool spectra-release "$@"
	elif [[ -d "$PROTOCOL_TOOL" ]]; then
        # Run the local checkout's command package in its module context; this
        # keeps dry runs independent of a published protocol module version.
        (cd "$PROTOCOL_TOOL" && go run . "$@")
	else
		go run "$PROTOCOL_TOOL" "$@"
    fi
}

GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"
NATIVE_ARCHIVE="$DIST_DIR/spectra_${VERSION}_${GOOS}_${GOARCH}.tar.gz"
if [[ ! -f "$NATIVE_ARCHIVE" ]]; then
    echo "error: native archive not found for capabilities schema: $NATIVE_ARCHIVE" >&2
    exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
tar -xzf "$NATIVE_ARCHIVE" -C "$tmp_dir"
NATIVE_BIN="$tmp_dir/spectra_${VERSION}_${GOOS}_${GOARCH}/bin/spectra"
if [[ ! -x "$NATIVE_BIN" ]]; then
    echo "error: native spectra binary missing or not executable in $NATIVE_ARCHIVE" >&2
    exit 1
fi
CAPABILITIES_JSON="$tmp_dir/capabilities.json"
"$NATIVE_BIN" capabilities --json > "$CAPABILITIES_JSON"
SCHEMA_VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["schema"]["version"])' "$CAPABILITIES_JSON")"
BUILT_VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["spectra_version"])' "$CAPABILITIES_JSON")"
if [[ "$BUILT_VERSION" != "$VERSION" ]]; then
    echo "error: archive spectra_version is '$BUILT_VERSION', expected '$VERSION'" >&2
    exit 1
fi

PUBLISHED_AT="$(git -C "$ROOT" log -1 --format=%cI | python3 -c 'import datetime,sys; value=sys.stdin.read().strip(); parsed=datetime.datetime.fromisoformat(value.replace("Z", "+00:00")); print(parsed.astimezone(datetime.timezone.utc).isoformat().replace("+00:00", "Z"))')"
MANIFEST="$DIST_DIR/spectra-release.json"
SIGNATURE="$DIST_DIR/spectra-release.json.sig"
PUBLIC_KEY="$(tr -d '\r\n' < "$PUBLIC_KEY_FILE")"

run_tool manifest --version "$VERSION" --public-key "$PUBLIC_KEY" \
    --capabilities-schema-version "$SCHEMA_VERSION" --published-at "$PUBLISHED_AT" \
    --out "$MANIFEST" "${archives[@]}"
run_tool sign --manifest "$MANIFEST" --out "$SIGNATURE"
VERIFY_OUTPUT="$(run_tool verify --manifest "$MANIFEST" --signature "$SIGNATURE" \
    --trusted-key "$PUBLIC_KEY" --artifacts-dir "$DIST_DIR")"
printf '%s\n' "$VERIFY_OUTPUT"
