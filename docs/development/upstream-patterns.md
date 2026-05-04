# Patterns lifted from upstream

Spectra is built on top of patterns proven in two adjacent repos:
[`kaeawc/golang-build`](https://github.com/kaeawc/golang-build) (a Go server
template) and [`kaeawc/krit`](https://github.com/kaeawc/krit) (a Kotlin
static analyzer). This page documents what was lifted, what was adapted,
and what was deliberately left behind.

The goal of capturing this is consistency: when you reach for an established
pattern from one of those projects, you should know whether Spectra has it,
where it lives, and how it was adjusted.

## From golang-build

### Build, CI, and project meta — copied verbatim or near-verbatim

| Item | Source | Spectra location | Adjustments |
|---|---|---|---|
| `.github/CODEOWNERS` | golang-build | `.github/CODEOWNERS` | none |
| `.github/dependabot.yml` | golang-build | `.github/dependabot.yml` | dropped Docker section, added pip section for mkdocs |
| `.github/pull_request_template.md` | golang-build | `.github/pull_request_template.md` | none |
| `.github/ISSUE_TEMPLATE/*` | golang-build | `.github/ISSUE_TEMPLATE/*` | bug template adjusted for CLI binary (Spectra version, macOS version, arch) |
| `.golangci.yml` | golang-build | `.golangci.yml` | gocyclo threshold raised 10 → 15 |
| CI workflow | golang-build `commit.yml` | `.github/workflows/ci.yml` | macOS for tests; ubuntu for lint/security/codeql/licenses; added docs job from earlier work |
| `scripts/release-check.sh` | golang-build | `scripts/release-check.sh` | adapted binary name `server` → `spectra`, replaced CHANGELOG check with mkdocs.yml + docs validation |
| `scripts/validate-workflows.sh` | golang-build | `scripts/validate-workflows.sh` | none |
| `ci/go-compile-without-link.sh` | golang-build | `ci/go-compile-without-link.sh` | none |
| `AGENTS.md` | golang-build pattern | `AGENTS.md` | rewritten for Spectra's project map and conventions |

### Zero-dep utility packages — copied with imports rewritten

These are stdlib-only Go packages worth having in tree because they'll be
used as the daemon, helper, and live-data subsystems land. Each has tests
that ship alongside.

| Package | Purpose | Spectra location | Notes |
|---|---|---|---|
| `clock` | `Clock` interface (`System`, `Fake`) for deterministic time tests | `internal/clock/` | unchanged |
| `random` | `Random` interface (`Crypto`, `Seeded`); Float64/IntN/Bytes/UUID, generic `Pick`/`Shuffle` | `internal/random/` | unchanged |
| `idgen` | `Generator` interface; `UUID` (crypto/rand) and `Sequence` (deterministic counter) | `internal/idgen/` | unchanged |
| `retry` | Exponential backoff with cap + jitter, `Sleeper` interface | `internal/retry/` | imports of `random` rewritten to Spectra path |
| `shutdown` | `Coordinator` runs LIFO hooks with per-hook timeout on SIGINT/SIGTERM | `internal/shutdown/` | unchanged |
| `logger` | `Logger` interface over `log/slog`; `Capture` for tests, `Discard` for benchmarks | `internal/logger/` | unchanged |
| `fsutil` | `WriteFileAtomic` and friends | `internal/fsutil/` | unchanged |
| `proc` | `Runner` interface over `os/exec`; `Fake` with scripted matchers | `internal/proc/` | unchanged |

`go test ./... -race` passes for all of them. None are wired into Spectra's
runtime code paths today; they're scaffolding for upcoming work.

### Code patterns adopted

- **Atomic file writes** through `internal/fsutil.WriteFileAtomic` rather
  than direct `os.WriteFile` for any state that must survive crashes.
- **Two-level hash sharding** for blob caches:
  `{root}/{hash[:2]}/{hash[2:]}.bin`. Documented in
  [../design/storage.md](../design/storage.md). Implementation deferred
  until first use site.
- **Versioned cache directories** with a single bumpable version constant
  for invalidation. Same source.
- **Async writer with bounded queue** for cache flushes that shouldn't
  block collectors. Same source.
- **Cache registry pattern** so each new cache kind gets uniform
  `cache stats` / `cache clear` commands.
- **Empirical thresholds in code with the benchmark inline.** When a
  cache has a min-size threshold, the comment cites the benchmark that
  justified it. Pattern from `krit/internal/scanner/parse_cache.go`.

### Conventions adopted

- **Stdlib first, third-party only with clear value.** Spectra's CLI today
  has zero third-party dependencies; that constraint stays unless a
  concrete feature demands otherwise.
