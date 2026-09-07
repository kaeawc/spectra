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

## Cross-compiling and Linux

Spectra builds and runs on Linux as well as macOS. The Linux build is
pure Go (`CGO_ENABLED=0`); the one cgo dependency —
`internal/process/thread_darwin.go`, which links `libproc` for per-process
thread counts — is **darwin-only**, so macOS builds keep `CGO_ENABLED=1`
while Linux disables it and reads thread counts from `/proc` instead.

```bash
# Linux (static, pure Go)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

On Linux, the host-aware collectors use native sources — `/etc/os-release`
and `/proc` for host facts, `/proc` for processes, `ip`/`ss`/`/proc/net`
for network state, `df`/`/proc/mounts` for storage, `/sys` for power — and
the daemon/helper install via systemd. App-bundle inspection
(`.app`/Mach-O/`codesign`/TCC) has no Linux equivalent and is refused with
a clear message. The `internal/hostos` package is the OS-selection layer
every collector consults.

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
