#!/usr/bin/env bash
set -euo pipefail

# Tests scripts/hive-setup.sh against fake releases served over file://. A
# curl shim on PATH serves this checkout's install.sh for the tagged installer
# URL, answers the latest-release lookup, and refuses any other network URL.
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0

case "$(uname -s)" in Darwin) os=darwin ;; *) os=linux ;; esac

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }

# make_release <tag>: archives for both arches (install.sh may pick the native
# one under Rosetta) whose stubs report the tag; spectra logs other commands
# to $DAEMON_LOG.
make_release() {
  local tag="$1" arch name
  mkdir -p "$WORK/releases/$tag"
  : > "$WORK/releases/$tag/checksums.txt"
  for arch in amd64 arm64; do
    name="spectra_${tag}_${os}_${arch}"
    mkdir -p "$WORK/src/$name/bin"
    cat > "$WORK/src/$name/bin/spectra" <<EOF
#!/bin/sh
if [ "\$1" = version ]; then echo $tag; else echo "\$*" >> "\$DAEMON_LOG"; fi
EOF
    printf '#!/bin/sh\necho "spectra-mcp %s"\n' "$tag" > "$WORK/src/$name/bin/spectra-mcp"
    chmod +x "$WORK/src/$name/bin/"*
    tar -czf "$WORK/releases/$tag/$name.tar.gz" -C "$WORK/src" "$name"
    echo "$(sha256 "$WORK/releases/$tag/$name.tar.gz")  $name.tar.gz" >> "$WORK/releases/$tag/checksums.txt"
  done
}

REAL_CURL="$(command -v curl)"
mkdir -p "$WORK/shim"
cat > "$WORK/shim/curl" <<EOF
#!/bin/sh
for a in "\$@"; do url="\$a"; done
case "\$url" in
  file://*) exec "$REAL_CURL" "\$@" ;;
  https://github.com/kaeawc/spectra/releases/latest)
    [ -n "\${FAKE_LATEST:-}" ] || exit 6
    echo "https://github.com/kaeawc/spectra/releases/tag/\$FAKE_LATEST" ;;
  https://raw.githubusercontent.com/kaeawc/spectra/*/scripts/install.sh)
    echo "\$url" >> "\$CURL_LOG"
    while [ "\$1" != -o ]; do shift; done
    cp "$HERE/install.sh" "\$2" ;;
  *) echo "blocked \$url" >> "\$CURL_LOG"; exit 7 ;;
esac
EOF
chmod +x "$WORK/shim/curl"
export PATH="$WORK/shim:$PATH"

export DAEMON_LOG="$WORK/daemon.log" CURL_LOG="$WORK/curl.log"
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

installer_fetches() {
  local rc=0
  [ "$(wc -l < "$CURL_LOG" | tr -d ' ')" -eq "$2" ] || rc=1
  report "$1" "$rc"
}

output_has() {
  local rc=0
  grep -qF "$2" <<<"$out" || rc=1
  report "$1" "$rc"
}

make_release v9.0.0
make_release v9.0.1
: > "$CURL_LOG"

out="$(setup --version v9.0.0)"
version_is "fresh install" v9.0.0
check "installs spectra-mcp" test -x "$SPECTRA_HOME/bin/spectra-mcp"
check "fetches install.sh at the tag" grep -qx "https://raw.githubusercontent.com/kaeawc/spectra/v9.0.0/scripts/install.sh" "$CURL_LOG"
check "starts the daemon" grep -qx "daemon start" "$DAEMON_LOG"
output_has "prints the MCP command" "MCP server command: $SPECTRA_HOME/bin/spectra-mcp"

out="$(setup --version v9.0.0)"
output_has "same version skips the download" "already installed"
installer_fetches "skip does not fetch the installer" 1
daemon_starts "restarts the daemon on rerun" 2

rm "$SPECTRA_HOME/bin/spectra-mcp"
out="$(setup --version v9.0.0 --no-daemon)"
check "half-finished install is redone" test -x "$SPECTRA_HOME/bin/spectra-mcp"
daemon_starts "--no-daemon skips the daemon" 2

out="$(setup --version v9.0.1 --no-daemon)"
version_is "upgrades to a new version" v9.0.1

out="$(FAKE_LATEST=v9.0.1 setup --no-daemon)"
output_has "latest resolves to the installed tag" "spectra v9.0.1 already installed"

out="$(setup --no-daemon)"
output_has "unresolvable latest keeps the install" "keeping spectra v9.0.1"

if out="$(setup --version v9.9.9 --no-daemon)"; then
  report "missing release fails" 1
else
  version_is "missing release fails and keeps the install" v9.0.1
fi

before="$(wc -l < "$CURL_LOG" | tr -d ' ')"
if out="$(setup --version '../evil' --no-daemon)"; then
  report "malformed tag is rejected" 1
else
  installer_fetches "malformed tag is rejected before any fetch" "$before"
fi

# Run from another repo's directory, the script must not use its install.sh.
mkdir -p "$WORK/other-repo"
printf '#!/bin/sh\ntouch "%s/planted-ran"\n' "$WORK" > "$WORK/other-repo/install.sh"
out="$(cd "$WORK/other-repo" && SPECTRA_HOME="$WORK/home2" sh -s -- --base-url "$BASE" --version v9.0.0 --no-daemon < "$HERE/hive-setup.sh" 2>&1)" || true
check "piped run ignores ./install.sh" test ! -e "$WORK/planted-ran"
check "piped run installs" test -x "$WORK/home2/bin/spectra-mcp"

check "no other network access" test -z "$(grep blocked "$CURL_LOG" || true)"

exit "$fail"
