# Network endpoints

Spectra extracts the URL hostnames embedded in an app's binary and
`app.asar` (when present) and reports them as a deduplicated list. This
is opt-in via `--network` because scanning a multi-hundred-MB asar
takes a couple of seconds per app.

## What it finds

Apps almost universally embed URL string literals — API endpoints,
documentation links, telemetry hosts, OAuth providers, third-party
services — as plain ASCII in their compiled JS bundles or main
executable. Spectra finds them with a single regex:

```
(?i)https?://([a-zA-Z0-9._-]+\.[a-zA-Z]{2,})
```

Case-insensitive to catch the occasional `HTTP://` literal. Hosts are
lowercased and deduplicated.

## Where it scans

Per app:

1. The main executable.
2. `Contents/Resources/app.asar` (Electron only).

Native modules (`.node` files) are not scanned — they rarely contain
URL literals worth extracting and add noise from bundled C library
strings.

## Junk filter

A small set of structural-URL hosts (XML namespaces, schema URIs)
are filtered as noise:

- `www.w3.org`
- `www.apple.com`
- `developer.apple.com`
- `schemas.microsoft.com`
- `schemas.openxmlformats.org`
- `json-schema.org`

`www.google.com` is *kept* because its presence is meaningful
(OAuth, search integration).

## Empirical examples

### Claude — 111 unique hosts

Notable entries:
- `api.anthropic.com` (expected)
- `api-staging.anthropic.com` (development host shipped to production)
- `accounts.google.com` (Google OAuth)
- `api.crossref.org`, `api.elsevier.com` (academic search integrations)

### Codex — 89 unique hosts

Notable entries:
- `api.openai.com`
- `ab.chatgpt.com` (A/B testing)
- `api.statsigcdn.com` (Statsig feature flags)

### Warp — 258 unique hosts

Largest count seen: Warp ships shell completion data for many CLI tools
(GitHub, GitLab, npm, Docker registries, etc.), each contributing
distinct hosts.

### Tuple — 26 unique hosts

Mix of API hosts, Apple cert validation endpoints, and arXiv (likely
for documentation links).

## Why this is interesting

The list isn't an audit of what an app *talks to* — that requires live
network monitoring. It's an audit of what the app's *code references*.
Useful for:

- **Surprise endpoints.** "This vendored binary talks to `api.foo.com`?"
  Worth a closer look.
- **Telemetry / analytics inventory.** Every analytics SDK leaves its
  endpoint host in the bundle.
- **Staging/dev leakage.** Catching `api-staging.anthropic.com` in a
  production build, like above, is exactly this signal.
- **Supply-chain spot check.** Does the bundle reference hosts the app
  has no business contacting?

## Limitations

- **String literals only.** Hosts assembled at runtime from constants
  won't be found: `host = config.region + '.' + base` produces nothing
  scannable.
- **No path information.** Just hostnames. The URL paths are in the
  binary too but rarely meaningful in isolation.
- **No protocol audit.** `http://` and `https://` are treated the same
  in the output; the recommendation engine may flag plain-HTTP
  references separately later.
- **Bundle-only.** This isn't live network observation. For current live
  sockets use `spectra network connections` or the daemon's
  `network.connections` / `network.byApp` methods, which are backed by
  `lsof -i -P -n`. Per-process throughput via `nettop` is still future work.

## Implementation reference

`internal/detect/detect.go`:
- `scanNetworkEndpoints(appPath, exe) []string`
- Streamed reader with overlapping window (256-byte tail) so URLs
  spanning chunk boundaries aren't missed.
- Deduped via map, then sorted for stable output.

Related live-network collectors:
- `internal/netstate/connections.go` — `lsof -i -P -n` socket list.
- `internal/netstate/netstate.go` — routes, DNS, proxy config, VPN state,
  listening ports, and connection counts.

## CLI usage

```bash
spectra -v --network /Applications/Slack.app
```

The `hosts (N): ...` line appears only in verbose mode. JSON output
always includes `NetworkEndpoints` when `--network` is set.
