//! The sample's JVM tools: what each `spectra jvm` command printed for the sample's leaking
//! checkout server (pid 4101), and the scenes that show its page with them.

use crate::app::App;
use crate::groups::Nav;
use crate::inspect::RunState;
use crate::jvm_tools::{HistogramReport, Recording, ThreadState, read};
use crate::tools::{Report, Tool};

/// The sample's leaking JVM.
pub const CHECKOUT: u32 = 4101;

const THREADS: &str = r#"{"summary":{"total":14,"daemon":9,"virtual":0,
    "by_state":{"BLOCKED":2,"RUNNABLE":5,"TIMED_WAITING":1,"WAITING":6},"deadlocks":1},
  "dump":{"runtime":"jvm","captured_at":"2026-11-20T11:58:02Z","threads":[
    {"name":"http-nio-8080-exec-7","state":"BLOCKED","category":"monitor","detail":"on object monitor","lock":"<0x00000007a1c2e018>",
     "top_frame":"com.acme.checkout.inventory.StockLedger.reserve(StockLedger.java:212)",
     "stack":["at com.acme.checkout.inventory.StockLedger.reserve(StockLedger.java:212)","at com.acme.checkout.cart.CartService.checkout(CartService.java:88)",
              "at com.acme.checkout.web.CheckoutController.submit(CheckoutController.java:41)","at org.apache.tomcat.util.threads.TaskThread$WrappingRunnable.run(TaskThread.java:61)",
              "at java.lang.Thread.run(java.base@21.0.5/Thread.java:1583)"]},
    {"name":"http-nio-8080-exec-8","state":"BLOCKED","category":"monitor","detail":"on object monitor","lock":"<0x00000007a1c2e2f0>",
     "top_frame":"com.acme.checkout.pricing.PriceCache.refresh(PriceCache.java:97)",
     "stack":["at com.acme.checkout.pricing.PriceCache.refresh(PriceCache.java:97)","at com.acme.checkout.inventory.StockLedger.restock(StockLedger.java:140)",
              "at com.acme.checkout.web.AdminController.restock(AdminController.java:63)","at org.apache.tomcat.util.threads.TaskThread$WrappingRunnable.run(TaskThread.java:61)",
              "at java.lang.Thread.run(java.base@21.0.5/Thread.java:1583)"]},
    {"name":"http-nio-8080-exec-1","state":"WAITING","category":"park","daemon":true,"top_frame":"jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)",
     "stack":["at jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)","at java.util.concurrent.locks.LockSupport.park(java.base@21.0.5/LockSupport.java:371)",
              "at java.util.concurrent.LinkedBlockingQueue.take(java.base@21.0.5/LinkedBlockingQueue.java:435)",
              "at org.apache.tomcat.util.threads.ThreadPoolExecutor.getTask(ThreadPoolExecutor.java:1114)","at java.lang.Thread.run(java.base@21.0.5/Thread.java:1583)"]},
    {"name":"http-nio-8080-exec-2","state":"WAITING","category":"park","daemon":true,"top_frame":"jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)",
     "stack":["at jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)","at java.util.concurrent.locks.LockSupport.park(java.base@21.0.5/LockSupport.java:371)",
              "at java.util.concurrent.LinkedBlockingQueue.take(java.base@21.0.5/LinkedBlockingQueue.java:435)",
              "at org.apache.tomcat.util.threads.ThreadPoolExecutor.getTask(ThreadPoolExecutor.java:1114)","at java.lang.Thread.run(java.base@21.0.5/Thread.java:1583)"]},
    {"name":"http-nio-8080-exec-3","state":"WAITING","category":"park","daemon":true,"top_frame":"jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)",
     "stack":["at jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)","at java.util.concurrent.locks.LockSupport.park(java.base@21.0.5/LockSupport.java:371)",
              "at java.util.concurrent.LinkedBlockingQueue.take(java.base@21.0.5/LinkedBlockingQueue.java:435)",
              "at org.apache.tomcat.util.threads.ThreadPoolExecutor.getTask(ThreadPoolExecutor.java:1114)","at java.lang.Thread.run(java.base@21.0.5/Thread.java:1583)"]},
    {"name":"http-nio-8080-exec-4","state":"WAITING","category":"park","daemon":true,"top_frame":"jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)",
     "stack":["at jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)","at java.util.concurrent.locks.LockSupport.park(java.base@21.0.5/LockSupport.java:371)",
              "at java.util.concurrent.LinkedBlockingQueue.take(java.base@21.0.5/LinkedBlockingQueue.java:435)",
              "at org.apache.tomcat.util.threads.ThreadPoolExecutor.getTask(ThreadPoolExecutor.java:1114)","at java.lang.Thread.run(java.base@21.0.5/Thread.java:1583)"]},
    {"name":"http-nio-8080-exec-5","state":"WAITING","category":"park","daemon":true,"top_frame":"jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)",
     "stack":["at jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)","at java.util.concurrent.locks.LockSupport.park(java.base@21.0.5/LockSupport.java:371)",
              "at java.util.concurrent.LinkedBlockingQueue.take(java.base@21.0.5/LinkedBlockingQueue.java:435)",
              "at org.apache.tomcat.util.threads.ThreadPoolExecutor.getTask(ThreadPoolExecutor.java:1114)","at java.lang.Thread.run(java.base@21.0.5/Thread.java:1583)"]},
    {"name":"main","state":"WAITING","category":"wait","top_frame":"java.lang.Object.wait0(java.base@21.0.5/Native Method)",
     "stack":["at java.lang.Object.wait0(java.base@21.0.5/Native Method)","at java.lang.Thread.join(java.base@21.0.5/Thread.java:2072)",
              "at org.apache.catalina.startup.Catalina.await(Catalina.java:812)","at com.acme.checkout.Server.main(Server.java:24)"]},
    {"name":"HikariPool-1 housekeeper","state":"TIMED_WAITING","category":"park","daemon":true,"top_frame":"jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)",
     "stack":["at jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)","at java.util.concurrent.locks.LockSupport.parkNanos(java.base@21.0.5/LockSupport.java:269)",
              "at java.util.concurrent.ScheduledThreadPoolExecutor$DelayedWorkQueue.take(java.base@21.0.5/ScheduledThreadPoolExecutor.java:1182)"]},
    {"name":"kafka-producer-network-thread | checkout","state":"RUNNABLE","category":"running","daemon":true,
     "top_frame":"sun.nio.ch.KQueue.poll(java.base@21.0.5/Native Method)",
     "stack":["at sun.nio.ch.KQueue.poll(java.base@21.0.5/Native Method)","at sun.nio.ch.KQueueSelectorImpl.doSelect(java.base@21.0.5/KQueueSelectorImpl.java:125)",
              "at org.apache.kafka.common.network.Selector.poll(Selector.java:507)","at org.apache.kafka.clients.producer.internals.Sender.run(Sender.java:239)"]},
    {"name":"http-nio-8080-Poller","state":"RUNNABLE","category":"running","daemon":true,"top_frame":"sun.nio.ch.KQueue.poll(java.base@21.0.5/Native Method)",
     "stack":["at sun.nio.ch.KQueue.poll(java.base@21.0.5/Native Method)","at sun.nio.ch.KQueueSelectorImpl.doSelect(java.base@21.0.5/KQueueSelectorImpl.java:125)",
              "at org.apache.tomcat.util.net.NioEndpoint$Poller.run(NioEndpoint.java:796)"]},
    {"name":"Reference Handler","state":"RUNNABLE","category":"running","daemon":true,"top_frame":"java.lang.ref.Reference.waitForReferencePendingList(java.base@21.0.5/Native Method)",
     "stack":["at java.lang.ref.Reference.waitForReferencePendingList(java.base@21.0.5/Native Method)"]},
    {"name":"Signal Dispatcher","state":"RUNNABLE","category":"running","daemon":true,"stack":[]},
    {"name":"C2 CompilerThread0","state":"RUNNABLE","category":"running","daemon":true,"stack":[]}],
   "deadlocks":[{"threads":["http-nio-8080-exec-7","http-nio-8080-exec-8"],
     "locks":["<0x00000007a1c2e018> (a com.acme.checkout.pricing.PriceCache)","<0x00000007a1c2e2f0> (a com.acme.checkout.inventory.StockLedger)"],
     "waits":[{"thread":"http-nio-8080-exec-7","lock":"<0x00000007a1c2e018> (a com.acme.checkout.pricing.PriceCache)","held_by":"http-nio-8080-exec-8"},
              {"thread":"http-nio-8080-exec-8","lock":"<0x00000007a1c2e2f0> (a com.acme.checkout.inventory.StockLedger)","held_by":"http-nio-8080-exec-7"}]}]}}"#;

