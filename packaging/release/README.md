# Release signing trust root

`spectra-release.pub` is the committed Ed25519 public key that consumers use
to verify Spectra release manifests. The matching private seed signs each
manifest and must never be committed to this repository.

Generate a key pair out of band with the pinned protocol tool:

```bash
go run github.com/kaeawc/spectra-protocol/cmd/spectra-release@v0.2.0 keygen \
  --private-out ~/spectra-release.key \
  --public-out packaging/release/spectra-release.pub
```

A maintainer must review and commit `spectra-release.pub` before enabling
releases. Store the private key file's contents as the `SPECTRA_RELEASE_ED25519_KEY`
secret of the protected `release` environment (not a repository secret; see
docs/operations/releasing.md), and keep an offline backup of the private key.
