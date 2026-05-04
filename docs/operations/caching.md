# Caching

> **Status: planned.** Documents the cache layout the daemon will use.
> Today's CLI does no caching — every invocation re-runs every collector.

The cache layout follows the patterns proven out in
[krit](https://github.com/kaeawc/krit). See
[../design/storage.md](../design/storage.md) for the full storage stack.

## Layout

```
~/.cache/spectra/v1/
├── detect/
│   └── {hash[:2]}/{hash[2:]}.json.zst
├── hprof/
│   └── {hash[:2]}/{hash[2:]}.hprof
├── jfr/
│   └── {hash[:2]}/{hash[2:]}.jfr
├── threads/
│   └── {hash[:2]}/{hash[2:]}.txt
├── samples/
│   └── {hash[:2]}/{hash[2:]}.txt
└── netcap/
    └── {hash[:2]}/{hash[2:]}.pcap
```

- `v1/` is a version segment. Bumping it invalidates every cache kind
  at once; old data becomes unreachable. Schema migrations are not
  needed.
- `{hash[:2]}/{hash[2:]}` is two-level sharding on a content hash.
  Keeps no directory above 256 entries.
- File extensions reflect kind-specific encoding (zstd-compressed JSON
  for detect results; raw .hprof / .jfr for JVM artifacts).

## Cache kinds

| Kind | Key | Notes |
|---|---|---|
| `detect` | hash of `Info.plist` + main exe | Detect() result for one app at one version |
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

## Why content hashing matters for detect

`Detect()` is deterministic given the same `Info.plist` + main
executable bytes. Hashing those two as the cache key gives implicit
invalidation: when an app updates, its bytes change, its hash changes,
its old cache entry is unreachable. No timestamps, no version
comparisons, no migration logic.

This is the same pattern krit uses for its parse cache — the comment
in `krit/internal/scanner/parse_cache.go` is worth reading for the
rationale.

## Implementation reference (planned)

`internal/cache/` — modeled after krit's `internal/cacheutil/`:
- `registry.go` — `Register(name, ClearFunc, StatsFunc)`
- `sharded.go` — two-level hash store
- `versioned_dir.go` — `v<N>/` segment management
- `async_writer.go` — bounded-queue background flushes
- `zstd_json.go` — wrapped zstd-compressed JSON envelope

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