/// Two histograms a few minutes apart: the session cache grows.
const HISTOGRAM_BEFORE: &str = r#"{"histogram":{"entries":[
    {"rank":1,"instances":412000,"bytes":402653184,"class_name":"[B","module":"java.base@21.0.5"},
    {"rank":2,"instances":1810000,"bytes":43440000,"class_name":"java.lang.String","module":"java.base@21.0.5"},
    {"rank":3,"instances":520000,"bytes":24960000,"class_name":"java.util.HashMap$Node","module":"java.base@21.0.5"},
    {"rank":4,"instances":210000,"bytes":16800000,"class_name":"com.acme.checkout.session.CartSnapshot"},
    {"rank":5,"instances":96000,"bytes":12288000,"class_name":"[Ljava.util.HashMap$Node;","module":"java.base@21.0.5"},
    {"rank":6,"instances":180000,"bytes":5760000,"class_name":"java.util.concurrent.ConcurrentHashMap$Node","module":"java.base@21.0.5"},
    {"rank":7,"instances":15000,"bytes":4320000,"class_name":"[Ljava.lang.Object;","module":"java.base@21.0.5"},
    {"rank":8,"instances":42000,"bytes":2016000,"class_name":"com.acme.checkout.pricing.Price"}],
  "total":{"instances":3285000,"bytes":512237184,"class_name":"Total"}},
  "suspects":[]}"#;

