# Diagnostic playbooks

Spectra's collectors are useful on their own, but incident work usually
starts from a symptom. These playbooks group the app, process, JVM,
network, storage, toolchain, snapshot, and remote collectors into repeatable
diagnostic flows.

Use them when the question is operational:

| Symptom | Playbook |
|---|---|
| Java app is slow or memory-heavy | [JVM memory](jvm-memory.md) |
| App cannot reach a service or endpoint | [Network failure](network-failure.md) |
| Disk is filling up or an app is unexpectedly large | [Storage bloat](storage-bloat.md) |
| Another Mac is behaving differently | [Remote triage](remote-triage.md) |
| Builds or tools differ across machines | [Toolchain drift](toolchain-drift.md) |

The collector reference remains under [Inspection](../inspection/). The
playbooks link back to those pages when the next step is to understand a
specific data source or JSON field.

The CLI can also render the same workflow definitions:

```bash
spectra playbook
spectra playbook jvm-memory
spectra playbook --commands network-failure
spectra playbook --json storage-bloat
```
