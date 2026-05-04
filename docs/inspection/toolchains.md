# Toolchain inventory

> **Status: planned.** Catalogs every package manager and language
> runtime Spectra will enumerate to power cross-host diff and the
> recommendations engine.

The "diff my Mac vs your Mac" feature for engineers stands or falls
on toolchain inventory. Two engineers debugging a flaky JVM test need
to know they're on different JDK builds, different Maven versions,
different `JAVA_HOME` settings — fast. Spectra collects each subsystem
independently and rolls them up under
[`Toolchains` in the system inventory](../design/system-inventory.md#toolchains).

## Subsystems

### Homebrew

Source: `brew info --json=v2 --installed` for formulae, `brew list --cask`
for casks, `brew tap` for taps.

```
toolchains.brew {
  formulae []{
    name              # "openjdk@21"
    version           # "21.0.6"
    installed_via ∈ {tap, core, cask-tied}
    deprecated bool
    pinned bool
  }
  casks []{ name, version }
  taps  []{ name }
}
```

Cheap and fast — `brew info --json=v2 --installed` gives everything in
one call.

### JDK installations

Spectra enumerates JDKs from every common install location. See the
list in [jvm.md](jvm.md#jdk-installation-discovery) for the full
location set.

```
toolchains.jdks []{
  install_id
  path                       # absolute path to JDK home
  source ∈ {system, brew, sdkman, asdf, mise, jbr-toolbox, manual}
  version_major, version_minor, version_patch
  vendor                     # "Eclipse Adoptium", "Azul Zulu", "JetBrains"
  release_string             # raw "21.0.6+7-LTS"
  is_active_for_terminal     # whether $JAVA_HOME points here
}
```

Each JDK install ships a `release` file in its root with parseable
version metadata:

```
JAVA_VERSION="21.0.6"
IMPLEMENTOR="Eclipse Adoptium"
JAVA_RUNTIME_VERSION="21.0.6+7-LTS"
JAVA_VERSION_DATE="2025-01-21"
```

Spectra parses this rather than running the JDK because (1) it's
faster and (2) it works for JDKs that aren't otherwise functional
(missing entitlements, broken codesign).

### Node.js runtimes

Multiple managers may coexist on the same machine. Spectra checks
each independently:

| Manager | Detection | Path probed |
|---|---|---|
| Homebrew | brew formula `node@*` | `/opt/homebrew/Cellar/node*/` |
| nvm | `~/.nvm/versions/node/` | per-version dirs |
| fnm | `~/.fnm/node-versions/` or `~/.local/share/fnm/node-versions/` | per-version dirs |
| volta | `~/.volta/tools/image/node/` | per-version dirs |
| mise | `~/.local/share/mise/installs/node/` | per-version dirs |
| asdf | `~/.asdf/installs/nodejs/` | per-version dirs |

Each version reports its `node --version` output if executable.

The `active` flag indicates which version `node` would resolve to
based on the current `$PATH` ordering — the inventory needs this to
explain "why is `node` resolving to v18 even though I installed v22?"

### Python runtimes

Same structure as Node, with sources:
- `system` — `/usr/bin/python3` (Apple's bundled)
- `brew` — Homebrew formulae `python@*`
- `pyenv` — `~/.pyenv/versions/`
- `uv` — `~/.local/share/uv/python/`
- `mise` / `asdf` — per-version dirs under their install paths

### Ruby, Go, Rust

Symmetric to Node and Python; each has its standard set of managers
(`rbenv`, `asdf`, `mise` for Ruby; `brew`, `goenv`, `mise`, `asdf` for
Go; `rustup` for Rust toolchains and channels).

### JVM-specific package managers

```
toolchains.jvm_managers ∈ {sdkman, asdf, mise, jenv}
```

Important because two of these can shadow each other (`jenv` rewrites
shims that `sdkman` might also install). Spectra reports which is
actively shimming `java` based on `$PATH` ordering.

### Build tools

| Tool | Source | Reported |
|---|---|---|
| Maven | `mvn -version`, brew formula | version, settings.xml location |
| Gradle | `gradle -version`, brew formula | version, GRADLE_USER_HOME |
| Bazel | `bazel version`, brew formula | version, .bazelrc location |
| Make | system `xcrun make`, brew formula | version |
| CMake | brew formula | version |

Skipped unless installed; running `mvn -version` on a machine without
Maven is a wasted fork.

## Why the level of detail

Each engineer's machine has dozens of toolchains layered on top of
each other. Spectra's purpose is to make drift visible — not just
"you're on a different JDK," but "your `JAVA_HOME` is set by jenv
even though you also have sdkman installed, and the `java` binary
resolved through PATH is from `mise`, not the one `JAVA_HOME` points
to." That's a real bug we've all spent hours on. Spectra shouldn't
make us run five `which`/`type` commands; it should print the picture.

## Diff semantics

For each subsystem:

- **Brew formulae**: matched by name; reports `added`, `removed`,
  `version_changed` rows.
- **JDKs**: matched by `(version_major, version_minor, version_patch, vendor)`;
  same identity at different paths is "compatible" (one host has it
  via brew, the other via SDKMAN — fine), different identities are
  "drift."
- **Active runtime versions**: matched by language; if the active
  `node` is v18 on host A and v22 on host B, that's a high-signal
  diff entry.

## Recommendations engine inputs

Toolchain rows feed several recommendation rules:

- `jdk-eol-version` — major version <= 11.
- `jdk-multiple-versions-installed` — same major version from
  multiple sources (legitimate occasionally; usually drift).
- `path-shadows-active-runtime` — `$PATH` order causes a different
  binary to win than the user's manager would suggest.
- `brew-deprecated-formula` — formula marked deprecated.
- `brew-stale-pin` — pinned formula far behind current.

## Implementation notes

- All discovery is **read-only** and **local**: no network calls,
  no `brew update`.
- Each subsystem is implemented behind an interface
  (`internal/toolchain/discoverer.go`):
  ```go
  type Discoverer interface {
      Name() string
      Discover(context.Context) (Subsystem, error)
  }
  ```
- Discoverers run in parallel for the cross-host snapshot, with a
  reasonable timeout per subsystem so a slow `mise` invocation
  doesn't block the whole snapshot.

## See also

- [../design/system-inventory.md](../design/system-inventory.md) —
  where this rolls up
- [jvm.md](jvm.md) — running-JVM inspection (uses these JDK rows
  for attribution)
- [../design/recommendations-engine.md](../design/recommendations-engine.md)
  — what fires against this data
