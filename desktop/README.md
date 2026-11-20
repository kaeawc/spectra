# spectra-desktop

A diagnosis-first desktop app for every process on this machine, built with
[Rune](https://github.com/anomalyco/rune). It opens on what's wrong, worst first; each finding
expands into its evidence and a fix. Next to that is a list of everything running, grouped the
way people think of it: an app with all its helpers is one row, twelve `zsh` are one row, and
each opens onto its processes.

It reads two feeds from the [Spectra](https://github.com/kaeawc/spectra) CLI, each on its own
thread, so nothing needs setting up:

- `spectra process -json --classify` about every 2 s: every process with its role (app, helper,
  AI agent, terminal, shell, developer tool, system), its runtime (Electron, JVM, Node, Bun, Go,
  Rust, Python, native and more), the app it belongs to and its code signature.
- `spectra jvm -json` for JVMs in depth: heap, GC, classes and threads. It is slow (about 17 s a
  reading), so JVM numbers land less often.

Set `SPECTRA_BIN` if `spectra` isn't on `PATH`. A Spectra too old for `--classify` gets a notice
saying so; the JVM views keep working.

A process reading that fails, or lists nothing, keeps the last good list on screen, and a plain
line says so: "Updated 12 s ago · last reading failed: …". The same line appears, without the
failure, once the numbers are more than three refreshes old. A reading still running after 30 s
(a cold classification takes about 20 s) is stopped and counts as failed; a JVM reading gets 90 s.

What it shows:

- **Overview**: process count, CPU in use, the kinds of process holding the most memory, then
  every finding. Process findings: CPU above 80% of a core in each of the last 31 readings
  (over a minute), memory up at least 20% and 50 MB across at least 60 readings (about two
  minutes) without falling more than 2% between two of them, an invalid code signature, and
  one summary of unsigned and ad hoc signed processes. A process younger than five minutes
  (from its start time) gets no CPU or memory finding: starting up is busy and grows. JVM
  findings: heap pressure, old-generation growth, slow full GCs, metaspace pressure, no heap
  dump on OOM.
- **All processes**, then by kind and by runtime in the sidebar, with live counts. Sort by CPU,
  memory or name; search by name, pid, bundle id or path. The table only builds the rows on
  screen, so the full list stays quick. Rows group an app with its helpers, and processes of one
  name running one executable; two `java` from different JDKs stay apart, each with the folder
  that tells them apart beside its name. Clicking a row or arrowing onto it only moves the
  cursor. Return or a double click opens a process's page or an app's page; on any other group
  (twelve `zsh`) it opens or closes the group. The triangle always only opens or closes it.
- **An app's page**: all its processes together (CPU, memory and threads over the last few
  minutes, summed), its network and energy while the page is open, its processes (each opens its
  page), and its bundle from `spectra --json <App.app>`, read once each time the page opens: what
  it's built with, version, frameworks, architectures, size, signature (team, hardened runtime,
  sandbox, App Store receipt), Gatekeeper, permissions granted, entitlements, helpers, login items
  and the data it keeps. A failed read says why and offers Try again.
