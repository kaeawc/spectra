#!/usr/bin/env bash
set -euo pipefail

# Push a rendered spectra.rb to Formula/spectra.rb in the Homebrew tap.
#
# Usage: scripts/release-tap.sh <tag> <formula>
#
# Environment:
#   TAP_DEPLOY_KEY  SSH private key of a write deploy key on kaeawc/homebrew-tap.
#                   Required for the default SSH remote.
#   TAP_REMOTE      Tap git remote. Defaults to git@github.com:kaeawc/homebrew-tap.git;
#                   tests point it at a local bare repository.
#   TAP_BRANCH      Branch to push. Defaults to main.
#
# Prerelease tags are skipped: `brew install kaeawc/tap/spectra` tracks stable
# releases only. A tag older than the tap's current formula is skipped too, so
# re-running an old release never downgrades the tap. Running it twice for the
# same release is a no-op.

TAP_REMOTE="${TAP_REMOTE:-git@github.com:kaeawc/homebrew-tap.git}"
TAP_BRANCH="${TAP_BRANCH:-main}"
FORMULA_PATH="Formula/spectra.rb"

# GitHub's published SSH host keys (https://api.github.com/meta, ssh_keys).
GITHUB_KNOWN_HOSTS='github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg='

STABLE_RE='^v([0-9]+)\.([0-9]+)\.([0-9]+)$'
TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <tag> <formula>" >&2
    exit 2
fi
TAG="$1"
FORMULA="$2"

if [[ ! "$TAG" =~ $TAG_RE ]]; then
    echo "error: tag must be vX.Y.Z or vX.Y.Z-<pre>, got '$TAG'" >&2
    exit 1
fi
if [[ ! "$TAG" =~ $STABLE_RE ]]; then
    echo "Skipping the tap for prerelease $TAG; it tracks stable releases only."
    exit 0
fi
if [[ ! -f "$FORMULA" ]]; then
    echo "error: formula not found: $FORMULA" >&2
    exit 1
fi
if ! grep -q "releases/download/${TAG}/spectra_${TAG}_" "$FORMULA"; then
    echo "error: $FORMULA does not point at the $TAG release archives" >&2
    exit 1
fi
if grep -Eq '@[A-Z0-9_]+@' "$FORMULA"; then
    echo "error: $FORMULA still has template placeholders" >&2
    exit 1
fi
FORMULA="$(cd "$(dirname "$FORMULA")" && pwd)/$(basename "$FORMULA")"

# version_key <tag> prints a zero-padded sort key for a stable vX.Y.Z tag.
version_key() {
    if [[ "$1" =~ $STABLE_RE ]]; then
        printf '%06d%06d%06d' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
    else
        return 1
    fi
}

# formula_tag <file> prints the release tag the formula's archive URLs use.
formula_tag() {
    sed -n 's|.*/releases/download/\(v[^/]*\)/spectra_.*|\1|p' "$1" | head -n 1
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [[ "$TAP_REMOTE" == git@* || "$TAP_REMOTE" == ssh://* ]]; then
    if [[ -z "${TAP_DEPLOY_KEY:-}" ]]; then
        echo "error: TAP_DEPLOY_KEY is empty. Create the HOMEBREW_TAP_DEPLOY_KEY secret in the" >&2
        echo "  'release' environment (see packaging/release/README.md); the GitHub release" >&2
        echo "  is already published, only the tap update is missing." >&2
        exit 1
    fi
    (umask 077 && printf '%s\n' "$TAP_DEPLOY_KEY" > "$WORK/deploy_key")
    printf '%s\n' "$GITHUB_KNOWN_HOSTS" > "$WORK/known_hosts"
    export GIT_SSH_COMMAND="ssh -i '$WORK/deploy_key' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile='$WORK/known_hosts'"
fi

git clone --quiet --depth 1 --branch "$TAP_BRANCH" "$TAP_REMOTE" "$WORK/tap"
cd "$WORK/tap"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"

if [[ -f "$FORMULA_PATH" ]]; then
    current="$(formula_tag "$FORMULA_PATH")"
    if ! current_key="$(version_key "$current")"; then
        echo "error: cannot read the stable release tag from the tap's $FORMULA_PATH (got '$current')" >&2
        exit 1
    fi
    if [[ "$current_key" > "$(version_key "$TAG")" ]]; then
        echo "The tap already has $current, newer than $TAG; leaving it unchanged."
        exit 0
    fi
fi

mkdir -p "$(dirname "$FORMULA_PATH")"
cp "$FORMULA" "$FORMULA_PATH"
git add "$FORMULA_PATH"
if git diff --cached --quiet; then
    echo "The tap's $FORMULA_PATH is already current for $TAG."
    exit 0
fi
git commit --quiet -m "spectra $TAG"

# Another release (for example auto-mobile's) can push to the tap at the same
# time; rebase onto it and retry rather than fail.
attempt=1
until git push --quiet origin "HEAD:$TAP_BRANCH"; do
    if [[ "$attempt" -ge 3 ]]; then
        echo "error: pushing to $TAP_REMOTE $TAP_BRANCH failed after $attempt attempts" >&2
        exit 1
    fi
    attempt=$((attempt + 1))
    git pull --quiet --rebase origin "$TAP_BRANCH"
done
echo "Pushed spectra $TAG to $TAP_REMOTE ($FORMULA_PATH)."
