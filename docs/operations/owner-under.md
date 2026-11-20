# Working-directory owners

`spectra owner --under DIR` answers "is any process working inside this
tree?" before a worktree or build directory is removed. `spectra owner
/path` only answers who holds that exact path.

```bash
spectra owner --under ~/src/repo/.worktrees/lane-3
spectra owner --under --json --timeout 10s /tmp/build-a /tmp/build-b
```

Every visible process whose working directory is one of the directories, or
anywhere below one, is listed with its pid, user, command, cwd, executable,
start time and parent chain (nearest parent first). Relative directories are
resolved against the current directory, and each must exist.

## How it reads working directories

| OS | Source |
|---|---|
| macOS | one `lsof -w -nP -a -d cwd -Fpn` for the whole host |
| Linux | `readlink /proc/<pid>/cwd`; a removed directory's ` (deleted)` suffix is stripped |

Each directory is matched both as given and with symlinks resolved, and a
process cwd that does not match lexically is resolved too, so `/tmp/x` and
`/private/tmp/x` are the same tree. Matching is by path component:
`/work/repo` does not match `/work/repository`.

Spectra itself and its direct children (the `lsof` it runs) are left out, so
running the check from inside the tree does not report itself. Its parent
shell is reported: it is working there.

## Incomplete results

`incomplete: true` (with a `hint`) means some process could not be read:
on macOS without root, another user's process has no cwd in the `lsof`
output; on Linux, a `/proc/<pid>/cwd` link was permission-denied (also under
root in a container without `CAP_SYS_PTRACE`). Processes that exited during
the scan are not counted as unreadable.

## Exit codes and run time

| Code | Meaning |
|---|---|
| `0` | at least one process is working under a directory |
| `3` | none found (check `incomplete` before treating the tree as free) |
| `1` | error, including reaching `--timeout` |
| `2` | usage |

`--timeout` (default `30s`) bounds the whole run. Reaching it is an error,
never "none found", so a script that removes the tree on exit `3` stays safe.