const HISTOGRAM: &str = r#"{"histogram":{"entries":[
    {"rank":1,"instances":498000,"bytes":486539264,"class_name":"[B","module":"java.base@21.0.5"},
    {"rank":2,"instances":2105000,"bytes":50520000,"class_name":"java.lang.String","module":"java.base@21.0.5"},
    {"rank":3,"instances":381000,"bytes":30480000,"class_name":"com.acme.checkout.session.CartSnapshot"},
    {"rank":4,"instances":560000,"bytes":26880000,"class_name":"java.util.HashMap$Node","module":"java.base@21.0.5"},
    {"rank":5,"instances":99000,"bytes":12672000,"class_name":"[Ljava.util.HashMap$Node;","module":"java.base@21.0.5"},
    {"rank":6,"instances":181000,"bytes":5792000,"class_name":"java.util.concurrent.ConcurrentHashMap$Node","module":"java.base@21.0.5"},
    {"rank":7,"instances":15000,"bytes":4320000,"class_name":"[Ljava.lang.Object;","module":"java.base@21.0.5"},
    {"rank":8,"instances":40000,"bytes":1920000,"class_name":"com.acme.checkout.pricing.Price"}],
  "total":{"instances":3879000,"bytes":619123264,"class_name":"Total"}},
  "suspects":[
    {"class_name":"[B","reason":"largest live shallow size in class histogram","instances":498000,"bytes":486539264},
    {"class_name":"com.acme.checkout.session.CartSnapshot","reason":"application class with high instance count","instances":381000,"bytes":30480000}]}"#;

