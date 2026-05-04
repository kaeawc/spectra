# Contributing

## Development loop

```bash
git clone https://github.com/kaeawc/spectra.git
cd spectra
make build
make test
```

After any change, before sending a PR:

```bash
make ci      # vet + test + complexity
```

## Code style

- Standard library first. Spectra's CLI ships with **zero
  third-party dependencies** today. New deps go through review;
  prefer shelling out to a system tool over adding a Go module.
- Detection logic lives in `internal/detect/`. Per-collector functions
  are short and single-purpose; a new sub-detection adds its own
  function rather than overloading an existing one.
- Sub-detection results are added to `detect.Result` as new fields,
  not as overloaded behavior on existing fields. The schema in
  [../reference/result-schema.md](../reference/result-schema.md)
  documents the contract.

## Adding detection for a new framework

1. Pick the layer it belongs to (see
   [../detection/overview.md](../detection/overview.md)).
2. Implement the matcher in the appropriate function:
   - Layer 1 → `classifyByBundleMarkers`
   - Layer 2 → `classifyByLinkedLibs`
   - Layer 3 → `classifyByStrings` and/or `scanBinaryMarkers`
3. Add an entry to the framework signal table in
   [../detection/frameworks.md](../detection/frameworks.md).
4. Write a synthetic-bundle test (see
   [testing.md](testing.md)).

## Adding metadata or live-state collectors

1. Add the field(s) to `detect.Result` (or a nested struct).
2. Implement a `scanX(...)` function returning a partial result.
3. Wire it into the appropriate place in `Detect` /
   `populateMetadata`.
4. Add a verbose-mode printer line in `cmd/spectra/main.go`'s
   `printMeta`.
5. Document it in `docs/inspection/<name>.md` and link it from the
   index.
6. Update the schema doc in `docs/reference/result-schema.md`.

## Documentation rules

- Each subsystem gets a markdown page under the matching
  `docs/<area>/` subdirectory.
- New pages must be added to `mkdocs.yml` nav; the
  `validate_mkdocs_nav.sh` script catches orphans.
- Internal links use markdown link syntax; lychee verifies they
  resolve. See [docs.md](docs.md) for the validation pipeline.
- Don't write planning or commentary docs. Living docs only.

## Commits

- Conventional Commit prefixes: `feat:`, `fix:`, `refactor:`,
  `test:`, `docs:`, `chore:`.
- Reference the user-visible change, not the code mechanics.
- One concern per commit.

## What "done" means for a contribution

- Code change passes `make ci`.
- Documentation updated (matching the rules above).
- `scripts/validate_mkdocs_nav.sh` passes (no orphan or missing
  doc files).
- `scripts/lychee/validate_lychee.sh` passes (no broken internal
  links).
- New behavior has a test.