- **Internal packages live under `internal/`.** Shared by `cmd/<name>/`
  binaries.
- **Tests live next to the code as `_test.go`** with `t.TempDir()` for
  filesystem fixtures.
- **Inject through interfaces** when introducing testable code: time
  via `clock`, randomness via `random`, env via reader interfaces,
  subprocess via `proc`, filesystem via `vfs` (when it lands).
- **Branch prefix `work/` for agent-created branches.** Never push to
  main directly.

## What was deliberately deferred

These golang-build packages and patterns are valuable but require
architectural commitments Spectra hasn't made yet. Each will be lifted
when the corresponding feature lands.

| Item | Why deferred | When to lift |
|---|---|---|
| `internal/cacheutil/` | Adds `klauspost/compress/zstd` as a third-party dep; no use site yet | When the daemon's blob cache lands ([../operations/caching.md](../operations/caching.md)) |
| `internal/tui/` | Adds Bubble Tea, lipgloss, bubbles deps; no use site yet | When the daemon RPC stabilizes and we build the TUI client ([../design/architecture.md](../design/architecture.md)) |
| `internal/httpserver/`, `internal/httpmw/`, `internal/httpx/` | HTTP machinery for the daemon | When `spectra serve` is implemented ([../operations/daemon.md](../operations/daemon.md)) |
| `internal/auth/tailscale.go` | Tailscale identity authenticator | When `tsnet` is integrated ([../design/remote-portal.md](../design/remote-portal.md)) |
| `internal/limiter/`, `internal/ratelimit/`, `internal/circuitbreaker/` | Rate limiting and outbound-call resilience | When the daemon makes outbound calls or accepts concurrent clients |
| `internal/scheduler/` | In-process job scheduler | When the daemon runs periodic snapshots |
| `internal/eventbus/` | In-process pub/sub | If the daemon needs internal event distribution between collectors |
| `internal/healthcheck/` | Liveness/readiness probes | When the daemon exposes `/health` |
| `internal/jsonresp/` | JSON response helpers + structured error envelope | When daemon RPC handlers are written |
| `internal/perf/` | Local timing tracker, no OTel | If we want a built-in CLI perf summary |
| `internal/tokens/` | HMAC-signed JWT-compatible tokens | If we add per-host bearer tokens beyond Tailscale ACLs |
| `internal/paginator/` | Cursor pagination for list endpoints | If snapshot-list responses get long enough to warrant it |
| `internal/kv/` | Generic in-memory store with TTL | If the daemon caches anything in RAM with expiry |
| `internal/db/`, `sql/`, `sqlc.yaml` | Postgres-specific | Never — Spectra uses SQLite, see [../design/storage.md](../design/storage.md) |
| `internal/handlers/`, `internal/middleware/`, `internal/jobs/`, `internal/investigations/` | Server-specific business logic | Never — those are golang-build's product |
| `internal/tracing/`, `internal/profiling/` | Full OpenTelemetry + Pyroscope | Likely never; the daemon may add a minimal `perf` instead |
| `internal/blobstore/` | S3-backed object store | Never — Spectra's blob store is local filesystem |
| `internal/cache/` (Valkey wrapper) | Redis/Valkey | Never |
| `cmd/server/`, `cmd/loadgen/`, `cmd/onboard/`, `cmd/scaffold/`, `cmd/admin/` | Server-template entry points | Never |
| `web/`, `docker-compose.yml`, `fly.toml` | Server-template ops | Never |

## From krit

See [../design/storage.md](../design/storage.md) for the storage-layer
patterns and [../operations/caching.md](../operations/caching.md) for the
cache-registry pattern. Both are lifted from `krit/internal/cacheutil/`
and `krit/internal/store/`. The implementation is deferred until first
use site (same as golang-build's `cacheutil/`).

The krit precedent that's worth absorbing now is more cultural than
code:

- **Document the *why* of empirical thresholds.** Krit's
  `parseCacheMinFileSize = 1024` constant ships with a comment that
  cites the issue and benchmark. Spectra should do the same when
  adding similar thresholds.
- **Per-language sibling subdirs for caches that may evolve
  independently.** Krit's tree-sitter caches under `parse-cache/kotlin/`
  and `parse-cache/java/`. Spectra's analog is per-cache-kind subdirs
  under the versioned root.
- **Bump the version, abandon old data.** Versioned cache dirs mean
  no migration code is ever needed.

## See also

- [contributing.md](contributing.md) — PR rules
- [docs.md](docs.md) — docs validation pipeline
- [building.md](building.md) — local build
- [testing.md](testing.md) — test patterns
