# Running processes

Spectra cross-references currently-running processes against the app
bundle and reports which ones belong to it. Live-state companion to the
otherwise static inspection.

## Source

`ps -axwwo pid=,rss=,lstart=,comm=` for per-app attribution, and
`ps -axwwo pid=,ppid=,pcpu=,rss=,vsz=,uid=,user=,lstart=,command=`
for the full process subcommand. Equals signs suppress the column
headers; `axww` gets every process with full unwrapped command lines.
For full process inventory, Darwin builds with cgo enrich those rows with
`libproc` calls (`proc_pidinfo(PROC_PIDTASKALLINFO)` and `proc_pidpath`)
to collect thread counts, BSD process names, and executable paths without
extra forks. In that collector, a process is attributed to the app when
its libproc executable path, falling back to its command line, starts
with `<app-path>/`.

For each match Spectra captures:

- **PID** — process identifier
- **RSS (KiB)** — resident set size
- **CPU%** — point-in-time `pcpu` from `ps` in `spectra process`
- **Start time** — parsed from `lstart`
- **Command** — bundle-relative path (so `Contents/MacOS/Slack Helper`
  rather than the full `/Applications/Slack.app/...`)
- **Executable path** — direct `proc_pidpath` value when visible
- **BSD name** — direct `p_comm` value when visible

For app-level inspection, Spectra also computes `AppStartedAt` from the
oldest matching process and `AppUptimeSeconds` from that timestamp to the
inspection time.

With `spectra process --deep`, Spectra also runs one batched
`lsof -p <pid1,pid2,...>` call to collect:

- **Open file descriptors** — counted from numeric FD rows
- **Listening TCP ports** — parsed from `(LISTEN)` socket rows
- **Outbound TCP connections** — remote `host:port` values

## Sample output

```
Claude
  running: 35 processes, 2.4 GB RSS, uptime 3h42m
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
],
"AppStartedAt": "2026-05-06T08:18:00Z",
"AppUptimeSeconds": 13320
```

## Why this matters

- **Live correlation.** Static inspection tells you what an app *can*
  do; the process list tells you whether it's *currently doing* it.
- **Bundle-grouped view.** Activity Monitor's flat process list shows
  35 Claude Helper processes. Spectra collapses them to one row with
  a count and total RSS — much easier to scan.
- **Snapshot foundation.** This collector also supplies the point-in-time
  process data stored in local snapshots.

## Limitations

- **Apparent RSS, not unique.** Shared library pages count toward each
  process's RSS but only exist once on disk. The 2.4 GB total for
  Claude is overcounted; real OS-level memory pressure is lower.
  Activity Monitor has the same issue.
- **CPU% is point-in-time.** It is useful for "what is hot right now,"
  but trends require the daemon metrics ring buffer.
- **Thread counts are best effort.** macOS `ps` does not expose
  Linux-style `nlwp`/`thcount` fields on this host, so Spectra fills
  `thread_count` with `proc_pidinfo(PROC_PIDTASKALLINFO)` on Darwin when
  the process is visible to the current user. Processes that cannot be
  queried keep `thread_count: 0`.

## Process families

Many small processes can saturate a machine without any single one looking
heavy (for example three booted iOS simulators with hundreds of children
each). `spectra process --group-by family [--top N] [--json]` sums CPU per
family instead of listing processes. Families:

- **simulator** - one per device, keyed by the UDID in the
  `CoreSimulator/Devices/<UDID>` path of `launchd_sim` (its arguments) or of a
  descendant's executable (arguments of other processes are ignored);
  processes without the path inherit the family by walking the parent chain.
- **xcode** - `xcodebuild`, `XCBBuildService`, `SourceKitService`, anything under
  `Xcode.app`, and any descendant of those that no other family claims.
- **emulator** - `emulator` and `qemu-*`, plus any descendant that no other
  family claims.
- **build** - `make`, `ninja`, `cargo`, `clang`, Gradle/Kotlin daemons, etc.
- **browser** - one family per browser app.

Anything else is grouped by command name under kind `other`, which is shown by
`--group-by family` but never raised as a `whatswrong` finding. Each family
reports member count, CPU, share of all process CPU, RSS, the age of its oldest
member (so "steady for 3h" reads differently from a boot burst), and the
heaviest members. Parent chains with missing parents or cycles stop the walk.
The logic is pure and lives in `internal/process/family.go`.

Matching rules: a process in a simulator device (own UDID path, or a
`launchd_sim` ancestor) always stays in that device's family, even if it is
MobileSafari or a runtime hosted inside `Xcode.app`. A `launchd_sim` with no
UDID is keyed by its PID so devices never merge. Browsers match on the app
bundle or binary name only, never on arguments or directory names. Compiler
tools (`clang`, `ld`, ...) join the build family only under a build driver
(`make`, `ninja`, `gradle`, `cargo`, `bazel`, ...). A parent that started after
its child is ignored as PID reuse.

