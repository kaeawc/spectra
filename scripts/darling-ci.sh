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

# Run a command line inside the Darling prefix. `darling shell CMD` passes
# CMD to the guest shell (no -c flag — darling would try to execute "-c"),
# and maps the Linux cwd to the same path inside the prefix, so relative
# paths work. timeout(1) guards against hangs in darlingserver or the guest.
indarling() {
  timeout "$DARLING_TIMEOUT" darling shell "$*" </dev/null
}

if ! command -v darling >/dev/null 2>&1; then
  echo "error: darling is not installed" >&2
  exit 1
fi

log "Cross-compiling darwin/amd64 binaries"
mkdir -p dist-darling
for tool in spectra spectra-mcp spectra-helper; do
  CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o "dist-darling/$tool" "./cmd/$tool/" || exit 1
done

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
for tool in plutil otool codesign file sqlite3 ps sysctl sw_vers; do
  if path="$(indarling "command -v $tool" 2>/dev/null)"; then
    echo "found: $tool -> $path"
    summary "| \`$tool\` | yes — \`$path\` |"
  else
    echo "missing: $tool"
    summary "| \`$tool\` | no |"
  fi
done

log "Running spectra under Darling"
if ! indarling './dist-darling/spectra version'; then
  summary ""
  summary "**\`spectra version\` did not execute — Go darwin binaries do not run under this Darling build.**"
  echo "error: spectra binary did not run under Darling" >&2
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
  if indarling "./dist-darling/spectra $probe"; then
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
  if ! CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go test -c -o "$dir/$bin" "$import_path"; then
    nocompile=$((nocompile + 1))
    summary "| \`$short\` | did not compile for darwin |"
    continue
  fi

  echo "--- $import_path"
  if (cd "$dir" && indarling "./$bin -test.timeout 240s") >"$TEST_LOG" 2>&1; then
    pass=$((pass + 1))
    timeouts_in_a_row=0
    summary "| \`$short\` | pass |"
  else
    rc=$?
    fail=$((fail + 1))
    if [ "$rc" -eq 124 ]; then
      timeouts_in_a_row=$((timeouts_in_a_row + 1))
      summary "| \`$short\` | timed out |"
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
