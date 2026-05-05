# Running processes

Spectra cross-references currently-running processes against the app
bundle and reports which ones belong to it. Live-state companion to the
otherwise static inspection.

## Source

`ps -axwwo pid=,rss=,lstart=,comm=` for per-app attribution, and
`ps -axwwo pid=,ppid=,pcpu=,rss=,vsz=,uid=,user=,lstart=,command=`
for the full process subcommand. Equals signs suppress the column
headers; `axww` gets every process with full unwrapped command lines.
A process is attributed to the app when its executable path starts
with `<app-path>/`.

For each match Spectra captures:

- **PID** — process identifier
- **RSS (KiB)** — resident set size
- **CPU%** — point-in-time `pcpu` from `ps` in `spectra process`
- **Start time** — parsed from `lstart`
- **Command** — bundle-relative path (so `Contents/MacOS/Slack Helper`
  rather than the full `/Applications/Slack.app/...`)

With `spectra process --deep`, Spectra also runs one batched
`lsof -p <pid1,pid2,...>` call to collect:

- **Open file descriptors** — counted from numeric FD rows
- **Listening TCP ports** — parsed from `(LISTEN)` socket rows
- **Outbound TCP connections** — remote `host:port` values

## Sample output

```
Claude
  running: 35 processes, 2.4 GB RSS
```

The 35 procs are Chromium's main + helper architecture: one main
Electron, one renderer per window, plus GPU + Plugin + Network helpers.
2.4 GB RSS includes shared pages, so it's not the actual memory cost
to the system, but it's the right number for "what does Activity
Monitor blame this app for."

```
Docker
  running: 9 processes, 923 MB RSS
```

Docker Desktop's main app + qemu/krunkit + various daemons.

```
JetBrains Toolbox
  running: 1 processes, 671 MB RSS
```

Single JVM process (the toolbox launcher itself).

## Verbose JSON output

The `RunningProcesses` field on the JSON result holds the full list:

```json
"RunningProcesses": [
  {"PID": 412, "RSSKiB": 184320, "Command": "Contents/MacOS/Claude"},
  {"PID": 415, "RSSKiB": 92160,  "Command": "Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper"},
  ...
]
```

## Why this matters

- **Live correlation.** Static inspection tells you what an app *can*
  do; the process list tells you whether it's *currently doing* it.
- **Bundle-grouped view.** Activity Monitor's flat process list shows
  35 Claude Helper processes. Spectra collapses them to one row with
  a count and total RSS — much easier to scan.
- **Foundation for the daemon.** When `spectra serve` lands, this same
  collector will run on a 1-second interval and produce the in-memory
  ring buffer described in [../design/storage.md](../design/storage.md).

## Limitations

- **Apparent RSS, not unique.** Shared library pages count toward each
  process's RSS but only exist once on disk. The 2.4 GB total for
  Claude is overcounted; real OS-level memory pressure is lower.
  Activity Monitor has the same issue.
- **CPU% is point-in-time.** It is useful for "what is hot right now,"
  but trends require the daemon metrics ring buffer.
- **No thread counts yet.** macOS `ps` does not expose Linux-style
  `nlwp`/`thcount` fields on this host. Thread counts are still a
  future `libproc`/`proc_pidinfo` collector.

## Implementation reference

Per-app attribution in `internal/detect/detect.go`:
- `scanRunningProcesses(appPath) []ProcessInfo`
- Filters `ps` output by command-path prefix.
- Single fork per inspection (not per process), so calling `Detect()`
  on many apps is O(N) forks of `ps`, not O(N×M).

Full process inventory in `internal/process/`:
- `CollectAll(ctx, opts)` — parses `ps` rows, including `pcpu`
- `enrichDeep(procs, run)` — one batched `lsof -p` call for FD and
  socket detail when `--deep` is set

## Future ideas

- Switch to `libproc` via cgo (or `process.NewProcess` from
  `gopsutil`) to get richer per-process data without forking.
- Add thread count via `proc_pidinfo`.
- Compute "app uptime" from the oldest matching process's start time.
