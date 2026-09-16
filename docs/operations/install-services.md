# Install services

Spectra's only optional background service is `spectra-helper`, a local
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

There is no `spectra install-daemon`; background remote operation is owned by
the separately distributed Spectra Remote agent.