const GC: &str = r#"{"s0c":0,"s1c":16384,"s0u":0,"s1u":16384,"ec":245760,"eu":81920,"oc":1835008,"ou":1670000,
    "mc":61440,"mu":60000,"ccsc":8192,"ccsu":7300,"ygc":64,"ygct":1.13,"fgc":6,"fgct":4.2,"cgc":18,"cgct":0.41,"gct":5.74}"#;

const NMT: &str = r#"{"enabled":true,"total_reserved_kib":4380000,"total_committed_kib":2512000,"categories":[
    {"name":"Java Heap","reserved_kib":2097152,"committed_kib":2097152},
    {"name":"Thread","reserved_kib":112000,"committed_kib":112000},
    {"name":"Class","reserved_kib":1056000,"committed_kib":98000},
    {"name":"GC","reserved_kib":180000,"committed_kib":96000},
    {"name":"Code","reserved_kib":250000,"committed_kib":52000},
    {"name":"Internal","reserved_kib":31000,"committed_kib":31000},
    {"name":"Symbol","reserved_kib":19000,"committed_kib":19000},
    {"name":"Other","reserved_kib":7000,"committed_kib":7000}]}"#;

const VM_MEMORY: &str = r#"{"pid":4101,
    "heap_info":{"output":"4101:\n garbage-first heap   total 2097152K, used 1768304K [0x0000000780000000, 0x0000000800000000)\n  region size 1024K, 80 young (81920K), 16 survivors (16384K)\n Metaspace       used 60000K, committed 61440K, reserved 1114112K\n  class space    used 7300K, committed 8192K, reserved 1048576K\n"},
    "code_cache":{"output":"4101:\nCodeCache: size=245760Kb, used=51800Kb, max_used=52240Kb, free=193960Kb\n total_blobs=14210, nmethods=11980, adapters=1460, full_count=0\n"}}"#;

const EXPLAIN: &str = r#"{"pid":4101,"jdk_vendor":"Temurin","jdk_version":"21.0.5","vm_args":"-Xmx2g -Xms512m -XX:+UseG1GC",
  "observations":[
    {"id":"old-gen-occupancy","severity":"warning","summary":"Old generation is 91% used.","evidence":"1670000 KiB used / 1835008 KiB capacity",
     "recommendation":"Take two heap histograms a few minutes apart and compare what grows; raise -Xmx only once you know it isn't a leak."},
    {"id":"full-gc-count","severity":"warning","summary":"Full GC has run 6 times.","evidence":"4.200s full-GC time, 5.740s total GC time"},
    {"id":"oom-dump-disabled","severity":"info","summary":"It won't leave a heap dump if it runs out of memory.","evidence":"-XX:+HeapDumpOnOutOfMemoryError is not set",
     "recommendation":"Add -XX:+HeapDumpOnOutOfMemoryError so the next crash leaves evidence."},
    {"id":"code-cache-use","severity":"info","summary":"Code cache is 21% used.","evidence":"51800 KiB used / 245760 KiB reserved"},
    {"id":"soft-refs-none","severity":"good","summary":"No live SoftReference instances were visible in the class histogram."}]}"#;

const HEAP_DUMP: &str = "/Users/user/Downloads/Server-4101-20261120T115800Z.hprof\n";

