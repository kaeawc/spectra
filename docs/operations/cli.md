# CLI

`spectra` performs diagnostics on the local Mac.

```bash
spectra /Applications/Slack.app
spectra --json /Applications/Slack.app
spectra snapshot --baseline pre-incident
spectra diff baseline pre-incident live
spectra jvm
spectra network
spectra toolchain
```

The principal commands are `inspect`, `list`, `snapshot`, `diff`, `jvm`,
`network`, `power`, `storage`, `process`, `toolchain`, `rules`, `issues`, `alerts`,
`whatswrong`, and `playbook`. Run `spectra help` for the installed command list.

`spectra daemon start|stop|status|install|uninstall|print-plist|logs` manages
the local per-user daemon. `spectra daemon run` runs it in the foreground.

`spectra alerts [--state firing|resolved|all] [--limit N] [--json]` lists alerts.
`spectra alerts ack <id> [--json]` acknowledges one. `spectra alerts health
[--json]` summarizes load, memory, disk, processes, and active alerts.
`spectra alerts watch [--samples] [--json]` streams daemon notifications.
These commands require a running daemon. `spectra whatswrong [--json]` keeps its
local ranking and adds active daemon alerts and a five-minute trend when available.
Both commands accept `--no-daemon`, and `SPECTRA_NO_DAEMON=1` disables daemon
access globally.

Spectra has no `serve`, `connect`, `fan`, `hosts`, or `install-daemon`
commands. Cross-machine operations use the separately distributed Spectra
Remote project.