`whatswrong` raises a finding for simulator and emulator families at 100% CPU
or more (high at 200%). Browser, build and Xcode families need 400% or 50% of
machine CPU and stay at medium. Titles read "N% of machine CPU" (family CPU
divided by logical cores x 100); if the core count is unknown they fall back
to the family's share of all process CPU. This is separate from the
hostwatch "simulator process count high" alert, which counts processes and is
unchanged.

### Family JSON

`spectra process --group-by family --json` prints an array; `spectra whatswrong
--json` carries the multi-process ones under `families`. Each entry:

| Field | Meaning |
| --- | --- |
| `key`, `kind`, `name` | Stable id, kind (`simulator`, `xcode`, `emulator`, `build`, `browser`, `other`) and display name |
| `udid` | Simulator device UDID (simulator families only) |
| `members` | Process count |
| `cpu_pct` | Summed CPU in percent of one core (250 = 2.5 cores busy) |
| `share_pct` | Share of CPU used by all listed processes (not of machine capacity) |
| `rss_kib` | Summed RSS in KiB |
| `started_at` | Earliest member start (RFC 3339); omitted when unknown |
| `age_seconds` | Age of the oldest member in seconds; omitted when unknown |
| `heaviest` | Top members: `pid`, `command`, `cpu_pct`, `age_seconds` |

`--group-by family` cannot be combined with `--sort`, `--tree`, `--ppid`,
`--user`, `--min-rss`, `--min-vsz`, `--min-elapsed` or `--classify` (exit
status 2): filters would remove ancestors needed for attribution.

## Process classification

`spectra process --classify` gives every listed process (after `--top`)
a role, a runtime, its owning app and, on macOS, a code-signing summary,
so a desktop client can group processes the way Activity Monitor does.
Without the flag none of this runs and the output is unchanged.

| JSON field | Values |
|---|---|
| `role` | `app`, `app_helper`, `system`, `shell`, `terminal`, `ai_agent`, `cli` |
| `runtime` | `electron`, `native`, `rust`, `go`, `jvm`, `node`, `bun`, `deno`, `python`, `ruby`, `shell`, `unknown` |
| `app` | `{name, bundle_path, bundle_id}` for the outermost owning `.app` (macOS) |
| `signing` | macOS only: `{status, team_id, hardened_runtime}`, status `apple`, `developer_id`, `adhoc`, `unsigned`, `invalid` or `unknown` |
| `evidence` | `["role: …", "runtime: …", "signing: …"]`, why each was chosen (the `signing` line on macOS only) |

