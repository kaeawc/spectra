# JVM inspection

Spectra implements the JDK shell-tool layer of JVM inspection today:
running-process discovery, structured per-PID metadata, thread dumps,
heap histograms, heap dumps, one-shot GC stats, VM-internal memory
diagnostics, local JMX management-agent control, and JFR start/dump/stop.
The JVM snapshot includes one-shot `jstat -gc` counters for each running
JVM when `jstat` is available. It also parses `jfr summary` output into
structured event counts and attributes running JVMs back to the
installed-JDK inventory when `java.home` matches a discovered JDK path.
The optional in-process `spectra-agent.jar` adds direct MBean enumeration
and lightweight in-process probes after an explicit attach step.

Heap forensics beyond capture is intentionally split into reusable
application-runtime interfaces before it grows a command surface.
`internal/heap` defines generic heap records, snapshots, parsers, and
analyzers that JVM, native, Node, Python, WebKit, and future collectors can
adapt to. The JVM adapter parses `jcmd GC.class_histogram` output into
structured rows, compares histogram snapshots, and ranks shallow-size and
growth suspects. It does not parse `.hprof` object graphs yet, so dominator
trees, retained sizes, paths-to-GC-roots, object queries, and heap-dump
diffing remain future work.

Spectra is intended to **supplant VisualVM** for the day-to-day
"what's this Java process doing" question. JVM inspection is a
first-class subsystem alongside the app scanner, not a bolt-on.

Entry points:

```bash
spectra jvm
spectra jvm --json
spectra jvm <pid>
spectra jvm explain [--samples 1] [--interval 1s] <pid>
spectra jvm thread-dump <pid>
spectra jvm heap-histogram <pid>
spectra jvm heap-dump [--out <path>] <pid>
spectra jvm gc-stats [--json] <pid>
spectra jvm vm-memory [--json] <pid>
spectra jvm jmx status [--json] <pid>
spectra jvm jmx start-local [--json] <pid>
spectra jvm attach [--agent <spectra-agent.jar>] [--json] <pid>
spectra jvm mbeans [--json] <pid>
spectra jvm mbean-read [--json] <pid> <object-name> <attribute>
spectra jvm mbean-invoke [--json] <pid> <object-name> <zero-arg-operation>
spectra jvm probe [--json] <pid>
spectra jvm flamegraph [--event cpu] [--duration 30] [--out <path>] <pid>
spectra jvm jfr start <pid> [--name spectra]
spectra jvm jfr dump <pid> [--name spectra] [--out <path>]
spectra jvm jfr stop <pid> [--name spectra] [--out <path>]
spectra jvm jfr summary [--json] <recording.jfr>
```

Daemon methods:

- `jvm.list`
- `jvm.inspect`
- `jvm.explain`
- `jvm.thread_dump` / `jvm.threadDump`
- `jvm.heap_histogram` / `jvm.heapHistogram`
- `jvm.heap_dump` / `jvm.heapDump`
- `jvm.gc_stats` / `jvm.gcStats`
- `jvm.vm_memory` / `jvm.vmMemory`
- `jvm.jmx.status`
- `jvm.jmx.start_local` / `jvm.jmx.startLocal`
- `jvm.attach`
- `jvm.mbeans`
- `jvm.mbean.read`
- `jvm.mbean.invoke`
- `jvm.probe`
- `jvm.flamegraph`
- `jvm.jfr.start`
- `jvm.jfr.dump`
- `jvm.jfr.stop`
- `jvm.jfr.summary`
- `jdk.list`
- `jdk.scan`

## Two implementation layers

VisualVM's capabilities split cleanly into "what the JDK gives you for
free" and "what requires injecting code into the target JVM." Spectra
implements the first layer today and defines the second as the agent
boundary.

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
| Heap layout | `jcmd <pid> GC.heap_info` |
| Metaspace / compressed class space | `jcmd <pid> VM.metaspace` |
| Native memory tracking | `jcmd <pid> VM.native_memory summary` |
| Classloader metadata | `jcmd <pid> VM.classloader_stats` |
| JIT code cache | `jcmd <pid> Compiler.codecache`, `Compiler.CodeHeap_Analytics` |
| Local JMX connector | `jcmd <pid> ManagementAgent.status`, `ManagementAgent.start_local` |
| Flamegraph capture | `asprof -d <seconds> -e <event> -f <path> <pid>` |
| JFR start / stop / dump | `jcmd <pid> JFR.start`, `JFR.dump` |
| JFR summary | `jfr summary <recording.jfr>` |

`spectra jvm` and `spectra jvm <pid>` parse output into structured JSON
when `--json` is passed. `spectra jvm jfr summary --json <file>` returns
recording metadata plus per-event counts and byte sizes. Thread dump text
and JFR dump files are copied into the
[sharded blob store](../design/storage.md) when the cache stores are
initialized. Heap dumps are written to the requested path or to
`~/.spectra/<pid>-<timestamp>.hprof`.

