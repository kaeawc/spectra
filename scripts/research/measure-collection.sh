#!/usr/bin/env bash
# Measures Spectra's collection cost for docs/research/process-data-push.md.
#
#   scripts/research/measure-collection.sh <spectra binary> [daemon seconds]
#
# For each command: median-of-3 wall/user/sys time (children included) and the
# number of child processes it execs, counted through PATH shims that log each
# invocation. Absolute-path execs (/bin/launchctl, /usr/sbin/spindump) bypass
# the shims, so exec counts are a lower bound. Then the daemon runs for the
# given seconds (default 120) at its default 15 s tick, in an isolated HOME.
# On Linux, strace gives exact exec and syscall counts instead; see the doc.
set -euo pipefail

bin=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
daemon_secs=${2:-120}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
shims=$work/shims
log=$work/exec.log
mkdir -p "$shims"

real_path=$PATH
IFS=: read -r -a dirs <<<"$real_path"
for d in "${dirs[@]}"; do
	[ -d "$d" ] || continue
	for f in "$d"/*; do
		name=$(basename "$f")
		[ -x "$f" ] && [ ! -d "$f" ] && [ ! -e "$shims/$name" ] || continue
		printf '#!/bin/sh\necho %s >>"%s"\nexec "%s" "$@"\n' "$name" "$log" "$f" >"$shims/$name"
		chmod +x "$shims/$name"
	done
done

export SPECTRA_NO_DAEMON=1
TIMEFORMAT='%R %U %S'

measure() {
	local label=$1
	shift
	local times=$work/times
	: >"$times"
	for _ in 1 2 3; do
		{ time "$bin" "$@" >/dev/null 2>&1; } 2>>"$times" || true
	done
	read -r wall usr sys < <(sort -n "$times" | sed -n 2p)
	: >"$log"
	PATH=$shims:$real_path "$bin" "$@" >/dev/null 2>&1 || true
	local execs top
	execs=$(wc -l <"$log" | tr -d ' ')
	top=$(sort "$log" | uniq -c | sort -rn | head -6 | awk '{printf "%s×%s ", $2, $1}')
	printf '| `%s` | %s | %s | %s | %s | %s |\n' "$label" "$wall" "$usr" "$sys" "$execs" "$top"
}

echo "| command | wall s | user s | sys s | child execs | exec'd |"
echo "|---|---|---|---|---|---|"
while read -r line; do
	[ -n "$line" ] || continue
	read -r -a argv <<<"$line"
	measure "spectra $line" "${argv[@]}"
done <<'EOF'
process
process --deep
system
memory
power
thermal
network
storage
toolchain
jvm
services
startup
whatswrong
load
snapshot --json
EOF

echo
echo "daemon: ${daemon_secs}s idle at the default 15s tick"
dh=$work/daemon
mkdir -p "$dh"
: >"$log"
(
	export HOME=$dh XDG_RUNTIME_DIR=$dh/run XDG_STATE_HOME=$dh/state SPECTRA_WATCH_DB=$dh/watch.db
	unset SPECTRA_NO_DAEMON
	mkdir -p "$XDG_RUNTIME_DIR"
	{ time PATH=$shims:$real_path "$bin" daemon run --no-notify >"$dh/log" 2>&1; } 2>"$work/daemon.time" &
	sleep "$daemon_secs"
	pkill -TERM -f "$bin daemon run" || true
	wait
)
read -r wall usr sys <"$work/daemon.time"
execs=$(wc -l <"$log" | tr -d ' ')
echo "wall=${wall}s user=${usr}s sys=${sys}s (children included) execs=$execs ($(awk -v e="$execs" -v s="$daemon_secs" 'BEGIN{printf "%.1f", e*60/s}')/min)"
sort "$log" | uniq -c | sort -rn | head
