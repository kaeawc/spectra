//! A JVM's tools: Spectra's `jvm` commands parsed into what a JVM's process page shows (threads,
//! heap, GC, native memory, Spectra's diagnosis, heap dumps and flight recordings), and the
//! sums the page works out from them, such as a histogram's growth since the last one.

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use serde::Deserialize;

use crate::bundle::nullable;
use crate::findings::Severity;

/// The name the page's flight recording goes by, so stopping it touches no one else's.
pub const RECORDING: &str = "spectra-desktop";

// Thread dump: `spectra jvm thread-dump --json <pid>`.

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct ThreadDumpReport {
    #[serde(deserialize_with = "nullable")]
    pub summary: ThreadSummary,
    #[serde(deserialize_with = "nullable")]
    pub dump: Dump,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct ThreadSummary {
    pub total: u64,
    pub daemon: u64,
    #[serde(rename = "virtual")]
    pub virtual_threads: u64,
    /// `RUNNABLE`, `BLOCKED`, `WAITING`, `TIMED_WAITING`, …
    #[serde(deserialize_with = "nullable")]
    pub by_state: BTreeMap<String, u64>,
    pub deadlocks: u64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Dump {
    #[serde(deserialize_with = "nullable")]
    pub captured_at: String,
    #[serde(deserialize_with = "nullable")]
    pub threads: Vec<JavaThread>,
    #[serde(deserialize_with = "nullable")]
    pub deadlocks: Vec<Deadlock>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct JavaThread {
    #[serde(deserialize_with = "nullable")]
    pub name: String,
    #[serde(deserialize_with = "nullable")]
    pub state: String,
    #[serde(deserialize_with = "nullable")]
    pub category: String,
    #[serde(deserialize_with = "nullable")]
    pub detail: String,
    /// The monitor it waits for, when it's blocked.
    #[serde(deserialize_with = "nullable")]
    pub lock: String,
    #[serde(deserialize_with = "nullable")]
    pub top_frame: String,
    /// Innermost first, each `at …`.
    #[serde(deserialize_with = "nullable")]
    pub stack: Vec<String>,
    #[serde(deserialize_with = "nullable")]
    pub raw_block: String,
    pub daemon: bool,
}

impl JavaThread {
    /// Its frames, innermost first, without the `at `. Spectra's parsed stack can stop short of
    /// the raw block; the longer of the two wins.
    pub fn frames(&self) -> Vec<String> {
        let clean = |l: &str| l.trim().strip_prefix("at ").map(str::to_owned);
        let parsed: Vec<String> = self
            .stack
            .iter()
            .filter_map(|l| clean(l).or_else(|| Some(l.trim().to_owned())))
            .collect();
        let raw: Vec<String> = self.raw_block.lines().filter_map(clean).collect();
        if raw.len() > parsed.len() {
            raw
        } else {
            parsed
        }
    }

    pub fn bucket(&self) -> ThreadState {
        ThreadState::of(&self.state)
    }
}

/// The states a person filters threads by.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum ThreadState {
    Runnable,
    Blocked,
    Waiting,
    TimedWaiting,
    Other,
}

impl ThreadState {
    pub const SHOWN: [ThreadState; 4] = [
        ThreadState::Runnable,
        ThreadState::Blocked,
        ThreadState::Waiting,
        ThreadState::TimedWaiting,
    ];

    pub fn of(state: &str) -> ThreadState {
        match state.trim().to_ascii_uppercase().as_str() {
            "RUNNABLE" => ThreadState::Runnable,
            "BLOCKED" => ThreadState::Blocked,
            "WAITING" => ThreadState::Waiting,
            "TIMED_WAITING" => ThreadState::TimedWaiting,
            _ => ThreadState::Other,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            ThreadState::Runnable => "Runnable",
            ThreadState::Blocked => "Blocked",
            ThreadState::Waiting => "Waiting",
            ThreadState::TimedWaiting => "Timed waiting",
            ThreadState::Other => "Other",
        }
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Deadlock {
    #[serde(deserialize_with = "nullable")]
    pub threads: Vec<String>,
    #[serde(deserialize_with = "nullable")]
    pub locks: Vec<String>,
    #[serde(deserialize_with = "nullable")]
    pub waits: Vec<Wait>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Wait {
    #[serde(deserialize_with = "nullable")]
    pub thread: String,
    #[serde(deserialize_with = "nullable")]
    pub lock: String,
    #[serde(deserialize_with = "nullable")]
    pub held_by: String,
}

impl Deadlock {
    /// Who waits for what, a line each: `a waits for <lock>, held by b`.
    pub fn lines(&self) -> Vec<String> {
        if self.waits.is_empty() {
            return vec![format!(
                "{} wait on {}",
                self.threads.join(", "),
                self.locks.join(", ")
            )];
        }
        self.waits
            .iter()
            .map(|w| {
                let held = if w.held_by.is_empty() {
                    String::new()
                } else {
                    format!(", held by {}", w.held_by)
                };
                format!("{} waits for {}{held}", w.thread, w.lock)
            })
            .collect()
    }
}

/// Threads with the same stack, together.
#[derive(Clone, Debug, PartialEq)]
pub struct ThreadGroup {
    /// Its first thread's name, which stays put when the filter changes.
    pub id: String,
    pub frames: Vec<String>,
    pub names: Vec<String>,
    pub states: Vec<ThreadState>,
    /// Whether any of them is in a deadlock.
    pub deadlocked: bool,
}

impl ThreadGroup {
    pub fn top(&self) -> &str {
        self.frames
            .first()
            .map_or("(no Java frames)", String::as_str)
    }
}

impl ThreadDumpReport {
    /// How many threads are in each state, counted from the dump when the summary has none.
    pub fn count(&self, state: ThreadState) -> u64 {
        if self.summary.by_state.is_empty() {
            return self
                .dump
                .threads
                .iter()
                .filter(|t| t.bucket() == state)
                .count() as u64;
        }
        self.summary
            .by_state
            .iter()
            .filter(|(k, _)| ThreadState::of(k) == state)
            .map(|(_, v)| *v)
            .sum()
    }

    pub fn total(&self) -> u64 {
        if self.summary.total > 0 {
            self.summary.total
        } else {
            self.dump.threads.len() as u64
        }
    }

    pub fn deadlocked(&self) -> Vec<&str> {
        self.dump
            .deadlocks
            .iter()
            .flat_map(|d| d.threads.iter().map(String::as_str))
            .collect()
    }

    /// The threads whose name holds `query` (any case) and, when given, in `state`, grouped by
    /// identical stack: deadlocked groups first, then the biggest.
    pub fn groups(&self, query: &str, state: Option<ThreadState>) -> Vec<ThreadGroup> {
        let q = query.trim().to_lowercase();
        let dead = self.deadlocked();
        let mut by_stack: HashMap<Vec<String>, usize> = HashMap::new();
        let mut out: Vec<ThreadGroup> = Vec::new();
        for t in &self.dump.threads {
            if (!q.is_empty() && !t.name.to_lowercase().contains(&q))
                || state.is_some_and(|s| t.bucket() != s)
            {
                continue;
            }
            let frames = t.frames();
            let i = *by_stack.entry(frames.clone()).or_insert_with(|| {
                out.push(ThreadGroup {
                    id: t.name.clone(),
                    frames,
                    names: Vec::new(),
                    states: Vec::new(),
                    deadlocked: false,
                });
                out.len() - 1
            });
            let g = &mut out[i];
            g.names.push(t.name.clone());
            if !g.states.contains(&t.bucket()) {
                g.states.push(t.bucket());
            }
            g.deadlocked |= dead.contains(&t.name.as_str());
        }
        out.sort_by(|a, b| {
            b.deadlocked
                .cmp(&a.deadlocked)
                .then(b.names.len().cmp(&a.names.len()))
        });
        out
    }
}

// Heap histogram: `spectra jvm heap-histogram --json <pid>`.

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct HistogramReport {
    #[serde(deserialize_with = "nullable")]
    pub histogram: Histogram,
    #[serde(deserialize_with = "nullable")]
    pub suspects: Vec<HistogramSuspect>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Histogram {
    #[serde(deserialize_with = "nullable")]
    pub entries: Vec<ClassCount>,
    #[serde(deserialize_with = "nullable")]
    pub total: ClassCount,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct ClassCount {
    pub rank: u64,
    pub instances: u64,
    pub bytes: u64,
    #[serde(deserialize_with = "nullable")]
    pub class_name: String,
    #[serde(deserialize_with = "nullable")]
    pub module: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct HistogramSuspect {
    #[serde(deserialize_with = "nullable")]
    pub class_name: String,
    #[serde(deserialize_with = "nullable")]
    pub reason: String,
    pub instances: u64,
    pub bytes: u64,
}

/// How one class changed between two histograms.
#[derive(Clone, Debug, PartialEq)]
pub struct Growth {
    pub class_name: String,
    pub bytes: i64,
    pub instances: i64,
    pub bytes_now: u64,
}

impl HistogramReport {
    pub fn total_bytes(&self) -> u64 {
        if self.histogram.total.bytes > 0 {
            self.histogram.total.bytes
        } else {
            self.histogram.entries.iter().map(|e| e.bytes).sum()
        }
    }

    pub fn total_instances(&self) -> u64 {
        if self.histogram.total.instances > 0 {
            self.histogram.total.instances
        } else {
            self.histogram.entries.iter().map(|e| e.instances).sum()
        }
    }

    /// The classes with the most bytes, then with the most instances.
    pub fn by_bytes(&self) -> Vec<&ClassCount> {
        let mut v: Vec<&ClassCount> = self.histogram.entries.iter().collect();
        v.sort_by(|a, b| b.bytes.cmp(&a.bytes).then(a.class_name.cmp(&b.class_name)));
        v
    }

    pub fn by_instances(&self) -> Vec<&ClassCount> {
        let mut v: Vec<&ClassCount> = self.histogram.entries.iter().collect();
        v.sort_by(|a, b| {
            b.instances
                .cmp(&a.instances)
                .then(a.class_name.cmp(&b.class_name))
        });
        v
    }
}

/// What grew from `before` to `now`, by class: the most bytes gained first, unchanged classes
/// left out. A class only in `now` grew by all of it; one gone from `now` shrank by all of it.
pub fn growth(before: &HistogramReport, now: &HistogramReport) -> Vec<Growth> {
    let old: HashMap<&str, &ClassCount> = before
        .histogram
        .entries
        .iter()
        .map(|e| (e.class_name.as_str(), e))
        .collect();
    let mut out: Vec<Growth> = now
        .histogram
        .entries
        .iter()
        .map(|e| {
            let (b, i) = old
                .get(e.class_name.as_str())
                .map_or((0, 0), |o| (o.bytes, o.instances));
            Growth {
                class_name: e.class_name.clone(),
                bytes: e.bytes as i64 - b as i64,
                instances: e.instances as i64 - i as i64,
                bytes_now: e.bytes,
            }
        })
        .collect();
    let here: std::collections::HashSet<&str> = now
        .histogram
        .entries
        .iter()
        .map(|e| e.class_name.as_str())
        .collect();
    for e in &before.histogram.entries {
        if !here.contains(e.class_name.as_str()) {
            out.push(Growth {
                class_name: e.class_name.clone(),
                bytes: -(e.bytes as i64),
                instances: -(e.instances as i64),
                bytes_now: 0,
            });
        }
    }
    out.retain(|g| g.bytes != 0 || g.instances != 0);
    out.sort_by(|a, b| {
        b.bytes
            .cmp(&a.bytes)
            .then(b.instances.cmp(&a.instances))
            .then(a.class_name.cmp(&b.class_name))
    });
    out
}

/// A JVM class name as people read it: `[B` as `byte[]`, `[Ljava.lang.String;` as
/// `java.lang.String[]`.
pub fn class_label(name: &str) -> String {
    let dims = name.chars().take_while(|c| *c == '[').count();
    if dims == 0 {
        return name.to_owned();
    }
    let rest = &name[dims..];
    let base = match rest {
        "B" => "byte",
        "C" => "char",
        "I" => "int",
        "J" => "long",
        "S" => "short",
        "Z" => "boolean",
        "F" => "float",
        "D" => "double",
        r => r
            .strip_prefix('L')
            .and_then(|r| r.strip_suffix(';'))
            .unwrap_or(r),
    };
    format!("{base}{}", "[]".repeat(dims))
}

// GC stats: `spectra jvm gc-stats --json <pid>`, jstat's `-gc` columns, sizes in KiB.

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct GcReport {
    pub s0c: f64,
    pub s1c: f64,
    pub s0u: f64,
    pub s1u: f64,
    pub ec: f64,
    pub eu: f64,
    pub oc: f64,
    pub ou: f64,
    pub mc: f64,
    pub mu: f64,
    pub ccsc: f64,
    pub ccsu: f64,
    pub ygc: u64,
    pub ygct: f64,
    pub fgc: u64,
    pub fgct: f64,
    /// Concurrent cycles, where the collector reports them.
    pub cgc: u64,
    pub cgct: f64,
    pub gct: f64,
}

/// One kind of collection: how many and how long they paused.
#[derive(Clone, Debug, PartialEq)]
pub struct Collector {
    pub name: &'static str,
    pub count: u64,
    pub seconds: f64,
}

impl Collector {
    /// The average pause in milliseconds, none when it never ran.
    pub fn avg_ms(&self) -> Option<f64> {
        (self.count > 0).then(|| self.seconds * 1000.0 / self.count as f64)
    }
}

/// A heap or metaspace area: used of capacity, KiB.
#[derive(Clone, Debug, PartialEq)]
pub struct Area {
    pub name: &'static str,
    pub used_kib: f64,
    pub capacity_kib: f64,
}

impl Area {
    pub fn percent(&self) -> Option<f64> {
        (self.capacity_kib > 0.0).then(|| 100.0 * self.used_kib / self.capacity_kib)
    }
}

impl GcReport {
    pub fn collectors(&self) -> Vec<Collector> {
        let mut v = vec![
            Collector {
                name: "Young",
                count: self.ygc,
                seconds: self.ygct,
            },
            Collector {
                name: "Full",
                count: self.fgc,
                seconds: self.fgct,
            },
        ];
        if self.cgc > 0 || self.cgct > 0.0 {
            v.push(Collector {
                name: "Concurrent",
                count: self.cgc,
                seconds: self.cgct,
            });
        }
        v
    }

    pub fn total_seconds(&self) -> f64 {
        if self.gct > 0.0 {
            self.gct
        } else {
            self.ygct + self.fgct + self.cgct
        }
    }

    pub fn areas(&self) -> Vec<Area> {
        vec![
            Area {
                name: "Eden",
                used_kib: self.eu,
                capacity_kib: self.ec,
            },
            Area {
                name: "Survivors",
                used_kib: self.s0u + self.s1u,
                capacity_kib: self.s0c + self.s1c,
            },
            Area {
                name: "Old",
                used_kib: self.ou,
                capacity_kib: self.oc,
            },
            Area {
                name: "Metaspace",
                used_kib: self.mu,
                capacity_kib: self.mc,
            },
            Area {
                name: "Class space",
                used_kib: self.ccsu,
                capacity_kib: self.ccsc,
            },
        ]
    }
}

/// The collector a JVM's arguments pick, if they pick one: `-XX:+UseG1GC` is G1.
pub fn collector_flag(vm_args: &str) -> Option<&'static str> {
    [
        ("-XX:+UseG1GC", "G1"),
        ("-XX:+UseZGC", "ZGC"),
        ("-XX:+UseShenandoahGC", "Shenandoah"),
        ("-XX:+UseParallelGC", "Parallel"),
        ("-XX:+UseSerialGC", "Serial"),
        ("-XX:+UseEpsilonGC", "Epsilon (no collection)"),
    ]
    .into_iter()
    .find(|(flag, _)| vm_args.contains(flag))
    .map(|(_, name)| name)
}

// Native memory: `spectra jvm nmt --json <pid>` and `spectra jvm vm-memory --json <pid>`.

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Nmt {
    pub enabled: bool,
    pub total_reserved_kib: f64,
    pub total_committed_kib: f64,
    #[serde(deserialize_with = "nullable")]
    pub categories: Vec<NmtCategory>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct NmtCategory {
    #[serde(deserialize_with = "nullable")]
    pub name: String,
    pub reserved_kib: f64,
    pub committed_kib: f64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Section {
    #[serde(deserialize_with = "nullable")]
    pub output: String,
    #[serde(deserialize_with = "nullable")]
    pub error: String,
}

impl Section {
    /// Its lines without jcmd's leading `<pid>:` and blanks.
    pub fn lines(&self) -> Vec<String> {
        self.output
            .lines()
            .map(str::trim_end)
            .filter(|l| !l.trim().is_empty())
            .filter(|l| !l.trim_end_matches(':').chars().all(|c| c.is_ascii_digit()))
            .map(str::to_owned)
            .collect()
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct VmMemory {
    #[serde(deserialize_with = "nullable")]
    pub heap_info: Section,
    #[serde(deserialize_with = "nullable")]
    pub metaspace: Section,
    #[serde(deserialize_with = "nullable")]
    pub code_cache: Section,
}

impl VmMemory {
    /// The heap as the JVM describes it: collector, size, regions, metaspace.
    pub fn heap(&self) -> Vec<String> {
        self.heap_info
            .lines()
            .into_iter()
            .map(|l| l.trim().to_owned())
            .collect()
    }

    /// The code cache's summary line.
    pub fn code_cache(&self) -> Option<String> {
        self.code_cache
            .lines()
            .into_iter()
            .map(|l| l.trim().to_owned())
            .find(|l| l.starts_with("CodeCache:"))
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct NativeMemoryReport {
    /// NMT's categories, or why there are none: Spectra's words.
    pub nmt: Result<Nmt, String>,
    pub vm: Option<VmMemory>,
}

impl NativeMemoryReport {
    /// Why NMT has nothing, when it's because tracking is off.
    pub fn off(&self) -> Option<String> {
        match &self.nmt {
            Ok(n) if !n.enabled => Some(NMT_OFF.into()),
            Ok(_) => None,
            Err(why) if why.to_lowercase().contains("not enabled") => Some(why.clone()),
            Err(_) => None,
        }
    }

    /// Categories, the most committed first.
    pub fn categories(&self) -> Vec<&NmtCategory> {
        let mut v: Vec<&NmtCategory> = self
            .nmt
            .as_ref()
            .map(|n| n.categories.iter().collect())
            .unwrap_or_default();
        v.sort_by(|a, b| b.committed_kib.total_cmp(&a.committed_kib));
        v
    }
}

/// What the page says when NMT is off and Spectra gave no reason of its own.
pub const NMT_OFF: &str =
    "Native memory tracking is not enabled; restart the JVM with -XX:NativeMemoryTracking=summary";

/// Puts NMT's answer and vm-memory's together. Fails only when both did, with NMT's reason.
pub fn native_memory(
    nmt: Result<String, String>,
    vm: Result<String, String>,
) -> Result<NativeMemoryReport, String> {
    let nmt = nmt.and_then(|o| read::<Nmt>(&o));
    let vm = vm.and_then(|o| read::<VmMemory>(&o));
    match (nmt, vm) {
        (Err(why), Err(_)) if !why.to_lowercase().contains("not enabled") => Err(why),
        (nmt, vm) => Ok(NativeMemoryReport { nmt, vm: vm.ok() }),
    }
}

// Explain: `spectra jvm explain --json <pid>`.

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct ExplainReport {
    pub pid: u32,
    #[serde(deserialize_with = "nullable")]
    pub jdk_vendor: String,
    #[serde(deserialize_with = "nullable")]
    pub jdk_version: String,
    #[serde(deserialize_with = "nullable")]
    pub java_home: String,
    #[serde(deserialize_with = "nullable")]
    pub vm_args: String,
    #[serde(deserialize_with = "nullable")]
    pub observations: Vec<Observation>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Observation {
    #[serde(deserialize_with = "nullable")]
    pub id: String,
    #[serde(deserialize_with = "nullable")]
    pub severity: String,
    #[serde(deserialize_with = "nullable")]
    pub summary: String,
    #[serde(deserialize_with = "nullable")]
    pub evidence: String,
    #[serde(deserialize_with = "nullable")]
    pub recommendation: String,
}

/// Spectra's severity words, as the app's.
pub fn severity(word: &str) -> Severity {
    match word.to_ascii_lowercase().as_str() {
        "critical" | "high" | "error" | "severe" => Severity::Critical,
        "warning" | "warn" | "medium" | "moderate" => Severity::Warning,
        "good" | "ok" | "healthy" | "pass" => Severity::Healthy,
        _ => Severity::Info,
    }
}

impl ExplainReport {
    /// Its observations, the worst first; equal ones keep Spectra's order.
    pub fn findings(&self) -> Vec<&Observation> {
        let mut v: Vec<&Observation> = self.observations.iter().collect();
        v.sort_by_key(|o| severity(&o.severity));
        v
    }
}

// Heap dump: `spectra jvm heap-dump --out <path> <pid>`, then analyses of the file.

#[derive(Clone, Debug, Default, PartialEq)]
pub struct HeapDumpReport {
    pub path: String,
}

/// Spectra prints the file it wrote as its last line.
pub fn heap_dump(out: &str) -> Result<HeapDumpReport, String> {
    match out.lines().map(str::trim).rfind(|l| !l.is_empty()) {
        Some(path) => Ok(HeapDumpReport {
            path: path.to_owned(),
        }),
        None => Err("Spectra didn't say where it wrote the heap dump.".into()),
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct LeakReport {
    #[serde(deserialize_with = "nullable")]
    pub report: Leaks,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Leaks {
    pub total_shallow_bytes: u64,
    pub reachable_objects: u64,
    pub reachable_bytes: u64,
    pub threshold_percent: f64,
    #[serde(deserialize_with = "nullable")]
    pub suspects: Vec<LeakSuspect>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct LeakSuspect {
    #[serde(deserialize_with = "nullable")]
    pub kind: String,
    #[serde(deserialize_with = "nullable")]
    pub class_name: String,
    pub instances: u64,
    pub retained_bytes: u64,
    pub percent_of_heap: f64,
    pub accumulation_point: Option<Accumulation>,
    pub path_to_root: Option<RootPath>,
    #[serde(deserialize_with = "nullable")]
    pub description: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Accumulation {
    #[serde(deserialize_with = "nullable")]
    pub class_name: String,
    pub retained_bytes: u64,
    pub percent_of_heap: f64,
    pub direct_children: u64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct RootPath {
    #[serde(deserialize_with = "nullable")]
    pub roots: Vec<Root>,
    #[serde(deserialize_with = "nullable")]
    pub steps: Vec<Step>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Root {
    #[serde(deserialize_with = "nullable")]
    pub kind: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Step {
    #[serde(deserialize_with = "nullable")]
    pub class_name: String,
}

impl RootPath {
    /// `[root] A → B → C`.
    pub fn line(&self) -> String {
        let roots = self
            .roots
            .iter()
            .map(|r| r.kind.as_str())
            .filter(|k| !k.is_empty())
            .collect::<Vec<_>>()
            .join(", ");
        let steps = self
            .steps
            .iter()
            .map(|s| class_label(&s.class_name))
            .collect::<Vec<_>>()
            .join(" → ");
        if roots.is_empty() {
            steps
        } else {
            format!("[{roots}] {steps}")
        }
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct DominatorReport {
    pub total_shallow_bytes: u64,
    pub reachable_objects: u64,
    pub reachable_bytes: u64,
    #[serde(deserialize_with = "nullable")]
    pub suspects: Vec<Retained>,
    #[serde(deserialize_with = "nullable")]
    pub classes: Vec<Retained>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Retained {
    #[serde(deserialize_with = "nullable")]
    pub class_name: String,
    pub instances: u64,
    pub shallow_bytes: u64,
    pub retained_bytes: u64,
    pub percent_of_heap: f64,
}

// Flight recording: `spectra jvm jfr start|dump|stop`, then `jfr summary --json <file>`.

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct JfrSummary {
    #[serde(deserialize_with = "nullable")]
    pub path: String,
    #[serde(deserialize_with = "nullable")]
    pub version: String,
    pub chunks: u64,
    #[serde(deserialize_with = "nullable")]
    pub start: String,
    #[serde(deserialize_with = "nullable")]
    pub duration: String,
    #[serde(deserialize_with = "nullable")]
    pub events: Vec<JfrEvent>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct JfrEvent {
    #[serde(rename = "type", deserialize_with = "nullable")]
    pub kind: String,
    pub count: u64,
    pub size_bytes: u64,
}

/// Events worth a figure of their own, with what they mean.
pub const HIGHLIGHTS: [(&str, &str); 6] = [
    ("jdk.GarbageCollection", "Garbage collections"),
    ("jdk.ExecutionSample", "CPU samples"),
    ("jdk.ObjectAllocationSample", "Allocation samples"),
    ("jdk.JavaMonitorEnter", "Waits for a lock"),
    ("jdk.ThreadPark", "Thread parks"),
    ("jdk.JavaExceptionThrow", "Exceptions thrown"),
];

/// Events that describe the JVM's setup rather than what it did.
fn setup_event(kind: &str) -> bool {
    let k = kind.strip_prefix("jdk.").unwrap_or(kind);
    k.ends_with("Flag")
        || k.ends_with("Configuration")
        || k.ends_with("Information")
        || k.starts_with("Initial")
        || k.starts_with("Module")
        || matches!(
            k,
            "ActiveSetting"
                | "ActiveRecording"
                | "SystemProcess"
                | "NativeLibrary"
                | "Metadata"
                | "Checkpoint"
                | "CPUTimeStampCounter"
                | "PhysicalMemory"
        )
}

impl JfrSummary {
    pub fn total(&self) -> u64 {
        self.events.iter().map(|e| e.count).sum()
    }

    pub fn count(&self, kind: &str) -> u64 {
        self.events
            .iter()
            .filter(|e| e.kind == kind)
            .map(|e| e.count)
            .sum()
    }

    /// What happened most during the recording, setup events left out.
    pub fn busiest(&self) -> Vec<&JfrEvent> {
        let mut v: Vec<&JfrEvent> = self
            .events
            .iter()
            .filter(|e| e.count > 0 && !setup_event(&e.kind))
            .collect();
        v.sort_by(|a, b| b.count.cmp(&a.count).then(a.kind.cmp(&b.kind)));
        v
    }
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct JfrReport {
    /// Where the recording was written.
    pub path: String,
    pub summary: JfrSummary,
}

/// A flight recording running on the open page's JVM.
#[derive(Clone, Debug, PartialEq)]
pub struct Recording {
    pub pid: u32,
    /// The window clock when it started.
    pub since: f64,
}

/// `spectra jvm jfr stop`, which ends the page's recording and keeps nothing.
pub fn stop_args(pid: u32) -> Vec<String> {
    ["jvm", "jfr", "stop", "--name", RECORDING, &pid.to_string()]
        .map(str::to_owned)
        .to_vec()
}

/// Where a heap dump or recording goes: Downloads when there is one, else the temp folder.
pub fn out_dir() -> PathBuf {
    std::env::var_os("HOME")
        .map(|h| Path::new(&h).join("Downloads"))
        .filter(|d| d.is_dir())
        .unwrap_or_else(std::env::temp_dir)
}

/// `<dir>/<name>-<pid>-<UTC time>.<ext>`, the name reduced to letters, digits, `.`, `_`, `-`.
/// A name already on disk, or already handed out by this app (its file may not be written yet),
/// gets `-2`, `-3`, … before the extension.
pub fn out_path(dir: &Path, name: &str, pid: u32, unix: i64, ext: &str) -> String {
    let safe: String = name
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || "._-".contains(c) {
                c
            } else {
                '-'
            }
        })
        .collect();
    let safe = safe.trim_matches(['-', '.']);
    let safe = if safe.is_empty() { "jvm" } else { safe };
    let base = format!("{safe}-{pid}-{}", stamp(unix));
    let mut issued = ISSUED.lock().unwrap_or_else(|e| e.into_inner());
    (1..)
        .map(|n| {
            let file = if n == 1 {
                format!("{base}.{ext}")
            } else {
                format!("{base}-{n}.{ext}")
            };
            dir.join(file)
        })
        .find(|p| !p.exists() && !issued.contains(p))
        .map(|p| {
            issued.insert(p.clone());
            p.to_string_lossy().into_owned()
        })
        .expect("an unused name")
}

/// Every output path this app has handed out.
static ISSUED: Mutex<BTreeSet<PathBuf>> = Mutex::new(BTreeSet::new());

/// Unix seconds as `20261121T221327Z`.
pub fn stamp(unix: i64) -> String {
    let (days, secs) = (unix.div_euclid(86_400), unix.rem_euclid(86_400));
    // Howard Hinnant's civil_from_days.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = yoe + era * 400 + i64::from(m <= 2);
    format!(
        "{y:04}{m:02}{d:02}T{:02}{:02}{:02}Z",
        secs / 3600,
        secs % 3600 / 60,
        secs % 60
    )
}

pub fn read<'a, T: Deserialize<'a>>(json: &'a str) -> Result<T, String> {
    serde_json::from_str(json).map_err(|e| format!("Couldn't read Spectra's output: {e}"))
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    // Captured from Spectra against a small JVM with a deadlock, trimmed; see tests/fixtures/jvm.
    pub const THREADS: &str = include_str!("../tests/fixtures/jvm/thread-dump.json");
    pub const HISTOGRAM: &str = include_str!("../tests/fixtures/jvm/heap-histogram.json");
    pub const GC: &str = include_str!("../tests/fixtures/jvm/gc-stats.json");
    pub const NMT: &str = include_str!("../tests/fixtures/jvm/nmt.json");
    pub const NMT_OFF_ERR: &str = include_str!("../tests/fixtures/jvm/nmt-off.txt");
    pub const VM_MEMORY: &str = include_str!("../tests/fixtures/jvm/vm-memory.json");
    pub const EXPLAIN: &str = include_str!("../tests/fixtures/jvm/explain.json");
    pub const HPROF: &str = include_str!("../tests/fixtures/jvm/heap-hprof.json");
    pub const DOMINATORS: &str = include_str!("../tests/fixtures/jvm/dominators.json");
    pub const JFR: &str = include_str!("../tests/fixtures/jvm/jfr-summary.json");
    pub const HEAP_DUMP_OUT: &str = "/Users/user/Downloads/Lab-96728-20261121T221200Z.hprof\n";
    pub const JFR_STARTED: &str = "JFR recording \"spectra-desktop\" started on PID 96728\n";

    #[test]
    fn a_thread_dump_counts_states_and_puts_the_deadlock_first() {
        let r: ThreadDumpReport = read(THREADS).unwrap();
        assert_eq!(r.total(), 17);
        assert_eq!(
            ThreadState::SHOWN.map(|s| r.count(s)),
            [8, 2, 1, 6],
            "runnable, blocked, waiting, timed waiting from Spectra's summary"
        );
        assert_eq!(r.deadlocked(), ["lab-worker-1", "lab-worker-2"]);
        let lines = r.dump.deadlocks[0].lines();
        assert_eq!(
            lines[0],
            "lab-worker-1 waits for <0x0000007080015a48> (a java.lang.Object), held by lab-worker-2"
        );
        let groups = r.groups("", None);
        assert!(
            groups[0].deadlocked && groups[1].deadlocked,
            "deadlocked threads lead: {:?}",
            groups.iter().map(|g| &g.id).collect::<Vec<_>>()
        );
        assert!(!groups[2].deadlocked);
        let sleepers = groups.iter().find(|g| g.id == "lab-sleeper-0").unwrap();
        assert_eq!(
            sleepers.names,
            ["lab-sleeper-0", "lab-sleeper-1", "lab-sleeper-2"],
            "same stack, one group"
        );
        assert_eq!(groups[2].id, "lab-sleeper-0", "then the biggest group");
        assert_eq!(
            sleepers.top(),
            "java.lang.Thread.sleep0(java.base@21.0.13.1/Native Method)"
        );
    }

    #[test]
    fn thread_groups_filter_by_name_and_state() {
        let r: ThreadDumpReport = read(THREADS).unwrap();
        let named = r.groups("WORKER", None);
        assert_eq!(named.iter().map(|g| g.names.len()).sum::<usize>(), 2);
        let blocked = r.groups("", Some(ThreadState::Blocked));
        assert!(blocked.iter().all(|g| g.states == [ThreadState::Blocked]));
        assert_eq!(blocked.len(), 2);
        assert!(r.groups("lab", Some(ThreadState::Runnable)).is_empty());
    }

    #[test]
    fn a_threads_frames_come_from_the_raw_block_when_the_parsed_stack_is_short() {
        let r: ThreadDumpReport = read(THREADS).unwrap();
        let mut main = r
            .dump
            .threads
            .iter()
            .find(|t| t.name == "main")
            .unwrap()
            .clone();
        assert_eq!(main.stack.len(), 3);
        main.stack.truncate(1);
        assert_eq!(
            main.frames(),
            [
                "java.lang.Thread.sleep0(java.base@21.0.13.1/Native Method)",
                "java.lang.Thread.sleep(java.base@21.0.13.1/Thread.java:509)",
                "Lab.main(Lab.java:10)"
            ]
        );
    }

    #[test]
    fn a_histogram_parses_and_growth_compares_two() {
        let before: HistogramReport = read(HISTOGRAM).unwrap();
        assert_eq!(before.total_bytes(), 14_733_912);
        assert_eq!(before.by_bytes()[0].class_name, "[B");
        assert_eq!(class_label(&before.by_bytes()[0].class_name), "byte[]");
        assert!(!before.suspects.is_empty());
        let mut now = before.clone();
        now.histogram.entries[0].bytes += 4096;
        now.histogram.entries[0].instances += 2;
        now.histogram.entries.remove(1);
        now.histogram.entries.push(ClassCount {
            class_name: "com.acme.Cache".into(),
            bytes: 900,
            instances: 3,
            ..ClassCount::default()
        });
        let g = growth(&before, &now);
        assert_eq!(
            g[0],
            Growth {
                class_name: "[B".into(),
                bytes: 4096,
                instances: 2,
                bytes_now: before.histogram.entries[0].bytes + 4096
            }
        );
        assert_eq!(
            (g[1].class_name.as_str(), g[1].bytes),
            ("com.acme.Cache", 900),
            "a new class grew by all of it"
        );
        let gone = g.last().unwrap();
        assert_eq!(
            (gone.class_name.as_str(), gone.bytes),
            (
                before.histogram.entries[1].class_name.as_str(),
                -(before.histogram.entries[1].bytes as i64)
            )
        );
        assert_eq!(g.len(), 3, "unchanged classes are left out");
        assert!(growth(&before, &before).is_empty());
    }

    #[test]
    fn class_names_read_as_java() {
        assert_eq!(class_label("[Ljava.lang.Object;"), "java.lang.Object[]");
        assert_eq!(class_label("[[I"), "int[][]");
        assert_eq!(class_label("java.lang.String"), "java.lang.String");
    }

    #[test]
    fn gc_stats_give_collectors_pauses_and_areas() {
        let r: GcReport = read(GC).unwrap();
        let c = r.collectors();
        assert_eq!(
            c.iter().map(|c| (c.name, c.count)).collect::<Vec<_>>(),
            [("Young", 0), ("Full", 1)]
        );
        assert_eq!(c[0].avg_ms(), None, "never ran");
        assert!((c[1].avg_ms().unwrap() - 6.0).abs() < 1e-9);
        let old = r.areas().into_iter().find(|a| a.name == "Old").unwrap();
        assert!((old.percent().unwrap() - 10.98).abs() < 0.01);
        assert_eq!(collector_flag("-Xmx2g -XX:+UseG1GC"), Some("G1"));
        assert_eq!(collector_flag("-Xmx2g"), None);
    }

    #[test]
    fn native_memory_with_nmt_on_lists_categories_by_committed() {
        let r = native_memory(Ok(NMT.into()), Ok(VM_MEMORY.into())).unwrap();
        assert_eq!(r.off(), None);
        let cats = r.categories();
        assert_eq!(cats[0].name, "Java Heap");
        assert!(
            cats.windows(2)
                .all(|w| w[0].committed_kib >= w[1].committed_kib)
        );
        let vm = r.vm.unwrap();
        assert!(
            vm.heap()[0].starts_with("garbage-first heap"),
            "{:?}",
            vm.heap()
        );
        assert!(
            vm.code_cache()
                .unwrap()
                .starts_with("CodeCache: size=245760Kb")
        );
    }

    #[test]
    fn native_memory_with_nmt_off_says_so_with_spectras_reason() {
        let r = native_memory(Err(NMT_OFF_ERR.trim().into()), Ok(VM_MEMORY.into())).unwrap();
        let off = r.off().expect("off");
        assert!(off.contains("-XX:NativeMemoryTracking=summary"), "{off}");
        assert!(r.categories().is_empty());
        assert!(r.vm.is_some(), "vm-memory still shows the heap");
        let still = native_memory(
            Err(NMT_OFF_ERR.trim().into()),
            Err("vm-memory failed".into()),
        )
        .unwrap();
        assert!(still.off().is_some(), "off is an answer, not a failure");
        let disabled = native_memory(Ok(r#"{"enabled":false}"#.into()), Err("x".into())).unwrap();
        assert_eq!(disabled.off().as_deref(), Some(NMT_OFF));
        assert_eq!(
            native_memory(Err("attach failed".into()), Err("x".into())),
            Err("attach failed".into())
        );
    }

    #[test]
    fn explain_puts_the_worst_findings_first() {
        let mut r: ExplainReport = read(EXPLAIN).unwrap();
        assert_eq!(r.jdk_version, "21.0.13.1");
        r.observations.push(Observation {
            id: "x".into(),
            severity: "warning".into(),
            summary: "Heap is filling".into(),
            ..Observation::default()
        });
        let f = r.findings();
        assert_eq!(f[0].id, "x");
        assert_eq!(severity(&f.last().unwrap().severity), Severity::Info);
        assert_eq!(severity("good"), Severity::Healthy);
        assert_eq!(severity("high"), Severity::Critical);
    }

    #[test]
    fn heap_dump_analyses_parse() {
        assert_eq!(
            heap_dump(HEAP_DUMP_OUT).unwrap().path,
            "/Users/user/Downloads/Lab-96728-20261121T221200Z.hprof"
        );
        assert!(heap_dump("\n").is_err());
        let l: LeakReport = read(HPROF).unwrap();
        let s = &l.report.suspects[0];
        assert_eq!(s.class_name, "class Lab");
        assert_eq!(s.accumulation_point.as_ref().unwrap().direct_children, 332);
        assert_eq!(
            s.path_to_root.as_ref().unwrap().line(),
            "[sticky_class] class sun.launcher.LauncherHelper → class Lab → java.util.ArrayList → java.lang.Object[]"
        );
        let d: DominatorReport = read(DOMINATORS).unwrap();
        assert_eq!(d.classes[0].class_name, "[B");
        assert!(d.suspects[0].retained_bytes > 0);
    }

    #[test]
    fn a_recording_summary_highlights_what_happened() {
        let j: JfrSummary = read(JFR).unwrap();
        assert!(j.total() > 0);
        assert_eq!(j.count("jdk.GarbageCollection"), 1);
        let busiest: Vec<&str> = j.busiest().iter().map(|e| e.kind.as_str()).collect();
        assert!(
            !busiest.contains(&"jdk.SystemProcess") && !busiest.contains(&"jdk.BooleanFlag"),
            "{busiest:?}"
        );
        assert!(busiest.contains(&"jdk.GCPhaseParallel"));
    }

    #[test]
    fn output_files_are_named_for_the_process_and_time() {
        assert_eq!(stamp(1_795_176_000), "20261120T120000Z");
        assert_eq!(stamp(0), "19700101T000000Z");
        let p = out_path(
            Path::new("/tmp/x"),
            "Server: prod/1",
            4101,
            1_795_176_000,
            "hprof",
        );
        assert_eq!(p, "/tmp/x/Server--prod-1-4101-20261120T120000Z.hprof");
        assert_eq!(
            out_path(Path::new("/d"), "///", 7, 0, "jfr"),
            "/d/jvm-7-19700101T000000Z.jfr"
        );
        assert_eq!(
            stop_args(7),
            ["jvm", "jfr", "stop", "--name", "spectra-desktop", "7"]
        );
    }

    #[test]
    fn nulls_from_spectra_read_as_empty() {
        let l: LeakReport =
            read(r#"{"report": {"suspects": null, "reachable_bytes": 9}}"#).unwrap();
        assert!(l.report.suspects.is_empty());
        assert_eq!(l.report.reachable_bytes, 9);
        let l: LeakReport = read(
            r#"{"report": {"suspects": [{"class_name": null, "description": null,
                "path_to_root": {"roots": null, "steps": null}}]}}"#,
        )
        .unwrap();
        assert_eq!(
            l.report.suspects[0].path_to_root.as_ref().unwrap().line(),
            ""
        );
        let l: LeakReport = read(r#"{"report": null}"#).unwrap();
        assert!(l.report.suspects.is_empty());
        let d: DominatorReport =
            read(r#"{"suspects": null, "classes": null, "reachable_bytes": 5}"#).unwrap();
        assert!(d.suspects.is_empty() && d.classes.is_empty());
        assert_eq!(d.reachable_bytes, 5);
        let e: ExplainReport =
            read(r#"{"pid": 7, "jdk_vendor": null, "observations": null}"#).unwrap();
        assert!(e.observations.is_empty() && e.findings().is_empty());
        assert_eq!(e.pid, 7);
        let t: ThreadDumpReport = read(
            r#"{"summary": {"by_state": null}, "dump": {"threads": [{"name": "main",
                "stack": null}], "deadlocks": null}}"#,
        )
        .unwrap();
        assert!(t.dump.deadlocks.is_empty() && t.summary.by_state.is_empty());
        assert!(t.dump.threads[0].stack.is_empty());
        let h: HistogramReport =
            read(r#"{"histogram": {"entries": null}, "suspects": null}"#).unwrap();
        assert!(h.histogram.entries.is_empty() && h.suspects.is_empty());
        let n: Nmt = read(r#"{"enabled": true, "categories": null}"#).unwrap();
        assert!(n.categories.is_empty());
        let j: JfrSummary = read(r#"{"events": null, "path": null}"#).unwrap();
        assert!(j.events.is_empty() && j.busiest().is_empty());
    }

    #[test]
    fn an_output_file_never_reuses_a_name() {
        let dir = std::env::temp_dir().join(format!("spectra-out-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let first = out_path(&dir, "Lab", 7, 0, "hprof");
        assert_eq!(
            first,
            dir.join("Lab-7-19700101T000000Z.hprof").to_string_lossy()
        );
        // Asked again in the same second before anything was written: a new name.
        let second = out_path(&dir, "Lab", 7, 0, "hprof");
        assert_eq!(
            second,
            dir.join("Lab-7-19700101T000000Z-2.hprof").to_string_lossy()
        );
        // A file already there (from an earlier run of the app) is skipped too.
        std::fs::write(dir.join("Lab-7-19700101T000000Z-3.hprof"), b"").unwrap();
        let fourth = out_path(&dir, "Lab", 7, 0, "hprof");
        assert_eq!(
            fourth,
            dir.join("Lab-7-19700101T000000Z-4.hprof").to_string_lossy()
        );
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
