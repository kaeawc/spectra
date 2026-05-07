# JVM memory

Use this when a Java process is slow, memory-heavy, GC-bound, or suspected
of leaking classloaders, heap, metaspace, direct buffers, or JIT code cache.

## Start local

List running JVMs and identify the target PID:

```bash
spectra jvm
spectra jvm --json
```

Inspect the target and ask Spectra for interpreted findings:

```bash
spectra jvm <pid>
spectra jvm explain <pid>
```

If the symptom is memory pressure, collect the VM-internal view next:

```bash
spectra jvm gc-stats <pid>
spectra jvm vm-memory <pid>
spectra jvm heap-histogram <pid>
```

## Read the result

Check these signals first:

| Signal | Meaning |
|---|---|
| Heap used near max | Java objects are pressuring `-Xmx`; compare with heap histogram |
| High GC count or long GC time | Allocation rate or heap sizing is a likely contributor |
| Metaspace or classloader growth | Plugin reloads, dynamic proxies, or classloader leaks are possible |
| Native memory tracking unavailable | The JVM was not started with NMT; other VM sections still matter |
| Code cache pressure | JIT compilation may be constrained or deoptimizing hot paths |

One snapshot can show pressure, not a leak. Take repeated samples when the
claim depends on growth:

```bash
spectra jvm explain --samples 5 --interval 10s <pid>
```

## Capture deeper evidence

Use a heap dump only when the target can tolerate the pause and the file
size:

```bash
spectra jvm heap-dump --out ~/Desktop/app.hprof <pid>
```

Use JFR or a flamegraph when the symptom is throughput, CPU, allocation
rate, lock contention, or thread scheduling:

```bash
spectra jvm jfr start <pid> --name incident
spectra jvm jfr dump <pid> --name incident --out ~/Desktop/incident.jfr
spectra jvm jfr stop <pid> --name incident
spectra jvm jfr summary ~/Desktop/incident.jfr

spectra jvm flamegraph --event cpu --duration 30 --out ~/Desktop/cpu.html <pid>
```

## Remote target

Run the same flow against a daemon target when the sick JVM is on another
Mac:

```bash
spectra connect work-mac jvm
spectra connect work-mac jvm-explain <pid>
spectra connect work-mac jvm-vm-memory <pid>
spectra connect work-mac jvm-threads <pid>
```

## References

- [JVM inspection](../inspection/jvm.md)
- [Toolchains](../inspection/toolchains.md)
- [Remote operations](../operations/remote.md)
