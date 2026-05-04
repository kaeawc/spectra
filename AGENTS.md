# Spectra

A diagnostic agent for macOS that combines deep static inspection of installed
apps with live process state, JVM toolchain awareness, and engineer-to-engineer
remote debugging over Tailscale. See [docs/index.md](docs/index.md) for the
product framing.

## Working Rules

- Keep code in Go. Standard library first; only add dependencies for clear
  value. The CLI today has zero third-party deps and we keep it that way
  unless a feature requires otherwise.
- Detection logic lives in `internal/detect/`. Per-collector functions are
  short and single-purpose; new sub-detections add their own functions
  rather than overloading existing ones.
- New entry points go under `cmd/<name>/`. Today there's only `cmd/spectra/`.
  The planned daemon and helper get their own.
- Internal packages (not part of any public API) live under `internal/`.
- Filesystem writes that must survive crashes go through
  `internal/fsutil.WriteFileAtomic`.
- macOS-only utilities (`plutil`, `otool`, `codesign`, `file`, `sqlite3`) are
  invoked via `os/exec`. CLI is a Mac-only tool today; cross-compilation is
  preserved for future platform expansion via build-tagged files like
  `internal/detect/syscall_darwin.go`.

## Build & Validate

```bash
make build       # go build -o spectra ./cmd/spectra/
make test        # go test ./... -count=1
make ci          # vet + test + complexity + docs-validate
make all         # build + vet + test + docs-validate
```

After any implementation change, run `go build ./... && go vet ./...`.
Use focused package tests while iterating: `go test ./internal/detect/ -run TestX -count=1`.

For docs changes: `make docs-validate` (mkdocs nav + lychee link check).

## Git

- Use branch prefix `work/` for agent-created branches.
- Never push to `main` directly; always open a PR.
- Each commit is one concern.

## Project Map

### Code

- `cmd/spectra/` — CLI entry point. Flag parsing, parallel scan worker pool,
  table + JSON output.
- `internal/detect/` — the entire static-inspection engine: framework
  classifier, sub-detections, metadata collectors, security/storage/network
  inspections, helpers, login items, processes, TCC reads.
  - `detect.go` — main `Detect()` entry, per-layer classifiers.
  - `syscall_darwin.go` / `syscall_other.go` — sparse-file-correct
    `Stat_t.Blocks` size on Darwin, fallback elsewhere.

Planned (per [docs/design/](docs/design/)):

- `cmd/spectra-helper/` — privileged helper, separate `main`.
- `internal/cache/` — sharded blob store + async writer (krit/cacheutil
  pattern; see [docs/operations/caching.md](docs/operations/caching.md)).
- `internal/snapshot/` — system-inventory collectors and SQLite persistence.
- `internal/rpc/` — JSON-RPC dispatcher used by both Unix-socket local
  clients and tsnet remote clients.
- `internal/tui/` — Bubble Tea TUI (Phase pattern lifted from golang-build
  when the daemon RPC stabilizes).

### Docs

Living docs in `docs/`. mkdocs nav at `mkdocs.yml`. Validation:

- `scripts/validate_mkdocs_nav.sh` — orphan / missing / TODO checks.
- `scripts/lychee/validate_lychee.sh` — internal + external link checks.
- `make docs-validate` — both, plus `mkdocs build --strict` in CI.

### CI

`.github/workflows/ci.yml` jobs:

- `test` — macOS runner; `go vet`, `go build`, `gotestsum -race`, smoke
  tests against `/System/Applications/Calculator.app` etc.
- `complexity` — `gocyclo -over 15`.
- `static-analysis` — `golangci-lint v2`.
- `check-licenses` — `go-licenses report`.
- `security` — `gosec` with documented exclusions, JUnit-formatted report.
- `codeql-analysis` — GitHub CodeQL on Go.
- `validate-workflows` — `shellcheck` + YAML parse on `.github/`.
- `docs` — mkdocs nav + lychee + `mkdocs build --strict`.

Go version comes from `go.mod`.

## Conventions

- **Errors:** wrap with `fmt.Errorf("context: %w", err)` so callers can
  `errors.Is/As`.
- **Comments:** only when the *why* is non-obvious. Don't restate what the
  code does.
- **Tests:** live next to the code as `_test.go`. Use `t.TempDir()` for
  filesystem fixtures. Synthetic `.app` bundles are built via
  `makeBundle(t, name)` in `detect_test.go` — see
  [docs/development/testing.md](docs/development/testing.md).
- **Time, randomness, env, subprocess, filesystem:** when introducing testable
  code, inject via interfaces (per the krit / golang-build pattern). Never
  call `time.Now()`, `os.Getenv`, `os/exec`, or raw `os.ReadFile` from code
  paths that need to be testable.
## Reference docs

- [Architecture](docs/design/architecture.md) — daemon-with-clients model.
- [Distribution](docs/design/distribution.md) — Homebrew, sudo helper, why
  not MAS.
- [Storage](docs/design/storage.md) — SQLite + sharded blob store + ring
  buffer (krit-style patterns).
- [System inventory](docs/design/system-inventory.md) — snapshot data model.
- [Threat model](docs/design/threat-model.md) — security posture.
- [Privileged helper](docs/design/privileged-helper.md) — boundary + IPC.
- [Detection overview](docs/detection/overview.md) — the three-layer
  classifier.
- [Result schema](docs/reference/result-schema.md) — JSON output contract.
- [Glossary](docs/reference/glossary.md) — terminology.
