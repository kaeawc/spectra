# Install

Spectra is currently distributed via source build. The repo also contains
release packaging for Homebrew and macOS tarball archives, but published
formulae and signed/notarized binaries are release-owner tasks (see
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

To build both the CLI and optional privileged helper:

```bash
make build-all
./spectra version
./spectra-helper --version
```

`spectra install-helper` expects the `spectra-helper` binary to live next
to the `spectra` binary that is running, which `make build-all` does in
the repo root.

## Homebrew

```bash
brew install kaeawc/tap/spectra
spectra /Applications/Slack.app
```

The formula template lives at `packaging/homebrew/spectra.rb`. It builds
`spectra`, `spectra-mcp`, and `spectra-helper` from source and does not
install the privileged helper automatically. Publishing requires replacing
the placeholder tag and checksum in the template and copying it into the
tap repository.

## Prebuilt archives

Release owners can build unsigned local archives with:

```bash
make dist
```

This writes `dist/spectra_<version>_darwin_arm64.tar.gz`,
`dist/spectra_<version>_darwin_amd64.tar.gz`, and `dist/checksums.txt`.
To sign binaries inside the archives:

```bash
CODESIGN_ID="Developer ID Application: Example Corp (TEAMID)" make dist
```

To submit built archives to Apple's notary service:

```bash
NOTARY_PROFILE=spectra-notary make notarize
```

The notary profile must already exist in the local keychain via
`xcrun notarytool store-credentials`.

## Privileged helper

For telemetry that requires root (system TCC.db, `fs_usage`, `powermetrics`),
Spectra has an optional helper installable from a source build:

```bash
make build-all
sudo spectra install-helper
spectra install-helper --status
```

The current installer provisions the `_spectra` group, adds the invoking
user to it, copies `spectra-helper` to `/Library/PrivilegedHelperTools/`,
writes a LaunchDaemon plist, and starts it with `launchctl`. The helper
exposes root-only data over `/var/run/spectra-helper.sock` as
`0660 root:_spectra`. On first install, log out and back in so the user's
new group membership is visible to shells and long-running processes. The
unprivileged tier still works without the helper for everything that does
not require root.

See [design/distribution.md](design/distribution.md) for the full
capability-vs-channel matrix.
