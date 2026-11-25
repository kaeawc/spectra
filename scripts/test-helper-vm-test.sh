#!/usr/bin/env bash
# shellcheck disable=SC2329 # the predicates below run through check "$@"
set -euo pipefail

# Tests scripts/helper-vm-test.sh without a Mac or a VM: shims for tart, go,
# security and uname on PATH record every call, fake the guest's results and
# let each case assert what ran. The certificate password and the .p12 bytes
# must reach the guest only on stdin, never a command line or the output.
#
# BASH_UNDER_TEST picks the bash that runs the script (default: bash); point
# it at a bash 3.2 build to check macOS /bin/bash compatibility.
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/helper-vm-test.sh"
GUEST="$HERE/helper-vm/guest.sh"
SH="${BASH_UNDER_TEST:-bash}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0
VERDICT=PASS

FAKE_PW="fake-p12-password-not-a-secret"
P12_MARKER="FAKE-P12-BYTES-not-a-certificate"
printf '%s\n' "$P12_MARKER" >"$WORK/cert.p12"

mkdir -p "$WORK/shim"
cat >"$WORK/shim/tart" <<'EOF'
#!/bin/sh
echo "tart $*" >>"$TART_LOG"
cmd="$1"; shift
case "$cmd" in
  --version) echo "2.30.0" ;;
  clone) exit 0 ;;
  stop) [ -s "$RUN_PID_FILE" ] && kill "$(cat "$RUN_PID_FILE")" 2>/dev/null; exit 0 ;;
  delete) echo "deleted $1" >>"$TART_LOG"; exit 0 ;;
  run) echo $$ >"$RUN_PID_FILE"; trap 'exit 0' TERM; while :; do sleep 1; done ;;
  exec)
    [ "$1" = --help ] && exit 0
    if [ "$1" = -i ]; then shift; cat >>"$STDIN_LOG"; fi
    shift
    [ "$*" = /usr/bin/true ] && [ -n "${FAKE_AGENT_HANG:-}" ] && exec sleep 600
    case "$*" in
      *"guest.sh import-cert") exit "${FAKE_IMPORT_RC:-0}" ;;
      *"guest.sh run-tests") cat "$FAKE_RESULTS/test.log"; exit "${FAKE_SUITE_RC:-0}" ;;
      *"-cf - .") tar -C "$FAKE_RESULTS" -cf - . ;;
    esac
    exit 0 ;;
esac
EOF
cat >"$WORK/shim/go" <<'EOF'
#!/bin/sh
echo "go $*" >>"$TART_LOG"
case "$1" in
  tool) cat ;;
  *) while [ $# -gt 0 ]; do [ "$1" = -o ] && : >"$2"; shift; done ;;
esac
EOF
cat >"$WORK/shim/security" <<'EOF'
#!/bin/sh
echo "security $*" >>"$TART_LOG"
for a in "$@"; do [ "$a" = -w ] && printf '%s\n' "$FAKE_PW"; done
exit 0
EOF
cat >"$WORK/shim/uname" <<'EOF'
#!/bin/sh
case "$1" in -m) echo arm64 ;; *) echo Darwin ;; esac
EOF
chmod +x "$WORK/shim/"*

# fake_results <dir> <PASS|FAIL>: what guest.sh run-tests leaves behind.
fake_results() {
  mkdir -p "$1"
  printf 'ProductName:\t\tmacOS\nProductVersion:\t\t26.1\nBuildVersion:\t\t25B78\n' >"$1/sw_vers.txt"
  echo "team: ABCDE12345" >"$1/identity.txt"
  {
    echo "=== RUN   TestVMDisableThenEnable"
    echo "--- PASS: TestVMDisableThenEnable (3.10s)"
    echo "=== RUN   TestVMRefusesAppleLabel"
    if [ "$2" = PASS ]; then
      echo "--- PASS: TestVMRefusesAppleLabel (0.40s)"
      echo "PASS"
    else
      echo "--- FAIL: TestVMRefusesAppleLabel (0.40s)"
      echo "FAIL"
    fi
  } >"$1/test.log"
}

# check <name> <command...>
check() {
  local name="$1"
  shift
  if "$@"; then
    echo "  ok: $name"
  else
    echo "  FAIL: $name"
    sed 's/^/    | /' "$C/out"
    fail=1
  fi
}

# in_file <file> <grep pattern>; not_in_file likewise.
in_file() { grep -aq -e "$2" "$1"; }
not_in_file() { ! grep -aq -e "$2" "$1"; }
exited() { [ "$rc" = "$1" ]; }
failed() { [ "$rc" != 0 ]; }
empty() { [ ! -s "$1" ]; }
absent() { [ ! -e "$1" ]; }
no_secret() {
  not_in_file "$C/tart.log" "$FAKE_PW" && not_in_file "$C/out" "$FAKE_PW" &&
    not_in_file "$C/tart.log" "$P12_MARKER" && not_in_file "$C/out" "$P12_MARKER"
}
secret_on_stdin() { in_file "$C/stdin.log" "$FAKE_PW" && in_file "$C/stdin.log" "$P12_MARKER"; }
reads_password_once() { [ "$(grep -c "find-generic-password -s spectra-vm-test -w" "$C/tart.log")" = 1 ]; }
deleted() { in_file "$C/tart.log" "^deleted spectra-helper-vm-"; }
kept() { in_file "$C/tart.log" "^tart stop" && not_in_file "$C/tart.log" "^tart delete"; }

