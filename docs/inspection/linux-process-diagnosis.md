# Linux process diagnosis and pressure

Linux counterpart of the macOS `sample` / `hang` workflow. Both commands read
only `/proc` and `/sys/fs/cgroup`: no external tool, no helper, and no
privilege for the caller's own processes.

```bash
spectra procdiag 1234                  # why is pid 1234 slow or stuck?
spectra procdiag --interval 5s --json 1234
spectra pressure                       # host PSI: is the machine stalled on cpu/memory/io?
spectra pressure --pid 1234            # plus the process's cgroup v2 limits and OOM history
```

Both exit `3` when they find anything at warning or above, so scripts can gate
on them, and `1` on non-Linux hosts (use `spectra sample` / `spectra memory`
there).

## Why two samples

One snapshot cannot tell a leak from a steady state, or a stall from a blink.
`procdiag` samples every thread twice (default 1s apart) and reasons about the
deltas.

| Finding | Evidence | Notes |
|---|---|---|
| `cpu-spin` | ≥90% of one core over the interval | Reports the hottest thread and the user/kernel split; a kernel-heavy spin is labelled a syscall storm. |
| `uninterruptible-io` | a thread in `D` state in **both** samples | One `D` reading is ordinary disk wait; persistent `D` means SIGKILL will not work (hung disk, NFS, FUSE). |
| `lock-wait` | ≥2 threads, all parked on a futex, none scheduled during the interval | Strong deadlock/lost-wakeup signal, but an idle thread pool looks the same: confirm with a stack dump. Only fires when **every** thread is parked, so a deadlock beside a thread that is still running (for example a sleeping main thread) is not reported. |
| `blocked-syscall` | main thread in the same non-idle syscall, unscheduled, both samples | `connect`/`recv*`/`send*` and `fsync`/`flock` are warnings; a plain `read` is info (a program waiting on input is normal). Idle waits (`epoll_wait`, `poll`, `nanosleep`, `accept`...) are never reported. |
| `fd-growth` | descriptor count climbing ≥5/s | Includes time-to-limit from `RLIMIT_NOFILE`. |
| `fd-exhaustion` | ≥80% (warning) / ≥95% (critical) of the soft limit | |
| `memory-growth` | RSS rising ≥8 MiB/s | Re-run with a longer `--interval` to separate a leak from warm-up. |
| `swapped-out` | ≥256 MiB and ≥25% of the footprint in swap | Info. |
| `zombie`, `stopped` | process state `Z`, `T`/`t` | |

Syscall numbers from `/proc/<pid>/task/<tid>/syscall` are resolved to names
for amd64 and arm64 (tables generated from the kernel UAPI headers); other
architectures show `syscall_<n>`.

Permissions: `wchan`, `syscall`, and the fd table are only readable for your
own processes (or as root). When they are not, the affected checks are skipped
and listed under `notes` rather than producing false negatives silently.

## Pressure (PSI) and cgroups

`/proc/pressure/{cpu,memory,io}` report the share of wall time that tasks were
**stalled** on a resource. `some` means at least one task stalled; `full`
means every non-idle task stalled at once, i.e. the resource is the
bottleneck. Thresholds (10s average): `some` ≥10% warning / ≥30% critical;
`full` ≥1% warning / ≥10% critical (memory and I/O only).

For a process, the cgroup v2 files add the cause of an otherwise mysterious
kill or slowdown:

| Finding | Source |
|---|---|
| `cgroup-oom-kill` | `memory.events oom_kill > 0` |
| `cgroup-memory-max-hit` / `-throttled` | `memory.events max` / `high` |
| `cgroup-memory-near-limit` | anonymous memory ≥90% of the tightest `memory.max` on the cgroup **or any ancestor** (a limit on a parent service applies to its children) |
| `cpu-throttled` | `cpu.stat nr_throttled / nr_periods` against the `cpu.max` quota |
| `pids-near-limit` | `pids.current` ≥90% of `pids.max` |

Hosts still on cgroup v1 report the cgroup section as unavailable; PSI needs
Linux 4.20+ built with `CONFIG_PSI` and not booted with `psi=0`.

## Implementation

- `internal/procdiag` — two-sample collector and finding rules. Roots are
  injectable (`Options.ProcRoot`, `CgroupRoot`, `Sleep`, `Now`), so every rule
  is tested against fixture trees on any host OS.
- `internal/pressure` — PSI and cgroup v2 parsers plus the assessment rules.

See also [Running processes](running-processes.md) and
[System limits](system-limits.md).
