# Capabilities manifest

`spectra capabilities --json` emits one JSON object describing the CLI interfaces this build supports. `spectra capabilities` prints a short human-readable table. The manifest is local and requires no host inspection.

```json
{
  "schema": {"name": "spectra.capabilities", "version": 1},
  "spectra_version": "dev",
  "os": "darwin",
  "arch": "arm64",
  "interfaces": [
    {"name": "version", "argv": ["version"], "output": "text"},
    {"name": "inspect", "argv": ["--json", "<app_path>..."], "output": "json", "result_schema": {"name": "spectra.inspect", "version": 1}},
    {"name": "snapshot", "argv": ["snapshot", "--json", "[--no-apps]"], "output": "json", "result_schema": {"name": "spectra.snapshot", "version": 1}},
    {"name": "capabilities", "argv": ["capabilities", "--json"], "output": "json", "result_schema": {"name": "spectra.capabilities", "version": 1}}
  ]
}
```

`spectra_version` is the exact text printed by `spectra version`, without its trailing newline. `os` and `arch` are the build's Go runtime platform. The inspect interface returns an array of [result objects](result-schema.md); snapshot and capabilities return single objects. The version interface is plain text and therefore omits `result_schema`.

The `argv` values describe supported invocation shapes. Angle brackets mark an absolute app path supplied by the caller; the ellipsis permits multiple paths. Square brackets mark an optional flag. A snapshot caller may also pass `--no-store` to avoid local persistence.

## Stability

Proxy consumers must check each JSON interface's `result_schema` name and version before decoding its result. Fields may be added within a major schema version. Removing or renaming a field, or changing its JSON type, requires a result schema version bump. This rule applies independently to the inspect, snapshot, and capabilities schemas. A new capability can add an interface entry without changing an existing result schema.
