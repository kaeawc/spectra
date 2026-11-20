//! The tools a process page runs on demand: which Spectra command each one is, how long it may
//! take, and its JSON, parsed into what the page shows.

use std::time::Duration;

use serde::Deserialize;

/// One of Spectra's per-process tools.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Tool {
    Sample,
    Hang,
    Vmmap,
    Vmregions,
    Memtrace,
    Lineage,
    Owner,
    Runtime,
    Spindump,
    Lsmp,
}

/// What a tool looks at, for the panel's headings.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Cpu,
    Memory,
    Relations,
    Runtime,
}

impl Kind {
    pub const ALL: [Kind; 4] = [Kind::Cpu, Kind::Memory, Kind::Relations, Kind::Runtime];

    pub fn label(self) -> &'static str {
        match self {
            Kind::Cpu => "CPU",
            Kind::Memory => "Memory",
            Kind::Relations => "Relations",
            Kind::Runtime => "Runtime",
        }
    }
}

/// How long memtrace watches, in seconds.
pub const MEMTRACE_SECS: u64 = 20;

impl Tool {
    pub const ALL: [Tool; 10] = [
        Tool::Sample,
        Tool::Hang,
        Tool::Spindump,
        Tool::Vmmap,
        Tool::Vmregions,
        Tool::Memtrace,
        Tool::Lsmp,
        Tool::Lineage,
        Tool::Owner,
        Tool::Runtime,
    ];

