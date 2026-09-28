# Local daemon

Spectra has a local, per-user daemon over a Unix socket; no network listener;
cross-machine access remains in Spectra Remote. The daemon currently provides
lifecycle and status RPCs. Diagnostic data integration and background monitoring
will follow in separate changes.

```bash
spectra daemon start
spectra daemon status --json
spectra daemon logs -n 50
spectra daemon stop
```

`spectra daemon run` is the sole foreground server entry point. `start` launches
that command in a detached session. `install` configures a macOS LaunchAgent or
Linux systemd user service to launch the same command. `uninstall` removes the
service, and `print-plist` prints the macOS agent configuration without installing
it. `run --idle-timeout 10m` can stop an unmanaged daemon after inactivity; the
default is no idle timeout.

The daemon keeps its socket, lock, and PID file in `~/.spectra` on macOS. On
Linux, it uses `$XDG_RUNTIME_DIR/spectra` when available and falls back to
`~/.spectra`. `SPECTRA_DAEMON_DIR` overrides the socket directory. The socket
directory is mode 0700 and the socket is mode 0600. Each connection is checked
against the kernel's peer UID before a request is accepted.
`SPECTRA_DAEMON_LOG` overrides the log file location for isolated runs.
`daemon stop` asks a reachable daemon to shut down over the socket. If the
socket is unavailable, it checks the lock before using the PID file. A free
lock means no daemon is running; stop removes stale PID and socket files and
does not signal that PID. A held lock permits a bounded SIGTERM fallback.

The transport is newline-delimited JSON-RPC 2.0. `daemon.status` reports the
protocol, version, PID, start time, socket, and methods. `daemon.shutdown`
responds before stopping. A persistent connection can receive server-to-client
notifications for future subscriptions.
