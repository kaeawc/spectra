# System limits

System limits explain machine-wide failures that surface in one unlucky app.
When `forkpty()`, `posix_spawn()`, or `open()` starts failing, the terminal,
IDE, or shell that reports the error is often only the victim. The culprit is
usually another long-running process holding too many pseudo-ttys, file
descriptors, or process slots.

Use:

```bash
spectra system limits
spectra system limits --json
spectra system limits --top
```

The command reports:

- pty slots from `kern.tty.ptmx_max` plus live `/dev/ptmx` and `/dev/ttys*`
  descriptor counts
- system open files from `kern.num_files` and `kern.maxfiles`
- per-process file cap from `kern.maxfilesperproc`
- system process cap from `kern.maxproc`
- per-user process cap from `kern.maxprocperuid`

Rows over 80% are `WARN`; rows over 95% are `CRITICAL`. The command exits
non-zero when any resource is critical so it can be used from cron, CI, or a
monitoring wrapper.

## Pty exhaustion

Terminal launch failures are the canonical pty symptom:

```text
Session Ended
```

or an empty terminal pane with no shell output. Check limits first:

```bash
spectra system limits --top
```

If `pty slots` is critical, the top table points at processes with the highest
pty descriptor counts. Pair that with the process breakdown:

```bash
spectra process --deep --fd-breakdown --sort rss
```

Quit or restart the suspect app, then rerun `spectra system limits`. A drop in
pty usage confirms that the terminal app was not the root cause.

## macOS quirks

Some macOS versions restrict selected `sysctl` values for non-root users. When
that happens, Spectra keeps the rest of the report and records the missing
source under `partial_failures` in JSON output.

`lsof` can also return partial data with a non-zero exit when some processes
are protected. Spectra still parses any stdout it receives, so unprivileged
diagnostics remain useful even when system-owned processes are hidden.

## Snapshots

Snapshots include `system_limits`, so `spectra snapshot diff a b` can show when
limit pressure changed between two captures. This is useful when the machine has
already recovered and you need to identify whether resource saturation was part
of the incident.
