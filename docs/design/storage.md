# Storage

Spectra's storage stack is three tiers, each with a clear job. The design
borrows directly from the caching layer in [krit](https://github.com/kaeawc/krit)
where the patterns have proven out at scale.

```
┌─ SQLite (relational, queryable) ────────────────────────────┐
│  hosts, snapshots, snapshot_apps, snapshot_processes,       │
│  helpers, login_items, granted_perms, issues,               │
│  applied_fixes, baselines, recommendations                  │
└─────────────────────────────────────────────────────────────┘
            ↑ references via blob_key column
┌─ Sharded file store (blobs, krit-style) ────────────────────┐
│  ~/.cache/spectra/v1/                                       │
│    ├ detect/{hash[:2]}/{hash[2:]}.json.zst                  │
│    ├ hprof/{hash[:2]}/{hash[2:]}.hprof                      │
│    ├ jfr/{hash[:2]}/{hash[2:]}.jfr                          │
│    ├ threads/{hash[:2]}/{hash[2:]}.txt                      │
│    └ samples/{hash[:2]}/{hash[2:]}.txt                      │
└─────────────────────────────────────────────────────────────┘
            ↑ flushed periodically via async writer
┌─ In-memory ring buffer (recent live samples) ───────────────┐
│  Last ~5min of CPU%/RSS/net-bytes per pid, 1Hz              │
│  Aggregated to 1min rows on flush                           │
└─────────────────────────────────────────────────────────────┘
```

## Tier 1: SQLite (relational)

Holds metadata and queryable facts. Anything you'd want to JOIN across
hosts and time:

- **hosts** — one row per Mac in the tailnet (or the local machine).
- **snapshots** — timestamped collection runs, one per host per moment.
- **snapshot_apps** — one row per app per snapshot, with the full
  `Detect()` result either inlined or referenced via a blob_key.
- **snapshot_processes** — running processes captured at snapshot time.
- **granted_perms** — TCC permissions per app per snapshot.
- **login_items** — LaunchAgent/Daemon plists discovered.
- **baselines** — frozen reference snapshots (the "last known good"
  state of a host) used for diff.
- **issues** — open/acknowledged/closed findings from the recommendations
  engine.
- **applied_fixes** — audit log of remediation actions.

### Driver

Use `modernc.org/sqlite` (pure Go, no CGo). ~30% slower than `mattn/go-sqlite3`
but trivial to cross-compile for Linux/Windows binaries from a Mac dev box.
Spectra's write rate is far below the gap (snapshots every 60s, not 60Hz).

### Pragmas

- `PRAGMA journal_mode=WAL;` — concurrent readers while collector writes.
- `PRAGMA synchronous=NORMAL;` — durability vs throughput tradeoff
  appropriate for non-financial data.
- `PRAGMA foreign_keys=ON;` — for issues → snapshots references.

### One database per host

Replication across the tailnet is "rsync the SQLite file when needed."
WAL doesn't span machines anyway, so there's no merge story. For
"diff my Mac vs your Mac," each daemon serves its own DB and the
client correlates across them.

## Tier 2: Sharded blob store

For everything SQLite shouldn't hold inline: heap dumps (multi-GB JFR
recordings, `.hprof` files, raw `app.asar` exports, network captures).

Layout, lifted from `krit/internal/store/file.go`:

```
~/.cache/spectra/v1/{kind}/{hash[:2]}/{hash[2:]}.bin
```

Two-level hash sharding keeps no directory above 256 entries even at
scale. Atomic writes via tempfile-rename. Multiple kinds share one
root.

### Why versioned (`v1/`)

Schema changes for a particular kind happen periodically. Bumping the
version constant invalidates the entire tree without a migration script —
old data is just unreachable. Borrowed from
`krit/internal/cacheutil/versioned_dir.go`.

### Cache registry

Each kind registers itself with `Name()`, `Clear(ctx)`, `Stats()`. The
CLI exposes uniform `spectra cache stats` and `spectra cache clear`
commands without per-kind code. Borrowed from
`krit/internal/cacheutil/registry.go`.

### Async writer

A bounded queue + worker pool flushes blobs in the background so
collection isn't blocked on I/O. Caller can fall back to synchronous
writes when the queue is full instead of dropping. Counters track
queued / completed / failed / bytes. Borrowed from
`krit/internal/cacheutil/async_writer.go`.

## Tier 3: In-memory ring buffer

Live data (CPU%, RSS, network bytes/sec, GC ticks) at ~1Hz is too dense
to write straight to SQLite. The collector keeps the most recent 5
minutes per process in RAM, then aggregates to 1-minute rows when
flushing. Per-second resolution is reachable for "what just happened"
queries but doesn't bloat persistent storage.

## What we are deliberately NOT carrying over from krit

- **Bloom filters.** Krit uses them for "does identifier X appear in
  shard Y" across millions of small shards — true negative-lookup is the
  hot path. Spectra's snapshots are coarse-grained relational rows;
  SQLite's B-tree indexes cover that pattern natively.
- **Hand-rolled columnar codec.** Krit replaced gob with a custom
  magic-versioned varint format because gob's per-record schema and
  string repetition were measurable bottlenecks at their data volume.
  Spectra isn't anywhere near that scale. Plain JSON for now.
- **Custom intern tables.** Premature optimization for our size class.

## Empirical thresholds

Whenever a caching threshold matters (don't cache below this size, don't
keep more than this many entries), document the benchmark that justified
it inline. The krit precedent:

```go
// Files below this threshold parse in under a millisecond; the gob
// serialization + filesystem round-trip dominates the savings.
parseCacheMinFileSize = 1024
```

Spectra should follow the same pattern when adding similar thresholds —
the next reader has a fighting chance of knowing whether it still
holds.
