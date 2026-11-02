#!/usr/bin/env bash
set -euo pipefail

# Runs a command, retrying with backoff when it fails. Use ONLY for network
# fetches (go install, go mod download); never wrap a scan, test, or lint step,
# or a genuine failure would be retried and slowed instead of failing fast.
#
# Usage: scripts/retry.sh <command> [args...]
#
# Environment:
#   RETRY_DELAYS  space-separated seconds to sleep before each retry; the
#                 number of retries equals the number of entries.
#                 Default "5 15" (3 attempts total).

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <command> [args...]" >&2
  exit 2
fi

read -r -a delays <<< "${RETRY_DELAYS:-5 15}"
attempts=$(( ${#delays[@]} + 1 ))

attempt=1
while true; do
  rc=0
  "$@" || rc=$?
  if [ "$rc" -eq 0 ]; then
    exit 0
  fi
  if [ "$attempt" -ge "$attempts" ]; then
    echo "retry: '$*' failed after $attempts attempts (exit $rc)" >&2
    exit "$rc"
  fi
  delay="${delays[$((attempt - 1))]}"
  echo "retry: '$*' failed (exit $rc), attempt $attempt/$attempts; retrying in ${delay}s" >&2
  sleep "$delay"
  attempt=$((attempt + 1))
done
