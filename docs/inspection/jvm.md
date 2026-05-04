# JVM inspection

> **Status: planned.** Captured here so the data model and architecture
> can be built with JVM-class introspection in mind from the start.

Spectra is intended to **supplant VisualVM** for the day-to-day
"what's this Java process doing" question. JVM inspection is a
first-class subsystem alongside the app scanner, not a bolt-on.

## Two implementation layers

VisualVM's capabilities split cleanly into "what the JDK gives you for
free" and "what requires injecting code into the target JVM." Spectra
implements both, one per layer.

### Layer 1 — JDK toolchain shell-out (no Java in Spectra)

Get ~80% of VisualVM's read-only inspection by shelling out to bundled
JDK commands:

| Capability | Command |
|---|---|
| Process discovery | `jps -l` |
| JVM args, system props | `jcmd <pid> VM.system_properties`, `VM.command_line` |
| Live thread dump | `jcmd <pid> Thread.print` |
| GC stats over time | `jstat -gc <pid> 1000` |
| Heap histogram | `jcmd <pid> GC.class_histogram` |
| Heap dump | `jcmd <pid> GC.heap_dump <path>` |
| JFR start / stop / dump | `jcmd <pid> JFR.start`, `JFR.dump` |

Output is parsed into structured JSON. The captured artifacts (.hprof,
.jfr, thread dump text) go into the [sharded blob store](../design/storage.md)
keyed by content hash; SQLite holds the metadata row pointing at them.

### Layer 2 — Java agent (in-process)

The remaining capabilities require code running inside the target JVM:

- Live MBean browsing (JMX over RMI).
- Async-profiler-grade flame graphs (bytecode instrumentation).
- Custom counters and probes.

Spectra ships a small `spectra-agent.jar` alongside the Go binary. On
demand, it's attached via the Attach API:

```bash
spectra jvm attach <pid>      # loads the agent into a running JVM
spectra jvm flamegraph <pid>  # async-profiler over the agent
```

The agent exposes its capabilities over a Unix socket so the Go daemon
can drive it without speaking JMX/RMI directly.

## JDK installation discovery

A first-class collector enumerates installed JDKs from every common
location:

| Source | Path pattern |
|---|---|
| System | `/Library/Java/JavaVirtualMachines/*.jdk` |
| Per-user | `~/Library/Java/JavaVirtualMachines/*.jdk` |
| JetBrains JBRs | `~/Library/Application Support/JetBrains/Toolbox/apps/*/jbr/` |
| Homebrew | `/opt/homebrew/opt/openjdk*/`, `/usr/local/opt/openjdk*/` |
| SDKMAN | `~/.sdkman/candidates/java/*/` |
| asdf / mise | `~/.asdf/installs/java/*/`, `~/.local/share/mise/installs/java/*/` |
| Coursier | `~/Library/Caches/Coursier/jvm/*/` |

Each install's `release` file is parsed for version + vendor:

```
JAVA_VERSION="21.0.6"
IMPLEMENTOR="Eclipse Adoptium"
JAVA_RUNTIME_VERSION="21.0.6+7-LTS"
```

Cross-referenced with running Java processes (each JVM's `java.home`
property): Spectra knows which JVM each process is using, and which
installed JDK that JVM came from.

## Sample output (planned)

```
$ spectra jvm
PID    JDK                           HEAP/MAX        UPTIME    APP
4012   Adoptium 21.0.6 (toolbox)     1.2GB/4GB       6h 12m    Android Studio
8410   Zulu 17.0.10                  450MB/2GB       2h 3m     gradle daemon
9123   Adoptium 21.0.6 (toolbox)     1.8GB/2GB       45m       IntelliJ IDEA

$ spectra jvm 4012
JVM            Adoptium 21.0.6 (Eclipse Adoptium)
Path           ~/Library/Application Support/JetBrains/Toolbox/apps/.../jbr/Contents/Home
Args           -Xmx4g -Xms256m -XX:+UseG1GC ...
Threads        87 (12 daemon, 5 blocked)
GC             G1, last full GC 12m ago
Loaded classes 18,432
Recent issues  jvm-heap-vs-system: 50% of system RAM at -Xmx4g
```

## Why this matters

VisualVM is venerable but increasingly painful: NetBeans-platform
Swing app, hard to keep working with modern JDKs, no remote story
beyond JMX-over-RMI port forwarding. Spectra's JVM subsystem is
purpose-built for the workflow that exists today:

- **Remote-by-default.** Same `spectra connect work-mac` portal as the
  rest of Spectra ([../design/remote-portal.md](../design/remote-portal.md)).
- **Persistent.** Heap dumps and JFR recordings stored in the blob
  cache; reusable across sessions, comparable across time.
- **Catalog-driven.** Recommendations engine fires JVM-specific rules
  (EOL versions, heap-vs-RAM ratios, GC pressure) the same way as
  every other inspection ([../design/recommendations-engine.md](../design/recommendations-engine.md)).

## Implementation order

1. JDK installation discovery (no live JVM required).
2. `jps`-based process discovery + JDK attribution.
3. `jcmd`-based read-only collectors (system props, command line,
   thread dump, class histogram).
4. Heap dump + JFR capture into the blob store.
5. Java agent JAR for in-process capabilities.
6. JFR file parser (the binary format is documented; OpenJDK has a
   reference implementation worth reading).