Every path rule uses the kernel-reported executable: libproc on macOS,
`readlink /proc/<pid>/exe` on Linux (`executable_path`, with
`executable_deleted` when the kernel reports the file as deleted). argv[0]
is never used as a path, since any process can set it (`exec -a`). When the
executable is not visible (another user's process on Linux), the runtime,
shell, terminal and agent rules may match on the argv[0] name with evidence
marked "from argv[0], unverified"; app and system roles cannot match. On
Linux the binary is read through `/proc/<pid>/exe`, so a replaced or deleted
file still yields the code the process runs. Only regular files are opened,
without blocking (a FIFO named like an executable or `Info.plist` is
skipped), `Info.plist` reads stop at 1 MiB, and bundle reads run on the
worker pool so cancelling returns promptly.

**Runtime**, first match wins:

1. Interpreter or managed runtime by executable base name: `java`, `node`,
   `bun`, `deno`, `python*`, `ruby*`, and shells (`sh`, `bash`, `zsh`,
   `fish`, …) as `shell`.
2. `electron` for any executable inside an `.app` whose outermost bundle has
   `Contents/Frameworks/Electron Framework.framework`, helpers included.
3. `jvm` for the main executable of an app that bundles a Java runtime
   (`Contents/jbr` or `Contents/runtime`, as JetBrains IDEs and jpackage apps do).
4. The binary itself, read with `debug/macho` (host slice of a universal
   binary) or `debug/elf`: a `__BUN` segment / `.bun` section is `bun`
   (a `bun build --compile` executable) and a `__go_buildinfo` /
   `.go.buildinfo` section is `go`. A defined `std::rt::lang_start` symbol
   (legacy or v0 mangling), which only a Rust `main` calls, is `rust`.
   Otherwise `/rustc/` or `RUST_BACKTRACE` in read-only data (at most
   32 MiB scanned per file) shows Rust std is linked, which C++ programs
   with Rust libraries (`adb`, `netsimd`) also have: it is `rust` only for
   a stripped binary with no C++ runtime (no `std::` or `operator new`
   symbols, no `__gxx_personality_v0`, no libc++/libstdc++ link), and
   `native` with evidence "links Rust std" when the symbol table survives
   without `lang_start` or a C++ runtime is present. Anything else is
   `native`; an unreadable or unknown file is `unknown`.

**Role** precedence (documented in `internal/classify/role.go`):

1. `system` for Linux kernel threads.
2. `ai_agent`, from the agent table in `internal/classify/role.go`:
   - `claude`, `codex`, `ollama`, `aider`, `cursor-agent` and `opencode`
     match on the executable base name alone; so does Claude Code's native
     install, a file named after its version inside a `claude` directory
     (`~/.local/share/claude/versions/2.1.220`), matched by path shape.
   - `gemini`, `copilot`, `qwen`, `amp`, `crush` and `goose` are names other
     tools also use (pressly/goose is a database migration tool), so they
     need a confirming signal: the binary's Go module
     (`github.com/charmbracelet/crush`), the npm package of the script
     (`@google/gemini-cli`, `@github/copilot`, `@qwen-code/qwen-code`,
     `@sourcegraph/amp`, `@charmland/crush`), the owning bundle id
     (`com.block.goose`) or a known install path (`~/.amp/bin/`,
     `~/.local/bin/goose`). Without one they are `cli`.
   - A node/bun/deno/python interpreter running a script from any listed
     npm package, or a script/module named after an unambiguous agent, is
     `ai_agent` with the interpreter's runtime. A bin/ symlink to the script
     is followed to find its package. Options that take a value
     (`node --require x app.js`) are skipped, so the value is never taken
     for the script, and arguments are never substring-matched.
   - argv[0] counts only when the executable is not visible, and only for
     the unambiguous names.
3. `terminal` for Terminal, iTerm2, Ghostty, kitty, Alacritty, WezTerm, Warp,
   Hyper, tmux, screen, zellij and similar.
4. `shell` by executable.
5. `app` for an app's `CFBundleExecutable`, `app_helper` for anything else in
   it. Bundles under `/System/Library/` or `/Library/Apple/` are `system`;
   a `.app` inside a `.framework` (Homebrew's `Python.app`) and tools under
   `<app>/Contents/Developer/` (Xcode's toolchain) are not app processes.
6. `system` on macOS under `/System`, `/usr/libexec`, `/usr/sbin`, `/sbin`,
   or a validated Apple platform binary (a non-root `/bin` or `/usr/bin`
   tool stays `cli`); on Linux a uid 0 process under `/usr/lib/systemd`,
   `/usr/sbin` or `/sbin`.
7. `cli` otherwise.

**Signing** runs `codesign -dv --verbose=2 <exe>` once per unique
executable. That output displays what the signature claims without
validating it, so an Apple claim (a `Platform identifier` or a
"Software Signing" leaf) is checked with
`codesign --verify --ignore-resources -R="anchor apple" <exe>`: passing is
`apple` (and only then counts as a platform binary for the `system` role),
failing is `invalid`, and a codesign failure (timeout, cancel) is `unknown`
and not cached. The verify still checks the code pages and CodeDirectory
(a patched byte in either fails it) but not bundle resource seals, which
genuine Apple code such as Simulator runtimes, Safari's cryptex XPC
services and XProtect fails with "resource envelope is obsolete". Other statuses are taken from the display: a Developer ID, Mac App
Store or Apple Development leaf is `developer_id` with its displayed
`team_id`, `Signature=adhoc` is `adhoc`, "not signed at all" is
`unsigned`. The `signing:` evidence line says whether the verdict was
validated and that it describes the file on disk, which may no longer be
what a long-running process loaded. Linux never runs codesign and omits
`signing`.

The per-executable work is cached by (path, size, mtime, device, inode,
ctime) in memory and in the `classify` cache kind, so a second run skips
codesign and binary reads entirely, while a replacement that keeps size and
mtime (`cp -p`, `rsync`, `tar`) still misses. Cold classification runs on a
bounded worker pool. On a Mac with about 1,000 processes the first run
takes a few seconds and later runs add well under 100 ms. The table adds
`ROLE` and `RUNTIME` columns, plus `SIGN` on macOS.

## Implementation reference

Per-app attribution in `internal/detect/detect.go`:
- `scanRunningProcesses(appPath) []ProcessInfo`
- Filters `ps` output by command-path prefix.
- Single fork per inspection (not per process), so calling `Detect()`
  on many apps is O(N) forks of `ps`, not O(N×M).

Full process inventory in `internal/process/`:
- `CollectAll(ctx, opts)` — parses `ps` rows, including `pcpu`
- `collectProcessDetails(procs)` — Darwin `libproc` enrichment for
  thread counts, BSD process names, and executable paths
- `enrichDeep(procs, run)` — one batched `lsof -p` call for FD and
  socket detail when `--deep` is set

## Future ideas

- Continue moving fields from forked tools to `libproc`, especially
  descriptor metadata that can reduce `lsof` dependence for process-only
  views. Socket peer attribution still needs either richer direct socket
  inspection or the privileged helper path.