const LEAKS: &str = r#"{"report":{"total_shallow_bytes":619123264,"reachable_objects":3879000,"reachable_bytes":601000000,"threshold_percent":10,
  "suspects":[{"kind":"object","class_name":"com.acme.checkout.session.SessionCache","instances":1,"retained_bytes":402000000,"percent_of_heap":66.9,
    "accumulation_point":{"class_name":"java.util.concurrent.ConcurrentHashMap","retained_bytes":401500000,"percent_of_heap":66.8,"direct_children":381000},
    "path_to_root":{"roots":[{"kind":"sticky_class"}],"steps":[{"class_name":"class com.acme.checkout.session.SessionCache"},
      {"class_name":"com.acme.checkout.session.SessionCache"},{"class_name":"java.util.concurrent.ConcurrentHashMap"},{"class_name":"[Ljava.util.concurrent.ConcurrentHashMap$Node;"}]},
    "description":"SessionCache retains 383 MiB."}]},"findings":[]}"#;

const DOMINATORS: &str = r#"{"total_shallow_bytes":619123264,"reachable_objects":3879000,"reachable_bytes":601000000,
  "suspects":[{"class_name":"com.acme.checkout.session.SessionCache","shallow_bytes":32,"retained_bytes":402000000,"percent_of_heap":66.9},
              {"class_name":"org.apache.catalina.core.StandardContext","shallow_bytes":512,"retained_bytes":61000000,"percent_of_heap":10.1},
              {"class_name":"com.acme.checkout.pricing.PriceCache","shallow_bytes":48,"retained_bytes":22000000,"percent_of_heap":3.7}],
  "classes":[{"class_name":"[B","instances":498000,"shallow_bytes":486539264,"retained_bytes":486539264,"percent_of_heap":81.0},
             {"class_name":"com.acme.checkout.session.CartSnapshot","instances":381000,"shallow_bytes":30480000,"retained_bytes":388000000,"percent_of_heap":64.6},
             {"class_name":"java.lang.String","instances":2105000,"shallow_bytes":50520000,"retained_bytes":120000000,"percent_of_heap":20.0}]}"#;

const JFR: &str = r#"{"path":"/Users/user/Downloads/Server-4101-20261120T120130Z.jfr","version":"2.1","chunks":1,"start":"2026-11-20 12:00:00 (UTC)","duration":"90 s",
  "events":[{"type":"jdk.SystemProcess","count":4671,"size_bytes":1011893},{"type":"jdk.ExecutionSample","count":4420,"size_bytes":88400},
            {"type":"jdk.ObjectAllocationSample","count":2980,"size_bytes":71520},{"type":"jdk.ThreadPark","count":1904,"size_bytes":60928},
            {"type":"jdk.JavaMonitorEnter","count":312,"size_bytes":9984},{"type":"jdk.GarbageCollection","count":41,"size_bytes":1640},
            {"type":"jdk.GCPhaseParallel","count":1230,"size_bytes":31000},{"type":"jdk.SocketRead","count":640,"size_bytes":25600},
            {"type":"jdk.BooleanFlag","count":980,"size_bytes":30808},{"type":"jdk.JavaExceptionThrow","count":88,"size_bytes":4400}]}"#;

/// What each JVM tool printed for the checkout server; `{}` for the rest.
pub fn output(tool: Tool) -> &'static str {
    match tool {
        Tool::Threads => THREADS,
        Tool::Histogram => HISTOGRAM,
        Tool::Gc => GC,
        Tool::NativeMemory => NMT,
        Tool::Explain => EXPLAIN,
        Tool::HeapDump => HEAP_DUMP,
        Tool::LeakSuspects => LEAKS,
        Tool::Dominators => DOMINATORS,
        Tool::JfrStart => "JFR recording \"spectra-desktop\" started on PID 4101\n",
        Tool::JfrStop => JFR,
        _ => "{}",
    }
}

fn done(s: &mut App, tool: Tool) {
    let report = tool.parse(output(tool)).expect("the sample output parses");
    s.inspect.put(tool, RunState::Done(Box::new(report)));
}

/// The checkout server's page: its JVM tools lead its tools.
pub fn jvm_tools_scene(s: &mut App) {
    crate::sample::sample_scene(s);
    s.expanded = None;
    s.show(Nav::All);
    s.open(CHECKOUT);
    crate::sample::sample_live(s);
}

