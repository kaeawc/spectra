# Building

## Prerequisites

- Go 1.26+
- macOS (the detection internals shell out to `plutil`, `otool`,
  `codesign`, `file`, and `sqlite3` — all macOS preinstalled)

## From source

```bash
git clone https://github.com/kaeawc/spectra.git
cd spectra
make build
./spectra /Applications/Slack.app
```

To build the optional privileged helper next to the CLI:

```bash
make build-all
```

## Make targets

| Target | What it does |
|---|---|
| `make build` | Build the `spectra` binary |
| `make build-helper` | Build the `spectra-helper` binary |
| `make build-all` | Build both binaries |
| `make test` | Run the test suite (`go test ./... -count=1`) |
| `make vet` | `go vet ./...` |
| `make fmt` | `gofmt -s -w .` |
| `make lint` | `golangci-lint run` |
| `make complexity` | `gocyclo -over 15` |
| `make tidy` | `go mod tidy` |
| `make ci` | vet + test + complexity + lint + security + licenses + docs |
| `make clean` | Remove build artifacts |

## Cross-compiling

The Go module has no CGo dependencies, so it cross-compiles cleanly:

```bash
GOOS=linux GOARCH=arm64 go build ./...
```

Note that the resulting Linux binary won't actually do anything useful
(every collector shells out to a macOS tool that doesn't exist on
Linux), but the build succeeds. This is what enables future Linux
detection: the `syscall_other.go` build-tagged file ensures the
non-darwin path doesn't reference `syscall.Stat_t.Blocks`.

## Build version stamping

The Makefile sets `main.version` via `-ldflags` from
`git describe --tags --always --dirty`:

```
$ spectra --version
v0.0.4-3-g9a8b7c6
```

Override:

```bash
make build VERSION=1.0.0-rc1
```

## Running with race detection

```bash
go test ./... -race
```

All tests pass under `-race`. Worth running before releases.

## Profiling

The CLI is fast enough that profiling is rarely needed, but for
investigating regressions:

```bash
go test -cpuprofile cpu.prof ./internal/detect/
go tool pprof cpu.prof
```
