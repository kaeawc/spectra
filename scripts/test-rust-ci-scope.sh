#!/usr/bin/env bash
# Tests scripts/rust-ci-scope.sh: which changes run the Rust CI jobs.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCOPE="$HERE/rust-ci-scope.sh"
fail=0

# expect RUST DESKTOP PATH...: the scope for a change touching exactly those paths.
expect() {
  local want="rust=$1 desktop=$2" got
  shift 2
  got=$(printf '%s\n' "$@" | bash "$SCOPE" | tr '\n' ' ')
  got=${got% }
  if [[ $got == "$want" ]]; then
    echo "PASS: $* -> $want"
  else
    echo "FAIL: $* -> $got, want $want"
    fail=1
  fi
}

expect true true crates/base/src/lib.rs
expect true true Cargo.lock
expect true true rust-toolchain.toml
expect true false .github/workflows/rust.yml
expect true false .github/actions/rust-toolchain/action.yml
expect true false contract/cases/unsupported/vmmap/expected.stdout
expect true true docs/index.md crates/sys/src/darwin.rs
expect true false scripts/rust-ci-scope.sh
expect false false internal/memstate/memstate_darwin.go
expect false false internal/x.go
expect false false docs/design/rust-port.md README.md
expect false false .github/workflows/ci.yml go.mod
expect false false
expect false false scripts/dist.sh
expect false true desktop/src/app.rs
expect false true desktop/Cargo.lock docs/index.md
expect true true desktop/src/app.rs crates/base/src/lib.rs

if [[ $(bash "$SCOPE" --all | tr '\n' ' ') != "rust=true desktop=true " ]]; then
  echo "FAIL: --all"
  fail=1
fi
if bash "$SCOPE" --bogus >/dev/null 2>&1; then
  echo "FAIL: an unknown argument should fail"
  fail=1
fi
exit "$fail"
