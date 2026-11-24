#!/usr/bin/env bash
set -euo pipefail

# Release readiness checklist for Spectra.
# Run before tagging a release; exits non-zero if anything fails.

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m'
PASS=0
FAIL=0
SKIP=0

check() {
    local name="$1"
    shift
    echo -n "  $name... "
    if "$@" > /dev/null 2>&1; then
        echo -e "${GREEN}PASS${NC}"
        PASS=$((PASS + 1))
    else
        echo -e "${RED}FAIL${NC}"
        FAIL=$((FAIL + 1))
    fi
}

skip() {
    local name="$1"
    echo -n "  $name... "
    echo -e "${YELLOW}SKIP${NC}"
    SKIP=$((SKIP + 1))
}

sign_binary() {
    local path="$1"
    codesign --force --timestamp --options runtime --sign "$SPECTRA_SIGN_IDENTITY" "$path"
}

# render_formula_for <tag> renders the formula from synthetic checksums.
render_formula_for() {
    local tag="$1" dir goos arch
    dir="$(mktemp -d)"
    for goos in darwin linux; do
        for arch in amd64 arm64; do
            printf '%064d  spectra_%s_%s_%s.tar.gz\n' 0 "$tag" "$goos" "$arch" >> "$dir/checksums.txt"
        done
    done
    bash scripts/release-formula.sh "$tag" "$dir"
    rm -rf "$dir"
}

# formula_is_tap_ready requires output that can be copied into the tap
# verbatim: no template header, no `version` line for `brew audit --strict`
# to reject on a plain tag, and an explicit version on a prerelease tag,
# which Homebrew cannot scan from the URL.
formula_is_tap_ready() {
    local out pre
    out="$(render_formula_for v0.0.0)" || return 1
    pre="$(render_formula_for v0.0.0-rc.1)" || return 1
    [[ "$(head -n 1 <<<"$out")" == "class Spectra < Formula" ]] &&
        ! grep -Eq '^[[:space:]]*version[[:space:]]' <<<"$out" &&
        grep -Fqx '  version "0.0.0-rc.1"' <<<"$pre"
}

notarize_archive() {
    local archive="$1"
    local submit_args=(notarytool submit "$archive" --wait)
    if [ -n "${SPECTRA_NOTARY_KEYCHAIN_PROFILE:-}" ]; then
        submit_args+=(--keychain-profile "$SPECTRA_NOTARY_KEYCHAIN_PROFILE")
    else
        submit_args+=(--apple-id "$SPECTRA_NOTARY_APPLE_ID" --team-id "$SPECTRA_NOTARY_TEAM_ID" --password "$SPECTRA_NOTARY_PASSWORD")
    fi
    xcrun "${submit_args[@]}"
}

create_notarization_zip() {
    rm -rf spectra-notary
    mkdir -p spectra-notary
    cp spectra spectra-helper spectra-notary/
    ditto -c -k --keepParent spectra-notary spectra-notary.zip
}

notary_configured() {
    if [ -n "${SPECTRA_NOTARY_KEYCHAIN_PROFILE:-}" ]; then
        return 0
    fi
    [ -n "${SPECTRA_NOTARY_APPLE_ID:-}" ] && [ -n "${SPECTRA_NOTARY_TEAM_ID:-}" ] && [ -n "${SPECTRA_NOTARY_PASSWORD:-}" ]
}

echo "=== Release Checklist ==="
echo ""

echo "Build:"
check "spectra binary" go build -ldflags "-s -w" -o spectra ./cmd/spectra/
check "spectra-helper binary" go build -ldflags "-s -w" -o spectra-helper ./cmd/spectra-helper/

echo ""
echo "Signing:"
if [ -n "${SPECTRA_SIGN_IDENTITY:-}" ]; then
    check "sign spectra" sign_binary spectra
    check "sign spectra-helper" sign_binary spectra-helper
    check "spectra signature valid" codesign --verify --strict --verbose=2 spectra
    check "spectra-helper Developer ID signature" bash -c 'codesign -dv --verbose=4 spectra-helper 2>&1 | grep -q "Authority=Developer ID Application:"'
    check "spectra-helper signature valid" codesign --verify --strict --verbose=2 spectra-helper
else
    skip "codesign binaries (set SPECTRA_SIGN_IDENTITY)"
fi

echo ""
echo "Notarization:"
if [ -n "${SPECTRA_SIGN_IDENTITY:-}" ] && notary_configured; then
    check "create notarization zip" create_notarization_zip
    check "submit notarization" notarize_archive spectra-notary.zip
else
    skip "notarize binaries (set signing + notary credentials)"
fi

echo ""
echo "Quality:"
check "go vet" go vet ./...
check "go test" go test ./... -count=1 -timeout 120s
# shellcheck disable=SC2016 # vars are evaluated by the inner bash, intentional
check "go mod tidy is clean" bash -c 'cp go.mod go.mod.bak; touch go.sum; cp go.sum go.sum.bak; go mod tidy; rc1=0; diff -q go.mod go.mod.bak >/dev/null 2>&1 || rc1=1; rc2=0; diff -q go.sum go.sum.bak >/dev/null 2>&1 || rc2=1; mv go.mod.bak go.mod; mv go.sum.bak go.sum; [ -s go.sum ] || rm -f go.sum; exit $((rc1 || rc2))'

echo ""
echo "Docs:"
check "mkdocs nav valid" ./scripts/validate_mkdocs_nav.sh
check "lychee links valid" ./scripts/lychee/validate_lychee.sh

echo ""
echo "Files:"
check "README.md exists" test -f README.md
check "LICENSE exists" test -f LICENSE
check "mkdocs.yml exists" test -f mkdocs.yml
check "dist script exists" test -x scripts/dist.sh
check "notarize script exists" test -x scripts/notarize-archives.sh
check "Homebrew formula template exists" test -f packaging/homebrew/spectra.rb
check "formula render script exists" test -x scripts/release-formula.sh
check "rendered formula is tap-ready" formula_is_tap_ready
check "install script exists" test -x scripts/install.sh

echo ""
echo "Git:"
check "working tree clean" git diff --quiet HEAD
# shellcheck disable=SC2016 # subshell is evaluated by the inner bash, intentional
check "no untracked Go files" bash -c '[ -z "$(git ls-files --others --exclude-standard "*.go")" ]'

echo ""
echo "================================"
echo -e "Results: ${GREEN}${PASS} passed${NC}, ${RED}${FAIL} failed${NC}, ${YELLOW}${SKIP} skipped${NC}"

if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "Fix failures before tagging release."
    exit 1
fi

echo ""
echo "Ready to release."
