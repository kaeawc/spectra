# Quickstart

After [installing](install.md):

## Inspect one app

```bash
spectra /Applications/Slack.app
```

```
APP                           UI                        RUNTIME         PACKAGING   CONFIDENCE
------------------------------------------------------------------------------------------------
Slack                         Electron                  Node+Chromium               high
```

## Verbose: full diagnosis

```bash
spectra -v /Applications/Claude.app
```

Shows everything Spectra extracts: bundle ID, app version, Electron version,
architectures, code-sign team, hardened runtime, sandbox status, declared
entitlements, declared privacy descriptions, granted privacy permissions
(from TCC.db), third-party frameworks, embedded npm packages, helper apps,
XPC services, plugins, login items, currently-running processes with RSS,
and the storage footprint across `~/Library`.

## Scan everything

```bash
spectra --all              # /Applications + /Applications/Utilities
spectra --all -v           # with full per-app detail
```

100 apps in ~10 seconds on Apple Silicon thanks to the parallel worker pool.

## Network endpoints

```bash
spectra -v --network /Applications/Slack.app
```

Adds an `embedded URL hosts` line that lists every `https://...` reference
found in the main executable and `app.asar`. Slower because it scans the
full asar payload.

## JSON output

```bash
spectra --json /Applications/Cursor.app | jq '.[] | {name, UI, GrantedPermissions}'
```

Stable structured output for scripting, CI, or feeding into the
not-yet-built recommendations engine.

## Common workflows

| Goal | Command |
|---|---|
| What framework is this app? | `spectra APP.app` |
| Why is this app heavy? | `spectra -v APP.app` (look at storage + processes) |
| What can this app do to my machine? | `spectra -v APP.app` (entitlements + granted) |
| What hosts does this app talk to? | `spectra -v --network APP.app` |
| Is anything new on my machine since last week? | _planned: `spectra diff baseline`_ |
| What's running on my work Mac right now? | _planned: `spectra connect work-mac`_ |
