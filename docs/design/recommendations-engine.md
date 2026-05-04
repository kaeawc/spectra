# Recommendations engine

> **Status: planned.** Design captured here so the data layer can be built
> with the eventual rules engine in mind.

The recommendations engine is what turns Spectra from "DataDog Agent for
Macs" into something genuinely new: **a persistent issue catalog where
declarative rules fire against structured snapshots and produce ranked,
actionable findings with remediation steps.**

## Shape

Rules are declarative, written in YAML with [CEL](https://github.com/google/cel-spec)
expressions for the matching predicate:

```yaml
# rules/jvm.yml
- id: jvm-eol-version
  match: jvm.version.major <= 11
  severity: medium
  message: "JDK {{.jvm.version}} is past public support."
  fix: |
    Recommend upgrading to JDK 21 LTS. Spectra can list installed candidates:
        spectra jdk list

- id: jvm-heap-vs-system
  match: jvm.max_heap_mb / system.ram_mb > 0.6
  severity: high
  message: "Max heap {{.jvm.max_heap_mb}}MB is {{percent jvm.max_heap_mb system.ram_mb}}% of system RAM."
  fix: "Reduce -Xmx, or expect OS-level swap thrashing under memory pressure."
```

## Why CEL

- Declarative — rules can be added without touching Go code.
- Sandboxed — evaluation has no side effects.
- Has a Go implementation maintained by Google.
- Operates on structured data (proto-message-like) — exactly the model
  for `detect.Result`.
- Used in production by Kubernetes admission controllers, GCP IAM,
  Envoy — proven at scale for exactly this pattern.

Alternatives considered:

- **Rules as Go code** — fastest path, but every new rule is a code
  change + binary release. Wrong incentives for the catalog's growth.
- **A custom DSL** — too much yak-shaving for v1. CEL is good enough.

## Issue lifecycle

```
discovered ─→ open ─→ acknowledged ─→ fixed ─→ closed
                ↘────────────────────────────↗
                           dismissed
```

Issues persist across snapshots. The same finding (e.g. "JDK 11 detected
on host X") seen on Monday and Tuesday is one issue with two
observations, not two separate issues. This is what makes Spectra useful
beyond a single point-in-time check — you can see when something
appeared, when it got fixed, and what was tried.

Schema lives in [storage.md](storage.md):

- `issues` — id, rule_id, host_id, first_seen_snapshot_id, last_seen_snapshot_id, status
- `applied_fixes` — id, issue_id, applied_at, applied_by, command, output, exit_code

## Rule sources

V1 catalog ships with the binary. Future:

- **Project-local overrides** — per-team `spectra.yml` extends the catalog.
- **Remote catalogs** — pull from a URL or git repo (e.g.
  `kaeawc/spectra-rules-jvm`).
- **AI-generated rules** — at the edge: feed the LLM a structured
  snapshot and let it propose new rules to add to the catalog.

## What the engine fires against

Every snapshot run dispatches over enabled rules. Inputs available to a
rule's `match` expression:

```
host.os_version, host.arch, host.ram_mb, host.cpu_cores
app.bundle_id, app.version, app.runtime, app.architectures, app.entitlements,
  app.granted_perms, app.storage.total_bytes, app.helpers, app.frameworks, ...
process.pid, process.rss_kib, process.cpu_pct, process.command
jvm.version, jvm.vendor, jvm.max_heap_mb, jvm.gc_count, jvm.thread_count
toolchain.brew_formulae[], toolchain.jdks[], toolchain.node_versions[]
diff.added_apps[], diff.removed_apps[], diff.changed_versions[]   # vs baseline
```

The data model in [storage.md](storage.md) is the source of truth for
what's queryable.

## Examples worth shipping in the V1 catalog

- JVM end-of-life version detected.
- Heap allocation > 60% of system RAM.
- App granted permissions far broader than declared usage descriptions
  (e.g. "Slack has Camera granted but no NSCameraUsageDescription").
- Apps not signed by a Developer ID team (unsigned binaries).
- Apps without hardened runtime (security regression).
- Login-item plist references a binary that no longer exists.
- Storage footprint > 5 GB for any user-data location.
- Multiple JDKs installed at the same major version (drift).
- `JAVA_HOME` references a JDK Spectra cannot find.
- Sparse-file inflation > 10× actual on-disk usage (likely Docker VM).

## Out of scope for v1

- **Auto-fix application.** The engine recommends; the user applies.
  Auto-fixing system state from a diagnostic tool is a trust ask we're
  not ready to make.
- **Rule conflict resolution / priority systems.** Rules fire
  independently; we'll deal with conflicts when we see them.
- **Multi-host rules** that fire on tailnet-wide patterns (e.g. "this
  team has version drift across machines"). V2.
