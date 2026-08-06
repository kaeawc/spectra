#!/usr/bin/env bash
# Regenerate internal/rules/electron_releases.json from Electron's public
# release metadata. Manual step — deliberately NOT wired into the build so the
# build and test paths stay network-free and stdlib-only. Run it when the
# Electron support window has moved and the electron-chromium-eol rule should
# learn about newer majors.
#
# Requires: curl, jq. Run from anywhere in the repo.
set -euo pipefail

SRC="https://releases.electronjs.org/releases.json"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="$here/../../internal/rules/electron_releases.json"
today="$(date -u +%F)"

curl -fsSL "$SRC" \
  | jq --arg asof "$today" '
      [ .[]
        # stable releases only (exclude -beta / -alpha / -nightly)
        | select(.version | test("^[0-9]+\\.[0-9]+\\.[0-9]+$"))
        | { major:    (.version | split(".")[0] | tonumber),
            chromium: (.chrome  | split(".")[0] | tonumber),
            node:     (.node    | split(".")[0]),
            released: .date } ]
      | group_by(.major)
      | map(min_by(.released))          # earliest stable of a major is its GA
      | sort_by(.major)
      | { "_comment": "Static Electron major -> bundled runtime versions. Used by the electron-chromium-eol rule. No network access at build or runtime. Regenerate with scripts/electron/gen_releases.sh (manual; not run in CI).",
          "_source": "https://releases.electronjs.org/releases.json",
          "table_as_of": $asof,
          "releases": . }
    ' > "$out"

echo "wrote $out (table_as_of=$today)"
