#!/usr/bin/env bash
# Open a release PR: bump the package version, date the CHANGELOG's Unreleased section, push a
# release/vX.Y.Z branch from origin/main and open the PR. Merging it runs
# .github/workflows/release.yml, which builds the macOS binaries and creates the tag and GitHub
# release.
#   scripts/release.sh 0.1.0
set -euo pipefail
new="${1:?usage: scripts/release.sh <version>}"
[[ "$new" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "not a semver version: $new" >&2; exit 1; }
cd "$(dirname "$0")/.."
[ -z "$(git status --porcelain)" ] || { echo "working tree not clean" >&2; exit 1; }

git fetch -q origin main --tags
git rev-parse -q --verify "refs/tags/v$new" > /dev/null && { echo "v$new already exists" >&2; exit 1; }
branch="release/v$new"
git switch -q -c "$branch" origin/main

old="$(sed -n 's/^version = "\(.*\)"$/\1/p' Cargo.toml | head -1)"
[ "$old" != "$new" ] || { echo "Cargo.toml is already at $new" >&2; exit 1; }
sed -i.bak -e "s/^version = \"$old\"$/version = \"$new\"/" Cargo.toml
rm Cargo.toml.bak
cargo update --workspace --offline

# "## [Unreleased]" keeps its heading; its entries move under the new version.
python3 - "$new" "$(date +%Y-%m-%d)" <<'EOF'
import pathlib, sys
version, day = sys.argv[1], sys.argv[2]
p = pathlib.Path("CHANGELOG.md")
s = p.read_text()
head = "## [Unreleased]\n"
if head not in s:
    sys.exit("CHANGELOG.md has no '## [Unreleased]' section")
p.write_text(s.replace(head, f"{head}\n## [{version}] - {day}\n", 1))
EOF

git commit -qam "Release v$new"
git push -q -u origin "$branch"
notes="$(awk -v v="$new" 'index($0, "## [" v "]") == 1 { on = 1; next } on && /^## \[/ { exit } on { print }' CHANGELOG.md)"
gh pr create --base main --head "$branch" --title "Release v$new" \
  --body "Bumps the package to $new. Merging builds the macOS binaries and creates the v$new tag and GitHub release.

$notes"