- **Signatures**: a check only for a signature Spectra validated (Apple's, today). A Developer
  ID that Spectra only read from `codesign -dv` shows as "Developer ID · not verified" with a
  dash, and its page says the team is "as displayed, not verified" and quotes Spectra's
  evidence. A group shows its least trusted member's signature. Linux has no signatures, so the
  column hides.
- **A process's page**: its parent (one click away), user, start time, executable, role,
  runtime, app and bundle id, signature with team and hardened runtime, what it started, CPU and
  memory over the last few minutes, its command line one flag per line, and why Spectra
  classified it as it did. A JVM's page adds its heap tiles, timeline and arguments, and "Pin
  baseline" compares its numbers against a moment you choose. Esc, Cmd-[ or the back button
  return to the page before, so following a parent link and going back lands on the child. A
  page closes if its pid comes back as another process.
- **Live charts** on a process or app page: CPU, memory and threads from the regular readings,
  and, only while the page is open, network in and out from `spectra network top --pid <pid>
  --json --duration 2s` (an app's main process with what it started) and energy impact from
  `spectra impact --json` (an app's processes summed). One round at a time, off the UI thread,
  each command with a deadline (15 s and 10 s), then a 2 s rest. A series that fails says why and
  isn't asked for again until the page is opened again; leaving the page stops its round.
- **Tools** on a process page, run only when you press Run, one at a time, each with a deadline,
  Cancel (Spectra is stopped; the row says "Cancelled" once it has), and a result or the reason it
  failed. Leaving the page stops what it was running.
  - CPU: Sample stacks (`spectra sample -json <pid>`): the main thread's verdict and the busiest
    stacks with their share, plus the `spectra flamegraph` command to run for a flame graph.
    Hang check (`spectra hang --json <pid>`): idle, busy, or blocked on a lock or I/O, and where.
  - Memory: Memory map (`spectra vmmap --json`): footprint, peak, dirty and swapped, and region
    types by dirty size. Dirty regions (`spectra vmregions --json`): anonymous and file-backed
    dirty memory and the single regions holding most. Memory trend (`spectra memtrace <pid>
    --duration 20s --json`): growing, steady, shrinking or sawtooth, with its readings charted.
  - Relations: Lineage (`spectra lineage --json`): what started it from launchd down, and its
    tree of descendants; each listed process opens its page. Owner (`spectra owner --json`): its
    user, executable, start and parent chain.
  - Runtime (`spectra runtime --json`): what it runs on, why, and Spectra's next steps.
  - Spindump and Mach ports (`lsmp`) need root and are shown off with the reason; Spectra Desktop
    never runs sudo. Tools that read another user's threads or memory (sample, hang, vmmap,
    vmregions) are off on their processes, with the reason.
- **Quit and Force quit**: in a process page's header, and over the list for the row under the
  cursor. An app's row acts on the app's main process ("Quit Slack"); a row of several
  processes that isn't one app (twelve `zsh`) has no action, so open it and pick one. ⌥⌘Q opens
  the Quit confirmation for the open page or the cursor's row. Quit asks the process to exit
  (SIGTERM); Force quit, red and marked with a cross, ends it at once (SIGKILL).
  - Nothing is sent without a confirmation that names the process and its pid and says what
    will happen, with an extra warning for a part of macOS. Cancel has focus; Esc, the close
    button or a click outside cancel. No key press sends on its own, and one confirmation sends
    at most one signal however often its button is pressed.
  - Just before sending, `ps` is asked who has the pid now (start time, in UTC, and command);
    unless that is the same second and program the reading showed, nothing is sent and the
    page says so: "That process has already ended."
  - Off, with the reason beside them: the kernel and launchd, Spectra Desktop and the readings
    it starts, another user's processes ("Owned by root; Spectra Desktop can't stop it"; there
    is no sudo), and a process whose start time Spectra didn't give.
  - Afterwards it says what happened, and no more: "Asked Slack to quit", then "Slack quit"
    once a reading no longer lists it (the feed reads again at once). Still listed 5 s later,
    it says so and offers Force quit. A failed `kill` says why. The page of a process that
    ended this way stays, says so and offers Back.
  - A finding about an invalid signature points to Force quit on the process's page.

It reads, and signals only a process the person confirmed (and a Spectra run of its own that
runs past its deadline or is cancelled).

```sh
cargo run                                              # the window
cargo run -- --snap out.png --scene sample --theme dark  # headless render with sample data
cargo run -- --list-scenes  # sample, all, apps, group, process, signed, detail, stale, old,
                            # quit, quit-list, quit-confirm, force-confirm, quit-root,
                            # quit-still, quit-ended, app, app-loading, tools, tools-root,
                            # tool-sample, tool-hang, tool-vmmap, tool-vmregions,
                            # tool-memtrace, tool-lineage, tool-owner, tool-runtime, tool-failed
cargo run -- --snap out.png --scene tool-sample --size 1100x3000  # tall, to show the tools
cargo test                                             # unit and headless UI tests
SPECTRA_BIN=/path/to/spectra cargo test -- --ignored --nocapture  # reads both live feeds, and
                            # runs every tool on a `sleep` the test starts and then stops
```

- `src/procs.rs`: the process feed and its parsing; `src/run.rs`: running Spectra with a
  deadline and an output cap.
- `src/groups.rs`: filtering, grouping and sorting the process list.
- `src/proc_findings.rs`: the rules that turn a process's readings into findings.
- `src/data.rs` and `src/findings.rs`: the JVM feed and its rules.
- `src/signal.rs`: Quit and Force quit: what may be signalled, the `ps` identity check, and
  `/bin/kill`, behind a `Signals` trait that tests replace with a recorder. Sample scenes use
  one that sends nothing, since their pids are made up.
- `src/inspect.rs`: what an open page runs (tools, live rounds, the bundle read): one job per
  thread, numbered so an answer lands only on the page it was for, behind a `Spectra` trait that
  tests replace with a fake. `src/tools.rs`, `src/live.rs` and `src/bundle.rs`: those commands
  and their JSON.
- `src/app.rs`: state and each process's recent history; `src/main.rs`: the polling threads.
- `src/sample.rs`: sample data for the headless scenes and tests.
- `src/ui/`: the view, one module per page.

Rune is pre-release and pinned to a `dev` rev in `Cargo.toml`; the `[patch.crates-io]` revs must
match it.
