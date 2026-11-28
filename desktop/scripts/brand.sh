#!/usr/bin/env bash
# Redraws every file in assets/brand/ (app and Dock icons, .icns, favicon, menu bar template,
# web icons, Linux icon theme, wordmark, SVGs) from the mark's description in src/mark/. Run it
# after changing the mark and commit what changes; a test fails while the SVGs are stale.
#
#   scripts/brand.sh            # into assets/brand/
#   scripts/brand.sh <dir>      # somewhere else, to compare
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cargo run --quiet --release --manifest-path "$root/Cargo.toml" --example brand -- "${1:-$root/assets/brand}"
