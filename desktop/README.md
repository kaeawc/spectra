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
  reading, and about as many CPU-seconds with a dozen JVMs), so it rests 30 s between readings,
  2 s while a JVM's page is open, and doesn't run at all while the process list shows no JVM.

The window asks for each reading after the last one landed and rested. While the window is
hidden, minimised or covered, nothing is read: the feeds, the Machine readings, an open page's
live charts and Logs' Follow, the Activity and Network pages, the Overview's "Right now" look and
the Storage page's Time Machine read and the Compare page's list all rest (except Activity's "Keep recording"; a folder being
measured may finish), and coming back reads at once.

**Spectra's daemon.** Where the daemon (`~/.spectra/daemon.sock`, started with
`spectra daemon start` if its socket is missing) already holds a reading, the app asks it on one
long-lived connection instead of starting a `spectra` process. One table in `src/daemon.rs`
decides, from the methods `daemon.status` lists: the process list from `process.list` (asked
every 2 s while the window shows, from the daemon's shared snapshot; taken fresh after a Quit or
Kill), JVMs from `jvm.list`, the load average from the host sample
`alerts.subscribe` pushes (else `watch.current`), and the Machine page's memory, thermal, power
and network readings from `memory.current`, `thermal.current`, `power.current` and
`network.current`. A daemon without a method, or none at all,
leaves that call to the CLI as before, and so does one error from it, for that call only. The
connection is made on a thread of its own: a call never waits on another's reconnect, and waits
at most half its deadline for one before the CLI runs it.

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
  - **Right now**, at the top: why this Mac may be slow at this moment, from
    `spectra whatswrong --json`. Up to three causes, worst first, each with its evidence in plain
    words and a link to the page that shows more: a process's page, an app's page when a busy
    group (a browser, an editor and what it runs) belongs to an app, or the Machine page for
    memory pressure, swap and heat. A group's busiest process is not listed again on its own. A
    cause that says what a finding already says shows once, up here, with that finding's fix,
    and the finding leaves the list below. When nothing stands out it says so in one line. It
    asks Spectra when the Overview opens and then at most every 30 s while the Overview is on
    screen, off the UI thread with a 20 s deadline and a cap on what it may print; another page,
    or quitting, stops a look in flight. The line above it says how old the answer is ("Checked
    12 s ago"), and a failed look keeps the last answer and says why.
- **Machine**, right under Overview: the whole machine, live. CPU in use (every process summed)
  and the 1-minute load, charted over the last six minutes, with the core count. Memory pressure
  as a colored gauge (green, amber, red), what's used, app memory, wired, file-backed and
  compressed memory with the compressor's ratio, swap used, and swap-ins, swap-outs and
  compressions since the last reading; free-for-new-work, compressed, compressions and swap are
  charted. Thermal state and CPU speed limit, battery and power source. Disks with used and
  free bars (only volumes people keep files on: no read-only system or simulator images, no
  memory-backed file systems). Network: default route, DNS, VPN, connections and how many ports
  are listening, with a link to the Network page for what is listening and who is moving bytes. Top
  consumers: the five busiest processes by CPU and the five largest by memory, each opening
  its page. On Linux, a part Spectra doesn't read (memory pressure, the battery) is left out
  rather than shown blank.
  - Readings: `spectra memory --json`, `spectra thermal --json` and `spectra load --json` every
    5 s; `spectra power --json` and `spectra network --json` every 30 s, and
    `spectra storage --json` about every five minutes, on two threads of their own, each with a deadline (20 s and 60 s). `thermal` exits 3 when
    warm and 4 when throttled, and `load` 3 or 4 when the machine is busy: those are readings,
    not failures. `spectra storage` also sizes `~/Library`, which can take many minutes, so it
    runs with an empty home (`/var/empty`) and only the volumes come back; one that still runs out
    of time isn't asked again for five minutes. A failed reading keeps the last good one, and
    the part says "Updated 40 s ago · last reading failed: …"; one that never landed says why.
  - Findings on the Overview, each opening the Machine page: memory pressure at warning or
    critical, swap in use and up at least 64 MB over the last minute, the CPU thermally
    throttled, and volumes over 90% full (critical from 97%). A pressure finding clears after three
    normal readings in a row, a volume finding once it is under 88%, and a part whose reading
    failed or is three rounds old has no finding.
- **Activity**, right under Machine: processes starting and exiting, live, including the
  short-lived ones the 2 s process list never sees. For when something keeps spawning and dying.
  Newest first: time, Started or Exited, the process and its parent (each opens its page while the
  process list has it), how long an exited one lived, and its command line when the process list
  saw it (else the chain of parents that started it). Click a row for the whole command line,
  every parent up to launchd, and how sure the lifetime is. Filter by name, pid or parent;
  "Short-lived only" keeps processes that lived under 2 s. Above it, the last minute: starts,
  exits, and the five parents that started the most ("bash · 1110 started 214 processes"), each
  opening that process's page, with "Show only these" to filter the table to its children.
  - Recording: `spectra spawns --json --interval 250ms --duration 10s` in rounds on a thread of
    its own, each with a 30 s deadline, with a 1 s rest between rounds, **only while the page is
    open** (a process page opened over it counts as leaving). "Keep recording" (off at start)
    keeps it going on every other page, checking once a second (`--interval 1s`) because looking
    every 250 ms costs close to half a core; it uses some CPU. Leaving the page or quitting stops
    the round in flight. A process that starts and ends between two checks (250 ms on the page,
    1 s in the background) can be missed, as can one in the rest between rounds; the page says
    so under its header. Lifetimes are approximate because start times have 1 s resolution. The
    latest 5000 events are kept.
  - Spectra Desktop's own readings (what it starts to look at the machine) are hidden from the
    table, the last-minute summary and the storm finding; "Show Spectra Desktop's own readings"
    (off) brings them back. "Short-lived only" matches a start to its exit by pid and start
    time, so a reused pid doesn't borrow another process's lifetime. A pid links to its process
    page only when the process list has that pid under the same name.
  - A failed round says why and the next starts 10 s later. An older Spectra without
    `spectra spawns` gets a plain notice and is asked again a minute later.
  - A finding on the Overview when one parent starts more than 100 processes in a minute (a
    spawn storm), opening Activity filtered to that parent. The finding appears only while
    recording is on (the page open, or Keep recording): with both off nothing is watching, so
    there is nothing to find.
- **Network**, under Activity: which processes are moving bytes right now, and what is
  listening. A line of the network's state on top: interface and gateway, VPN, proxy and DNS.
  "Using the network": the processes that moved the most over the last 5 s, most first, each with
  bytes in and out and the average rate, and up to three remote endpoints ("new" marks one that
  started moving bytes during the window). "Listening": every listening port with its protocol,
  bound address and process, those open on all interfaces (`*`, `0.0.0.0`, `::`) first and
  flagged, since other devices on your network may reach them. A process opens its page only
  when the process list has that pid under the same name (allowing for the names Spectra cuts
  short), so a reused pid opens nothing.
  - Measuring: `spectra network top --json --duration 5s --interval 1s --top 15` on a thread of
    its own with a 25 s deadline and a 4 MB output cap, then a 5 s rest (a window keeps about one
    core busy), **only while the page is open**; leaving it (or opening a process page over it)
    or quitting stops the window in flight. A failed window keeps the last ranking, says why and
    tries again 15 s later; an older Spectra without `network top` gets a plain notice and the
    listening ports still show.
  - The state line and the listening ports come from the Machine page's own `spectra network
    --json` reading every 30 s, so the page runs nothing extra for them.
- **Startup**, under Machine: everything that launches at login or boot, and the background
  services launchd runs, so you can see what runs without you asking. Grouped by the app it
  belongs to (an app's login item, agents, daemons and helpers together), then items no app
  owns, then other background services. Each row: name, kind (login item, launch agent, launch
  daemon, background service), when it starts (at login, at boot, kept alive, on an interval, on
  a schedule, on demand), whether it runs and its pid (opening the process's page when the list
  has it), Disabled, and the program's path. Search by name, label, app, path or pid; "Running
  now" keeps what runs; "Not from Apple" (on at first) hides Apple's items, and turning it off
  reads them too. Over 200 rows, the rest are counted and the search narrows them.
  - Read when the page opens and on Refresh: `spectra startup list --json --scope all` (with
    `--include-apple` once Apple's items are shown) and `spectra services --json --domain all`,
    off the UI thread with a 60 s deadline. A failed read says why with Try again, and a failed
    refresh keeps the list from before. Sources Spectra couldn't read and unreadable plists are
    named; a failed services read leaves the startup items and says so.
  - Disable and Enable, for your own launch agents only (`~/Library/LaunchAgents`, not Apple's,
    not a label two plists share): only on a press, behind a confirmation that names the item
    and its label and says what changes. Disabling stops it starting at login or on its own until
    you enable it, and Spectra also unloads it, so if it's loaded it's unloaded and if it's
    running it stops. Enabling says it will start at your next login, and that it starts now only
    when it isn't loaded. Cancel has focus, Esc cancels, and one press sends one
    `spectra startup disable|enable --json --scope user -- <label>`. Buttons stay off until the
    re-read shows the result, and quitting waits a moment for a change, then leaves it to finish
    rather than stopping it half done.
    The outcome is Spectra's own: done, refused (and why), or didn't take, and the list is read
    again. System items say "Needs an administrator; Spectra Desktop doesn't run sudo"; login
    items say to use System Settings › General › Login Items.
- **Storage**, under Startup: where disk space went. Read-only: nothing on it deletes, moves or
  writes anything.
  - Free space on each disk you keep files on, from the Machine page's own disk reading (never
    read twice), with a bar and a warning when a disk is low: under 20 GB free, or as full as the
    Machine page's full-disk finding. The Machine page has the full used/free figures.
  - The biggest folders in ~/Library, only on a press of Measure ~/Library:
    `spectra du --json --depth 1 --top 15 -- <path>`. Sizing walks every file and took over five
    minutes for a full ~/Library on a busy Mac, so it never runs on its own; the page says how
    long it has been going and Stop ends it. Each folder has Look inside, which sizes that folder
    the same way and lists its folders (each with Look inside again) and its biggest files; Back
    walks out. Look inside your home folder starts from `~`. One measurement runs at a time, with
    a 15 min deadline; a timeout suggests a smaller folder. Each result is kept, with how long
    ago it was measured, until Measure again; a failed re-measure keeps the sizes from before.
    Folders macOS wouldn't let Spectra open are counted and said.
  - Time Machine's local snapshots (`spectra timemachine --json`, read each time the page opens):
    when each was taken and its disk, and a backup being written now. Spectra doesn't report a
    snapshot's size, and the page says so.
  - Leaving the page or quitting stops a measurement or read on its way.
- **Compare**, under Storage: what's different from when it was fine. It lists the snapshots and
  baselines Spectra has stored (`spectra snapshot list --json`, read when the page opens, newest
  first), and you pick an earlier one with From, optionally a later one with To (Now by default),
  then press Compare: `spectra snapshot diff --json <earlier> <later|live>`. Against Now, Spectra
  inspects every app first, which can take a minute; the page says how long it has been going and
  Stop ends it. The differences come back in plain words, grouped by area, each group with its
  counts and its first eight lines (Show all for the rest): apps added, removed, updated or with
  a changed signature; Java installs; Homebrew packages; active language versions; listening
  ports; VPN; resources in use (terminal sessions, open files, processes); the Mac itself (macOS
  version, memory); PATH; kernel settings; and software installs. Areas with no change are left
  out. Spectra's diff does not include login items or background services, and the page says
  so; the Startup page shows those as they are now.
  - Take a snapshot now and Take a baseline run `spectra snapshot create` (with `--baseline` for
    the second) only on a press, and the page says where it went: Spectra's own store,
    `~/.spectra/spectra.db`, as the id it printed. Spectra keeps the newest 100 snapshots and
    never prunes a baseline. Spectra's snapshot code asks for no sudo; if it is refused for lack of access the
    page says so, and the app still never runs sudo. A snapshot is left to finish if you leave the
    page, since stopping it half-way could leave a partly written one.
  - No daemon method exists for these yet, so they run through the same seam as every other
    call, which runs the CLI, with a deadline (30 s for the list, 5 min for a diff or snapshot) and
    a 4 MB output cap, off the UI thread. While the window is hidden nothing is read.
- **Memory and alerts**, under Machine: two sections, read when the page opens and every 30 s
  while it is on screen and the window is visible (nothing while hidden or on another page).
  - *Memory jumps*, from `spectra anomalies --json` (Spectra has no daemon method for it, so the
    seam runs the CLI): processes whose memory is above their rolling baseline, each as "now X,
    usually Y" with a plain phrase (above, well above, far above its usual) and a link to its
    process page. Spectra compares the latest reading with a baseline, so this finds sudden
    jumps, not slow creep, and the page never calls a row "growing". Processes that have ended,
    drops, and rises under 1 MB are left out. With no stored history yet Spectra exits with an
    error; the page says it hasn't stored enough history, which is neither an error nor "nothing
    is jumping".
  - *Alerts*, from the daemon's `alerts.list` (through the seam, like every Spectra call), newest
    first, active, acknowledged or cleared, with Spectra's detail in plain words. When the daemon
    isn't available the page says so. An active alert has an Acknowledge button: it calls
    `alerts.ack` only when pressed, never retries it through the CLI, and says that it worked or
    why it didn't. Acknowledging doesn't clear the alert.
- **All processes**, then by kind and by runtime in the sidebar, with live counts. Sort by CPU,
  memory or name; search by name, pid, bundle id or path. The table only builds the rows on
  screen, so the full list stays quick. Rows group an app with its helpers, and processes of one
  name running one executable; two `java` from different JDKs stay apart, each with the folder
  that tells them apart beside its name. Clicking a row or arrowing onto it only moves the
  cursor. Return or a double click opens a process's page or an app's page; on any other group
  (twelve `zsh`) it opens or closes the group. The triangle always only opens or closes it.
- **Tree view**: Grouped | Tree over the list switches to who started whom, and the choice stays
  while the app runs. launchd's children (and any process whose parent isn't in the list) are
  the roots, so an app, a terminal or a daemon each heads its own branch; launchd is a row of its
  own. Each row shows its own CPU and memory, and "With children" what its whole branch uses.
  Siblings sort by their branch's totals, so a quiet shell running a busy build rises with it.
  Branches open with the triangle or Right, and close with Left (which then steps to the
  parent); what's open stays open from reading to reading while those processes run. A search
  keeps the matches and every process above them, opened down to each match. Return or a double
  click opens the process's page. Only the rows on screen are built, as in the grouped list.
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
  - JVM, first on a JVM's page and nowhere else. They attach to the JVM, so they're off on
    another user's JVMs, with the reason.
    - Thread dump (`spectra jvm thread-dump --json <pid>`): threads by state (runnable, blocked,
      waiting, timed waiting); a deadlock comes first, in red, naming its threads and who waits
      for which lock held by whom. Threads with the same stack are one row with a count; press it
      for the frames. Filter by name and by state.
    - Heap histogram (`spectra jvm heap-histogram --json <pid>`): classes by bytes and by
      objects, and Spectra's leak suspects. The page keeps the last histogram; the next one shows
      how much each class grew or shrank since.
    - GC stats (`spectra jvm gc-stats --json <pid>`): young, full and concurrent collections
      with count, total and average pause, and how full eden, survivors, old, metaspace and
      class space are.
    - Native memory (`spectra jvm nmt --json <pid>` and `spectra jvm vm-memory --json <pid>`):
      categories by committed memory, and the heap as the JVM describes it. When native memory
      tracking is off it says so, with Spectra's reason and the flag to restart with.
    - Explain (`spectra jvm explain --json <pid>`): Spectra's findings, worst first, each with
      its evidence and what to do.
    - Heap dump (`spectra jvm heap-dump --out <file> <pid>`), only on press: written to
      `~/Downloads/<name>-<pid>-<UTC time>.hprof` (the temp folder when there is no Downloads),
      and the page shows where. Then Leak suspects (`spectra jvm heap-hprof --json
      --leak-suspects <file>`) and Dominators (`spectra jvm dominators --json <file>`), each on
      press.
    - Flight recording: Start recording runs `spectra jvm jfr start --name spectra-desktop
      <pid>`; Stop and analyze runs `jfr dump --out ~/Downloads/<name>-<pid>-<UTC time>.jfr`,
      `jfr stop` and `jfr summary --json <file>`, and shows what happened most. Stop runs even
      when the dump fails. Leaving the page, or quitting the app, stops the recording and keeps
      nothing.
- **Logs** on a process page and an app page (its main process): recent entries it wrote to the
  unified log, from `spectra logs --json --process <name> --last <window> [--level <min>] --top
  2000`, only when you press Show logs. Pick how far back (5 min, 15 min, 1 h) and the lowest
  level (All, Error, Fault); picking again reads again. Rows show the time, the level, the
  subsystem and category (else the library that logged it) and the message, newest first;
  errors (amber) and faults (red, marked with a cross) stand out. A filter narrows them by
  message, subsystem or level.
  - It runs apart from the tools, with a 30 s deadline, an 8 MB output cap and Cancel. The page
    keeps the newest 500 entries. Spectra keeps the earliest 2,000 of a window; when a window
    holds more, the page says so and suggests a shorter window or Error.
  - The log knows processes by name. When several running processes share the page's name (five
    `zsh`), the page says so and keeps only its own pid's entries, counting the rest.
  - It says plainly when there are no entries, and when the query ran out of time.
  - Follow reads the last minute every 5 s while the page is open and adds what's newer at the
    top. Leaving the page or quitting stops it; it never reads in the background. A round that
    fails turns Follow off and says why.
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
                            # tool-memtrace, tool-lineage, tool-owner, tool-runtime, tool-failed,
                            # jvm-tools, tool-threads, tool-threads-deadlock, tool-histogram,
                            # tool-gc, tool-nmt, tool-nmt-off, tool-explain, tool-heapdump,
                            # tool-jfr, tool-jfr-summary,
                            # logs, logs-empty, logs-timeout,
                            # system, system-pressure, system-failed, system-linux,
                            # activity, activity-storm, activity-unsupported, startup,
                            # startup-confirm, startup-failed, tree, tree-search,
                            # storage, storage-measuring, storage-inside, storage-failed,
                            # compare, compare-list, compare-diffing, compare-empty,
                            # compare-failed, compare-no-list
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
  and their JSON; `src/jvm_tools.rs`: a JVM's tools' JSON and the sums made from it (histogram
  growth, thread groups), with fixtures captured from a real JVM in `tests/fixtures/jvm`.
- `src/logs.rs`: a page's Logs: the `spectra logs` arguments, its JSON (a fixture captured
  from Finder in `tests/fixtures/logs`), the level and name filters, and Follow;
  `src/sample_logs.rs`: its sample data.
- `src/system.rs`: the machine-wide readings, their threads, parsing and history;
  `src/system_findings.rs`: the rules that turn them into findings; `src/sample_system.rs`:
  their sample data.
- `src/startup.rs`: the Startup page's reads and changes, their JSON, grouping and filters,
  behind a `Runner` trait that tests replace with a fake that records what it was asked;
  `src/sample_startup.rs`: its sample data, from fixtures captured on a Mac in
  `tests/fixtures/startup`.
- `src/storage.rs`: the Storage page's `spectra du` and `spectra timemachine` runs, their JSON,
  the low-space rule and the measurement cache, behind a `Runner` trait that tests replace with
  a fake; `src/sample_storage.rs`: its sample data, from `tests/fixtures/storage`.
- `src/compare.rs`: the Compare page's snapshot list, diff and snapshot runs, their JSON, and the
  diff put into grouped plain words, behind the same `Runner` trait; `src/sample_compare.rs`: its
  sample data, from `tests/fixtures/compare`.
- `src/growth.rs`: the Memory and alerts page's reads and its one write (acknowledge), their
  JSON, the rows and the 30 s schedule, behind the `Spectra` trait that tests replace with a
  fake; `src/sample_growth.rs`: its sample data (invented), with the shapes in
  `tests/fixtures/growth`.
- `src/spawns.rs`: the Activity page's recording rounds, their JSON, the event buffer, the
  last-minute summary and the spawn-storm rule; `src/sample_activity.rs`: its sample data.
- `src/tree.rs`: the Tree view: building the parent/child tree, branch totals, search and
  flattening it into rows; `src/sample_tree.rs`: its sample data.
- `src/app.rs`: state and each process's recent history; `src/main.rs`: the polling threads.
- `src/sample.rs` and `src/sample_jvm.rs`: sample data for the headless scenes and tests.
- `src/ui/`: the view, one module per page.

Rune is pre-release and pinned to a `dev` rev in `Cargo.toml`; the `[patch.crates-io]` revs must
match it.
