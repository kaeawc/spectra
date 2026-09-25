#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

pattern='tailscale\.com|github\.com/slackhq/nebula|github\.com/kaeawc/spectra-proxy|github\.com/kaeawc/spectra-protocol'
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
