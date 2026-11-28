//! "Who started this process, and what did it start?" from a process snapshot (Go's
//! `internal/lineage`): the ancestor chain up to pid 1 and the descendant subtree with per-node
//! and subtree resource totals.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::fmt;

use serde::Serialize;
use spectra_base::gojson::{self, GoTime};

use crate::process::{CollectOptions, Collector, Info};

pub const FLAG_ZOMBIE: &str = "zombie";
pub const FLAG_REPARENTED: &str = "reparented";

/// The pid is not in the snapshot (Go's `pid %d: no such process` wrapping `ErrNotFound`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct NotFound(pub i64);

impl fmt::Display for NotFound {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "pid {}: no such process", self.0)
    }
}

impl std::error::Error for NotFound {}

/// One link of the chain, nearest first from the target (Go's `Ancestor`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Ancestor {
    pub pid: i64,
    pub ppid: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub user: String,
    pub start_time: GoTime,
    pub command: String,
}

/// A process in the descendant tree with subtree totals (Go's `Node`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Node {
    pub pid: i64,
    pub ppid: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub user: String,
    pub start_time: GoTime,
    pub command: String,
    pub rss_kib: i64,
    pub cpu_pct: f64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub flags: Vec<String>,
    pub count: i64,
    pub subtree_rss_kib: i64,
    pub subtree_cpu_pct: f64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub children: Vec<Node>,
}

/// The lineage of one process (Go's `Result`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Lineage {
    pub pid: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub flags: Vec<String>,
    pub ancestors: Vec<Ancestor>,
    pub tree: Node,
}

/// Go's `Collect`: snapshots the processes with `c` and builds `pid`'s lineage. What `spectra
/// lineage --json` prints.
pub fn collect(c: &dyn Collector, opts: &CollectOptions, pid: i64) -> Result<Lineage, NotFound> {
    build(&c.collect(opts), pid)
}

/// Go's `Build`.
pub fn build(procs: &[Info], pid: i64) -> Result<Lineage, NotFound> {
    let mut by_pid: HashMap<i64, &Info> = HashMap::new();
    for p in procs {
        by_pid.insert(p.pid, p);
    }
    let mut children: BTreeMap<i64, Vec<i64>> = BTreeMap::new();
    for p in procs.iter().filter(|p| p.pid != p.ppid) {
        children.entry(p.ppid).or_default().push(p.pid);
    }
    let target = *by_pid.get(&pid).ok_or(NotFound(pid))?;
    for c in children.values_mut() {
        c.sort_unstable();
    }
    let mut seen = HashSet::new();
    Ok(Lineage {
        pid,
        flags: reparented(target),
        ancestors: chain(target, &by_pid),
        tree: build_node(target, &by_pid, &children, &mut seen),
    })
}

fn chain(target: &Info, by_pid: &HashMap<i64, &Info>) -> Vec<Ancestor> {
    let mut out = Vec::new();
    let mut seen = HashSet::new();
    let mut cur = target;
    loop {
        seen.insert(cur.pid);
        out.push(Ancestor {
            pid: cur.pid,
            ppid: cur.ppid,
            user: cur.user.clone(),
            start_time: cur.start_time,
            command: cur.command.clone(),
        });
        if cur.pid == 1 || cur.ppid <= 0 || seen.contains(&cur.ppid) {
            return out;
        }
        let Some(&parent) = by_pid.get(&cur.ppid) else { return out };
        cur = parent;
    }
}

fn build_node(p: &Info, by_pid: &HashMap<i64, &Info>, children: &BTreeMap<i64, Vec<i64>>, seen: &mut HashSet<i64>) -> Node {
    seen.insert(p.pid);
    let mut n = Node {
        pid: p.pid,
        ppid: p.ppid,
        user: p.user.clone(),
        start_time: p.start_time,
        command: p.command.clone(),
        rss_kib: p.rss_kib,
        cpu_pct: p.cpu_pct,
        count: 1,
        subtree_rss_kib: p.rss_kib,
        subtree_cpu_pct: p.cpu_pct,
        ..Node::default()
    };
    if is_zombie(p) {
        n.flags.push(FLAG_ZOMBIE.to_string());
    }
    for cpid in children.get(&p.pid).into_iter().flatten() {
        if seen.contains(cpid) {
            continue;
        }
        let c = build_node(by_pid[cpid], by_pid, children, seen);
        n.count += c.count;
        n.subtree_rss_kib += c.subtree_rss_kib;
        n.subtree_cpu_pct += c.subtree_cpu_pct;
        n.children.push(c);
    }
    n
}

/// Go's `IsZombie`, inferred from the command form (the collector records no process state).
/// A procps-style `name <defunct>` whole command line with zero VSZ is direct evidence; on
/// Linux an empty cmdline shows as `[comm]`, which with zero VSZ is a zombie unless its parent
/// is pid 0 or 2 (kernel threads). macOS `(name)` is not used: ps prints it for live processes
/// whose arguments it cannot read.
pub fn is_zombie(p: &Info) -> bool {
    if p.vsize_kib != 0 {
        return false;
    }
    if is_defunct(&p.command) || is_defunct(&p.full_command_line) {
        return true;
    }
    !p.command.is_empty() && p.full_command_line == format!("[{}]", p.command) && p.ppid != 0 && p.ppid != 2
}

/// procps `name <defunct>`, and the bare `<defunct>` macOS ps prints for a zombie.
fn is_defunct(s: &str) -> bool {
    if s == "<defunct>" {
        return true;
    }
    s.strip_suffix(" <defunct>").is_some_and(|name| !name.is_empty() && !name.contains([' ', '\t']))
}

/// Go's `Reparented`: a non-root process whose parent is pid 1. A heuristic: per-user launchd
/// agents also have PPID 1.
pub fn reparented(p: &Info) -> Vec<String> {
    if p.ppid == 1 && p.uid != 0 { vec![FLAG_REPARENTED.to_string()] } else { Vec::new() }
}

#[cfg(test)]
mod tests;
