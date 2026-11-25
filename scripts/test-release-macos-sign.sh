#!/usr/bin/env bash
set -euo pipefail

# Tests the control flow of scripts/sign-darwin-archives.sh,
# scripts/notarize-archives.sh and scripts/verify-darwin-signature.sh with
# fake codesign, xcrun, plutil, ditto and spctl on PATH. Runs on Linux and
# macOS; no Apple credentials or network. Real signing is exercised only by
# the release workflow's codesign job.

# Glob and sort order must not depend on the runner's locale.
export LC_ALL=C
HERE="$(cd "$(dirname "$0")" && pwd)"
BASH_BIN="${BASH_BIN:-bash}"
fail=0

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
SHIMS="$WORK/shims"
mkdir -p "$SHIMS"
export FAKE_LOG="$WORK/calls.log"

# codesign marks a "binary" by appending a line, so the mark survives tar.
cat > "$SHIMS/codesign" <<'EOF'
#!/usr/bin/env bash
case "$1" in
--force)
    id=""
    while [[ "$1" == --* ]]; do
        case "$1" in
        --sign) id="$2"; shift ;;
        --keychain) echo "codesign keychain $2" >> "$FAKE_LOG"; shift ;;
        --options) shift ;;
        esac
        shift
    done
    for f in "$@"; do
        echo "FAKESIG id=$id runtime=${FAKE_RUNTIME:-1}" >> "$f"
    done
    ;;
-dv)
    f="${*: -1}"
    sig="$(grep '^FAKESIG' "$f" | tail -n 1)"
    if [[ -z "$sig" ]]; then
        echo "$f: code object is not signed at all" >&2
        exit 1
    fi
    {
        echo "Executable=$f"
        if [[ "$sig" == *runtime=1* ]]; then
            echo "CodeDirectory v=20500 size=1 flags=0x10000(runtime) hashes=1+7 location=embedded"
        else
            echo "CodeDirectory v=20400 size=1 flags=0x0(none) hashes=1+7 location=embedded"
        fi
        echo "Authority=Developer ID Application: Example (TEAM123456)"
        echo "Authority=Developer ID Certification Authority"
        echo "Timestamp=Nov 25, 2026 at 12:00:00"
    } >&2
    ;;
--verify)
    grep -q '^FAKESIG' "${*: -1}"
    ;;
esac
EOF

cat > "$SHIMS/xcrun" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == notarytool ]] || exit 1
shift
echo "notarytool $*" >> "$FAKE_LOG"
case "$1" in
submit)
    if [[ "${FAKE_SUBMIT_STATUS:-Accepted}" == none ]]; then
        echo "Error: network down" >&2
        exit 1
    fi
    printf '{"id":"sub-1","message":"done","status":"%s"}\n' "${FAKE_SUBMIT_STATUS:-Accepted}"
    ;;
info)
    printf '{"id":"sub-1","status":"%s"}\n' "${FAKE_INFO_STATUS:-In Progress}"
    ;;
log)
    echo '{"issues":[{"message":"The binary is not signed."}]}'
    ;;
esac
EOF

cat > "$SHIMS/plutil" <<'EOF'
#!/usr/bin/env bash
# plutil -extract <key> raw -o - <file>, for flat JSON objects only.
sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" "${*: -1}" | grep .
EOF

cat > "$SHIMS/ditto" <<'EOF'
#!/usr/bin/env bash
# ditto -c -k --keepParent <src> <zip>
src="$4"
tar -C "$(dirname "$src")" -cf "$5" "$(basename "$src")"
tar -tf "$5" >> "$FAKE_LOG"
EOF

cat > "$SHIMS/spctl" <<'EOF'
#!/usr/bin/env bash
echo "${*: -1}: accepted"
echo "source=${FAKE_SPCTL_SOURCE:-Notarized Developer ID}"
EOF

cat > "$SHIMS/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

if ! command -v shasum >/dev/null 2>&1; then
    cat > "$SHIMS/shasum" <<'EOF'
