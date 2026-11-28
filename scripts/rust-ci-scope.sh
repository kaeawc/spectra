#!/usr/bin/env bash
# Which Rust CI jobs a change needs (the `changes` job of .github/workflows/rust.yml).
#
#   git diff --no-renames --name-only BASE HEAD | scripts/rust-ci-scope.sh
#   scripts/rust-ci-scope.sh --all
#
# Prints `rust=true|false`: whether any changed path can affect the Rust workspace's build,
# lints, tests or packaging. A Go-only, docs-only or other-workflow change skips the Rust jobs,
# so it doesn't take a scarce macOS runner. Pushes to main and manual runs pass --all. The
# workflow fails safe: if this script fails, every job runs.
set -euo pipefail

# Paths the Rust workspace reads: its sources and manifests, the toolchain and lint configs, this
# workflow and its action, and the parity cases and test data crates read at test time.
affects_rust() {
  case $1 in
    crates/* | contract/* | agent/* | .cargo/* | .config/*) return 0 ;;
    Cargo.toml | Cargo.lock | rust-toolchain.toml | rustfmt.toml | clippy.toml | deny.toml) return 0 ;;
    .github/workflows/rust.yml | .github/actions/rust-toolchain/*) return 0 ;;
    scripts/rust-ci-scope.sh) return 0 ;;
    *) return 1 ;;
  esac
}

if [[ ${1:-} == --all ]]; then
  echo rust=true
  exit 0
fi
if [[ $# -gt 0 ]]; then
  echo "usage: scripts/rust-ci-scope.sh [--all] < changed-paths" >&2
  exit 2
fi

rust=false
while IFS= read -r path; do
  [[ -n $path ]] || continue
  if affects_rust "$path"; then
    rust=true
  fi
done
echo "rust=$rust"
