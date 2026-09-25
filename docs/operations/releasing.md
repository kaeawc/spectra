# Releasing Spectra

## One-time setup

Generate the signing key pair with the pinned protocol tool:

```bash
go run github.com/kaeawc/spectra-protocol/cmd/spectra-release@v0.2.0 keygen \
  --private-out ~/spectra-release.key \
  --public-out packaging/release/spectra-release.pub
```

Review and commit `packaging/release/spectra-release.pub`. Store the private
key file's contents as the repository secret
`SPECTRA_RELEASE_ED25519_KEY`, and keep an offline backup of the private key.
Protect `v*` tags with a tag ruleset: anyone who can push a matching tag can
trigger a signature over arbitrary release contents.

## Cutting a release

Run the CI checks, then push a version tag:

```bash
make ci
git tag vX.Y.Z
git push origin vX.Y.Z
```

The release workflow builds macOS and Linux archives, creates and verifies a
signed manifest, and publishes the assets as a GitHub release. A
`workflow_dispatch` run builds the same files and uploads them as a workflow
artifact without creating a GitHub release.

Published release assets are the platform archives, `checksums.txt`,
`spectra-release.json`, and `spectra-release.json.sig`.

## Verifying a release

Download the manifest, signature, archives, and the trusted public key. Run
the protocol verifier with the archive directory so it also checks artifact
hashes:

```bash
go run github.com/kaeawc/spectra-protocol/cmd/spectra-release@v0.2.0 verify \
  --manifest dist/spectra-release.json \
  --signature dist/spectra-release.json.sig \
  --trusted-key "$(cat packaging/release/spectra-release.pub)" \
  --artifacts-dir dist
```

Successful verification prints `ok <version> <keyid>`.

## Key rotation

Generate a new key pair and ship its public key in Spectra Proxy's trusted
keys list before switching the `SPECTRA_RELEASE_ED25519_KEY` secret. This
lets in-flight verifications accept signatures during the transition. Give
the new key a distinct key id.

The Homebrew formula, if present, is unchanged by this release pipeline.
