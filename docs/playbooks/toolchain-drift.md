# Toolchain drift

Use this when a build, test, JVM, package manager, or language runtime works
on one Mac but fails on another.

## Start local

Collect the toolchain inventory:

```bash
spectra toolchain
spectra toolchain --json
spectra toolchain brew --json
spectra toolchain jdks --json
```

Pay attention to version, vendor, install path, manager, and `$PATH` order.
The same version at a different path may be harmless; a different vendor,
major version, or shadowed binary is more likely to explain drift.

## Compare machines

Use fan-out when the question spans multiple Macs:

```bash
spectra fan --hosts alice-laptop,bob-laptop toolchains
spectra fan --hosts alice-laptop,bob-laptop jdk
```

Use snapshots and diffs when the drift is temporal:

```bash
spectra snapshot --baseline before-upgrade
spectra diff baseline before-upgrade live
```

## Read the result

| Signal | Meaning |
|---|---|
| JDK vendor or major version differs | JVM behavior, compiler flags, TLS roots, and GC defaults may differ |
| `$PATH` order differs | A different binary may be selected even when both machines have the tool |
| Brew formula version changed | Check release notes or pinning for changed behavior |
| Runtime manager differs | `mise`, `asdf`, `sdkman`, `jenv`, and shell init can select different runtimes |
| Xcode or Command Line Tools differ | Native builds and signing behavior may diverge |

## Link back to app symptoms

Toolchain drift often explains a higher-level failure. After finding drift,
return to the symptom-specific playbook:

```bash
spectra jvm explain <pid>
spectra network diagnose --app /Applications/App.app
spectra -v /Applications/App.app
```

## References

- [Toolchains](../inspection/toolchains.md)
- [System inventory](../design/system-inventory.md)
- [CLI toolchain commands](../operations/cli.md)
