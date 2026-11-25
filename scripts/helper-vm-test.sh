#!/usr/bin/env bash
#
# Runs the helper_vm integration suite for the privileged helper's launchd
# disable/enable methods in a disposable Tart VM on an Apple silicon Mac,
# then deletes the VM. See docs/design/helper-vm-test.md.
#
#   scripts/helper-vm-test.sh --p12 ~/secure/spectra-vm-test.p12 \
#       --p12-password-item spectra-vm-test-p12
#
# The Developer ID test certificate is the owner's: this script only takes
# its path and, optionally, the name of a host keychain item holding its
# password. Neither the certificate nor the password is printed, logged,
# written to the results or passed on a command line on the host; both reach
# the VM on stdin and the VM is deleted at the end.
#
# Bash 3.2 compatible (the macOS /bin/bash).
set -euo pipefail
set +x # never trace: the certificate password passes through this shell

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="ghcr.io/cirruslabs/macos-tahoe-base:latest"
GUEST_DIR="/private/var/tmp/spectra-vm"
BOOT_TIMEOUT=600
P12=""
PW_ITEM=""
RESULTS=""
KEEP_VM=0
DRY_RUN=0

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
VM="spectra-helper-vm-$STAMP-$$"
WORK=""
PAYLOAD=""
VM_CREATED=0
RUN_PID=""
P12_PW=""
STEP=0
SUITE_RC=0

usage() {
  cat <<EOF
usage: scripts/helper-vm-test.sh --p12 PATH [options]

Clones a Tart macOS image into a throwaway VM, installs the Developer ID
test certificate in a VM-only keychain, builds and signs a fixture daemon,
runs the helper_vm suite (SPECTRA_VM_TEST=1) as root, copies the results
out, and deletes the VM.

  --p12 PATH                 Developer ID Application test certificate and
                             key (.p12). Required unless --dry-run.
  --p12-password-item NAME   host keychain generic-password item (service
                             name) holding the .p12 password. Without it the
                             password is read from the terminal, hidden.
  --image REF                Tart base image (default: $IMAGE).
                             Needs the Tart guest agent: a Cirrus Labs
                             "-base" image, not "-vanilla".
  --results DIR              where results go
                             (default: helper-vm-results/<UTC timestamp>)
  --boot-timeout SECONDS     how long to wait for the guest agent
                             (default: $BOOT_TIMEOUT)
  --keep-vm                  stop but do not delete the VM (debugging only;
                             the certificate stays inside it)
  --dry-run                  print the steps and commands; change nothing
  -h, --help                 show this help

Exit status: 0 suite passed, 1 suite failed or a step failed, 2 usage.
EOF
}

die() {
  echo "helper-vm-test: $*" >&2
  exit 1
}

