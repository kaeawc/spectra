# Remote diagnostics

Remote diagnostics are not part of the Spectra core binary. Use the separately
distributed Spectra Remote controller and target agent for cross-machine work.

The core `spectra` CLI neither opens a network listener nor contains Tailscale,
Nebula, SSH, or other remote-transport dependencies.
