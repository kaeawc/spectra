# Hive VMs

[Hive](https://hive.codes) runs each agent session in its own Linux VM. A VM
that sleeps for hours may be replaced, and only files under `/workspaces`
carry over. Processes do not. `scripts/hive-setup.sh` handles both: it installs
Spectra under `/workspaces/.spectra` and starts the [daemon](daemon.md) on
every run.

## Setup script

Run it at the start of every session, or from whatever runs at boot:

```bash
curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/hive-setup.sh \
  | sh -s -- --version v0.1.3
```

The script:

1. Skips the download when `/workspaces/.spectra/bin/spectra version` already
   prints the requested tag. Otherwise it runs that tag's
   [`install.sh`](install-binaries.md), which checks the archive's SHA-256 and
   the release signature.
2. Runs `spectra daemon start`. It needs no systemd and does nothing when the
   daemon is already running.
3. Prints the `spectra-mcp` path to register.

Pin `--version` so every VM runs the same build. With `latest`, each run looks
up the newest release. If that lookup fails, an existing install is kept.

| Option | Environment | Default |
| --- | --- | --- |
| `--version TAG` | `SPECTRA_VERSION` | `latest` |
| `--home DIR` | `SPECTRA_HOME` | `/workspaces/.spectra` |
| `--no-daemon` | | start the daemon |
| `--base-url URL` | `SPECTRA_BASE_URL` | GitHub releases |

## MCP server

Register `spectra-mcp` once for the Hive account, in Settings, Skills & MCP,
MCP, as a stdio server with the command `/workspaces/.spectra/bin/spectra-mcp`
and no arguments. The absolute path means it does not depend on `PATH`. A
repository's `.mcp.json` works too, but only for that repository.

`spectra-mcp` does not start the daemon. Without it, `alerts`, `host_health`
and `process` history return `daemon_unavailable`, and `load` and `idle` take a fresh
sample on every call instead of answering from the daemon's CPU tracker.

The daemon's socket and history live outside `/workspaces`, so a replaced VM
starts with an empty history.

## What works on Linux

Host load and its cause (`load --root /workspaces`), memory, cgroup limits,
leftover and idle processes, port owners, network, storage, and JVM and Gradle
daemons. `jvm` needs a JDK's `jcmd` and `jps` on `PATH`.

Process sampling, flamegraphs, energy impact, `.app` inspection, TCC, and
launchd are macOS-only and return `unsupported_platform` on Linux.
