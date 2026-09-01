#!/usr/bin/env bash
set -uo pipefail

# Experimental Darling CI: cross-compile Spectra for darwin/amd64, then run
# the binaries and per-package test suites under Darling
# (https://darlinghq.org), a macOS translation layer for Linux.
#
# Requires: go, darling (see .github/workflows/darling.yml for the install).
#
# Exit status:
#   0 — Darling booted and the spectra binary executed; individual probe and
#       test-package failures are reported in the summary but do not fail the
#       script, because Darling's coverage of the macOS utilities Spectra
#       shells out to (plutil, otool, codesign, file, sqlite3) is incomplete.
#   1 — infrastructure failure: darling missing, cross-compile failed, the
#       Darling prefix would not boot, or spectra would not execute at all.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

SUMMARY_FILE="${GITHUB_STEP_SUMMARY:-/dev/null}"
DARLING_TIMEOUT="${DARLING_TIMEOUT:-300}"
TEST_LOG="$(mktemp)"
trap 'rm -f "$TEST_LOG"' EXIT

summary() { printf '%s\n' "$*" >>"$SUMMARY_FILE"; }
log() { printf '\n=== %s\n' "$*"; }

# Run a command line inside the Darling prefix. `darling shell` treats its
# arguments as literal argv words (no shell evaluation), so hand the line to
# the guest's bash explicitly. darling maps the Linux cwd to the same path
# inside the prefix, so relative paths work. timeout(1) guards against hangs
# in darlingserver or the guest process.
indarling() {
  timeout "$DARLING_TIMEOUT" darling shell /bin/bash -c "$*" </dev/null
}

# Like indarling, but with dist-darling/guestbin (the CI-only plutil shim,
# see ci/plutil) prepended to PATH. Used for spectra probes and test runs;
# the utility probe uses plain indarling so it reports what Darling itself
# ships. The host filesystem appears at /Volumes/SystemRoot in the guest.
GUEST_BIN="/Volumes/SystemRoot$REPO_ROOT/dist-darling/guestbin"
# Darling's sigtramp emulation crashes when signals (SIGCHLD from heavy
# subprocess exec, SIGURG from async preemption) land on threads inside
# libSystem syscalls ("SIGSEGV ... semasleep on Darwin signal stack").
# asyncpreemptoff and GOMAXPROCS=1 remove some signal sources and narrow
# the race window but do not fully prevent it — internal/detect's
# subprocess-heavy suite still crashes this way (Darling bug, not ours).
indarling_tools() {
  timeout "$DARLING_TIMEOUT" darling shell /bin/bash -c "export PATH=\"$GUEST_BIN:\$PATH\" GODEBUG=asyncpreemptoff=1 GOMAXPROCS=1 $SECSHIM_ENV; $*" </dev/null
}

if ! command -v darling >/dev/null 2>&1; then
  echo "error: darling is not installed" >&2
  exit 1
fi

# -ldflags=-w strips DWARF: Go's darwin binaries overlap the __DWARF and
# __LINKEDIT segment vmaddrs, which Darling's loader rejects with
# "Cannot mmap segment __LINKEDIT: File exists" (darlinghq/darling#1178).
log "Cross-compiling darwin/amd64 binaries"
mkdir -p dist-darling
for tool in spectra spectra-mcp spectra-helper; do
  CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags=-w -o "dist-darling/$tool" "./cmd/$tool/" || exit 1
done
# Darling ships neither plutil nor sqlite3; build the CI-only shims into the
# guest PATH dir.
mkdir -p dist-darling/guestbin
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags=-w -o dist-darling/guestbin/plutil ./ci/plutil/ || exit 1
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags=-w -o dist-darling/guestbin/sqlite3 ./ci/sqlite3/ || exit 1

# Stub the Security.framework symbols Darling's macOS 11.7 framework lacks
# (see ci/secshim/secshim.c). Requires zig as the darwin cross-linker; skip
# with a note when it's unavailable (e.g. local runs).
SECSHIM_ENV=""
if command -v zig >/dev/null 2>&1; then
  zig cc -target x86_64-macos -shared -Wl,-undefined,dynamic_lookup \
    -o dist-darling/guestbin/libsecshim.dylib ci/secshim/secshim.c || exit 1
  SECSHIM_ENV="DYLD_FORCE_FLAT_NAMESPACE=1 DYLD_INSERT_LIBRARIES=/Volumes/SystemRoot$REPO_ROOT/dist-darling/guestbin/libsecshim.dylib"
else
  echo "note: zig not found; Security stub disabled, crypto/x509 binaries will fail at dyld"
fi

log "Booting Darling prefix (first run initializes ~/.darling)"
if ! indarling 'sw_vers; uname -mrs'; then
  echo "error: Darling failed to boot" >&2
  exit 1
fi

summary "## Darling smoke test"
summary ""

