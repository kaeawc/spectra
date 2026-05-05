# Toolchain inventory

Spectra catalogs package managers, language runtimes, JVM managers, build
tools, and shell environment fields that affect runtime resolution. The
inventory is available from the CLI, the daemon RPC surface, and snapshots;
snapshot diffs and recommendation rules consume the same data.

The "diff my Mac vs your Mac" feature for engineers stands or falls
on toolchain inventory. Two engineers debugging a flaky JVM test need
to know they're on different JDK builds, different Maven versions,
different `JAVA_HOME` settings — fast. Spectra collects each subsystem
independently and rolls them up under
[`Toolchains` in the system inventory](../design/system-inventory.md#toolchains).

Entry points:

```bash
spectra toolchain
spectra toolchain --json
spectra toolchain brew --json
spectra toolchain jdks --json
spectra snapshot create --baseline pre-debug
spectra diff baseline pre-debug live
```

Daemon methods:

- `toolchain.scan`
- `toolchain.brew`
- `toolchain.runtimes`
- `toolchain.build_tools`
- `jdk.list`
- `jdk.scan`

## Subsystems

### Homebrew

Source: `brew info --json=v2 --installed` for formulae,
`brew list --cask --versions` for casks, `brew tap` for taps.

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

The collector never runs `brew update` and treats Homebrew failures as a
partial inventory, not a scan failure.

### JDK installations

Spectra enumerates JDKs from every common install location. See the
list in [jvm.md](jvm.md#jdk-installation-discovery) for the full
location set.

```
toolchains.jdks []{
  install_id
  path                       # absolute path to JDK home
  source ∈ {system, brew, sdkman, asdf, mise, coursier, jbr-toolbox, manual}
  version_major, version_minor, version_patch
  vendor                     # "Eclipse Adoptium", "Azul Zulu", "JetBrains"
  release_string             # raw "21.0.6+7-LTS"
  is_active_java_home        # whether $JAVA_HOME points here
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

### Language runtimes

Multiple managers may coexist on the same machine. Spectra checks each
runtime independently and marks the install whose binary wins current
`$PATH` resolution as `active`.

#### Node.js

| Manager | Detection | Path probed |
|---|---|---|
| Homebrew | brew formula `node@*` | `/opt/homebrew/Cellar/node*/` |
| nvm | `~/.nvm/versions/node/` | per-version dirs |
| fnm | `~/.fnm/node-versions/` or `~/.local/share/fnm/node-versions/` | per-version dirs |
| volta | `~/.volta/tools/image/node/` | per-version dirs |
| mise | `~/.local/share/mise/installs/node/` | per-version dirs |
| asdf | `~/.asdf/installs/nodejs/` | per-version dirs |

Each version reports its `node --version` output if executable; otherwise
it falls back to the directory name.

#### Python

Sources:

- `system` — `/usr/bin/python3` (Apple's bundled)
- `brew` — Homebrew formulae `python@*`
- `pyenv` — `~/.pyenv/versions/`
- `uv` — `~/.local/share/uv/python/`
- `mise` / `asdf` — per-version dirs under their install paths

### Ruby, Go, Rust

Ruby sources: `rbenv`, `mise`, `asdf`, Homebrew `ruby` / `ruby@*`.

Go sources: system `/usr/local/go`, `goenv`, `mise`, `asdf`, Homebrew
`go` / `go@*`.

Rust reports rustup-managed toolchains from `~/.rustup/toolchains/`, their
channel (`stable`, `beta`, `nightly`, or `custom`), and the default
toolchain from `~/.rustup/settings.toml`.

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

Homebrew Cellar checks are used first for common build tools. Version
commands are fallback probes so a machine without Maven, Gradle, Bazel,
Make, or CMake only pays a small number of failed subprocess lookups.

### Environment

Spectra records only the shell fields needed to explain runtime resolution:

```go
env {
  shell
  path_dirs[]
  java_home
  go_path
  go_root
  npm_prefix
  pnpm_home
  proxy_env_vars
}
```

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
- **PATH directories**: sequence-compared, so added, removed, and
  reordered entries are all visible.

## Recommendations engine inputs

Toolchain rows feed several recommendation rules:

- `jdk-eol-version` — major version <= 11.
- `jdk-multiple-versions-installed` — same major version from
  multiple sources (legitimate occasionally; usually drift).
- `java-home-mismatch` — `$JAVA_HOME` points outside the discovered JDK set.
- `path-shadows-active-runtime` — `$PATH` order causes a different
  binary to win than the user's manager would suggest.
- `brew-deprecated-formula` — formula marked deprecated.
- `brew-stale-pin` — pinned formula far behind current.

## Implementation notes

- All discovery is **read-only** and **local**: no network calls,
  no `brew update`.
- Code lives in `internal/toolchain/`.
- `Collect(ctx, opts)` runs subsystem collectors in parallel and returns
  partial results when one collector cannot read a source or a subprocess
  probe fails.
- Subprocess execution and environment reads are injectable through
  `CollectOptions.CmdRunner` and `CollectOptions.EnvLookup`, which keeps
  tests on synthetic homes and fake command output.
- The current collector has no per-subsystem hard timeout; callers can
  cancel the overall context before a task starts.

## See also

- [../design/system-inventory.md](../design/system-inventory.md) —
  where this rolls up
- [jvm.md](jvm.md) — running-JVM inspection (uses these JDK rows
  for attribution)
- [../design/recommendations-engine.md](../design/recommendations-engine.md)
  — what fires against this data
