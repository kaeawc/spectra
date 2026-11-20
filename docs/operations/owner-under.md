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
| macOS | one `lsof -w -nP -a -d cwd -FpRcn` for the whole host; any non-zero exit is an error, even with partial output |
| Linux | `readlink /proc/<pid>/cwd`; a removed directory's ` (deleted)` suffix is stripped |

`lsof -F` escapes names: a backslash is `\\`, `\t` `\n` `\r` `\b` `\f` are
C escapes, other non-printable bytes (including each byte of non-ASCII UTF-8,
such as `\xc3\xbc` for `ü`) are `\xNN`, and control characters are `^A`..`^_`.
Spectra decodes these before comparing; because a literal `^` is not escaped,
a name with a caret is read both ways and the spelling that exists is used.

Each directory is matched both as given and with symlinks resolved, and a
process cwd that does not match lexically is resolved too, so `/tmp/x` and
`/private/tmp/x` are the same tree. Matching is by path component:
`/work/repo` does not match `/work/repository`. On macOS, a cwd that still
does not match is compared by file identity (device and inode): the cwd and
each of its ancestors (up to 64 levels) are stat'ed against each directory,
so `--under /src/WT` matches a process lsof reports in `/src/wt` on a
case-insensitive APFS volume.

Holders come from the cwd source itself, not only from the process table: a
pid that `lsof` (or `/proc`) reports but `ps` missed, such as one started
between the two reads, is listed with the command and parent the cwd source
gave. An empty process table (`ps` or `/proc` unreadable) is an error.

Flags may come before or after the directories (`--under DIR --json`); use
`--` to end flags before a directory whose name starts with `-`.

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
| `3` | none found, and every process was read |
| `4` | none visible, but some processes could not be read (`incomplete: true`); re-run with sudo before treating the tree as free |
| `1` | error, including reaching `--timeout`, a failed `lsof`, or an empty process table |
| `2` | usage |

Exit `4` applies in text and `--json` modes alike. `--timeout` (default
`30s`) bounds the whole run. Reaching it is an error, never "none found", so a
script that removes the tree only on exit `3` stays safe.
