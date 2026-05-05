# JVM inspection

Spectra implements the JDK shell-tool layer of JVM inspection today:
running-process discovery, structured per-PID metadata, thread dumps,
heap histograms, heap dumps, one-shot GC stats, and JFR start/dump/stop.
It also parses `jfr summary` output into structured event counts and
attributes running JVMs back to the installed-JDK inventory when
`java.home` matches a discovered JDK path. The in-process Java agent layer
remains future work.

Spectra is intended to **supplant VisualVM** for the day-to-day
"what's this Java process doing" question. JVM inspection is a
first-class subsystem alongside the app scanner, not a bolt-on.

Entry points:

```bash
spectra jvm
spectra jvm --json
spectra jvm <pid>
spectra jvm thread-dump <pid>
spectra jvm heap-histogram <pid>
spectra jvm heap-dump [--out <path>] <pid>
spectra jvm gc-stats [--json] <pid>
spectra jvm jfr start <pid> [--name spectra]
spectra jvm jfr dump <pid> [--name spectra] [--out <path>]
spectra jvm jfr stop <pid> [--name spectra] [--out <path>]
spectra jvm jfr summary [--json] <recording.jfr>
```

Daemon methods:

- `jvm.list`
- `jvm.inspect`
- `jvm.thread_dump` / `jvm.threadDump`
- `jvm.heap_histogram` / `jvm.heapHistogram`
- `jvm.heap_dump` / `jvm.heapDump`
- `jvm.gc_stats` / `jvm.gcStats`
- `jvm.jfr.start`
- `jvm.jfr.dump`
- `jvm.jfr.stop`
- `jvm.jfr.summary`
- `jdk.list`
- `jdk.scan`

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
| GC stats snapshot | `jstat -gc <pid>` |
| Heap histogram | `jcmd <pid> GC.class_histogram` |
| Heap dump | `jcmd <pid> GC.heap_dump <path>` |
| JFR start / stop / dump | `jcmd <pid> JFR.start`, `JFR.dump` |
| JFR summary | `jfr summary <recording.jfr>` |

`spectra jvm` and `spectra jvm <pid>` parse output into structured JSON
when `--json` is passed. `spectra jvm jfr summary --json <file>` returns
recording metadata plus per-event counts and byte sizes. Thread dump text
and JFR dump files are copied into the
[sharded blob store](../design/storage.md) when the cache stores are
initialized. Heap dumps are written to the requested path or to
`~/.spectra/<pid>-<timestamp>.hprof`.

### Layer 2 — Java agent (in-process)

The remaining capabilities require code running inside the target JVM and
are not implemented yet:

- Live MBean browsing (JMX over RMI).
- Async-profiler-grade flame graphs (bytecode instrumentation).
- Custom counters and probes.

The planned shape is a small `spectra-agent.jar` alongside the Go binary,
attached on demand through the Attach API:

```bash
spectra jvm attach <pid>      # loads the agent into a running JVM
spectra jvm flamegraph <pid>  # async-profiler over the agent
```

The agent would expose its capabilities over a Unix socket so the Go daemon
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

Running Java processes expose their `java.home`, `java.vendor`, and
`java.version` properties through `jcmd VM.system_properties`. Spectra
prints and serializes those fields today, and uses `java.home` to attach
`jdk_install_id`, `jdk_source`, and `jdk_path` when the running JVM
matches a discovered install.

## Sample output

```
$ spectra jvm
PID      VERSION       THREADS   MAIN CLASS
----------------------------------------------------------------------
4012     21.0.6        87        com.intellij.idea.Main
8410     17.0.10       42        org.gradle.launcher.daemon.bootstrap.GradleDaemon

$ spectra jvm 4012
PID           4012
Main class    com.intellij.idea.Main
JDK vendor    JetBrains s.r.o.
JDK version   21.0.6
Java home     /Users/me/Library/Application Support/JetBrains/Toolbox/apps/.../jbr/Contents/Home
JDK install   jbr-toolbox-jetbrains-s-r-o-21.0.6
JDK source    jbr-toolbox
VM args       -Xmx4g -Xms256m ...
VM flags      -XX:+UseG1GC ...
Threads       87
```

## Why this matters

VisualVM is venerable but increasingly painful: NetBeans-platform
Swing app, hard to keep working with modern JDKs, no remote story
beyond JMX-over-RMI port forwarding. Spectra's JVM subsystem is
purpose-built for the workflow that exists today:

- **Remote-by-default.** Same `spectra connect work-mac` portal as the
  rest of Spectra once the remote portal lands
  ([../design/remote-portal.md](../design/remote-portal.md)).
- **Persistent.** Thread dumps and JFR recordings can be stored in the blob
  cache; heap dumps are written as explicit `.hprof` artifacts.
- **Catalog-driven.** Recommendations engine fires JVM-specific rules
  (EOL versions, heap-vs-RAM ratios, GC pressure) the same way as
  every other inspection ([../design/recommendations-engine.md](../design/recommendations-engine.md)).

## Implementation status

Implemented:

1. JDK installation discovery (no live JVM required).
2. `jps`-based running-JVM discovery.
3. `jcmd`-based system properties, command-line parsing, VM flags,
   thread count, thread dump, class histogram, heap dump, and JFR control.
4. `jstat -gc` one-shot GC counter parsing.
5. CLI and daemon RPC surfaces for the implemented collectors.
6. Path-based attribution from each running JVM's `java.home` to the
   installed-JDK inventory.
7. `jfr summary` parsing for structured recording metadata and event
   counts.

Future:

1. Java agent JAR for in-process capabilities.
2. Async-profiler / flamegraph integration.
