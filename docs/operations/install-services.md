# Install services

Spectra can run two launchd-managed background services:

- `spectra serve` as a per-user LaunchAgent.
- `spectra-helper` as an optional root LaunchDaemon for root-only telemetry.

Install the daemon first for normal local or remote operation. Install the
helper only when the machine needs system TCC, `powermetrics`, firewall,
bounded `fs_usage`, or packet-capture data.

## Service model

| Service | Command | launchd domain | Runs as | Privilege |
|---|---|---|---|---|
| User daemon | `spectra install-daemon` | `gui/<uid>` | current user | unprivileged |
| Privileged helper | `sudo spectra install-helper` | `system` | root | optional elevated telemetry |

The user daemon owns the SQLite database, local Unix socket, optional TCP
listener, optional `tsnet` listener, and daemon logs. The helper is local
only. Remote clients never connect to the helper directly; they call the
user daemon, which proxies the small helper RPC surface when needed.

## Build prerequisites

From a source checkout:

```bash
make build-all
./spectra version
./spectra-helper --version
```

`spectra install-helper` expects `spectra-helper` to be next to the running
`spectra` binary. `make build-all` produces that layout in the repo root.

## Install the user daemon

```bash
spectra install-daemon
spectra status
spectra install-daemon status
```

The installer writes:

| Path | Purpose |
|---|---|
| `~/Library/LaunchAgents/dev.spectra.daemon.plist` | Per-user LaunchAgent |
| `~/Library/Logs/Spectra/daemon.launchd.out.log` | launchd stdout |
| `~/Library/Logs/Spectra/daemon.launchd.err.log` | launchd stderr |
| `~/Library/Logs/Spectra/daemon.jsonl` | structured daemon lifecycle log, unless disabled |
| `~/.spectra/sock` | default local JSON-RPC Unix socket |
| `~/.spectra/spectra.db` | default SQLite state |

After writing the plist atomically, Spectra runs:

```bash
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/dev.spectra.daemon.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.spectra.daemon.plist
launchctl enable gui/$(id -u)/dev.spectra.daemon
launchctl kickstart -k gui/$(id -u)/dev.spectra.daemon
```

The `bootout` step is best-effort so reinstalling updates the service in
place. `RunAtLoad` and `KeepAlive` are both enabled, so launchd starts the
daemon at login and restarts it after unexpected exits.

Use `--no-load` to inspect or deploy the plist without bootstrapping it:

```bash
spectra install-daemon --no-load
spectra install-daemon print-plist
```

## Daemon listener options

Flags passed to `install-daemon` are persisted into the LaunchAgent's
`ProgramArguments` and passed to `spectra serve` on each launch:

```bash
spectra install-daemon --sock ~/.spectra/sock
spectra install-daemon --tcp 127.0.0.1:7878
spectra install-daemon --tsnet --tsnet-hostname work-mac
spectra install-daemon --tsnet --tsnet-allow-logins alice@example.com
spectra install-daemon --log-file ~/Library/Logs/Spectra/daemon.jsonl
spectra install-daemon --no-log-file
```

Non-loopback TCP binds require `--allow-remote`:

```bash
spectra install-daemon --tcp 100.64.0.10:7878 --allow-remote
```

Prefer `--tsnet` for remote access. Plain TCP has no Spectra-layer
authentication and should only be exposed over a trusted path.

## Check daemon health

Use the Spectra client path first:

```bash
spectra status
spectra connect ~/.spectra/sock
spectra connect 127.0.0.1:7878
```

Use launchd when the process did not start or keeps restarting:

```bash
spectra install-daemon status
launchctl print gui/$(id -u)/dev.spectra.daemon
tail -n 50 ~/Library/Logs/Spectra/daemon.launchd.err.log
tail -n 50 ~/Library/Logs/Spectra/daemon.jsonl
```

Common failure modes:

