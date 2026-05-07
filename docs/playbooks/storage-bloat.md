# Storage bloat

Use this when disk is filling up, an app is unexpectedly large, or an app's
state is spread across `~/Library` locations that Activity Monitor does not
attribute clearly.

## Start with one app

Inspect the app and its per-location storage footprint:

```bash
spectra -v /Applications/Docker.app
spectra -v /Applications/Slack.app
```

For structured output:

```bash
spectra --json /Applications/Docker.app | jq '.[0].Storage'
```

## Check system storage

Use the storage command when the question is broader than one bundle:

```bash
spectra storage
spectra storage --json
```

If a baseline exists, compare before and after the incident:

```bash
spectra snapshot --baseline pre-incident
spectra diff baseline pre-incident live
```

## Read the result

| Location | Interpretation |
|---|---|
| `appsupport` | Durable application state, indexes, projects, downloaded models |
| `caches` | Usually disposable, but check app-specific cache safety first |
| `containers` | Sandboxed app state; may include everything for App Sandbox apps |
| `group containers` | Shared state between app, helpers, extensions, and login items |
| `http` / `webkit` | Web content state, cookies, HSTS, WebKit local storage |
| `logs` | Useful for incident evidence but can grow without rotation |

Sparse files are reported by allocated disk blocks on macOS, so virtual
disk images and similar files should not be inflated to their apparent size.

## Correlate with runtime

If the app is currently running, connect storage growth to live process
state:

```bash
spectra process
spectra -v /Applications/Docker.app
```

For remote machines:

```bash
spectra connect work-mac storage
spectra connect work-mac storage /Applications/Docker.app
```

## References

- [Storage footprint](../inspection/storage-footprint.md)
- [Storage design](../design/storage.md)
- [CLI storage commands](../operations/cli.md)
