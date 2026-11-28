#!/usr/bin/env bash
set -euo pipefail

# Runs a command inside one of the .buildkite/images/<name> images, with the
# checkout mounted at /work and owned by the calling user. Used by
# .buildkite/pipeline.yml; works the same locally with Docker.
#
# Usage: .buildkite/run-in.sh <image> <command> [args...]   (image: docs)

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <image> <command> [args...]" >&2
  exit 2
fi

name="$1"
shift
root="$(cd "$(dirname "$0")/.." && pwd)"
context="$root/.buildkite/images/$name"
if [ ! -f "$context/Dockerfile" ]; then
  echo "no image named '$name' under .buildkite/images" >&2
  exit 2
fi

image="$(docker build --quiet "$context")"

# HOME (and GOPATH and the Go caches, for Go images) point at /tmp because the
# image's own directories belong to root and the command runs as the calling user.
exec docker run --rm \
  --volume "$root:/work" \
  --workdir /work \
  --user "$(id -u):$(id -g)" \
  --env HOME=/tmp/home \
  --env GOPATH=/tmp/go \
  --env GOCACHE=/tmp/go-cache \
  --env GOMODCACHE=/tmp/go/pkg/mod \
  --env PATH=/tmp/home/.local/bin:/tmp/go/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
  --env GIT_CONFIG_COUNT=1 \
  --env GIT_CONFIG_KEY_0=safe.directory \
  --env GIT_CONFIG_VALUE_0=/work \
  --env CI=true \
  "$image" "$@"
