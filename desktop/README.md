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
  cursor; Return or a double click opens a process or a group, as does the triangle of a group.
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

It only reads. It never quits or signals a process, except a Spectra reading of its own that
runs past its deadline.

```sh
cargo run                                              # the window
cargo run -- --snap out.png --scene sample --theme dark  # headless render with sample data
cargo run -- --list-scenes  # sample, all, apps, group, process, signed, detail, stale, old
cargo test                                             # unit and headless UI tests
SPECTRA_BIN=/path/to/spectra cargo test -- --ignored --nocapture  # reads both live feeds
```

- `src/procs.rs`: the process feed and its parsing; `src/run.rs`: running Spectra with a
  deadline and an output cap.
- `src/groups.rs`: filtering, grouping and sorting the process list.
- `src/proc_findings.rs`: the rules that turn a process's readings into findings.
- `src/data.rs` and `src/findings.rs`: the JVM feed and its rules.
- `src/app.rs`: state and each process's recent history; `src/main.rs`: the polling threads.
- `src/sample.rs`: sample data for the headless scenes and tests.
- `src/ui/`: the view, one module per page.

Rune is pre-release and pinned to a `dev` rev in `Cargo.toml`; the `[patch.crates-io]` revs must
match it.
