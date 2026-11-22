#!/usr/bin/env bash
set -euo pipefail

# Tests scripts/validate-workflows.sh reference extraction against fixtures.
HERE="$(cd "$(dirname "$0")" && pwd)"
FIX="$HERE/testdata/validate-workflows"
fail=0

run() { WORKFLOWS_DIR="$FIX/$1" REFS_ONLY=1 bash "$HERE/validate-workflows.sh"; }

expect_pass() {
  if out=$(run "$1" 2>&1); then
    echo "PASS: $1"
  else
    echo "FAIL: $1 should pass"
    echo "$out"
    fail=1
  fi
}

expect_missing() {
  if out=$(run "$1" 2>&1); then
    echo "FAIL: $1 should fail"
    fail=1
  elif grep -q "MISSING: $2" <<<"$out"; then
    echo "PASS: $1"
  else
    echo "FAIL: $1 lacked MISSING: $2"
    echo "$out"
    fail=1
  fi
}

expect_pass comment-only
expect_missing missing-run scripts/definitely-missing.sh
expect_missing uses-local-missing scripts/missing-action.sh
exit $fail
