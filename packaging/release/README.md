# Release checklist

`spectra-release.pub` in this directory is the committed Ed25519 public key
that consumers use to verify Spectra release manifests. The matching private
seed signs each manifest and must never be committed to this repository.

The release workflow (`.github/workflows/release.yml`) is fully wired; it
refuses to sign until the one-time gates below are in place. Full context is
in [docs/operations/releasing.md](../../docs/operations/releasing.md).

## One-time gates (repository owner)

1. **Generate the signing key** on a trusted machine, outside the repo, with
   the protocol tool pinned by the `tool` directive in `go.mod`:

   ```bash
   go tool spectra-release keygen \
     --private-out ~/spectra-release.key \
     --public-out packaging/release/spectra-release.pub
   ```

   The command prints the key id. Keep an offline backup of
   `~/spectra-release.key`.

2. **Commit the public key** at exactly `packaging/release/spectra-release.pub`
   (one line, base64) through a reviewed PR to `main`.
   `scripts/release-manifest.sh` fails if the file is missing or empty, and it
   embeds this key's id in every manifest.

3. **Create the `release` environment** (Settings, Environments, New
   environment, name `release`):
   - Required reviewers: the maintainers who approve each signature.
   - Deployment branches and tags: "Selected branches and tags", add a tag
     rule `v*`.
   - Environment secret `SPECTRA_RELEASE_ED25519_KEY` = the full contents of
     `~/spectra-release.key`. It must be an environment secret, not a
     repository secret, so only the `sign` job can read it.

   With `gh`:

   ```bash
   gh api -X PUT repos/kaeawc/spectra/environments/release \
     -F 'deployment_branch_policy[protected_branches]=false' \
     -F 'deployment_branch_policy[custom_branch_policies]=true'
   gh api -X POST repos/kaeawc/spectra/environments/release/deployment-branch-policies \
     -f name='v*' -f type=tag
   gh secret set SPECTRA_RELEASE_ED25519_KEY --env release \
     -R kaeawc/spectra < ~/spectra-release.key
   ```

   Add the required reviewers in the web UI (the API needs numeric user ids).

4. **Protect `v*` tags with a ruleset** (Settings, Rules, Rulesets, New tag
   ruleset): target `refs/tags/v*`, restrict creations, updates and
   deletions, and block force pushes, with a bypass only for release
   maintainers. Anyone who can push a matching tag can request a signature;
   the environment reviewers are the last check before one is produced.

5. **Optional, macOS signing**: a repository secret `CODESIGN_ID` (a Developer
   ID Application identity available on the runner) makes `scripts/dist.sh`
   codesign the darwin binaries. Without it the darwin binaries carry only the
   Go linker's ad-hoc signature, which is enough for the tarball, the install
   script and the Homebrew formula.

## Per-release steps

1. Run the local checks on a clean `main`:

   ```bash
   make ci
   make release-check     # build, vet, test, tidy, docs, clean tree
   VERSION=vX.Y.Z make dist   # optional dry run of all four archives
   ```

2. Tag and push (the ruleset decides who may):

   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

3. Approve the `sign` job's deployment to the `release` environment when
   GitHub asks. The jobs then run:
   - `build` (macOS): builds `spectra`, `spectra-mcp` and `spectra-helper` for
     darwin-arm64, darwin-amd64 (cgo on), linux-amd64 and linux-arm64 (static,
     cgo off), writes `checksums.txt`, checks that `spectra version` prints
     the tag, and renders the Homebrew formula.
   - `sign` (`release` environment): writes and verifies
     `spectra-release.json` and `spectra-release.json.sig`, after checking the
     native archive's `capabilities --json` reports the tag.
   - `publish`: creates the GitHub release (`--prerelease` for `-` tags) with
     the four `spectra_<tag>_<os>_<arch>.tar.gz` archives, `checksums.txt`,
     `spectra-release.json`, `spectra-release.json.sig` and `spectra.rb`.

4. **Update the Homebrew tap (manual)**. The workflow never pushes to
   `kaeawc/homebrew-tap`; copy the rendered formula by hand:

   ```bash
   gh release download vX.Y.Z -R kaeawc/spectra -p spectra.rb -D /tmp/spectra-formula
   # in a kaeawc/homebrew-tap checkout:
   cp /tmp/spectra-formula/spectra.rb Formula/spectra.rb
   brew audit --strict --formula ./Formula/spectra.rb
   brew install --formula ./Formula/spectra.rb && brew test spectra
   git commit -am "spectra vX.Y.Z" && git push
   ```

5. Smoke the install script against the new release:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/kaeawc/spectra/vX.Y.Z/scripts/install.sh \
     | sh -s -- --version vX.Y.Z --prefix "$(mktemp -d)"
   ```

A `workflow_dispatch` run builds the unsigned archives and the formula only,
uploads them as workflow artifacts, and never signs or publishes.
