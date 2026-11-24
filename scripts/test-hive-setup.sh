#!/usr/bin/env bash
set -euo pipefail

# Tests scripts/hive-setup.sh against fake releases served over file://.
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0

case "$(uname -s)" in Darwin) os=darwin ;; *) os=linux ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; *) arch=arm64 ;; esac

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }

# make_release <tag>: a release whose spectra stub prints the tag and logs
# each `daemon start` to $DAEMON_LOG.
make_release() {
  local tag="$1" name="spectra_$1_${os}_${arch}"
  mkdir -p "$WORK/src/$name/bin" "$WORK/releases/$tag"
  cat > "$WORK/src/$name/bin/spectra" <<EOF
#!/bin/sh
if [ "\$1" = version ]; then echo $tag; else echo "\$*" >> "\$DAEMON_LOG"; fi
EOF
  printf '#!/bin/sh\necho mcp\n' > "$WORK/src/$name/bin/spectra-mcp"
  chmod +x "$WORK/src/$name/bin/"*
  tar -czf "$WORK/releases/$tag/$name.tar.gz" -C "$WORK/src" "$name"
  echo "$(sha256 "$WORK/releases/$tag/$name.tar.gz")  $name.tar.gz" > "$WORK/releases/$tag/checksums.txt"
}

export DAEMON_LOG="$WORK/daemon.log"
export SPECTRA_HOME="$WORK/home"
BASE="file://$WORK/releases"

setup() { sh "$HERE/hive-setup.sh" --base-url "$BASE" "$@" 2>&1; }

# report <name> <status>
report() {
  if [ "$2" -eq 0 ]; then
    echo "PASS: $1"
  else
    echo "FAIL: $1"
    echo "$out"
    fail=1
  fi
}

# check <name> <command...>
check() {
  local name="$1" rc=0
  shift
  "$@" || rc=$?
  report "$name" "$rc"
}

version_is() {
  local rc=0
  [ "$("$SPECTRA_HOME/bin/spectra" version)" = "$2" ] || rc=1
  report "$1" "$rc"
}

daemon_starts() {
  local rc=0
  [ "$(wc -l < "$DAEMON_LOG" | tr -d ' ')" -eq "$2" ] || rc=1
  report "$1" "$rc"
}

output_has() {
  local rc=0
  grep -qF "$2" <<<"$out" || rc=1
  report "$1" "$rc"
}

make_release v9.0.0
make_release v9.0.1

out="$(setup --version v9.0.0)"
version_is "fresh install" v9.0.0
check "installs spectra-mcp" test -x "$SPECTRA_HOME/bin/spectra-mcp"
check "starts the daemon" grep -qx "daemon start" "$DAEMON_LOG"
output_has "prints the MCP command" "MCP server command: $SPECTRA_HOME/bin/spectra-mcp"

rm -rf "$WORK/releases/v9.0.0"
out="$(setup --version v9.0.0)"
output_has "same version skips the download" "already installed"
daemon_starts "restarts the daemon on rerun" 2

out="$(setup --version v9.0.1 --no-daemon)"
version_is "upgrades to a new version" v9.0.1
daemon_starts "--no-daemon skips the daemon" 2

if out="$(setup --version v9.9.9 --no-daemon)"; then
  echo "FAIL: missing release should fail"; echo "$out"; fail=1
else
  version_is "missing release fails and keeps the install" v9.0.1
fi

# Piped into sh, the script must not run an install.sh from the current dir.
mkdir -p "$WORK/other-repo"
printf '#!/bin/sh\ntouch "%s/planted-ran"\n' "$WORK" > "$WORK/other-repo/install.sh"
out="$(cd "$WORK/other-repo" && sh -s -- --base-url "$BASE" --version v9.9.8 --no-daemon < "$HERE/hive-setup.sh" 2>&1)" || true
check "piped run ignores ./install.sh" test ! -e "$WORK/planted-ran"

exit "$fail"
