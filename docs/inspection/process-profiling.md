# Process profiling

Process profiling is the active side of Spectra's process inspection. The
running-process view answers "what belongs to this app right now?" Profiling
answers "what is it doing, how did it get here, and is the behavior changing?"

This page covers four related workflows:

- point-in-time samples with `sample`
- process trees and app-scoped child process attribution
- daemon-backed process history
- CPU and RSS trend interpretation

## Workflow

Start with the cheapest view and escalate only when the question needs it:

```bash
spectra process --deep
spectra connect local process-tree /Applications/Slack.app
spectra connect local metrics
spectra connect local metrics 4012 120
spectra connect local sample 4012 5 1
```

`spectra process --deep` gives the flat inventory with CPU%, RSS, thread
counts, open file descriptor counts, listening ports, and outbound
connections. `process-tree` adds parent/child context. `metrics` shows recent
daemon samples. `sample` captures call stacks when the process is actively
burning CPU.

## Samples

macOS ships `sample <pid>`, which records stack traces over a bounded interval.
Spectra exposes it as an explicit action because sampling can be noisy,
potentially sensitive, and slow compared with the normal inventory path.

```bash
spectra sample --duration 5 --interval 10 4012
spectra connect local sample 4012 5 1
spectra connect work-mac sample 4012 10 1
```

The first positional value is the duration in seconds. The second is the sample
interval in milliseconds. The local `spectra sample` command writes sample
output to stdout and stores it in the sample blob cache unless `--no-cache` is
set. The daemon `process.sample` RPC returns the text output to the caller;
persisting remote sample metadata and blob keys is the storage-backed shape for
future history views.

Use samples when:

- CPU is high now and you need hot call paths.
- A renderer/helper process is stuck and the command line alone is ambiguous.
- You need a text artifact to compare across machines or across time.

Avoid treating one sample as a full diagnosis. A short capture is best at
showing where time was spent during that window. It does not prove that the
same stack was hot before or after the capture.

## Process trees

Flat process lists hide ownership. Electron, Chromium, Java launchers,
updaters, login items, and XPC services often produce many sibling processes
with similar names. The tree view joins process rows by PID and PPID, then
optionally scopes the result to one or more app bundles:

```bash
spectra connect local process-tree
spectra connect local process-tree /Applications/Claude.app
```

The scoped view is useful for answering:

- Which helper is the parent of the hot child?
- Did a GUI app spawn a long-lived updater or login item?
- Is an orphaned helper still running after the main app exited?
- Are JVM, Node, or Python child processes part of the app or user-launched?

The unprivileged daemon can only see process details visible to the current
user. The privileged helper design reserves `helper.process.tree()` for a
full-system tree when root-owned daemons or other users' processes matter.

## Process history

When `spectra serve` is running, the daemon samples process-level CPU%, RSS,
and virtual size at about 1Hz into an in-memory ring buffer. Recent samples are
served through `process.live`; per-PID history is served through
`process.history`:

```bash
spectra metrics
spectra connect local metrics
spectra connect local metrics 4012 120
```

The ring buffer is for immediate troubleshooting: "what changed in the last few
minutes?" The daemon also flushes one-minute aggregates to SQLite so longer
views can be queried without storing every per-second row forever.

History rows should carry:

- timestamp
- PID and stable process identity fields
- CPU percent
- RSS KiB
- virtual size KiB
- command or executable path when visible
- host identity when queried remotely

PIDs are reused, so history consumers should treat `(pid, process_start_time)`
or the daemon's internal process identity as the stable key. PID-only joins are
acceptable for a short live window but unsafe for longer history.

## CPU trends

CPU% from `ps` is point-in-time. It is good for ranking active processes, but
bad for explaining whether load is sustained. Use daemon history for trend
questions:

- **Short spike** — a high value for one or two samples, then back to idle.
- **Sustained burn** — repeated high values across many samples.
- **Sawtooth** — regular bursts, often timers, polling, rendering, or GC.
- **Load shift** — one helper cools down while another heats up, common in
  multi-process apps.

For a sustained burn, capture `sample` while CPU is high. For a sawtooth, use a
longer history window first, then sample during the hot phase.

## RSS trends

RSS is resident memory attributed to a process. It includes shared pages, so
sum-of-process RSS overstates unique memory pressure for apps with many
helpers. It is still the right first signal for "what does Activity Monitor
blame this app for?"

Use history to separate normal warm-up from suspicious growth:

- **Warm-up plateau** — RSS climbs after launch, then stabilizes.
- **Linear growth** — RSS increases steadily under similar workload.
- **Step growth** — RSS jumps after a specific action and never returns.
- **Helper churn** — total app RSS is stable, but memory moves between child
  processes.

RSS alone does not identify leaks. Pair it with process trees, app actions,
JVM heap data for Java processes, and samples only when CPU behavior is also
interesting.

## Stored metrics

The storage model splits process metrics by density:

- recent per-second samples stay in the daemon's in-memory ring buffer
- one-minute aggregates are flushed to SQLite
- heavyweight artifacts such as `sample` output live in the sharded blob store

This keeps the common local workflow cheap while still supporting remote
debugging questions such as:

- "Was Slack already hot before I connected?"
- "Which process grew over the last 30 minutes?"
- "Did the helper process restart between snapshots?"
- "Can two Macs compare the same app's CPU/RSS pattern?"

Stored rows should avoid sensitive payloads. Command lines can contain secrets,
so logs and persisted metadata should keep only the fields needed for
attribution and debugging.

## Remote profiling

The same RPC surface works through a local Unix socket, explicit TCP, or
Tailscale `tsnet` listener:

```bash
spectra connect work-mac processes
spectra connect work-mac process-tree /Applications/IntelliJ\ IDEA.app
spectra connect work-mac metrics 4012 120
spectra connect work-mac sample 4012 5 1
```

Remote sampling is intentionally explicit. It can expose stack frames,
filenames, class names, and library names. Sensitive captures should require
the same confirmation posture as heap dumps, JFR recordings, and network
captures when they are persisted or copied off-host.

## Limitations

- `sample` requires permission to inspect the target process. Same-user
  processes usually work; other users' or protected processes may require the
  privileged helper or may remain unavailable.
- CPU% is scheduler-time attribution, not a business-level explanation of what
  the app is doing.
- RSS counts shared pages and is not unique set size.
- PID reuse means historical views need a process start time or other stable
  identity.
- Daemon history only exists while the daemon is running. Snapshots can show
  past point-in-time state, but not pre-daemon per-second trends.

## Implementation reference

- [running-processes.md](running-processes.md) — base process inventory and
  app attribution.
- [live-data-sources.md](live-data-sources.md) — source commands and expected
  costs for `ps`, `libproc`, `lsof`, `sample`, and `nettop`.
- [../operations/daemon.md](../operations/daemon.md) — daemon RPC surface and
  live data ring buffer.
- [../design/storage.md](../design/storage.md) — SQLite, blob store, and
  in-memory metrics tiers.
- [../design/threat-model.md](../design/threat-model.md) — sensitive artifact
  and remote-operation posture.