/// Its thread dump with no deadlock, one group open.
pub fn tool_threads_scene(s: &mut App) {
    jvm_tools_scene(s);
    let Ok(Report::Threads(mut r)) = Tool::Threads.parse(THREADS) else {
        panic!("the sample thread dump")
    };
    r.dump.deadlocks.clear();
    r.summary.deadlocks = 0;
    s.inspect
        .put(Tool::Threads, RunState::Done(Box::new(Report::Threads(r))));
    s.inspect
        .jvm
        .threads_open
        .insert("http-nio-8080-exec-1".into());
}

/// Its thread dump with two request threads deadlocked, filtered to the blocked ones.
pub fn tool_threads_deadlock_scene(s: &mut App) {
    jvm_tools_scene(s);
    done(s, Tool::Threads);
    s.inspect
        .jvm
        .threads_open
        .insert("http-nio-8080-exec-7".into());
    s.inspect.jvm.threads_state = Some(ThreadState::Blocked);
}

/// Its second heap histogram, compared with the first.
pub fn tool_histogram_scene(s: &mut App) {
    jvm_tools_scene(s);
    s.inspect.jvm.prev_histogram =
        Some(read::<HistogramReport>(HISTOGRAM_BEFORE).expect("the earlier histogram"));
    done(s, Tool::Histogram);
}

pub fn tool_gc_scene(s: &mut App) {
    jvm_tools_scene(s);
    done(s, Tool::Gc);
}

/// Native memory with NMT on, and the JVM's own view of its heap.
pub fn tool_nmt_scene(s: &mut App) {
    jvm_tools_scene(s);
    let r = crate::jvm_tools::native_memory(Ok(NMT.into()), Ok(VM_MEMORY.into()))
        .expect("the sample native memory");
    s.inspect.put(
        Tool::NativeMemory,
        RunState::Done(Box::new(Report::NativeMemory(r))),
    );
}

/// Native memory with NMT off: Spectra's reason and how to turn it on.
pub fn tool_nmt_off_scene(s: &mut App) {
    jvm_tools_scene(s);
    let off = "native memory tracking is not enabled; restart the JVM with -XX:NativeMemoryTracking=summary (or =detail for call sites)";
    let r = crate::jvm_tools::native_memory(Err(off.into()), Ok(VM_MEMORY.into()))
        .expect("the sample native memory");
    s.inspect.put(
        Tool::NativeMemory,
        RunState::Done(Box::new(Report::NativeMemory(r))),
    );
}

pub fn tool_explain_scene(s: &mut App) {
    jvm_tools_scene(s);
    done(s, Tool::Explain);
}

/// A heap dump taken, with its leak suspects and dominators read.
pub fn tool_heapdump_scene(s: &mut App) {
    jvm_tools_scene(s);
    done(s, Tool::HeapDump);
    done(s, Tool::LeakSuspects);
    done(s, Tool::Dominators);
}

/// A flight recording running for a minute and a half.
pub fn tool_jfr_scene(s: &mut App) {
    jvm_tools_scene(s);
    done(s, Tool::JfrStart);
    s.inspect.jvm.recording = Some(Recording {
        pid: CHECKOUT,
        since: s.now - 90.0,
    });
}

/// A flight recording stopped and summarised.
pub fn tool_jfr_summary_scene(s: &mut App) {
    jvm_tools_scene(s);
    done(s, Tool::JfrStart);
    done(s, Tool::JfrStop);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_sample_jvm_output_parses() {
        for tool in Tool::ALL
            .into_iter()
            .filter(|t| t.kind() == crate::tools::Kind::Jvm)
        {
            assert!(tool.parse(output(tool)).is_ok(), "{tool:?}");
        }
        assert!(read::<HistogramReport>(HISTOGRAM_BEFORE).is_ok());
        assert!(read::<crate::jvm_tools::VmMemory>(VM_MEMORY).is_ok());
    }
}
