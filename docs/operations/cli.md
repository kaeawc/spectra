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

`spectra whois [--json] [--sudo] [--duration 3s] <pid>` (macOS only) attributes a
busy XPC/Mach service to the processes driving it. It samples the service, reads
the PID from `com.apple.NSXPCConnection...anonymous.<pid>` dispatch queue names,
and resolves each client to its command line, parent chain, and launching tool
(for example `xcodebuild`) with its share of the sampled work. When the queue
names carry no PID it falls back to `lsmp` port ownership (`--sudo` for root).
`spectra whatswrong` adds the same attribution, such as `OSLogService (17874)
busy, driven by CtrlProxyUITests-Runner (17649), launched by xcodebuild (17491)`,
when the busiest process is over 80% CPU.

Spectra has no `serve`, `connect`, `fan`, `hosts`, or `install-daemon`
commands. Cross-machine operations use the separately distributed Spectra
Remote project.