# Spectra shells out to macOS-only utilities; whichever are missing here
# predict which detections degrade under Darling.
log "Probing for macOS utilities inside Darling"
summary "### macOS utilities available inside Darling"
summary ""
summary "| utility | present |"
summary "|---------|---------|"
for tool in plutil otool codesign file sqlite3 ps sysctl sw_vers spctl launchctl xattr vm_stat; do
  if path="$(indarling "command -v $tool" 2>/dev/null)"; then
    echo "found: $tool -> $path"
    summary "| \`$tool\` | yes — \`$path\` |"
  else
    echo "missing: $tool"
    summary "| \`$tool\` | no |"
  fi
done
summary ""
summary "CI-only shims are injected for the probes and tests below: \`plutil\` (ci/plutil) and \`sqlite3\` (ci/sqlite3) on PATH, plus Security.framework symbol stubs (ci/secshim) via DYLD_INSERT_LIBRARIES when built."

# Gate on a dependency-free binary: it proves the Go darwin runtime itself
# executes under Darling. The spectra binaries additionally bind
# Security.framework symbols via crypto/x509 (Go 1.24+ targets macOS 12,
# Darling emulates 11.7), so their load failures are findings, not gates.
log "Checking the Go darwin runtime under Darling"
cat >dist-darling/hello.go <<'EOF'
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("go-darwin-ok pid", os.Getpid())
}
EOF
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags=-w -o dist-darling/hello dist-darling/hello.go || exit 1
if ! indarling './dist-darling/hello'; then
  summary ""
  summary "**Go darwin binaries do not execute under this Darling build.**"
  echo "error: Go darwin runtime did not run under Darling" >&2
  exit 1
fi

# A synthetic bundle in the shape detect_test.go's makeBundle produces, so
# the inspect path (plutil/otool/codesign/file) gets exercised end to end.
rm -rf dist-darling/Sample.app
mkdir -p dist-darling/Sample.app/Contents/MacOS
cat >dist-darling/Sample.app/Contents/Info.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>org.example.sample</string>
	<key>CFBundleName</key><string>Sample</string>
	<key>CFBundleExecutable</key><string>Sample</string>
	<key>CFBundleShortVersionString</key><string>1.0</string>
</dict>
</plist>
EOF
cp dist-darling/spectra dist-darling/Sample.app/Contents/MacOS/Sample

summary ""
summary "### spectra probes"
summary ""
summary "| command | result |"
summary "|---------|--------|"
probes=(
  "version"
  "help"
  "toolchain"
  "storage"
  "power"
  "network"
  "process"
  "--json dist-darling/Sample.app"
)
for probe in "${probes[@]}"; do
  log "spectra $probe"
  if indarling_tools "./dist-darling/spectra $probe"; then
    summary "| \`spectra $probe\` | pass |"
  else
    rc=$?
    summary "| \`spectra $probe\` | fail (exit $rc) |"
  fi
done

log "Compiling and running test binaries under Darling"
summary ""
summary "### go test packages (darwin/amd64 binaries executed under Darling)"
summary ""
summary "| package | result |"
summary "|---------|--------|"
pass=0
fail=0
nocompile=0
timeouts_in_a_row=0
while IFS=' ' read -r import_path dir; do
  [ -n "$import_path" ] || continue
  short="${import_path#github.com/kaeawc/spectra}"
  short="${short:-/}"

  if [ "$timeouts_in_a_row" -ge 3 ]; then
    summary "| \`$short\` | skipped (3 consecutive timeouts) |"
    continue
  fi

  bin="$(basename "$import_path").darling.test"
  if ! CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go test -c -ldflags=-w -o "$dir/$bin" "$import_path"; then
    nocompile=$((nocompile + 1))
    summary "| \`$short\` | did not compile for darwin |"
    continue
  fi

  echo "--- $import_path"
  if (cd "$dir" && indarling_tools "./$bin -test.timeout 240s") >"$TEST_LOG" 2>&1; then
    pass=$((pass + 1))
    timeouts_in_a_row=0
    summary "| \`$short\` | pass |"
  else
    rc=$?
    fail=$((fail + 1))
    if [ "$rc" -eq 124 ]; then
      timeouts_in_a_row=$((timeouts_in_a_row + 1))
      summary "| \`$short\` | timed out |"
    elif grep -q 'Symbol not found' "$TEST_LOG"; then
      timeouts_in_a_row=0
      sym="$(grep -m1 'Symbol not found' "$TEST_LOG" | sed 's/.*Symbol not found: //')"
      summary "| \`$short\` | dyld: missing \`$sym\` |"
    else
      timeouts_in_a_row=0
      summary "| \`$short\` | fail (exit $rc) |"
    fi
    tail -n 80 "$TEST_LOG"
  fi
  rm -f "$dir/$bin"
done < <(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}} {{.Dir}}{{end}}' ./...)

summary ""
summary "**Test packages: $pass passed, $fail failed, $nocompile did not compile.**"
log "Test packages: $pass passed, $fail failed, $nocompile did not compile"

exit 0
