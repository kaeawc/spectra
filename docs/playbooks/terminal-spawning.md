# Terminal spawning

Use this when iTerm2, Terminal.app, VS Code, or an IDE terminal opens an empty
pane and immediately reports that the session ended. When there is no shell
output, the shell is often not the culprit. Another process may have exhausted
pty slots, file descriptors, or per-user process slots.

## Rule out shell startup

Before chasing system limits, confirm the login shell can start outside the
terminal UI:

```bash
time zsh -i -l -c true
```

If this hangs, prints shell errors, or exits non-zero, inspect `~/.zshenv`,
`~/.zprofile`, `~/.zshrc`, and sourced files first. If it succeeds quickly,
continue to system resources.

## Check system limits

Start with the machine-wide view:

```bash
spectra system limits
spectra system limits --top
```

Read the critical row first:

| Signal | Meaning |
|---|---|
| `pty slots` is `CRITICAL` | A process is holding too many pseudo-ttys |
| `open files` is `CRITICAL` | New shells may fail to open stdio or libraries |
| `processes/uid` is `CRITICAL` | A runaway process tree is consuming user process slots |

`--top` shows the processes most likely to explain the saturated resource.

## Find descriptor holders

When ptys or files are high, inspect live descriptor classes:

```bash
spectra process --deep --fd-breakdown --sort rss
spectra process --deep --json
```

Look for a process with a high `PTY` count outside the terminal app itself.
Electron, Chromium, IDE, and chat apps can run many helpers, but they should
not hold dozens of ptys unless they are actively hosting terminal sessions.

## Inspect the suspect

Use the suspect app path from the process table:

```bash
spectra -v /Applications/Claude.app
spectra connect local process-tree /Applications/Claude.app
spectra snapshot --baseline terminal-spawning-before
```

The app inspection confirms helper count, uptime, and live process ownership.
The process tree shows whether one parent is repeatedly spawning children. The
baseline gives you a before state to compare after remediation.

## Confirm recovery

Quit or restart the suspect app, then rerun:

```bash
spectra system limits
spectra diff baseline terminal-spawning-before live
```

If the critical resource drops below `WARN` and terminal sessions launch again,
the restarted app was holding the exhausted resource. If pressure remains high,
continue down the top-holder list or inspect processes owned by another user or
the system.

## Worked example

A terminal pane opens and immediately exits. `time zsh -i -l -c true` succeeds,
so shell startup is clean. `spectra system limits --top` reports `pty slots` as
critical and lists a long-running Electron helper as the top pty holder.
`spectra process --deep --fd-breakdown --sort rss` confirms that helper has a
large `PTY` count while iTerm2 has only the expected active sessions.

After saving a baseline, quitting the Electron app drops pty usage back below
the warning threshold. A new terminal session starts normally, and `spectra diff
baseline terminal-spawning-before live` preserves the before/after evidence.

## References

- [System limits](../inspection/system-limits.md)
- [Process profiling](../inspection/process-profiling.md)
- [Helpers and XPC](../inspection/helpers-and-xpc.md)
