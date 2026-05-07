#!/usr/bin/env bash
set -euo pipefail

# Release readiness checklist for Spectra.
# Run before tagging a release; exits non-zero if anything fails.

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'
PASS=0
FAIL=0

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

echo "=== Release Checklist ==="
echo ""

echo "Build:"
check "spectra binary" go build -ldflags "-s -w" -o spectra ./cmd/spectra/
check "spectra-helper binary" go build -ldflags "-s -w" -o spectra-helper ./cmd/spectra-helper/

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

echo ""
echo "Git:"
check "working tree clean" git diff --quiet HEAD
# shellcheck disable=SC2016 # subshell is evaluated by the inner bash, intentional
check "no untracked Go files" bash -c '[ -z "$(git ls-files --others --exclude-standard "*.go")" ]'

echo ""
echo "================================"
echo -e "Results: ${GREEN}${PASS} passed${NC}, ${RED}${FAIL} failed${NC}"

if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "Fix failures before tagging release."
    exit 1
fi

echo ""
echo "Ready to release."