    pub fn id(self) -> &'static str {
        match self {
            Tool::Sample => "sample",
            Tool::Hang => "hang",
            Tool::Vmmap => "vmmap",
            Tool::Vmregions => "vmregions",
            Tool::Memtrace => "memtrace",
            Tool::Lineage => "lineage",
            Tool::Owner => "owner",
            Tool::Runtime => "runtime",
            Tool::Spindump => "spindump",
            Tool::Lsmp => "lsmp",
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Tool::Sample => "Sample stacks",
            Tool::Hang => "Hang check",
            Tool::Vmmap => "Memory map",
            Tool::Vmregions => "Dirty regions",
            Tool::Memtrace => "Memory trend",
            Tool::Lineage => "Lineage",
            Tool::Owner => "Owner",
            Tool::Runtime => "Runtime",
            Tool::Spindump => "Spindump",
            Tool::Lsmp => "Mach ports",
        }
    }

    /// What it does, in a line.
    pub fn about(self) -> String {
        match self {
            Tool::Sample => "Samples every thread for 1 s and lists where the time went".into(),
            Tool::Hang => "Watches the main thread for 2 s: idle, busy or blocked".into(),
            Tool::Vmmap => "Memory by region type, most dirty first".into(),
            Tool::Vmregions => "The single regions holding the most dirty memory".into(),
            Tool::Memtrace => format!("Watches its memory for {MEMTRACE_SECS} s and says whether it grows"),
            Tool::Lineage => "What started it, and everything it started".into(),
            Tool::Owner => "Its user, executable, start and parent chain".into(),
            Tool::Runtime => "What it runs on and which tools fit it".into(),
            Tool::Spindump => "Whole-process stacks, kernel frames included".into(),
            Tool::Lsmp => "The Mach ports it holds".into(),
        }
    }

    pub fn kind(self) -> Kind {
        match self {
            Tool::Sample | Tool::Hang | Tool::Spindump => Kind::Cpu,
            Tool::Vmmap | Tool::Vmregions | Tool::Memtrace | Tool::Lsmp => Kind::Memory,
            Tool::Lineage | Tool::Owner => Kind::Relations,
            Tool::Runtime => Kind::Runtime,
        }
    }

    /// Why it's off for everyone: it needs root, and Spectra Desktop never runs sudo.
    pub fn needs_root(self) -> Option<&'static str> {
        match self {
            Tool::Spindump | Tool::Lsmp => Some("Needs root; Spectra Desktop doesn't run sudo"),
            _ => None,
        }
    }

    /// Whether it reads another process's memory or stacks, which only works on one's own.
    pub fn reads_task(self) -> bool {
        matches!(self, Tool::Sample | Tool::Hang | Tool::Vmmap | Tool::Vmregions)
    }

    /// The Spectra arguments that run it on `pid`. Flags go before the pid, which Go's flag
    /// package needs; memtrace takes its pid first.
    pub fn args(self, pid: u32) -> Vec<String> {
        let pid = pid.to_string();
        let v: Vec<&str> = match self {
            Tool::Sample => vec!["sample", "-json", &pid],
            Tool::Hang => vec!["hang", "--json", &pid],
            Tool::Vmmap => vec!["vmmap", "--json", &pid],
            Tool::Vmregions => vec!["vmregions", "--json", &pid],
            Tool::Memtrace => return vec!["memtrace".into(), pid, "--duration".into(), format!("{MEMTRACE_SECS}s"), "--json".into()],
            Tool::Lineage => vec!["lineage", "--json", &pid],
            Tool::Owner => vec!["owner", "--json", &pid],
            Tool::Runtime => vec!["runtime", "--json", &pid],
            Tool::Spindump => vec!["spindump", "--json", &pid],
            Tool::Lsmp => vec!["lsmp", "--json", &pid],
        };
        v.into_iter().map(str::to_owned).collect()
    }

    /// How long a run may take before it's stopped: its own capture time and room to spare.
    pub fn deadline(self) -> Duration {
        Duration::from_secs(match self {
            Tool::Sample | Tool::Hang => 30,
            Tool::Vmmap | Tool::Vmregions => 60,
            Tool::Memtrace => MEMTRACE_SECS + 20,
            Tool::Lineage | Tool::Owner | Tool::Runtime => 15,
            Tool::Spindump | Tool::Lsmp => 30,
        })
    }

    /// What it says while running.
    pub fn running(self) -> String {
        match self {
            Tool::Sample => "Sampling…".into(),
            Tool::Hang => "Watching the main thread…".into(),
            Tool::Memtrace => format!("Watching memory for {MEMTRACE_SECS} s…"),
            _ => "Reading…".into(),
        }
    }

    pub fn parse(self, json: &str) -> Result<Report, String> {
        fn read<'a, T: Deserialize<'a>>(json: &'a str) -> Result<T, String> {
            serde_json::from_str(json).map_err(|e| format!("Couldn't read Spectra's output: {e}"))
        }
        Ok(match self {
            Tool::Sample => Report::Sample(read(json)?),
            Tool::Hang => Report::Hang(read(json)?),
            Tool::Vmmap => Report::Vmmap(read(json)?),
            Tool::Vmregions => Report::Regions(read(json)?),
            Tool::Memtrace => Report::Memtrace(read(json)?),
            Tool::Lineage => Report::Lineage(read(json)?),
            Tool::Owner => Report::Owner(read(json)?),
            Tool::Runtime => Report::Runtime(read(json)?),
            Tool::Spindump | Tool::Lsmp => return Err("Spectra Desktop doesn't run this tool.".into()),
        })
    }
}

/// A tool's result.
#[derive(Clone, Debug, PartialEq)]
pub enum Report {
    Sample(SampleReport),
    Hang(HangReport),
    Vmmap(VmmapReport),
    Regions(RegionsReport),
    Memtrace(MemtraceReport),
    Lineage(LineageReport),
    Owner(OwnerReport),
    Runtime(RuntimeReport),
}

/// What the main thread was doing, as `sample` and `hang` judge it.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct MainThread {
    pub found: bool,
    /// `idle`, `spinning`, `lock-blocked`, `io-blocked` or `unknown`.
    pub verdict: String,
    pub reason: String,
    pub leaf: String,
    pub top_frames: Vec<String>,
}

impl MainThread {
    pub fn headline(&self) -> String {
        if !self.found {
            return "Main thread not found".into();
        }
        match self.verdict.as_str() {
            "idle" => "Idle: waiting for work".into(),
            "spinning" => "Busy: the main thread is running code".into(),
            "lock-blocked" => "Blocked on a lock".into(),
            "io-blocked" => "Blocked on I/O".into(),
            "" | "unknown" => "No verdict".into(),
            v => v.to_owned(),
        }
    }

