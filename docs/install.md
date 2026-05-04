# Install

Spectra is currently distributed only via source build. Homebrew formula and
prebuilt binaries are planned (see
[design/distribution.md](design/distribution.md)).

## From source

Requires Go 1.26+ and a macOS host (the detection internals shell out to
`plutil`, `otool`, `codesign`, `file`, and `sqlite3`, which are all macOS
preinstalled).

```bash
git clone https://github.com/kaeawc/spectra.git
cd spectra
make build
./spectra /Applications/Slack.app
```

## Planned: Homebrew

```bash
brew install kaeawc/tap/spectra
spectra /Applications/Slack.app
```

## Planned: Privileged helper

For telemetry that requires root (system TCC.db, `fs_usage`, `powermetrics`),
Spectra will ship an optional helper installable via:

```bash
sudo spectra install-helper
```

The helper registers as a `SMAppService.daemon` (macOS 13+) and exposes
root-only data over a local Unix socket to the unprivileged daemon. The
unprivileged tier still works without it for everything we currently scan.

See [design/distribution.md](design/distribution.md) for the full
capability-vs-channel matrix.