# run_case <name> [VAR=value ...] -- <script args...>: runs the script with
# the shims in case dir $C; RES=$C/res is free for --results. Sets $rc.
run_case() {
  local name="$1"
  shift
  C="$WORK/$name"
  RES="$C/res"
  mkdir -p "$C"
  : >"$C/tart.log"
  : >"$C/stdin.log"
  fake_results "$C/results-src" "$VERDICT"
  local envs=()
  while [ "$1" != -- ]; do
    envs+=("$1")
    shift
  done
  shift
  local args=()
  for a in "$@"; do
    [ "$a" = @RES ] && a="$RES"
    args+=("$a")
  done
  set +e
  env PATH="$WORK/shim:$PATH" TART_LOG="$C/tart.log" STDIN_LOG="$C/stdin.log" RUN_PID_FILE="$C/run.pid" \
    FAKE_RESULTS="$C/results-src" FAKE_PW="$FAKE_PW" ${envs[@]+"${envs[@]}"} \
    "$SH" "$SCRIPT" ${args[@]+"${args[@]}"} </dev/null >"$C/out" 2>&1
  rc=$?
  set -e
  echo "case $name (exit $rc)"
}

ITEM=(--p12 "$WORK/cert.p12" --p12-password-item spectra-vm-test --results @RES)

run_case pass -- "${ITEM[@]}"
check "exits 0 when the suite passes" exited 0
check "clones the base image" in_file "$C/tart.log" "^tart clone ghcr.io/cirruslabs/macos-tahoe-base:latest spectra-helper-vm-"
check "builds the suite with the helper_vm tag" in_file "$C/tart.log" "^go test -c -tags helper_vm"
check "runs the suite in the guest" in_file "$C/tart.log" "guest.sh run-tests"
check "deletes the VM" deleted
check "summary says PASS" in_file "$RES/summary.txt" "^spectra helper_vm suite: PASS"
check "summary counts the tests" in_file "$RES/summary.txt" "^tests: 2 passed, 0 failed, 0 skipped"
check "summary names the guest" in_file "$RES/summary.txt" "^guest: macOS 26.1 25B78"
check "summary names the signing team" in_file "$RES/summary.txt" "^signing team: ABCDE12345"
check "password and certificate reach the guest on stdin" secret_on_stdin
check "password and certificate never on a command line or in output" no_secret
check "the password is read once, with -w, from the named item" reads_password_once

VERDICT=FAIL
run_case suite-fails FAKE_SUITE_RC=1 -- "${ITEM[@]}"
check "exits 1 when the suite fails" exited 1
check "summary says FAIL" in_file "$RES/summary.txt" "^spectra helper_vm suite: FAIL"
check "deletes the VM after a failed suite" deleted
VERDICT=PASS

run_case import-fails FAKE_IMPORT_RC=1 -- "${ITEM[@]}"
check "a failed step exits non-zero" failed
check "a failed step still deletes the VM" deleted
check "a failed step does not run the suite" not_in_file "$C/tart.log" "guest.sh run-tests"
check "no secret after a failed step" no_secret

start=$SECONDS
run_case agent-hangs FAKE_AGENT_HANG=1 -- "${ITEM[@]}" --boot-timeout 3
elapsed=$((SECONDS - start))
check "a stalled guest agent probe fails the boot wait" exited 1
check "says the agent did not answer" in_file "$C/out" "did not answer within 3s"
check "the boot deadline bounds a stalled probe" test "$elapsed" -lt 20
check "deletes the VM after a boot timeout" deleted
run_case bad-boot-timeout -- "${ITEM[@]}" --boot-timeout soon
check "a non-numeric --boot-timeout is a usage error" exited 2

run_case keep-vm -- "${ITEM[@]}" --keep-vm
check "--keep-vm exits 0" exited 0
check "--keep-vm stops but does not delete" kept
check "--keep-vm warns that the certificate stays" in_file "$C/out" "still holds the test certificate"

run_case dry-run -- --dry-run --results @RES
check "dry run exits 0" exited 0
check "dry run runs no tart, go or security" empty "$C/tart.log"
check "dry run prints the clone" in_file "$C/out" "tart clone"
check "dry run prints the suite run" in_file "$C/out" "guest.sh run-tests"
check "dry run prints the delete" in_file "$C/out" "tart delete"
check "dry run creates no results" absent "$RES"
mkdir -p /tmp/tmp.XXXXXXXX
run_case dry-run-keeps-placeholder -- --dry-run
check "dry run leaves an existing placeholder path alone" test -d /tmp/tmp.XXXXXXXX
rmdir /tmp/tmp.XXXXXXXX

run_case no-tty -- --p12 "$WORK/cert.p12" --results @RES
check "no keychain item and no terminal refuses" exited 1
check "says how to pass the password" in_file "$C/out" "pass --p12-password-item"
check "refuses before creating a VM" not_in_file "$C/tart.log" "^tart clone"

run_case no-p12 -- --results @RES
check "missing --p12 is a usage error" exited 2
run_case unknown-arg -- --bogus
check "unknown argument is a usage error" exited 2
run_case help -- --help
check "--help exits 0" exited 0
check "--help documents --dry-run" in_file "$C/out" "--dry-run"

C="$WORK/guest"
mkdir -p "$C"
set +e
"$SH" "$GUEST" run-tests >"$C/out" 2>&1
rc=$?
set -e
echo "case guest-outside-vm (exit $rc)"
if [ "$(uname -s)" = Darwin ] && [ "$(/usr/sbin/sysctl -n kern.hv_vmm_present 2>/dev/null)" = 1 ]; then
  echo "  skip: running inside a VM"
else
  check "guest.sh refuses outside a VM" exited 1
  check "guest.sh says why" in_file "$C/out" "refusing to run outside a virtual machine"
fi

exit "$fail"
