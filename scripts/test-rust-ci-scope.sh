#!/usr/bin/env bash
# Tests scripts/rust-ci-scope.sh: which changes run the Rust CI jobs.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCOPE="$HERE/rust-ci-scope.sh"
fail=0

expect() {
  local want=$1 got
  shift
  got=$(printf '%s\n' "$@" | bash "$SCOPE")
  if [[ $got == "rust=$want" ]]; then
    echo "PASS: $* -> $want"
  else
    echo "FAIL: $* -> $got, want rust=$want"
    fail=1
  fi
}

expect true crates/base/src/lib.rs
expect true Cargo.lock
expect true rust-toolchain.toml
expect true .github/workflows/rust.yml
expect true .github/actions/rust-toolchain/action.yml
expect true contract/cases/unsupported/vmmap/expected.stdout
expect true docs/index.md crates/sys/src/darwin.rs
expect true scripts/rust-ci-scope.sh
expect false internal/memstate/memstate_darwin.go
expect false docs/design/rust-port.md README.md
expect false .github/workflows/ci.yml go.mod
expect false
expect false scripts/dist.sh

if [[ $(bash "$SCOPE" --all) != rust=true ]]; then
  echo "FAIL: --all"
  fail=1
fi
if bash "$SCOPE" --bogus >/dev/null 2>&1; then
  echo "FAIL: an unknown argument should fail"
  fail=1
fi
exit "$fail"
