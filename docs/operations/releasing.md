# Releasing Spectra

## One-time setup

Generate the signing key pair with the pinned protocol tool:

```bash
go tool spectra-release keygen \
  --private-out ~/spectra-release.key \
  --public-out packaging/release/spectra-release.pub
```

The release tool version is pinned by the `tool` directive in `go.mod`.

Review and commit `packaging/release/spectra-release.pub`. Create a GitHub
environment named `release` with a deployment rule that allows only `v*` tags
(no required reviewers), then store the private key file's contents as the
environment secret `SPECTRA_RELEASE_ED25519_KEY` (not a repository secret).
Keep an offline backup of the private key. Also protect `v*` tags with a tag
ruleset that lets only repository admins create, update, or delete them:
pushing a matching tag signs and publishes a release with no further approval,
so that ruleset is the gate on who can release.

## Cutting a release

Run the CI checks, then push a version tag:

```bash
make ci
git tag vX.Y.Z
git push origin vX.Y.Z
```

The release workflow builds macOS and Linux archives without access to the
signing key. A separate `sign` job, which runs only for tag pushes inside the
`release` environment, creates and verifies the signed manifest, and the
`publish` job creates the GitHub release. The whole run is automatic once the
tag is pushed. A `workflow_dispatch` run builds
unsigned archives only and uploads them as a workflow artifact.

The `build` job forces cgo on for both darwin targets (Go otherwise drops
cgo when cross-compiling darwin/amd64 on the arm64 runner) and builds the
Linux targets static with cgo off, then fails if any binary was built with the
wrong cgo setting or if `spectra version` does not print the tag.

Published release assets are the four platform archives
(`spectra_<tag>_{darwin,linux}_{arm64,amd64}.tar.gz`, each with `spectra`,
`spectra-mcp` and `spectra-helper`), `checksums.txt`, `spectra-release.json`,
`spectra-release.json.sig`, and the rendered Homebrew formula `spectra.rb`.
The formula is kept outside the signed manifest, which covers the archives.

For stable tags, the `homebrew-verify` job then installs and tests the
rendered formula against the published archives, and the `homebrew-tap` job
pushes it to `kaeawc/homebrew-tap` with a write deploy key stored as the
`release` environment secret `HOMEBREW_TAP_DEPLOY_KEY`. Prerelease tags are
never pushed to the tap.

The per-release checklist is in `packaging/release/README.md`.

## Verifying a release

Download the manifest, signature, archives, and the trusted public key. Run
the protocol verifier with the archive directory so it also checks artifact
hashes:

```bash
go tool spectra-release verify \
  --manifest dist/spectra-release.json \
  --signature dist/spectra-release.json.sig \
  --trusted-key "$(cat packaging/release/spectra-release.pub)" \
  --artifacts-dir dist
```

Successful verification prints `ok <version> <keyid>`.

## Key rotation

Generate a new key pair and ship its public key in Spectra Proxy's trusted
keys list first, so installed proxies accept signatures from either key during
the transition. Then, in one change before the next tag, commit the new public
key to `packaging/release/spectra-release.pub` (the release script embeds its
key id in the manifest and verifies against it) and replace the `release`
environment's `SPECTRA_RELEASE_ED25519_KEY` secret. Each key has a distinct
key id.

## Homebrew formula

`packaging/homebrew/spectra.rb` is a template for a binary formula that
installs the release archives. `scripts/release-formula.sh <tag> dist out.rb`
fills in the tag and the per-platform SHA-256 values from `checksums.txt` and
fails if any is missing. It drops the template's header comment and, for a
plain `vX.Y.Z` tag, the explicit `version` line (a prerelease tag keeps it,
since Homebrew cannot scan one from the URL), so the result passes `brew
audit --strict` and can be copied into the tap verbatim.
The workflow attaches it to the release, and for stable tags
`scripts/release-tap.sh` commits it to `Formula/spectra.rb` in
`kaeawc/homebrew-tap`. The script skips prereleases, never replaces a newer
formula with an older one, and does nothing when the tap is already current.
`scripts/release-brew-check.sh` runs the tap CI's checks (`brew style`,
`brew audit --strict`, and with the archives published `--online`,
`brew install` and `brew test`) before the push.

## Version strings

Release builds stamp the tag with `-ldflags "-X main.version=<tag>"`, which
`spectra version`, `spectra-mcp --version`, `spectra-helper --version` and
`spectra capabilities --json` (`spectra_version`) report. Source builds report
`dev+<12-character commit>` (with `.dirty` for uncommitted changes) from the
VCS stamp the Go toolchain embeds, or plain `dev` when there is none.
