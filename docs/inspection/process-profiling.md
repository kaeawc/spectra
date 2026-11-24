# Process profiling

Process profiling is the active side of Spectra's process inspection. The
running-process view answers "what belongs to this app right now?" Profiling
answers "what is it doing, how did it get here, and is the behavior changing?"

This page covers four related workflows:

- point-in-time samples with `sample`
- process ownership and app-scoped child-process attribution
- point-in-time CPU and RSS interpretation
- CPU and RSS trend interpretation

## Workflow

Start with the cheapest view and escalate only when the question needs it:

```bash
spectra process --deep
spectra sample --duration 5 --interval 10 4012
```

`spectra process --deep` gives the flat inventory with CPU%, RSS, thread
counts, open file descriptor counts, listening ports, and outbound
connections. `sample` captures call stacks when the process is actively
burning CPU.

For descriptor leaks, add `--fd-breakdown`:

```bash
spectra process --deep --fd-breakdown --sort rss
```

This keeps the same process inventory but splits open descriptors into ptys,
sockets, regular files, pipes, character devices, kqueues, and other handles.
The JSON output includes `fd_breakdown` automatically whenever `--deep`
populates descriptor data.

## Samples

macOS ships `sample <pid>`, which records stack traces over a bounded interval.
Spectra exposes it as an explicit action because sampling can be noisy,
potentially sensitive, and slow compared with the normal inventory path.

```bash
spectra sample --duration 5 --interval 10 4012
```

The first positional value is the duration in seconds. The second is the sample
interval in milliseconds. The local `spectra sample` command writes sample
output to stdout and stores it in the sample blob cache unless `--no-cache` is
set.

`--json` (or `-json`) prints one object instead of the raw report: `pid`,
`process`, `taken_at`, `duration_ms`, `interval_ms`, `total_samples` (sum of
self-time counts), `stack_count`, `stacks` (the 50 heaviest self-time stacks as
`{frames, count, percent}`, root first) and `main_thread` (the `spectra hang`
verdict). The raw text report is still stored in the sample blob cache unless
`--no-cache` is set; only the temporary capture file is removed. Failures exit
non-zero with the error on stderr and nothing on stdout.

### Frame identity

`sample` and `spindump` reports end with a `Binary Images:` section listing
each loaded binary's address range, UUID and path, and every call-tree frame
carries its address (`[0x18a6e4c34]`). Spectra maps each frame address to the
image containing it, so a disassembler can open the right binary at the right
offset without searching by symbol name:

- `spectra sample --json`: each stack in `stacks` gains `frame_details`, one
  object per entry in `frames`, in the same order.
- `spectra flamegraph --json`: after writing the SVG, prints `svg` (the path
  written), `stack_count` and `stacks`, every folded stack as `{frames,
  count, frame_details}`. The SVG itself is unchanged.
- `spectra spindump --json`: each `heaviest` frame gains the fields inline,
  next to `symbol` and `samples`. A symbol seen at several addresses uses the
  address of its heaviest line.

| Field | Meaning |
|---|---|
| `image` | Base name of the image path (what frames print as `(in AppKit)`). |
| `image_path`, `image_uuid` | Path and UUID from the Binary Images entry. |
| `arch` | Process architecture from `Code Type:` (sample) or `Architecture:` (spindump), normalized to `arm64`, `arm64e`, `x86_64`. |
| `image_base` | Load address of the image, hex string. |
| `image_offset` | `address - image_base`, hex string. |
| `address` | The frame's address, hex string. |

The names match `frame_details` in `spectra crash inspect --json`. Fields are
omitted when unknown: a frame whose address falls outside every listed image
(JIT code, a thread header line) has an empty `{}` entry in `frame_details`
and no extra fields in spindump. When no frame in a stack resolves, or the
report has no Binary Images section, `frame_details` is absent and the
output is byte-for-byte what it was before these fields existed. The
`image_uuid` is the UUID at capture time; compare it with the binary on disk
before trusting a disassembly, because the binary may have been updated
since.

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

The scoped view is useful for answering:

- Which helper is the parent of the hot child?
- Did a GUI app spawn a long-lived updater or login item?
- Is an orphaned helper still running after the main app exited?
- Are JVM, Node, or Python child processes part of the app or user-launched?

The local CLI can only see process details visible to the current user. The
privileged helper design reserves `helper.process.tree()` for a full-system
tree when root-owned processes or other users' processes matter.

## CPU trends

CPU% from `ps` is point-in-time. It is good for ranking active processes, but
does not establish whether load is sustained:

- **Short spike** — a high value for one or two samples, then back to idle.
- **Sustained burn** — repeated high values across many samples.
- **Sawtooth** — regular bursts, often timers, polling, rendering, or GC.
- **Load shift** — one helper cools down while another heats up, common in
  multi-process apps.

For a sustained burn, capture `sample` while CPU is high and compare local
snapshots over time.

## Pty leaks

Pseudo-tty leaks usually present as a terminal problem even when another app is
responsible. iTerm2, Terminal.app, VS Code, or a JetBrains terminal can show an
empty pane or an immediate "Session Ended" because `forkpty()` cannot allocate a
new `/dev/ptmx` or `/dev/ttys*` pair.

Start with the descriptor breakdown:

```bash
spectra process --deep --fd-breakdown --sort rss
```

Look for one app or helper with a high `PTY` count. Electron and Chromium apps
normally run several helpers, but they should not hold dozens of ptys when no
embedded terminal sessions are active.

If the pty count drops after quitting the suspect app, the terminal was the
victim and the descriptor holder was the culprit. Use snapshots before and
after remediation when you need a durable incident record.

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

RSS alone does not identify leaks. Pair it with app actions, JVM heap data for
Java processes, and samples only when CPU behavior is also interesting.

## Limitations

- `sample` requires permission to inspect the target process. Same-user
  processes usually work; other users' or protected processes may require the
  privileged helper or may remain unavailable.
- CPU% is scheduler-time attribution, not a business-level explanation of what
  the app is doing.
- RSS counts shared pages and is not unique set size.

## Implementation reference

- [running-processes.md](running-processes.md) — base process inventory and
  app attribution.
- [live-data-sources.md](live-data-sources.md) — source commands and expected
  costs for `ps`, `libproc`, `lsof`, `sample`, and `nettop`.
- [../design/storage.md](../design/storage.md) — SQLite, blob store, and
  local artifact tiers.
- [../design/threat-model.md](../design/threat-model.md) — sensitive artifact
  posture.
