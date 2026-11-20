# Install release binaries

Every tagged release publishes prebuilt archives for macOS and Linux:

| Platform | Archive |
| --- | --- |
| macOS, Apple silicon | `spectra_<tag>_darwin_arm64.tar.gz` |
| macOS, Intel | `spectra_<tag>_darwin_amd64.tar.gz` |
| Linux, x86-64 | `spectra_<tag>_linux_amd64.tar.gz` |
| Linux, ARM64 | `spectra_<tag>_linux_arm64.tar.gz` |

Each archive holds `bin/spectra`, `bin/spectra-mcp` and `bin/spectra-helper`,
plus docs, the license and (on Linux) the systemd unit templates.
`checksums.txt` lists the SHA-256 of every archive, and
`spectra-release.json` with its `.sig` is the signed release manifest.

## One-line install

```bash
curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/main/scripts/install.sh | sh
```

`scripts/install.sh` is POSIX `sh`. It picks the archive from `uname -s` and
`uname -m` (preferring arm64 under Rosetta), downloads it and
`checksums.txt`, refuses to install if the SHA-256 does not match, and copies
the three binaries into `<prefix>/bin`. It needs `curl` (or `wget` with an
explicit version), `tar`, and `sha256sum` or `shasum`.

| Option | Environment | Default |
| --- | --- | --- |
| `--version vX.Y.Z` or `latest` | `SPECTRA_VERSION` | `latest` |
| `--prefix DIR` | `SPECTRA_PREFIX` | `$HOME/.local` |
| `--base-url URL` | `SPECTRA_BASE_URL` | GitHub release downloads |

Files are fetched from `<base-url>/<tag>/<file>`, so a mirror or a local
`file://` directory laid out the same way works too.

The helper binary is copied but not enabled. Run `sudo spectra install-helper`
to install it as a LaunchDaemon (macOS) or systemd unit (Linux).

## CI

Pin both the script and the release to a tag so builds are reproducible. In
GitHub Actions the script appends `<prefix>/bin` to `$GITHUB_PATH`, so later
steps find `spectra` on `PATH`:

```yaml
- name: Install Spectra
  run: curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/v0.1.0/scripts/install.sh | sh -s -- --version v0.1.0
- run: spectra version
```

Elsewhere, add the prefix to `PATH` yourself:

```bash
curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/v0.1.0/scripts/install.sh \
  | sh -s -- --version v0.1.0 --prefix /opt/spectra
export PATH="/opt/spectra/bin:$PATH"
```

## Verifying the signed manifest

The checksum check protects against a corrupted or swapped download from the
same release. To also verify the release signature, download the archives,
`spectra-release.json` and `spectra-release.json.sig`, and run the verifier
described in [Releasing](releasing.md#verifying-a-release) against the
committed public key.

## Homebrew

```bash
brew install kaeawc/tap/spectra
```

The tap formula installs the same release archives; see
[Install](../install.md#homebrew).
