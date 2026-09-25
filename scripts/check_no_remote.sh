#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

pattern='tailscale\.com|github\.com/slackhq/nebula|github\.com/kaeawc/spectra-proxy'
if grep -Eq "$pattern" go.mod go.sum; then
    echo 'core boundary: remote dependency found in go.mod or go.sum' >&2
    exit 1
fi

for dir in internal/serve internal/rpc; do
    if [[ -d "$dir" ]]; then
        echo "core boundary: forbidden directory $dir exists" >&2
        exit 1
    fi
done

deps="$(go list -deps ./cmd/...)"
if grep -Eq "$pattern" <<< "$deps"; then
    echo 'core boundary: remote package found in command dependencies' >&2
    exit 1
fi

if ! module_graph="$(go mod graph)"; then
    echo 'core boundary: could not inspect Go module graph' >&2
    exit 1
fi
# The go@ edge records the dependency's minimum toolchain, not a module dependency.
if printf '%s\n' "$module_graph" | awk '$1 ~ /^github.com\/kaeawc\/spectra-protocol@/ && $2 !~ /^go@/ { found = 1 } END { exit !found }'; then
    echo 'core boundary: spectra-protocol must not have transitive module dependencies' >&2
    exit 1
fi