| Symptom | Check |
|---|---|
| `spectra status` cannot connect | Is `~/.spectra/sock` present and owned by the user? |
| `launchctl print` reports repeated exits | Read `daemon.launchd.err.log` for flag or path errors. |
| TCP clients cannot connect | Confirm the plist includes `--tcp`; non-loopback needs `--allow-remote`. |
| `tsnet` has no IP | Check `daemon.jsonl` for the Tailscale login URL or auth-key errors. |

## Uninstall the user daemon

```bash
spectra install-daemon uninstall
```

This runs `launchctl bootout gui/<uid>` for the LaunchAgent and removes the
plist. It does not remove `~/.spectra/`, the SQLite database, logs, or cache
data.

## Install the privileged helper

```bash
make build-all
sudo spectra install-helper
spectra install-helper --status
```

The helper installer performs only fixed administrative operations:

1. Creates the `_spectra` group if needed.
2. Adds the invoking user to `_spectra`.
3. Creates `/Library/PrivilegedHelperTools/`.
4. Copies `spectra-helper` to
   `/Library/PrivilegedHelperTools/spectra-helper`.
5. Sets helper ownership to `root:wheel` and mode `0755`.
6. Writes `/Library/LaunchDaemons/dev.spectra.helper.plist`.
7. Writes `/etc/newsyslog.d/spectra-helper.conf`.
8. Loads the LaunchDaemon with `launchctl load -w`.

The installer invokes `sudo` only for an allowlisted command set:
`chmod`, `chown`, `cp`, `dseditgroup`, `launchctl`, `mkdir`, and `rm`.
The helper itself exposes structured RPC methods; it is not an arbitrary
root command runner.

On first install, log out and back in so the user's new `_spectra` group
membership is visible to shells and long-running processes.

## Helper launchd behavior

The helper plist uses:

- Label: `dev.spectra.helper`
- Program: `/Library/PrivilegedHelperTools/spectra-helper`
- `RunAtLoad`: enabled
- `KeepAlive`: enabled
- stderr: `/var/log/spectra-helper.log`

At startup the helper creates `/var/run/spectra-helper.sock` as
`root:_spectra` with `0660` permissions. The unprivileged daemon and CLI
helper client use that socket for local JSON-RPC. Full Disk Access is still
required for system TCC database queries:

```text
System Settings -> Privacy & Security -> Full Disk Access -> add spectra-helper
```

## Check helper health

```bash
spectra install-helper --status
launchctl print system/dev.spectra.helper
ls -l /var/run/spectra-helper.sock
tail -n 50 /var/log/spectra-helper.log
```

The status command checks the helper socket and calls `helper.health`.
If the socket is unreachable, check the LaunchDaemon state and stderr log.
If system TCC queries fail but `helper.health` works, verify Full Disk
Access for the installed helper binary.

## Uninstall the helper

```bash
sudo spectra install-helper uninstall
```

This unloads the LaunchDaemon and removes:

- `/Library/LaunchDaemons/dev.spectra.helper.plist`
- `/etc/newsyslog.d/spectra-helper.conf`
- `/Library/PrivilegedHelperTools/spectra-helper`

It does not remove the `_spectra` group, group membership, or historical
`/var/log/spectra-helper.log` rotations.

## Sudo boundary

Only helper installation and uninstallation require administrator
privilege. The user daemon never runs as root. Normal inspection, local RPC,
remote `tsnet` access, snapshots, metrics, and cache management stay in the
user account.

When helper-backed data is requested and the helper is not available,
Spectra reports that the privileged helper is not running and points to:

```bash
sudo spectra install-helper
```

The helper remains additive. Machines without it still support static app
inspection, user-owned process data, user TCC reads, local daemon storage,
remote daemon access, and normal CLI workflows.

## Related docs

- [Daemon mode](daemon.md) — RPC surfaces, logs, and daemon behavior.
- [Remote operation](remote.md) — remote access patterns.
- [Privileged helper design](../design/privileged-helper.md) — helper RPC,
  authentication, audit logging, and threat boundary.
- [Distribution](../design/distribution.md) — source, Homebrew, and future
  signed packaging plan.