#!/usr/bin/env bash
shift 2
exec sha256sum "$@"
EOF
fi
chmod +x "$SHIMS"/*
export PATH="$SHIMS:$PATH"

TAG=v1.2.3

# make_dist <dir> writes four release archives with stand-in binaries.
make_dist() {
    local dir="$1" goos arch name cmd
    rm -rf "$dir"
    mkdir -p "$dir/stage"
    for goos in darwin linux; do
        for arch in arm64 amd64; do
            name="spectra_${TAG}_${goos}_${arch}"
            mkdir -p "$dir/stage/$name/bin" "$dir/stage/$name/docs"
            for cmd in spectra spectra-mcp spectra-helper; do
                echo "binary $cmd $goos $arch" > "$dir/stage/$name/bin/$cmd"
            done
            echo readme > "$dir/stage/$name/README.md"
            tar -C "$dir/stage" -czf "$dir/$name.tar.gz" "$name"
        done
    done
    rm -rf "$dir/stage"
    (cd "$dir" && shasum -a 256 spectra_*.tar.gz > checksums.txt)
}

check() {
    local name="$1" want="$2" got="$3"
    if [[ "$got" == "$want" ]]; then
        echo "PASS: $name"
    else
        echo "FAIL: $name: want '$want', got '$got'"
        fail=1
    fi
}

passes() {
    if "$@" >"$WORK/out" 2>&1; then echo pass; else echo fail; fi
}

sum_of() {
    awk -v n="$2" '$2 == n { print $1 }' "$1/checksums.txt"
}

# --- sign-darwin-archives.sh ---------------------------------------------

D="$WORK/dist"
make_dist "$D"
linux_before="$(sum_of "$D" "spectra_${TAG}_linux_amd64.tar.gz")"
darwin_before="$(sum_of "$D" "spectra_${TAG}_darwin_arm64.tar.gz")"
: > "$FAKE_LOG"
check "sign succeeds" pass "$(passes env CODESIGN_ID=ABC123 CODESIGN_KEYCHAIN=/tmp/kc "$BASH_BIN" "$HERE/sign-darwin-archives.sh" "$TAG" "$D")"
check "sign uses the keychain" 2 "$(grep -c 'codesign keychain /tmp/kc' "$FAKE_LOG")"

X="$WORK/x"
mkdir -p "$X"
tar -xzf "$D/spectra_${TAG}_darwin_amd64.tar.gz" -C "$X"
check "darwin binaries signed" 3 "$(grep -l 'FAKESIG id=ABC123' "$X/spectra_${TAG}_darwin_amd64/bin/"* | wc -l | tr -d ' ')"
check "layout kept" "README.md bin docs" "$(cd "$X/spectra_${TAG}_darwin_amd64" && echo *)"
check "no AppleDouble entries" 0 "$(tar -tzf "$D/spectra_${TAG}_darwin_amd64.tar.gz" | grep -c '/\._' || true)"
check "linux archive untouched" "$linux_before" "$(sum_of "$D" "spectra_${TAG}_linux_amd64.tar.gz")"
if [[ "$darwin_before" != "$(sum_of "$D" "spectra_${TAG}_darwin_arm64.tar.gz")" ]]; then
    check "darwin checksum rewritten" changed changed
else
    check "darwin checksum rewritten" changed same
fi
check "checksums match archives" pass "$(passes sh -c "cd '$D' && shasum -a 256 -c checksums.txt")"

make_dist "$D"
before="$(cat "$D/checksums.txt")"
check "sign fails without hardened runtime" fail "$(passes env FAKE_RUNTIME=0 CODESIGN_ID=ABC123 "$BASH_BIN" "$HERE/sign-darwin-archives.sh" "$TAG" "$D")"
cp "$WORK/out" "$WORK/sign.out"
check "runtime failure is named" pass "$(passes grep -q 'hardened runtime not enabled' "$WORK/sign.out")"
check "failed sign leaves archives alone" "$before" "$(cat "$D/checksums.txt")"
check "sign needs CODESIGN_ID" fail "$(passes env CODESIGN_ID= "$BASH_BIN" "$HERE/sign-darwin-archives.sh" "$TAG" "$D")"

# --- notarize-archives.sh ------------------------------------------------

make_dist "$D"
env CODESIGN_ID=ABC123 "$BASH_BIN" "$HERE/sign-darwin-archives.sh" "$TAG" "$D" >/dev/null 2>&1
darwin=("$D"/spectra_"$TAG"_darwin_*.tar.gz)
notarize() {
    passes env NOTARY_KEY_PATH=/tmp/key.p8 NOTARY_KEY_ID=KEYID "$@" "$BASH_BIN" "$HERE/notarize-archives.sh" "${darwin[@]}"
}

: > "$FAKE_LOG"
check "accepted notarization passes" pass "$(notarize NOTARY_ISSUER=ISSUER)"
check "one submission for both archives" 1 "$(grep -c '^notarytool submit' "$FAKE_LOG")"
check "zip holds six binaries" 6 "$(grep -c '/bin/spectra' "$FAKE_LOG")"
check "api key auth with issuer" 1 "$(grep -c -- '--key /tmp/key.p8 --key-id KEYID --issuer ISSUER --wait' "$FAKE_LOG")"

: > "$FAKE_LOG"
notarize >/dev/null
check "individual key omits issuer" 0 "$(grep -c -- '--issuer' "$FAKE_LOG" || true)"

: > "$FAKE_LOG"
check "invalid verdict fails" fail "$(notarize FAKE_SUBMIT_STATUS=Invalid FAKE_INFO_STATUS=Invalid)"
check "invalid verdict fetches the log" 1 "$(grep -c '^notarytool log sub-1' "$FAKE_LOG")"

check "timed-out wait then accepted passes" pass "$(notarize "FAKE_SUBMIT_STATUS=In Progress" FAKE_INFO_STATUS=Accepted)"
check "still in progress fails" fail "$(notarize "FAKE_SUBMIT_STATUS=In Progress")"

: > "$FAKE_LOG"
check "upload failure fails" fail "$(notarize FAKE_SUBMIT_STATUS=none)"
check "upload is retried" 3 "$(grep -c '^notarytool submit' "$FAKE_LOG")"

check "keychain profile auth" pass "$(passes env NOTARY_PROFILE=spectra-notary "$BASH_BIN" "$HERE/notarize-archives.sh" "${darwin[@]}")"
check "no credentials is a usage error" fail "$(passes env -u NOTARY_PROFILE "$BASH_BIN" "$HERE/notarize-archives.sh" "${darwin[@]}")"

make_dist "$D"
: > "$FAKE_LOG"
check "unsigned binaries are not uploaded" fail "$(notarize)"
check "nothing submitted for unsigned" 0 "$(grep -c '^notarytool submit' "$FAKE_LOG" || true)"

# --- verify-darwin-signature.sh ------------------------------------------

B="$WORK/bin"
mkdir -p "$B"
echo plain > "$B/unsigned"
echo signed > "$B/signed"
codesign --force --timestamp --options runtime --sign ABC123 "$B/signed"
check "verify signed" pass "$(passes "$BASH_BIN" "$HERE/verify-darwin-signature.sh" "$B/signed")"
check "verify unsigned" fail "$(passes "$BASH_BIN" "$HERE/verify-darwin-signature.sh" "$B/signed" "$B/unsigned")"
check "verify notarized" pass "$(passes "$BASH_BIN" "$HERE/verify-darwin-signature.sh" --notarized "$B/signed")"
check "verify not notarized" fail "$(passes env "FAKE_SPCTL_SOURCE=Unnotarized Developer ID" "$BASH_BIN" "$HERE/verify-darwin-signature.sh" --notarized "$B/signed")"

exit "$fail"
