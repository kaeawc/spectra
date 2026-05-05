# Caching

Spectra uses a versioned, hash-sharded blob cache for expensive or
large artifacts. The CLI initializes all cache stores at startup and
exposes shared `stats` / `clear` commands through a registry.

The cache layout follows the patterns proven out in
[krit](https://github.com/kaeawc/krit). See
[../design/storage.md](../design/storage.md) for the full storage stack.

## Layout

```
~/.cache/spectra/v1/
├── detect/
│   └── {hash[:2]}/{hash[2:]}
├── hprof/
│   └── {hash[:2]}/{hash[2:]}
├── jfr/
│   └── {hash[:2]}/{hash[2:]}
├── threads/
│   └── {hash[:2]}/{hash[2:]}
├── samples/
│   └── {hash[:2]}/{hash[2:]}
└── netcap/
    └── {hash[:2]}/{hash[2:]}
```

- `v1/` is a version segment. Bumping it invalidates every cache kind
  at once; old data becomes unreachable. Schema migrations are not
  needed.
- `{hash[:2]}/{hash[2:]}` is two-level sharding on a content hash.
  Keeps no directory above 256 entries.
- Cache paths are extensionless today. The kind directory names carry
  the semantic type; payload encoding is owned by each caller.

## Cache kinds

| Kind | Key | Notes |
|---|---|---|
| `detect` | hash of `Info.plist` + first 64 KiB of main exe | Detect() result for one app at one version; used by daemon/snapshot paths |
| `hprof` | content hash of dump file | `jcmd GC.heap_dump` output |
| `jfr` | content hash of recording | Java Flight Recorder file |
| `threads` | hash of `(pid, timestamp)` | Thread dump text |
| `samples` | hash of `(pid, timestamp)` | `sample <pid>` output |
| `netcap` | hash of capture metadata | Future: pcap recordings |

## Eviction

No automatic eviction in v1. Cache directories grow until the user
runs:

```bash
spectra cache clear              # nuke everything
spectra cache clear --kind hprof # nuke just heap dumps
spectra cache stats              # bytes, entries, last-write per kind
```

The unified registry pattern (lifted from
`krit/internal/cacheutil/registry.go`) means each new cache kind gets
these commands automatically without touching CLI code.

## Async writer

Cache writes don't block the collector. A bounded queue (workers +
queueSize) flushes blobs in the background; if the queue is full, the
caller falls back to a synchronous write rather than dropping data.
Counters track queued / completed / failed / bytes.

The async writer is implemented in `internal/cache/async_writer.go`.
Callers can still write synchronously when they need the artifact to be
present before returning.

## Why content hashing matters for detect

`Detect()` is deterministic enough for snapshot reuse given the same
`Info.plist` + main executable prefix. Hashing those inputs gives
implicit invalidation: when an app updates, its metadata or executable
bytes change, its hash changes, its old cache entry is unreachable. No
timestamps, no version comparisons, no migration logic.

This is the same pattern krit uses for its parse cache — the comment
in `krit/internal/scanner/parse_cache.go` is worth reading for the
rationale.

## Implementation reference

`internal/cache/` — modeled after krit's `internal/cacheutil/`:
- `registry.go` — `Register(name, ClearFunc, StatsFunc)`
- `sharded.go` — two-level hash store
- `kinds.go` — well-known cache kind constants and store registration
- `async_writer.go` — bounded-queue background flushes

Related call sites:
- `cmd/spectra/cache.go` — `spectra cache stats` and `cache clear`
- `internal/snapshot/detect_cache.go` — Detect() result reuse
- `cmd/spectra/jvm.go` / `cmd/spectra/sample.go` — artifact caching

## Empirical thresholds

The krit precedent worth following: when a caching threshold matters
(don't cache below this size, don't keep more than this many
entries), document the benchmark inline.

```go
// Files below this threshold parse in under a millisecond; the gob
// serialization + filesystem round-trip dominates the savings.
parseCacheMinFileSize = 1024
```

Spectra should follow the same pattern when adding similar thresholds.