    /// Whether the verdict says the main thread can't respond: blocked, or spinning.
    pub fn stuck(&self) -> bool {
        matches!(self.verdict.as_str(), "lock-blocked" | "io-blocked" | "spinning")
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Stack {
    /// Outermost first, the leaf last.
    pub frames: Vec<String>,
    pub count: u64,
    pub percent: f64,
}

impl Stack {
    pub fn leaf(&self) -> &str {
        self.frames.last().map_or("(no frames)", String::as_str)
    }

    /// The callers just above the leaf, nearest last, skipping the thread's own name.
    pub fn callers(&self, n: usize) -> Vec<&str> {
        let inner: Vec<&str> = self
            .frames
            .iter()
            .map(String::as_str)
            .filter(|f| !f.starts_with("Thread_"))
            .collect();
        let end = inner.len().saturating_sub(1);
        inner[end.saturating_sub(n)..end].to_vec()
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct SampleReport {
    pub pid: u32,
    pub process: String,
    pub duration_ms: u64,
    pub total_samples: u64,
    pub stack_count: u64,
    pub stacks: Vec<Stack>,
    pub main_thread: Option<MainThread>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct HangReport {
    pub main_thread: MainThread,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct RegionType {
    #[serde(rename = "type")]
    pub kind: String,
    pub virtual_bytes: u64,
    pub resident_bytes: u64,
    pub dirty_bytes: u64,
    pub swapped_bytes: u64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct VmmapReport {
    pub footprint_bytes: u64,
    pub footprint_peak_bytes: u64,
    pub regions: Vec<RegionType>,
    pub total_virtual_bytes: u64,
    pub total_resident_bytes: u64,
    pub total_dirty_bytes: u64,
    pub total_swapped_bytes: u64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Region {
    #[serde(rename = "type")]
    pub kind: String,
    pub addr_start: String,
    pub addr_end: String,
    pub prot: String,
    pub share: String,
    /// What's mapped there: a library, a malloc zone, a thread's stack.
    pub detail: String,
    pub resident_bytes: u64,
    pub dirty_bytes: u64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct RegionsReport {
    pub total_resident_bytes: u64,
    pub total_dirty_bytes: u64,
    pub shared_resident_bytes: u64,
    pub file_backed_dirty_bytes: u64,
    pub anonymous_dirty_bytes: u64,
    pub top_dirty: Vec<Region>,
}

#[derive(Clone, Copy, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct MemSample {
    pub elapsed_ms: u64,
    pub rss_kib: f64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct MemSummary {
    pub samples: u64,
    pub min_kib: f64,
    pub max_kib: f64,
    pub first_kib: f64,
    pub last_kib: f64,
    pub net_change_kib: f64,
    pub growth_kib_per_min: f64,
    /// `steady`, `growing`, `shrinking`, `sawtooth`, `insufficient` or `unreadable`.
    pub verdict: String,
    pub pattern: String,
    pub exited: bool,
    pub exit_reason: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct MemtraceReport {
    pub duration_ms: u64,
    pub interval_ms: u64,
    pub samples: Vec<MemSample>,
    pub polls_failed: u64,
    pub summary: MemSummary,
}

impl MemtraceReport {
    pub fn headline(&self) -> String {
        let s = &self.summary;
        let rate = crate::procs::fmt_mem(s.growth_kib_per_min.abs());
        let under = if s.pattern == "sawtooth" { " under a sawtooth" } else { "" };
        let head = match s.verdict.as_str() {
            "steady" => "Steady: not growing".to_owned(),
            "growing" => format!("Growing{under}: {rate} a minute"),
            "shrinking" => format!("Shrinking: {rate} a minute"),
            "sawtooth" => "Sawtooth: rises and falls, no trend".to_owned(),
            "insufficient" => "Too few readings to say".to_owned(),
            "unreadable" => "Its memory couldn't be read".to_owned(),
            v => v.to_owned(),
        };
        if s.exited {
            let why = match s.exit_reason.as_str() {
                "zombie" => "it exited and waits for its parent",
                _ => "it exited",
            };
            format!("{head} ({why} before the end)")
        } else {
            head
        }
    }

    pub fn growing(&self) -> bool {
        self.summary.verdict == "growing"
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Ancestor {
    pub pid: u32,
    pub ppid: u32,
    pub user: String,
    pub start_time: String,
    pub command: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Node {
    pub pid: u32,
    pub user: String,
    pub command: String,
    pub rss_kib: f64,
    pub cpu_pct: f64,
    pub flags: Vec<String>,
    /// It and everything under it.
    pub count: u64,
    pub subtree_rss_kib: f64,
    pub children: Vec<Node>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct LineageReport {
    pub pid: u32,
    pub flags: Vec<String>,
    /// It first, then its parent, up to launchd.
    pub ancestors: Vec<Ancestor>,
    pub tree: Option<Node>,
}

impl LineageReport {
    /// The tree under it, a row per process, depth first: (depth, node).
    pub fn descendants(&self) -> Vec<(usize, &Node)> {
        fn walk<'a>(n: &'a Node, depth: usize, out: &mut Vec<(usize, &'a Node)>) {
            out.push((depth, n));
            for c in &n.children {
                walk(c, depth + 1, out);
            }
        }
        let mut out = Vec::new();
        if let Some(t) = &self.tree {
            walk(t, 0, &mut out);
        }
        out
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Link {
    pub pid: u32,
    pub command: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Holder {
    pub pid: u32,
    pub user: String,
    pub command: String,
    pub executable: String,
    pub started: String,
    pub hold: String,
    pub mode: String,
    pub detail: String,
    /// Its parent first, up to launchd.
    pub chain: Vec<Link>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct OwnerReport {
    pub holders: Vec<Holder>,
    /// Without root only one's own processes are visible; Spectra says so here.
    pub incomplete: bool,
    pub hint: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Capability {
    pub name: String,
    pub how: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct RuntimeReport {
    pub pid: u32,
    pub command: String,
    pub executable_path: String,
    pub runtime: String,
    pub evidence: String,
    pub capabilities: Vec<Capability>,
}

/// Bytes as `912 KB`, `48 MB`, `1.2 GB`.
pub fn fmt_bytes(b: u64) -> String {
    let b = b as f64;
    if b < 1024.0 * 1023.5 {
        format!("{:.0} KB", b / 1024.0)
    } else {
        crate::procs::fmt_mem(b / 1024.0)
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    // Shaped like each command's real output, from a run against a `sleep`; names changed.
    pub const SAMPLE: &str = r#"{"pid":4242,"process":"sleep","taken_at":"2026-11-20T10:50:23.944215Z","duration_ms":1000,
        "interval_ms":10,"total_samples":85,"stack_count":2,
        "stacks":[{"frames":["Thread_100","start","main","nanosleep","__semwait_signal"],"count":80,"percent":94.1},
                  {"frames":["Thread_101","start_wqthread","_pthread_wqthread","__workq_kernreturn"],"count":5,"percent":5.9}],
        "main_thread":{"found":true,"verdict":"idle","reason":"main thread is in a timed sleep (nanosleep → __semwait_signal), not blocked on I/O or a lock",
        "leaf":"__semwait_signal","top_frames":["Thread_100","start","main","nanosleep","__semwait_signal"]}}"#;
    pub const HANG: &str = r#"{"main_thread":{"found":true,"verdict":"lock-blocked","reason":"main thread is waiting on a mutex (pthread_mutex_lock → __psynch_mutexwait)",
        "leaf":"__psynch_mutexwait","top_frames":["Thread_100","start","main","pthread_mutex_lock","__psynch_mutexwait"]}}"#;
    pub const VMMAP: &str = r#"{"footprint_bytes":933888,"footprint_peak_bytes":950272,
        "regions":[{"type":"MALLOC metadata","virtual_bytes":868352,"resident_bytes":294912,"dirty_bytes":294912,"swapped_bytes":0},
                   {"type":"Stack","virtual_bytes":8372224,"resident_bytes":32768,"dirty_bytes":32768,"swapped_bytes":4096}],
        "total_virtual_bytes":841587097,"total_resident_bytes":182557081,"total_dirty_bytes":950272,"total_swapped_bytes":0}"#;
    pub const VMREGIONS: &str = r#"{"total_resident_bytes":182426610,"total_dirty_bytes":784395,"shared_resident_bytes":32768,
        "file_backed_dirty_bytes":266628,"anonymous_dirty_bytes":517767,"writable_resident_bytes":3367108,"executable_resident_bytes":6221824,
        "top_dirty":[{"type":"MALLOC metadata","addr_start":"10339c000","addr_end":"1033e8000","prot":"rw-","max_prot":"rwx","share":"PRV",
                      "detail":"DefaultMallocZone_0x10339c000","resident_bytes":262144,"dirty_bytes":262144},
                     {"type":"shared memory","addr_start":"103088000","addr_end":"103090000","prot":"r--","max_prot":"r--","share":"SHM",
                      "resident_bytes":32768,"dirty_bytes":32768}]}"#;
    pub const MEMTRACE: &str = r#"{"pid":4242,"command":"sleep","tree":false,"duration_ms":20107,"interval_ms":2000,
        "samples":[{"elapsed_ms":110,"rss_kib":1200},{"elapsed_ms":2110,"rss_kib":1400},{"elapsed_ms":4421,"rss_kib":1700},
                   {"elapsed_ms":6235,"rss_kib":1900},{"elapsed_ms":8108,"rss_kib":2300}],
        "polls_failed":0,"polls_skipped":0,
        "summary":{"samples":5,"min_kib":1200,"max_kib":2300,"first_kib":1200,"last_kib":2300,"net_change_kib":1100,
                   "growth_kib_per_min":8200,"verdict":"growing"},"growers":[]}"#;
    pub const LINEAGE: &str = r#"{"pid":4300,"ancestors":[
        {"pid":4300,"ppid":4299,"user":"someone","start_time":"2026-11-20T05:50:47-05:00","command":"bash"},
        {"pid":4299,"ppid":1,"user":"someone","start_time":"2026-11-20T05:50:47-05:00","command":"zsh"},
        {"pid":1,"ppid":0,"user":"root","start_time":"2026-11-20T02:57:44-05:00","command":"launchd"}],
        "tree":{"pid":4300,"ppid":4299,"user":"someone","start_time":"2026-11-20T05:50:47-05:00","command":"bash","rss_kib":8368,"cpu_pct":0.6,
                "count":3,"subtree_rss_kib":10768,"subtree_cpu_pct":0.6,
                "children":[{"pid":4302,"ppid":4300,"user":"someone","command":"sleep","rss_kib":1200,"cpu_pct":0,"count":2,"subtree_rss_kib":2400,"subtree_cpu_pct":0,
                             "children":[{"pid":4303,"ppid":4302,"user":"someone","command":"cat","rss_kib":1200,"cpu_pct":0,"count":1}]}]}}"#;
    pub const OWNER: &str = r#"{"target":{"kind":"pid","raw":"4242","pid":4242},"holders":[{"pid":4242,"user":"someone","command":"sleep",
        "executable":"/bin/sleep","started":"2026-11-20T05:50:15-05:00","hold":"process","chain":[{"pid":1,"command":"launchd"}]}]}"#;
    pub const RUNTIME: &str = r#"{"pid":4242,"command":"sleep","executable_path":"/bin/sleep","runtime":"native",
        "evidence":"no managed-runtime signal; treated as a native binary",
        "capabilities":[{"name":"attach a native debugger","how":"lldb -p 4242  (or spindump for a whole-system stack)"},
                        {"name":"user-space CPU sample","how":"spectra sample 4242"}]}"#;

    #[test]
    fn each_tool_runs_its_command_with_flags_before_the_pid() {
        assert_eq!(Tool::Sample.args(7), ["sample", "-json", "7"]);
        assert_eq!(Tool::Hang.args(7), ["hang", "--json", "7"]);
        assert_eq!(Tool::Vmregions.args(7), ["vmregions", "--json", "7"]);
        assert_eq!(Tool::Memtrace.args(7), ["memtrace", "7", "--duration", "20s", "--json"]);
        assert_eq!(Tool::Lineage.args(7), ["lineage", "--json", "7"]);
        assert!(Tool::Memtrace.deadline() > Duration::from_secs(MEMTRACE_SECS), "longer than it watches");
        let root: Vec<Tool> = Tool::ALL.into_iter().filter(|t| t.needs_root().is_some()).collect();
        assert_eq!(root, [Tool::Spindump, Tool::Lsmp]);
        assert!(Tool::Spindump.parse("{}").is_err(), "never run");
    }

    #[test]
    fn parses_a_sample() {
        let Report::Sample(r) = Tool::Sample.parse(SAMPLE).unwrap() else { panic!() };
        assert_eq!((r.total_samples, r.stacks.len()), (85, 2));
        assert_eq!(r.stacks[0].leaf(), "__semwait_signal");
        assert_eq!(r.stacks[0].callers(2), ["main", "nanosleep"], "the thread's name is skipped");
        let main = r.main_thread.unwrap();
        assert_eq!(main.headline(), "Idle: waiting for work");
        assert!(!main.stuck());
    }

    #[test]
    fn parses_a_hang_verdict() {
        let Report::Hang(r) = Tool::Hang.parse(HANG).unwrap() else { panic!() };
        assert_eq!(r.main_thread.headline(), "Blocked on a lock");
        assert!(r.main_thread.stuck());
        assert_eq!(MainThread::default().headline(), "Main thread not found");
    }

    #[test]
    fn parses_memory_maps() {
        let Report::Vmmap(r) = Tool::Vmmap.parse(VMMAP).unwrap() else { panic!() };
        assert_eq!(r.regions[0].kind, "MALLOC metadata");
        assert_eq!(r.regions[1].swapped_bytes, 4096);
        assert_eq!(fmt_bytes(r.footprint_bytes), "912 KB");
        let Report::Regions(r) = Tool::Vmregions.parse(VMREGIONS).unwrap() else { panic!() };
        assert_eq!(r.top_dirty.len(), 2);
        assert_eq!(r.top_dirty[1].detail, "", "a region with no detail");
        assert_eq!(r.top_dirty[0].share, "PRV");
    }

    #[test]
    fn parses_a_memory_trend() {
        let Report::Memtrace(r) = Tool::Memtrace.parse(MEMTRACE).unwrap() else { panic!() };
        assert_eq!(r.samples.len(), 5);
        assert!(r.growing());
        assert_eq!(r.headline(), "Growing: 8.0 MB a minute");
        let mut steady = r.clone();
        steady.summary.verdict = "steady".into();
        steady.summary.exited = true;
        steady.summary.exit_reason = "gone".into();
        assert_eq!(steady.headline(), "Steady: not growing (it exited before the end)");
    }

    #[test]
    fn parses_lineage_into_a_tree() {
        let Report::Lineage(r) = Tool::Lineage.parse(LINEAGE).unwrap() else { panic!() };
        assert_eq!(r.ancestors.iter().map(|a| a.pid).collect::<Vec<_>>(), [4300, 4299, 1]);
        let rows: Vec<(usize, u32)> = r.descendants().iter().map(|(d, n)| (*d, n.pid)).collect();
        assert_eq!(rows, [(0, 4300), (1, 4302), (2, 4303)]);
    }

    #[test]
    fn parses_owner_and_runtime() {
        let Report::Owner(r) = Tool::Owner.parse(OWNER).unwrap() else { panic!() };
        assert_eq!(r.holders[0].chain[0].command, "launchd");
        assert!(!r.incomplete);
        let Report::Runtime(r) = Tool::Runtime.parse(RUNTIME).unwrap() else { panic!() };
        assert_eq!(r.capabilities.len(), 2);
        assert_eq!(r.runtime, "native");
    }

    #[test]
    fn output_that_isnt_json_is_an_error() {
        let err = Tool::Lineage.parse("usage: spectra lineage").unwrap_err();
        assert!(err.starts_with("Couldn't read Spectra's output"), "{err}");
    }
}
