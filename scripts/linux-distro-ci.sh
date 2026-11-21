#!/bin/sh
# Cross-distro Linux validation (CI job `linux-distros`).
#
# Each distro runs prebuilt static binaries, so no distro needs a Go
# toolchain:
#
#   scripts/linux-distro-ci.sh build <outdir>
#       Host (needs go): build the static spectra binary and one static test
#       binary per Linux-backend package into <outdir>, plus a manifest that
#       maps each test binary to its package directory.
#
#   scripts/linux-distro-ci.sh container <image> <outdir>
#       Host (needs docker): start <image> with the repo and <outdir> mounted
#       read-only, run `install` in it as root, then `run` as an unprivileged
#       user (several cmd/spectra tests refuse to act as root, and real users
#       are not root).
#
#   scripts/linux-distro-ci.sh install
#       In the container, as root: install the distro's real host tools
#       (ip/ss, ps, df, jq) and create the unprivileged test user.
#
#   scripts/linux-distro-ci.sh run <outdir>
#       In the container, from the repo root: run every test binary from its
#       package directory (so testdata resolves), then smoke-test the OS-aware
#       commands.
#
# Local example (Docker Desktop on Apple silicon):
#   GOARCH=arm64 scripts/linux-distro-ci.sh build /tmp/spectra-linux
#   scripts/linux-distro-ci.sh container alpine:3.20 /tmp/spectra-linux
set -eu

# Keep in sync with the `go test (Linux backends)` step of test-linux in
# .github/workflows/ci.yml.
PACKAGES="./internal/hostos/... ./internal/process/... ./internal/metrics/...
./internal/storagestate/... ./internal/netstate/... ./internal/sysinfo/...
./internal/toolchain/... ./internal/helper/... ./cmd/spectra/
./internal/detect/ ./internal/netdiag/ ./internal/snapshot/"

TEST_USER=spectra-ci

usage() {
	echo "usage: $0 build <outdir> | container <image> <outdir> | install | run <outdir>" >&2
	exit 2
}

build() {
	out=$1
	mkdir -p "$out/tests"
	: >"$out/manifest"
	export CGO_ENABLED=0 GOOS=linux
	go build -o "$out/spectra" ./cmd/spectra/
	module=$(go list -m)
	# shellcheck disable=SC2086 # PACKAGES is a word list of patterns.
	for pkg in $(go list $PACKAGES); do
		rel=${pkg#"$module"/}
		name=$(printf '%s' "$rel" | tr '/' '_')
		go test -c -o "$out/tests/$name.test" "$pkg"
		# Packages without tests produce no binary.
		if [ -f "$out/tests/$name.test" ]; then
			printf '%s %s\n' "$name" "$rel" >>"$out/manifest"
		fi
	done
	echo "built $(wc -l <"$out/manifest" | tr -d " ") test binaries into $out"
}

container() {
	image=$1
	out=$(cd "$2" && pwd)
	cid=$(docker run -d --rm -v "$(pwd):/src:ro" -v "$out:/dist:ro" -w /src "$image" sleep 3600)
	# shellcheck disable=SC2064 # expand cid now.
	trap "docker rm -f $cid >/dev/null" EXIT
	docker exec "$cid" sh scripts/linux-distro-ci.sh install
	docker exec -u "$TEST_USER" -e HOME="/home/$TEST_USER" "$cid" \
		sh scripts/linux-distro-ci.sh run /dist
}

install() {
	if command -v apt-get >/dev/null 2>&1; then
		apt-get update -qq
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
			iproute2 procps coreutils jq ca-certificates >/dev/null
	elif command -v dnf >/dev/null 2>&1; then
		dnf install -y -q iproute procps-ng coreutils jq shadow-utils >/dev/null
	elif command -v apk >/dev/null 2>&1; then
		apk add --no-cache -q iproute2 iproute2-ss procps-ng coreutils jq >/dev/null
	else
		echo "no supported package manager (apt-get, dnf, apk)" >&2
		exit 1
	fi
	if command -v useradd >/dev/null 2>&1; then
		useradd -m "$TEST_USER"
	else
		adduser -D "$TEST_USER"
	fi
}

# FAILED accumulates failures so one broken package or command does not hide
# the rest. (set -e is inert inside functions used in a || list, so each
# check records its own result instead.)
FAILED=""
fail() {
	FAILED="$FAILED
  $*"
}

run_tests() {
	out=$1
	root=$(pwd)
	while read -r name rel; do
		echo "=== test $rel"
		(cd "$root/$rel" && "$out/tests/$name.test" -test.count=1 -test.timeout=10m) ||
			fail "test $rel"
	done <"$out/manifest"
}

# check runs one spectra invocation and requires exit 0; output is shown
# truncated so logs stay readable.
check() {
	echo "+ spectra $*"
	if "$SPECTRA" "$@" >"$SCRATCH/out" 2>&1; then
		head -5 "$SCRATCH/out"
	else
		cat "$SCRATCH/out"
		fail "spectra $*"
	fi
}

# check_json additionally requires the --json output to parse.
check_json() {
	echo "+ spectra $* --json"
	if ! "$SPECTRA" "$@" --json >"$SCRATCH/out.json"; then
		fail "spectra $* --json (exit)"
	elif ! jq -e . "$SCRATCH/out.json" >/dev/null; then
		fail "spectra $* --json (invalid JSON)"
	fi
}

smoke() {
	SPECTRA="$1/spectra"
	SCRATCH=$(mktemp -d)
	check process
	check storage
	check snapshot
	check network
	check system limits
	check_json process
	check_json storage
	check_json network
	check_json system limits
	if "$SPECTRA" inspect /nonexistent.app 2>&1 | grep -qi 'unsupported'; then
		echo "app inspection correctly refused on Linux"
	else
		fail "inspect did not report unsupported on Linux"
	fi
}

case ${1:-} in
build)
	[ $# -eq 2 ] || usage
	build "$2"
	;;
container)
	[ $# -eq 3 ] || usage
	container "$2" "$3"
	;;
install)
	[ $# -eq 1 ] || usage
	install
	;;
run)
	[ $# -eq 2 ] || usage
	# shellcheck disable=SC1091 # provided by the container image.
	. /etc/os-release
	echo "distro: $PRETTY_NAME (libc: $(ldd --version 2>&1 | head -1)), user: $(id -un)"
	out=$(cd "$2" && pwd)
	run_tests "$out"
	smoke "$out"
	if [ -n "$FAILED" ]; then
		echo "FAILED on $PRETTY_NAME:$FAILED" >&2
		exit 1
	fi
	echo "all checks passed on $PRETTY_NAME"
	;;
*) usage ;;
esac