usage_error() {
  echo "helper-vm-test: $*" >&2
  echo "Run with --help for usage." >&2
  exit 2
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --p12) [ $# -ge 2 ] || usage_error "--p12 needs a path"; P12="$2"; shift 2 ;;
      --p12=*) P12="${1#*=}"; shift ;;
      --p12-password-item) [ $# -ge 2 ] || usage_error "--p12-password-item needs a name"; PW_ITEM="$2"; shift 2 ;;
      --p12-password-item=*) PW_ITEM="${1#*=}"; shift ;;
      --image) [ $# -ge 2 ] || usage_error "--image needs a reference"; IMAGE="$2"; shift 2 ;;
      --image=*) IMAGE="${1#*=}"; shift ;;
      --results) [ $# -ge 2 ] || usage_error "--results needs a directory"; RESULTS="$2"; shift 2 ;;
      --results=*) RESULTS="${1#*=}"; shift ;;
      --boot-timeout) [ $# -ge 2 ] || usage_error "--boot-timeout needs seconds"; BOOT_TIMEOUT="$2"; shift 2 ;;
      --boot-timeout=*) BOOT_TIMEOUT="${1#*=}"; shift ;;
      --keep-vm) KEEP_VM=1; shift ;;
      --dry-run) DRY_RUN=1; shift ;;
      -h | --help) usage; exit 0 ;;
      *) usage_error "unknown argument: $1" ;;
    esac
  done
  case "$BOOT_TIMEOUT" in
    '' | *[!0-9]*) usage_error "--boot-timeout must be a whole number of seconds" ;;
  esac
  if [ -z "$P12" ]; then
    [ "$DRY_RUN" = 1 ] || usage_error "--p12 is required"
    P12="<path to the .p12>"
  fi
  [ -n "$RESULTS" ] || RESULTS="$ROOT/helper-vm-results/$STAMP"
  # Resolve relative paths now: main changes to the repository root.
  case "$RESULTS" in /*) ;; *) RESULTS="$PWD/$RESULTS" ;; esac
  case "$P12" in /* | "<"*) ;; *) P12="$PWD/$P12" ;; esac
}

step() {
  STEP=$((STEP + 1))
  printf '\n==> %d. %s\n' "$STEP" "$1"
}

# show prints a command with shell quoting; run shows it, then runs it
# unless this is a dry run.
show() {
  printf '    $'
  printf ' %q' "$@"
  printf '\n'
}

run() {
  show "$@"
  [ "$DRY_RUN" = 1 ] || "$@"
}

# showline prints a pipeline or redirection that run cannot express.
showline() {
  printf '    $ %s\n' "$1"
}

gexec() {
  run tart exec "$VM" "$@"
}

cleanup() {
  local rc=$?
  set +e
  if [ "$VM_CREATED" = 1 ]; then
    if [ "$KEEP_VM" = 1 ]; then
      tart stop "$VM" >/dev/null 2>&1
      echo "helper-vm-test: kept VM $VM (it still holds the test certificate); delete it with: tart delete $VM" >&2
    else
      echo "==> Stopping and deleting VM $VM"
      tart stop "$VM" >/dev/null 2>&1
      if [ -n "$RUN_PID" ]; then
        kill "$RUN_PID" >/dev/null 2>&1
        wait "$RUN_PID" >/dev/null 2>&1
      fi
      if ! tart delete "$VM"; then
        echo "helper-vm-test: could not delete VM $VM; delete it by hand: tart delete $VM" >&2
        [ "$rc" != 0 ] || rc=1
      fi
    fi
  fi
  P12_PW=""
  [ -z "$WORK" ] || rm -rf "$WORK"
  exit "$rc"
}

check_host() {
  step "Check the host: Apple silicon macOS with tart (guest agent exec) and go"
  if [ "$DRY_RUN" = 1 ]; then
    showline 'uname -s = Darwin, uname -m = arm64; tart exec --help; go version'
    showline "test -r '$P12'"
    if [ -n "$PW_ITEM" ]; then
      showline "security find-generic-password -s '$PW_ITEM' >/dev/null   # item exists; password not read"
    else
      showline 'read -rs (the .p12 password, from the terminal, hidden)'
    fi
    return
  fi
  [ "$(uname -s)" = Darwin ] || die "Tart runs only on macOS"
  [ "$(uname -m)" = arm64 ] || die "Tart runs macOS guests only on Apple silicon"
  command -v tart >/dev/null 2>&1 || die "tart not found; install it with: brew install cirruslabs/cli/tart"
  tart exec --help >/dev/null 2>&1 || die "this tart has no 'exec' command; upgrade tart"
  command -v go >/dev/null 2>&1 || die "go not found"
  [ -f "$P12" ] && [ -r "$P12" ] || die "cannot read the certificate file given with --p12"
  if [ -n "$PW_ITEM" ]; then
    security find-generic-password -s "$PW_ITEM" >/dev/null 2>&1 ||
      die "no generic-password item with service '$PW_ITEM' in the host keychain"
  else
    [ -t 0 ] || die "no terminal to read the .p12 password from; pass --p12-password-item"
    printf 'Password for the .p12 (not echoed): ' >&2
    IFS= read -rs P12_PW
    printf '\n' >&2
  fi
}

build_payload() {
  step "Build the helper_vm suite and the fixture daemon for darwin/arm64"
  # WORK is set only when this run created it, so cleanup never removes
  # the dry run's display path.
  if [ "$DRY_RUN" = 1 ]; then
    PAYLOAD="/tmp/tmp.XXXXXXXX/payload"
  else
    WORK="$(mktemp -d)"
    PAYLOAD="$WORK/payload"
    mkdir -p "$PAYLOAD"
  fi
  run env GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c -tags helper_vm \
    -o "$PAYLOAD/helper_vm.test" ./internal/helper
  run env GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build \
    -o "$PAYLOAD/vmfixture" ./internal/helper/testdata/vmfixture
  run cp scripts/helper-vm/guest.sh "$PAYLOAD/guest.sh"
}

boot_vm() {
  step "Clone the base image into a throwaway VM"
  run tart clone "$IMAGE" "$VM"
  [ "$DRY_RUN" = 1 ] || VM_CREATED=1

  step "Boot it headless"
  showline "tart run --no-graphics $VM > $RESULTS/tart-run.log 2>&1 &"
  if [ "$DRY_RUN" = 0 ]; then
    mkdir -p "$RESULTS"
    tart run --no-graphics "$VM" >"$RESULTS/tart-run.log" 2>&1 &
    RUN_PID=$!
  fi

  step "Wait for the Tart guest agent (up to ${BOOT_TIMEOUT}s)"
  showline "until tart exec $VM /usr/bin/true; do sleep 5; done   # each probe bounded"
  [ "$DRY_RUN" = 1 ] && return
  local deadline=$((SECONDS + BOOT_TIMEOUT))
  until probe_agent "$deadline"; do
    kill -0 "$RUN_PID" 2>/dev/null || die "tart run exited; see $RESULTS/tart-run.log"
    [ "$SECONDS" -lt "$deadline" ] || die "the guest agent did not answer within ${BOOT_TIMEOUT}s"
    sleep 5
  done
}

# probe_agent <deadline>: one tart exec probe, killed if it runs past 30s or
# past the boot deadline (SECONDS), since a stalled probe must not outlive
# the timeout. macOS has no timeout(1).
probe_agent() {
  local limit=$((SECONDS + 30)) pid
  [ "$limit" -le "$1" ] || limit="$1"
  tart exec "$VM" /usr/bin/true >/dev/null 2>&1 &
  pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$SECONDS" -ge "$limit" ]; then
      kill "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      return 1
    fi
    sleep 1
  done
  wait "$pid"
}

copy_in() {
  step "Copy the suite, the fixture and guest.sh into the VM"
  gexec /bin/mkdir -m 0700 -p "$GUEST_DIR"
  showline "tar -C $PAYLOAD -cf - . | tart exec -i $VM /usr/bin/tar -xf - -C $GUEST_DIR"
  if [ "$DRY_RUN" = 0 ]; then
    tar -C "$PAYLOAD" -cf - . | tart exec -i "$VM" /usr/bin/tar -xf - -C "$GUEST_DIR"
  fi

  step "Copy the test certificate into the VM (stdin; never printed)"
  showline "tart exec -i $VM /bin/sh -c 'umask 077 && cat > \"\$1\"' sh $GUEST_DIR/cert.p12 < (the --p12 file)"
  if [ "$DRY_RUN" = 0 ]; then
    # shellcheck disable=SC2016 # $1 expands in the guest shell
    tart exec -i "$VM" /bin/sh -c 'umask 077 && cat > "$1"' sh "$GUEST_DIR/cert.p12" <"$P12"
  fi
}

feed_password() {
  if [ -n "$PW_ITEM" ]; then
    security find-generic-password -s "$PW_ITEM" -w
  else
    printf '%s\n' "$P12_PW"
  fi
}

sign_and_test() {
  step "Import the certificate into a VM-only keychain (password on stdin)"
  showline "<password from the keychain item or the prompt> | tart exec -i $VM /bin/bash $GUEST_DIR/guest.sh import-cert"
  if [ "$DRY_RUN" = 0 ]; then
    feed_password | tart exec -i "$VM" /bin/bash "$GUEST_DIR/guest.sh" import-cert
    P12_PW=""
  fi

  step "Build and sign the fixture app in the VM"
  gexec /bin/bash "$GUEST_DIR/guest.sh" build-fixture

  step "Run the helper_vm suite as root in the VM (SPECTRA_VM_TEST=1)"
  showline "tart exec $VM /bin/bash $GUEST_DIR/guest.sh run-tests"
  showline "    (in the VM: sudo env SPECTRA_VM_TEST=1 ... helper_vm.test -test.v -test.run '^TestVM')"
  if [ "$DRY_RUN" = 0 ]; then
    tart exec "$VM" /bin/bash "$GUEST_DIR/guest.sh" run-tests || SUITE_RC=$?
  fi
}

collect() {
  step "Copy the results out of the VM"
  showline "tart exec $VM /usr/bin/tar -C $GUEST_DIR/results -cf - . | tar -xf - -C $RESULTS"
  if [ "$DRY_RUN" = 0 ]; then
    mkdir -p "$RESULTS"
    tart exec "$VM" /usr/bin/tar -C "$GUEST_DIR/results" -cf - . | tar -xf - -C "$RESULTS"
  fi
  showline "go tool test2json -t -p github.com/kaeawc/spectra/internal/helper < $RESULTS/test.log > $RESULTS/test.json"
  if [ "$DRY_RUN" = 0 ]; then
    go tool test2json -t -p github.com/kaeawc/spectra/internal/helper <"$RESULTS/test.log" >"$RESULTS/test.json" || true
  fi
}

count_lines() {
  grep -c -- "$1" "$RESULTS/test.log" || true
}

summarize() {
  step "Write $RESULTS/summary.txt"
  [ "$DRY_RUN" = 1 ] && return 0
  local pass fail skip verdict commit dirty guest team
  pass="$(count_lines '^--- PASS: TestVM')"
  fail="$(count_lines '^--- FAIL: TestVM')"
  skip="$(count_lines '^--- SKIP: TestVM')"
  verdict=FAIL
  if [ "$SUITE_RC" = 0 ] && [ "$pass" -gt 0 ] && [ "$fail" = 0 ] && [ "$skip" = 0 ]; then
    verdict=PASS
  fi
  commit="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
  dirty=""
  if [ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ]; then
    dirty=" (uncommitted changes)"
  fi
  guest="$(awk -F':[[:space:]]*' '{printf "%s%s", sep, $2; sep=" "}' "$RESULTS/sw_vers.txt" 2>/dev/null || true)"
  team="$(sed -n 's/^team: //p' "$RESULTS/identity.txt" 2>/dev/null || true)"
  {
    echo "spectra helper_vm suite: $verdict"
    echo "commit: $commit$dirty"
    echo "image: $IMAGE"
    echo "tart: $(tart --version 2>/dev/null || echo unknown)"
    echo "guest: ${guest:-unknown}"
    echo "signing team: ${team:-unknown}"
    echo "tests: $pass passed, $fail failed, $skip skipped (suite exit $SUITE_RC)"
    echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } >"$RESULTS/summary.txt"
  echo
  cat "$RESULTS/summary.txt"
  echo "Results: $RESULTS"
  [ "$verdict" = PASS ]
}

main() {
  parse_args "$@"
  cd "$ROOT"
  trap cleanup EXIT
  trap 'exit 130' INT TERM
  if [ "$DRY_RUN" = 1 ]; then
    echo "Dry run: nothing is built, cloned, read or deleted."
  fi
  check_host
  build_payload
  boot_vm
  copy_in
  sign_and_test
  collect
  local ok=0
  summarize || ok=1
  step "Stop and delete the VM (also on failure or Ctrl-C)"
  showline "tart stop $VM; tart delete $VM"
  return "$ok"
}

main "$@"
