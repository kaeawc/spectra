# Idle holders

`spectra idle` finds processes that hold something (a build slot, a lock, a
worktree) while doing nothing: alive for a while, near 0% CPU, and with no
busy descendants. A holder doing real work shows CPU or busy children
(Gradle, `xcodebuild`); a wrapper waiting on itself shows neither, and a
`kill -0` liveness check never frees it.

```bash
spectra idle --root ~/src/repo/.worktrees
spectra idle --pids 4242 --min-age 20m --json
spectra idle --root /tmp/lanes --max-cpu 0.5 --window 10s
```

Nothing is ever signalled. The output is evidence for a person or a
supervisor to act on.

## What counts as idle

A process is in scope when its working directory is under a `--root`
(repeatable; default the current directory unless `--pids` is given), or it
is listed in `--pids`. It is reported when all of these hold:

- it has been alive at least `--min-age` (default `10m`);
- its CPU plus every descendant's CPU, summed, is under `--max-cpu` percent
  of one CPU (default `1`) over the `--window` sample (default `5s`);
- no descendant started during the window (a wrapper that keeps spawning
  short tasks is working; this is checked before CPU coverage, so a child
  born and gone inside the window counts as busy, not unmeasured);
- CPU was measured for it and every descendant.

When an idle process has an idle ancestor that is also in scope, only the
topmost is reported; the others appear in its `descendants` list (capped at
64, with `descendants_total`). Spectra, its ancestors and its descendants are
never candidates.

## CPU sampling

| OS | Source | `cpu_basis` |
|---|---|---|
| Linux | two `/proc/<pid>/stat` readings via `spectra load`'s sampler, matched by pid and start time | `window` |
| macOS | two `ps -axo pid=,time=` readings of cumulative CPU time; a process started inside the window is scored on all of its CPU | `window` |

On Linux, a running daemon (or a long-lived `spectra-mcp`) answers from a CPU
reading it took at least `--window` ago, so it does not sleep through the
window; see [`spectra load`](cli.md) for details and `--no-daemon`.

If CPU could not be measured over a window (`cpu_basis` is not `window`),
the run is an error (exit `1`): a lifetime or decaying average is not
evidence of idleness now. Working directories are read and matched as in
[Working-directory owners](owner-under.md), including lsof escape decoding
and the macOS file-identity fallback.

## Wait evidence

For reported processes and their descendants, read only where cheap:

- Linux: `/proc/<pid>/wchan` (the kernel function it sleeps in, such as
  `futex_wait_queue` or `pipe_read`) and open files named like locks
  (`*.lock`, `*.lck`, `LOCK`, `lockfile`, `.lock*`) from `/proc/<pid>/fd`;
- macOS: open files named like locks from one `lsof -p <pids>`.

Missing evidence is omitted, never an error.

## Output and exit codes

`--json` emits `roots`, `pids`, `min_age_seconds`, `max_cpu_pct`,
`window_ms`, `cpu_basis`, `incomplete`/`hint` (some working directories could
not be read), `limitations`, and `result` with `holders` and the counts of
in-scope processes skipped as `skipped_busy`, `skipped_young`,
`skipped_unmeasured`, plus `nested` and `missing_pids`.

| Code | Meaning |
|---|---|
| `0` | no idle holders found |
| `3` | idle holders found |
| `1` | error, including reaching `--timeout` (default `1m`, must exceed `--window`), CPU not measured over a window, an empty process table, or a `--root` that is not an existing directory |
| `2` | usage |
