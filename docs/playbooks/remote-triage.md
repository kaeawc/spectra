# Remote triage

Use this when a teammate's Mac is slow, failing a workflow, or behaving
differently from a known-good machine. Spectra's remote surface is explicit:
the target must run a daemon and the network path must be trusted.

## Establish the target

On the target Mac, run the daemon in the intended mode:

```bash
spectra serve --tcp 127.0.0.1:7878
spectra serve --tsnet
```

From the client:

```bash
spectra connect work-mac
spectra connect work-mac snapshot
```

Use loopback TCP for local testing, SSH/Tailscale for trusted remote access,
or the tsnet daemon mode when the host should appear directly on the tailnet.

## First pass

Collect the broad view before chasing one subsystem:

```bash
spectra connect work-mac processes
spectra connect work-mac network
spectra connect work-mac storage
spectra connect work-mac toolchains
spectra connect work-mac jvm
```

This separates machine-wide symptoms from app-specific symptoms.

## Compare against a baseline

Use snapshots and diffs when "works on my machine" is the problem:

```bash
spectra snapshot --baseline local-good
spectra connect work-mac snapshot
spectra diff local-good work-mac
```

For team-wide checks:

```bash
spectra fan --hosts alice-laptop,bob-laptop snapshot
spectra fan --hosts alice-laptop,bob-laptop toolchains
```

## Narrow by symptom

| Symptom | Remote command |
|---|---|
| Java app slow | `spectra connect work-mac jvm-explain <pid>` |
| Endpoint failing | `spectra connect work-mac network-by-app /Applications/App.app` |
| Disk full | `spectra connect work-mac storage /Applications/App.app` |
| Build differs | `spectra connect work-mac toolchains` |
| App differs | `spectra diff local-good work-mac --filter app=AppName` |

## References

- [Remote operations](../operations/remote.md)
- [Daemon operations](../operations/daemon.md)
- [Threat model](../design/threat-model.md)
