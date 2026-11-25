#!/usr/bin/env bash
set -euo pipefail

# Tests scripts/release-tap.sh against a local bare repository standing in for
# kaeawc/homebrew-tap. Needs git; no network or credentials.

HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/release-tap.sh"
BASH_BIN="${BASH_BIN:-bash}"
fail=0

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid

# formula <tag> <out> writes a minimal rendered formula for <tag>.
formula() {
    cat > "$2" <<EOF
class Spectra < Formula
  url "https://github.com/kaeawc/spectra/releases/download/$1/spectra_$1_darwin_arm64.tar.gz"
end
EOF
}

# new_tap [tag] creates a bare tap remote, seeded with a formula for [tag].
new_tap() {
    rm -rf "$WORK/remote.git" "$WORK/seed"
    git init --quiet --bare --initial-branch=main "$WORK/remote.git"
    git init --quiet --initial-branch=main "$WORK/seed"
    mkdir -p "$WORK/seed/Formula"
    : > "$WORK/seed/Formula/.gitkeep"
    if [[ -n "${1:-}" ]]; then
        formula "$1" "$WORK/seed/Formula/spectra.rb"
    fi
    git -C "$WORK/seed" add -A
    git -C "$WORK/seed" commit --quiet -m seed
    git -C "$WORK/seed" push --quiet "$WORK/remote.git" main
}

tap_tag() {
    git --git-dir="$WORK/remote.git" show main:Formula/spectra.rb 2>/dev/null |
        sed -n 's|.*/releases/download/\(v[^/]*\)/.*|\1|p'
}

commits() {
    git --git-dir="$WORK/remote.git" rev-list --count main
}

run() {
    TAP_REMOTE="file://$WORK/remote.git" "$BASH_BIN" "$SCRIPT" "$@"
}

check() {
    local name="$1" want="$2" got="$3"
    if [[ "$got" == "$want" ]]; then
        echo "PASS: $name"
    else
        echo "FAIL: $name: want '$want', got '$got'"
        fail=1
    fi
}

# Updates an older formula and commits as "spectra <tag>".
new_tap v0.1.2
formula v0.1.3 "$WORK/spectra.rb"
run v0.1.3 "$WORK/spectra.rb" >/dev/null
check "update to newer tag" v0.1.3 "$(tap_tag)"
check "commit message" "spectra v0.1.3" "$(git --git-dir="$WORK/remote.git" log -1 --format=%s main)"

# A second run for the same release changes nothing.
run v0.1.3 "$WORK/spectra.rb" >/dev/null
check "idempotent rerun" 2 "$(commits)"

# An older tag never downgrades the tap.
formula v0.1.2 "$WORK/old.rb"
out="$(run v0.1.2 "$WORK/old.rb")"
check "no downgrade" v0.1.3 "$(tap_tag)"
check "no downgrade message" 1 "$(grep -c 'newer than v0.1.2' <<<"$out")"

# Minor and patch compare numerically, not as strings.
new_tap v0.9.0
formula v0.10.0 "$WORK/ten.rb"
run v0.10.0 "$WORK/ten.rb" >/dev/null
check "numeric compare" v0.10.0 "$(tap_tag)"

# A tap without the formula gets it.
new_tap
formula v0.1.3 "$WORK/spectra.rb"
run v0.1.3 "$WORK/spectra.rb" >/dev/null
check "first publish" v0.1.3 "$(tap_tag)"

# Prereleases are skipped.
new_tap v0.1.3
formula v0.2.0-rc.1 "$WORK/rc.rb"
run v0.2.0-rc.1 "$WORK/rc.rb" >/dev/null
check "prerelease skipped" v0.1.3 "$(tap_tag)"

# A formula for another tag, a leftover placeholder, or a bad tag fails.
formula v0.1.4 "$WORK/other.rb"
if run v0.1.5 "$WORK/other.rb" >/dev/null 2>&1; then
    check "tag mismatch fails" fail pass
else
    check "tag mismatch fails" fail fail
fi
printf '  sha256 "@SHA256_LINUX_AMD64@"\n' >> "$WORK/other.rb"
if run v0.1.4 "$WORK/other.rb" >/dev/null 2>&1; then
    check "placeholder fails" fail pass
else
    check "placeholder fails" fail fail
fi
if run 0.1.4 "$WORK/spectra.rb" >/dev/null 2>&1; then
    check "bad tag fails" fail pass
else
    check "bad tag fails" fail fail
fi

# The default SSH remote without a deploy key fails with the secret's name.
if out="$(TAP_DEPLOY_KEY='' "$BASH_BIN" "$SCRIPT" v0.1.3 "$WORK/spectra.rb" 2>&1)"; then
    check "missing deploy key fails" fail pass
else
    check "missing deploy key names the secret" 1 "$(grep -c 'HOMEBREW_TAP_DEPLOY_KEY' <<<"$out")"
fi

# A tap formula whose tag cannot be read is an error, not an overwrite.
new_tap
printf 'class Spectra < Formula\nend\n' > "$WORK/seed/Formula/spectra.rb"
git -C "$WORK/seed" add -A
git -C "$WORK/seed" commit --quiet -m odd
git -C "$WORK/seed" push --quiet "$WORK/remote.git" main
if run v0.1.3 "$WORK/spectra.rb" >/dev/null 2>&1; then
    check "unreadable tap formula fails" fail pass
else
    check "unreadable tap formula fails" fail fail
fi

exit "$fail"
