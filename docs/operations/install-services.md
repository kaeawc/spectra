# Install services

Spectra has an optional per-user daemon and an optional `spectra-helper`, a local
privileged LaunchDaemon for root-only diagnostic data such as system TCC,
`powermetrics`, firewall rules, and bounded capture helpers.

```bash
make build-all
sudo spectra install-helper
spectra install-helper --status
```

The helper listens only on its local Unix socket and is not a remote-access
component. It remains optional: static inspection and user-owned live
diagnostics work without it.

To remove it:

```bash
sudo spectra install-helper uninstall
```

Install the local per-user daemon with `spectra daemon install` and remove it
with `spectra daemon uninstall`. On macOS this installs a LaunchAgent; on Linux
it installs a systemd user unit. Both run `spectra daemon run`. The daemon has
no network listener. Cross-machine operation remains in Spectra Remote.