`spectra jvm vm-memory --json <pid>` returns every memory section with
its underlying command, output, and per-section error. Native memory
tracking, for example, only reports detailed categories when the target JVM
was started with `-XX:NativeMemoryTracking=summary` or `detail`; Spectra
keeps the NMT error alongside successful metaspace, classloader, heap, and
code-cache sections instead of failing the whole inspection.

`spectra jvm jmx start-local <pid>` starts the JVM's local management
agent. That makes the same local JMX connector available to jconsole and
VisualVM-style tools, and is the bridge Spectra will use for live MBean
browsing once the in-process agent is present.

`spectra jvm flamegraph <pid>` drives async-profiler when `asprof` is
installed. This is an explicit capture action because async-profiler loads
its own native agent into the target JVM. Output defaults to an HTML file
under `~/.spectra/`; `--event wall`, `--event alloc`, and `--event lock`
can be used for non-CPU profiles.

`spectra jvm explain <pid>` turns the raw diagnostics into findings:

- metaspace and classloader footprint, with clear language that a leak
  requires growth over time;
- JVM-argument checks for heap sizing, GC selection, OOM heap dumps,
  native memory tracking, and soft-reference policy;
- code-cache use, including nmethod/adaptor counts and `full_count`;
- soft-reference evidence from the class histogram;
- native-memory availability, including why fragmentation cannot be judged
  when NMT is off;
- optional repeated `jstat` samples via `--samples` and `--interval` for
  short-window old-gen/metaspace/full-GC trends.

### Layer 2 — Java agent (in-process)

The in-process layer is a small Java agent built from `agent/`:

```bash
make agent
spectra jvm attach <pid>
spectra jvm attach --transport unix <pid>
spectra jvm attach --counter heap=java.lang:type=Memory:HeapMemoryUsage <pid>
spectra jvm mbeans <pid>
spectra jvm mbean-read <pid> java.lang:type=Memory HeapMemoryUsage
spectra jvm mbean-invoke <pid> java.lang:type=Memory gc
spectra jvm probe <pid>
```

The attach command runs the JDK Attach API through the agent JAR's
`AttachMain` entry point:

```bash
java --add-modules jdk.attach \
  -cp spectra-agent.jar com.spectra.agent.AttachMain <pid> spectra-agent.jar
```

Once loaded, the agent starts a loopback-only HTTP endpoint inside the
target JVM by default, publishes `spectra.agent.port` and
`spectra.agent.token` as system properties, and requires the token on every
request. `spectra jvm attach --transport unix` starts the same control
protocol on a Unix-domain socket and publishes `spectra.agent.socket`
instead. Spectra reads those properties with
`jcmd <pid> VM.system_properties` and uses the endpoint to enumerate the
platform MBean server or fetch lightweight probes.

Implemented agent endpoints:

| Endpoint | CLI | Capability |
|---|---|---|
| `/mbeans` | `spectra jvm mbeans <pid>` | MBean names, implementation class names, attributes, and operation signatures |
| `/mbean-attribute` | `spectra jvm mbean-read <pid> ...` | One MBean attribute value |
| `/mbean-operation` | `spectra jvm mbean-invoke <pid> ...` | Explicit zero-argument MBean operation invocation |
| `/probes` | `spectra jvm probe <pid>` | Runtime memory, processor count, live thread count, named counters, and workflow probes |

Named counters are opt-in at attach time with repeatable `--counter`
definitions in the form `name=object-name:attribute`. Workflow probes group
counter definitions with repeatable `--workflow` values such as
`memory=heap=java.lang:type=Memory:HeapMemoryUsage+threads=java.lang:type=Threading:ThreadCount`.

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
  rest of Spectra over explicit TCP or managed `tsnet`
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
3. JDK shell-tool diagnostics: `jcmd`, `jstat`, JFR, local JMX control, and
   async-profiler orchestration.
3. `jcmd`-based system properties, command-line parsing, VM flags,
   thread count, thread dump, class histogram, heap dump, and JFR control.
4. `jstat -gc` one-shot GC counter parsing and snapshot attachment.
5. `jcmd`-based VM memory diagnostics for heap layout, metaspace, native
   memory tracking, classloader stats, and code cache/code heap state.
6. Local JMX management-agent status/start commands.
7. Async-profiler flamegraph capture when `asprof` is installed.
8. `spectra jvm explain` interpretation for JVM args, GC pressure,
   metaspace/classloader footprint, code cache, soft references, native
   memory tracking, and short-window trends.
9. CLI and daemon RPC surfaces for the implemented collectors.
10. Path-based attribution from each running JVM's `java.home` to the
   installed-JDK inventory.
11. `jfr summary` parsing for structured recording metadata and event
   counts.
12. JVM GC-pressure recommendations from old-generation occupancy and
   full-GC counters.
13. Optional Java Attach API agent with MBean browsing, MBean attribute
    reads, zero-argument operation invocation, in-process probes, named
    counters, workflow probes, and optional Unix-domain-socket transport.
