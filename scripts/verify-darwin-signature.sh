#!/usr/bin/env bash
set -euo pipefail

# Verify that macOS binaries carry a release-grade signature.
#
# Usage: scripts/verify-darwin-signature.sh [--notarized] <binary>...
#
# Each binary must pass what `spectra install-helper --require-signed` checks
# (a Developer ID Application authority and strict `codesign --verify`), and
# what Apple's notary service requires: the hardened runtime and a secure
# timestamp. --notarized also asks Gatekeeper (`spctl`), which looks up the
# notarization ticket online because bare binaries cannot be stapled.

notarized=0
if [[ "${1:-}" == "--notarized" ]]; then
    notarized=1
    shift
fi
if [[ $# -eq 0 ]]; then
    echo "Usage: $0 [--notarized] <binary>..." >&2
    exit 2
fi

fail=0
for bin in "$@"; do
    if [[ ! -f "$bin" ]]; then
        echo "FAIL $bin: not found" >&2
        fail=1
        continue
    fi
    if ! info="$(codesign -dv --verbose=4 "$bin" 2>&1)"; then
        echo "FAIL $bin: not signed" >&2
        printf '%s\n' "$info" >&2
        fail=1
        continue
    fi
    problems=()
    if ! grep -q '^Authority=Developer ID Application:' <<<"$info"; then
        problems+=("no Developer ID Application authority")
    fi
    if ! grep -Eq '^CodeDirectory .*flags=0x[0-9a-f]*\(([a-z-]+,)*runtime(,[a-z-]+)*\)' <<<"$info"; then
        problems+=("hardened runtime not enabled")
    fi
    if ! grep -q '^Timestamp=' <<<"$info"; then
        problems+=("no secure timestamp")
    fi
    if ! out="$(codesign --verify --strict --verbose=2 "$bin" 2>&1)"; then
        problems+=("strict verification failed: $out")
    fi
    if [[ "$notarized" == 1 ]] && ! out="$(spctl --assess --type install -vv "$bin" 2>&1)"; then
        problems+=("Gatekeeper rejected it: $out")
    elif [[ "$notarized" == 1 ]] && ! grep -q 'source=Notarized Developer ID' <<<"$out"; then
        problems+=("Gatekeeper did not report a notarization: $out")
    fi
    if [[ ${#problems[@]} -gt 0 ]]; then
        for p in "${problems[@]}"; do
            echo "FAIL $bin: $p" >&2
        done
        fail=1
        continue
    fi
    authority="$(grep -m 1 '^Authority=' <<<"$info")"
    echo "ok   $bin (${authority#Authority=})"
done
exit "$fail"
