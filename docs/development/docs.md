# Documentation tooling

Spectra's docs are validated as living docs. Three checks run locally
and in CI to keep `docs/` in shape:

1. **mkdocs nav validation** — every file in `docs/` is linked from
   `mkdocs.yml` and every reference in `mkdocs.yml` resolves.
2. **lychee link checking** — every internal markdown link points at
   a file that exists; external links return a sensible HTTP status.
3. **mkdocs build** — the static site builds without warnings.

The scripts are direct ports of the equivalent ones from
[auto-mobile](https://github.com/kaeawc/auto-mobile/tree/main/scripts).

## Running locally

```bash
make docs-validate            # nav + lychee + build, the full check
make docs-nav                 # nav only — fast
make docs-lychee              # lychee only — slow if external URLs
make docs-build               # mkdocs build
make docs-serve               # mkdocs serve --dev-addr 127.0.0.1:8080
```

Each target shells out to the corresponding script under `scripts/`.

## mkdocs nav validation

`scripts/validate_mkdocs_nav.sh` checks:

- Every `.md` file under `docs/` is listed in `mkdocs.yml`'s `nav:`
  section, OR is in the explicit excluded list inside the script.
- Every file referenced from `nav:` actually exists in `docs/`.
- No file is referenced more than once.
- No file contains `TODO` markers (with a small allowlist for files
  where TODO is prose, not an action item).
- No file is empty or whitespace-only.

Exit code 1 means orphaned, missing, duplicate, TODO-tagged, or empty
files were found. The script prints what to do for each.

## Lychee link checking

`scripts/lychee/validate_lychee.sh` runs lychee against `docs/` and
top-level `*.md` files. Behavior:

- Reads `.lycherc.toml` for config (excluded URL patterns, accepted
  status codes, retry policy).
- Detects broken `file://` links and suggests likely replacements
  by searching the current tree and git history for renamed files.
- Exits 0 on success, 2 on broken links, 1 on configuration errors.

Lychee itself is installed by `scripts/lychee/install_lychee.sh` —
prefers Homebrew, falls back to direct binary download from GitHub
releases. CI calls this script as a setup step.

### Configuring lychee

`.lycherc.toml` controls:

- `exclude` — URL patterns to skip (localhost, private fork repos,
  hub.docker.com which returns transient 500s, etc.).
- `accept` — HTTP status codes that count as "link works" beyond 2xx.
  Includes `403`, `429`, `502`, `503` to tolerate transient and
  auth-protected URLs.
- `timeout`, `max_retries`, `max_concurrency`.
- `cache = true` — speeds up repeat invocations.

## MkDocs build

`mkdocs.yml` declares the site with the Material theme. The Markdown
extensions and theme settings track auto-mobile's setup.

```bash
mkdocs build      # builds into site/
mkdocs serve      # local dev server with live reload
```

CI runs `mkdocs build` to catch broken references the nav check
might miss.

## CI

The `docs` job in `.github/workflows/ci.yml` runs:

```yaml
- run: pip install mkdocs mkdocs-material
- run: ./scripts/validate_mkdocs_nav.sh
- run: ./scripts/lychee/install_lychee.sh
- run: ./scripts/lychee/validate_lychee.sh
- run: mkdocs build --strict
```

`--strict` turns warnings into errors. Any unreferenced file or
broken link fails the build.

## Adding a new doc

1. Write the `.md` file in the appropriate `docs/<area>/` directory.
2. Add it to `mkdocs.yml` under the relevant nav section.
3. Run `make docs-nav` to confirm it's properly registered.
4. Run `make docs-lychee` to confirm internal links resolve.
5. Commit the file and the `mkdocs.yml` change together.

## Removing a doc

1. Delete the `.md` file.
2. Remove its entry from `mkdocs.yml`.
3. Run `make docs-validate` to confirm nothing else linked to it.
   If something did, lychee will fail with the path of the broken
   reference.
