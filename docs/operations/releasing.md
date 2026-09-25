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
environment named `release` with required reviewers and a deployment rule that
allows only `v*` tags, then store the private key file's contents as the
environment secret `SPECTRA_RELEASE_ED25519_KEY` (not a repository secret).
Keep an offline backup of the private key. Also protect `v*` tags with a tag
ruleset: anyone who can push a matching tag can request a signature, and the
environment reviewers are the last check before one is produced.

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
`publish` job creates the GitHub release. A `workflow_dispatch` run builds
unsigned archives only and uploads them as a workflow artifact.

Published release assets are the platform archives, `checksums.txt`,
`spectra-release.json`, and `spectra-release.json.sig`.

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

The Homebrew formula, if present, is unchanged by this release pipeline.
