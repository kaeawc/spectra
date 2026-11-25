#!/usr/bin/env bash
#
# Runs inside the disposable Tart VM that scripts/helper-vm-test.sh creates,
# as the image's admin user (tart exec). Never run it on a real machine: it
# imports a signing certificate and changes system launch daemons as root.
#
#   guest.sh import-cert     read the .p12 password on stdin, import cert.p12
#                            into a VM-only keychain, then delete cert.p12
#   guest.sh build-fixture   build and sign the fixture app
#   guest.sh run-tests       run the helper_vm suite as root, write results/
#
# Bash 3.2 compatible (the macOS /bin/bash).
set -euo pipefail
set +x # never trace: the certificate password passes through this shell

DIR="$(cd "$(dirname "$0")" && pwd)"
KC="$DIR/spectra-vm.keychain-db"
KC_PASS_FILE="$DIR/keychain-password"
IDENTITY_FILE="$DIR/identity"
RESULTS="$DIR/results"
APP="$DIR/fixture/SpectraVMFixture.app"
LABEL="dev.spectra.vmfixture.agentd"
BUNDLE_ID="dev.spectra.vmfixture"
# Apple intermediates codesign needs to build the chain: Developer ID G1 and
# G2, and WWDR G3. Public certificates, fetched from Apple.
APPLE_CAS="DeveloperIDCA DeveloperIDG2CA AppleWWDRCAG3"

die() {
  echo "guest.sh: $*" >&2
  exit 1
}

require_vm() {
  [ "$(/usr/sbin/sysctl -n kern.hv_vmm_present 2>/dev/null || echo 0)" = 1 ] ||
    die "refusing to run outside a virtual machine (kern.hv_vmm_present != 1)"
}

unlock_keychain() {
  [ -f "$KC_PASS_FILE" ] || die "no VM keychain; run import-cert first"
  /usr/bin/security unlock-keychain -p "$(cat "$KC_PASS_FILE")" "$KC"
}

import_cert() {
  local cert="$DIR/cert.p12" p12pass kcpass ca line count sha name team
  local search
  [ -f "$cert" ] || die "missing $cert"
  IFS= read -r p12pass || die "no certificate password on stdin"
  umask 077
  kcpass="$(/usr/bin/openssl rand -hex 32)"
  printf '%s' "$kcpass" >"$KC_PASS_FILE"
  rm -f "$KC"
  /usr/bin/security create-keychain -p "$kcpass" "$KC"
  /usr/bin/security set-keychain-settings "$KC"
  /usr/bin/security unlock-keychain -p "$kcpass" "$KC"
  for ca in $APPLE_CAS; do
    /usr/bin/curl -fsSL --retry 3 -o "$DIR/$ca.cer" "https://www.apple.com/certificateauthority/$ca.cer"
    /usr/bin/security import "$DIR/$ca.cer" -k "$KC" >/dev/null
  done
  # The password reaches only this process's argv, inside the disposable VM.
  /usr/bin/security import "$cert" -k "$KC" -f pkcs12 -P "$p12pass" -T /usr/bin/codesign >/dev/null
  p12pass=""
  rm -f "$cert"
  /usr/bin/security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$kcpass" "$KC" >/dev/null 2>&1
  kcpass=""
  # Put the VM keychain on the user search list so codesign finds the chain.
  search=("$KC")
  while IFS= read -r line; do
    line="$(echo "$line" | sed -e 's/^[[:space:]]*"//' -e 's/"[[:space:]]*$//')"
    [ -n "$line" ] && [ "$line" != "$KC" ] && search+=("$line")
  done < <(/usr/bin/security list-keychains -d user)
  /usr/bin/security list-keychains -d user -s "${search[@]}"

  count="$(/usr/bin/security find-identity -v -p codesigning "$KC" | grep -c '"Developer ID Application: ' || true)"
  [ "$count" = 1 ] || die "want exactly one valid \"Developer ID Application\" identity in the test certificate, found $count"
  line="$(/usr/bin/security find-identity -v -p codesigning "$KC" | grep '"Developer ID Application: ')"
  sha="$(echo "$line" | awk '{print $2}')"
  name="$(echo "$line" | sed -e 's/^[^"]*"//' -e 's/"[^"]*$//')"
  team="$(echo "$name" | sed -n 's/.*(\([A-Z0-9]\{10\}\))$/\1/p')"
  [ -n "$team" ] || die "could not read a team id from the signing identity"
  printf '%s\n' "$sha" >"$IDENTITY_FILE"
  mkdir -p "$RESULTS"
  printf 'team: %s\n' "$team" >"$RESULTS/identity.txt"
  echo "imported a Developer ID Application identity for team $team into the VM keychain"
}

info_plist() {
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>SpectraVMFixture</string>
	<key>CFBundleIdentifier</key>
	<string>$BUNDLE_ID</string>
	<key>CFBundleName</key>
	<string>SpectraVMFixture</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0</string>
	<key>CFBundleVersion</key>
	<string>1</string>
</dict>
</plist>
EOF
}

build_fixture() {
  local id daemon
  [ -f "$DIR/vmfixture" ] || die "missing $DIR/vmfixture"
  [ -f "$IDENTITY_FILE" ] || die "no signing identity; run import-cert first"
  id="$(cat "$IDENTITY_FILE")"
  unlock_keychain
  rm -rf "$DIR/fixture"
  daemon="$APP/Contents/Library/LaunchServices/$LABEL"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Library/LaunchServices"
  install -m 0755 "$DIR/vmfixture" "$APP/Contents/MacOS/SpectraVMFixture"
  install -m 0755 "$DIR/vmfixture" "$daemon"
  info_plist >"$APP/Contents/Info.plist"
  # Nested code first, then the bundle. No timestamp: the VM may be offline
  # from Apple's timestamp service, and the helper does not require one.
  /usr/bin/codesign --force --timestamp=none --keychain "$KC" --sign "$id" --identifier "$LABEL" "$daemon"
  /usr/bin/codesign --force --timestamp=none --keychain "$KC" --sign "$id" "$APP"
  /usr/bin/codesign --verify --strict --verbose=2 "$APP"
  echo "built and signed $APP"
}

run_tests() {
  local uid rc
  [ -x "$DIR/helper_vm.test" ] || die "missing $DIR/helper_vm.test"
  [ -d "$APP" ] || die "no fixture app; run build-fixture first"
  uid="$(id -u)"
  mkdir -p "$RESULTS"
  /usr/bin/sw_vers >"$RESULTS/sw_vers.txt"
  /usr/bin/uname -a >"$RESULTS/uname.txt"
  set +e
  # shellcheck disable=SC2024 # the log is written by admin on purpose
  sudo -n /usr/bin/env \
    SPECTRA_VM_TEST=1 \
    SPECTRA_VM_FIXTURE_APP="$APP" \
    SPECTRA_VM_NONROOT_UID="$uid" \
    SPECTRA_VM_AUDIT_LOG="$RESULTS/audit.jsonl" \
    "$DIR/helper_vm.test" -test.v -test.count=1 -test.timeout=20m -test.run '^TestVM' \
    >"$RESULTS/test.log" 2>&1
  rc=$?
  set -e
  sudo -n /usr/sbin/chown -R "$uid" "$RESULTS"
  printf '%s\n' "$rc" >"$RESULTS/exit_code"
  cat "$RESULTS/test.log"
  return "$rc"
}

require_vm
case "${1:-}" in
  import-cert) import_cert ;;
  build-fixture) build_fixture ;;
  run-tests) run_tests ;;
  *) die "usage: guest.sh import-cert|build-fixture|run-tests" ;;
esac
