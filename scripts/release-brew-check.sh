#!/usr/bin/env bash
set -euo pipefail

# Check a rendered spectra.rb the way the tap's own CI does, before it is
# pushed to kaeawc/homebrew-tap.
#
# Usage: scripts/release-brew-check.sh [--offline] <formula>
#
# Default: brew style, brew audit --strict --online, brew install and brew test
# against the published release archives. --offline runs only brew style and
# brew audit --strict, for builds whose archives are not published yet.

TAP="spectra-release/check"

offline=0
if [[ "${1:-}" == "--offline" ]]; then
    offline=1
    shift
fi
if [[ $# -ne 1 ]]; then
    echo "Usage: $0 [--offline] <formula>" >&2
    exit 2
fi
FORMULA="$1"
if [[ ! -f "$FORMULA" ]]; then
    echo "error: formula not found: $FORMULA" >&2
    exit 1
fi
if ! command -v brew >/dev/null 2>&1; then
    echo "error: brew is not installed" >&2
    exit 1
fi

export HOMEBREW_NO_AUTO_UPDATE=1
export HOMEBREW_NO_ANALYTICS=1
export HOMEBREW_NO_ENV_HINTS=1

# brew audits formulae by name, so stage the file in a throwaway local tap.
tap_dir="$(brew --repository "$TAP")"
if [[ ! -d "$tap_dir/Formula" ]]; then
    brew tap-new --no-git "$TAP" >/dev/null
fi
# Newer Homebrew refuses to load formulae from taps nobody trusted.
if brew help trust >/dev/null 2>&1; then
    brew trust "$TAP"
fi
cp "$FORMULA" "$tap_dir/Formula/spectra.rb"

brew style "$TAP/spectra"
if [[ "$offline" == 1 ]]; then
    brew audit --strict --formula "$TAP/spectra"
    echo "brew style and brew audit --strict passed (offline)."
    exit 0
fi

brew audit --strict --online --formula "$TAP/spectra"
brew install --formula "$TAP/spectra"
brew test "$TAP/spectra"
echo "brew style, audit --strict --online, install and test passed."
