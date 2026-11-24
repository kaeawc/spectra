# Hive VMs

[Hive](https://hive.codes) runs each agent session in its own Linux VM. A VM
that sleeps for hours may be replaced, and only files under `/workspaces`
carry over. Processes do not. `scripts/hive-setup.sh` handles both: it installs
Spectra under `/workspaces/.spectra` and starts the [daemon](daemon.md) on
every run.

## Setup script

Run it at the start of every session, or from whatever runs at boot:

```bash
curl -fsSL -o /tmp/hive-setup.sh \
  https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/hive-setup.sh &&
  sh /tmp/hive-setup.sh --version v0.1.3
```

Downloading to a file first means a failed download stops the command. Piped
into `sh`, a failed `curl` would leave `sh` with empty input, and the pipeline
would still succeed.

The script:

1. Skips the download when the installed `spectra` and `spectra-mcp` both
   report the requested tag, so a half-finished install is redone. Otherwise
   it downloads [`install.sh`](install-binaries.md) from that tag, wherever the
   setup script itself came from, and runs it. The installer always checks the
   archive's SHA-256 against the release's `checksums.txt`. It also checks the
   Ed25519 signature when the release has `spectra-release.json` and its
   `.sig`, the public key can be fetched, and `openssl` supports Ed25519. If
   any of those is missing, it prints a warning and installs after only the
   SHA-256 check.
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

`spectra-mcp` does not start the daemon. Without it, `alerts` and
`host_health` return `daemon_unavailable`, and `process` with
`operation: "history"` returns an error that says to start the daemon. `load`
and `idle` still work, but only from `spectra-mcp`'s own CPU readings, so the
first call takes a full sample.

The daemon's socket and history live outside `/workspaces`, so a replaced VM
starts with an empty history.

## What works on Linux

Host load and its cause (`load --root /workspaces`), memory, cgroup limits,
leftover and idle processes, port owners, network, storage, and JVM and Gradle
daemons. `jvm` needs a JDK's `jcmd` and `jps` on `PATH`.

Process sampling, flamegraphs, energy impact, `.app` inspection, TCC, and
launchd are macOS-only and return `unsupported_platform` on Linux.
