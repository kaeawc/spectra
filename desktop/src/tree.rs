//! The process list as its real parent and child hierarchy: who started whom, each branch with
//! what it and everything under it use, flattened into the rows on screen.
//!
//! The roots are launchd's (or init's) children and every process whose parent the list doesn't
//! hold: one launchd row holding everything would say nothing. launchd itself is a row of its
//! own with nothing under it.

use std::collections::{HashMap, HashSet};

use crate::groups::{Row, RowRef, Sort};
use crate::procs::Process;

/// How the process list is laid out.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum View {
    /// An app with its helpers, same-named processes together.
    #[default]
    Grouped,
    /// Parents over their children.
    Tree,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Node {
    pub pid: u32,
    /// An index into the nodes.
    pub parent: Option<usize>,
    /// Indexes into the nodes, in the list's sort order.
    pub children: Vec<usize>,
    pub cpu: f64,
    pub rss_kib: f64,
    /// It and everything under it.
    pub sub_cpu: f64,
    pub sub_rss_kib: f64,
    /// Processes in its branch, itself included.
    pub count: usize,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Tree {
    pub nodes: Vec<Node>,
    /// Indexes into the nodes, in the list's sort order.
    pub roots: Vec<usize>,
    index: HashMap<u32, usize>,
}

/// Whether a process with parent `ppid` starts a branch of its own: launchd's and init's
/// children do, and so does a process that names itself as its parent.
fn starts_branch(pid: u32, ppid: u32) -> bool {
    ppid <= 1 || ppid == pid
}

impl Tree {
    /// Builds the tree over `procs` (a pid listed twice counts once). Siblings are ordered by
    /// `sort` on their branch's totals, so a quiet shell running a busy build sits with the busy
    /// rows; by name, names decide. A parent loop is cut at its lowest pid, which becomes a root.
    pub fn build(procs: &[&Process], sort: Sort) -> Tree {
        let mut index = HashMap::with_capacity(procs.len());
        let mut kept: Vec<&Process> = Vec::with_capacity(procs.len());
        for p in procs {
            if let std::collections::hash_map::Entry::Vacant(e) = index.entry(p.pid) {
                e.insert(kept.len());
                kept.push(p);
            }
        }
        let mut parent: Vec<Option<usize>> = kept
            .iter()
            .map(|p| {
                if starts_branch(p.pid, p.ppid) {
                    None
                } else {
                    index.get(&p.ppid).copied()
                }
            })
            .collect();
        break_loops(&mut parent, &kept);

        let n = kept.len();
        let mut children: Vec<Vec<usize>> = vec![Vec::new(); n];
        let mut roots = Vec::new();
        for (i, p) in parent.iter().enumerate() {
            match p {
                Some(p) => children[*p].push(i),
                None => roots.push(i),
            }
        }
        let mut nodes: Vec<Node> = kept
            .iter()
            .zip(parent)
            .zip(children)
            .map(|((p, parent), children)| Node {
                pid: p.pid,
                parent,
                children,
                cpu: p.cpu_pct,
                rss_kib: p.rss_kib,
                sub_cpu: p.cpu_pct,
                sub_rss_kib: p.rss_kib,
                count: 1,
            })
            .collect();

        // Parents come before their children here, so summing it backwards fills each branch.
        let mut order = Vec::with_capacity(n);
        let mut stack: Vec<usize> = roots.clone();
        while let Some(i) = stack.pop() {
            order.push(i);
            stack.extend(nodes[i].children.iter().copied());
        }
        for &i in order.iter().rev() {
            if let Some(p) = nodes[i].parent {
                let (cpu, rss, count) = (nodes[i].sub_cpu, nodes[i].sub_rss_kib, nodes[i].count);
                let up = &mut nodes[p];
                up.sub_cpu += cpu;
                up.sub_rss_kib += rss;
                up.count += count;
            }
        }

        let names: Vec<_> = kept.iter().map(|p| p.name()).collect();
        let cmp = |a: &usize, b: &usize| {
            let (x, y) = (&nodes[*a], &nodes[*b]);
            sort.order(
                (x.sub_cpu, x.sub_rss_kib, &names[*a], ""),
                (y.sub_cpu, y.sub_rss_kib, &names[*b], ""),
            )
            .then(x.pid.cmp(&y.pid))
        };
        roots.sort_by(cmp);
        let mut sorted: Vec<Vec<usize>> = nodes.iter().map(|n| n.children.clone()).collect();
        for c in &mut sorted {
            c.sort_by(cmp);
        }
        for (node, c) in nodes.iter_mut().zip(sorted) {
            node.children = c;
        }
        Tree {
            nodes,
            roots,
            index,
        }
    }

    pub fn node(&self, pid: u32) -> Option<&Node> {
        self.index_of(pid).map(|i| &self.nodes[i])
    }

    pub fn index_of(&self, pid: u32) -> Option<usize> {
        self.index.get(&pid).copied()
    }

    pub fn parent_of(&self, pid: u32) -> Option<u32> {
        self.node(pid)?.parent.map(|p| self.nodes[p].pid)
    }

    /// Which nodes a search keeps: those `matches` says yes to, and every node above one, so a
    /// match shows where it lives. Also how many matched.
    pub fn keep(&self, matches: impl Fn(u32) -> bool) -> (Vec<bool>, usize) {
        let mut keep = vec![false; self.nodes.len()];
        let mut hits = 0;
        for (i, n) in self.nodes.iter().enumerate() {
            if !matches(n.pid) {
                continue;
            }
            hits += 1;
            let mut at = Some(i);
            // Stops at the first node already kept: everything above it is too.
            while let Some(j) = at {
                if std::mem::replace(&mut keep[j], true) {
                    break;
                }
                at = self.nodes[j].parent;
            }
        }
        (keep, hits)
    }

    /// The rows on screen: each root, and under each node its children while it's open. Without
    /// a search a node is open when `open` holds its pid. With one (`keep`), only kept nodes
    /// show, and every node with a kept child is open unless `closed` holds it. A row's `group`
    /// is its node's index.
    pub fn flatten(
        &self,
        open: &HashSet<u32>,
        keep: Option<&[bool]>,
        closed: &HashSet<u32>,
    ) -> Vec<Row> {
        let shown = |i: usize| keep.is_none_or(|k| k[i]);
        let mut rows = Vec::with_capacity(self.roots.len());
        let mut stack: Vec<(usize, usize)> = self
            .roots
            .iter()
            .rev()
            .filter(|&&i| shown(i))
            .map(|&i| (i, 0))
            .collect();
        while let Some((i, depth)) = stack.pop() {
            let n = &self.nodes[i];
            rows.push(Row {
                id: format!("p:{}", n.pid),
                depth: u8::try_from(depth).unwrap_or(u8::MAX),
                item: RowRef::Process(n.pid),
                group: i,
            });
            if self.is_open(i, open, keep, closed) {
                stack.extend(
                    n.children
                        .iter()
                        .rev()
                        .filter(|&&c| shown(c))
                        .map(|&c| (c, depth + 1)),
                );
            }
        }
        rows
    }

    /// Whether node `i` has children to show: any without a search, kept ones with one.
    pub fn has_shown_children(&self, i: usize, keep: Option<&[bool]>) -> bool {
        let c = &self.nodes[i].children;
        keep.map_or(!c.is_empty(), |k| c.iter().any(|&c| k[c]))
    }

    /// Whether node `i` shows its children, by the rules of [`Tree::flatten`].
    pub fn is_open(
        &self,
        i: usize,
        open: &HashSet<u32>,
        keep: Option<&[bool]>,
        closed: &HashSet<u32>,
    ) -> bool {
        let n = &self.nodes[i];
        match keep {
            None => open.contains(&n.pid),
            Some(k) => !closed.contains(&n.pid) && n.children.iter().any(|&c| k[c]),
        }
    }
}

/// Cuts every loop in `parent` (a process its own ancestor), each at its lowest pid.
fn break_loops(parent: &mut [Option<usize>], procs: &[&Process]) {
    // 0: not seen, 1: on the walk now, 2: known to reach a root.
    let mut state = vec![0u8; parent.len()];
    let mut path = Vec::new();
    for start in 0..parent.len() {
        path.clear();
        let mut at = Some(start);
        while let Some(i) = at {
            match state[i] {
                0 => {
                    state[i] = 1;
                    path.push(i);
                    at = parent[i];
                }
                1 => {
                    let from = path.iter().position(|&j| j == i).unwrap_or(0);
                    if let Some(&cut) = path[from..].iter().min_by_key(|&&j| procs[j].pid) {
                        parent[cut] = None;
                    }
                    break;
                }
                _ => break,
            }
        }
        for &j in &path {
            state[j] = 2;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::groups::SortKey;
    use crate::groups::tests::proc;

    fn child(pid: u32, ppid: u32, name: &str, cpu: f64, mb: f64) -> Process {
        let mut p = proc(pid, name, cpu, mb * 1024.0);
        p.ppid = ppid;
        p
    }

    /// launchd, a terminal with two shells (one running a build), and a daemon.
    fn sample() -> Vec<Process> {
        vec![
            child(1, 0, "launchd", 0.1, 20.0),
            child(100, 1, "Terminal", 1.0, 100.0),
            child(110, 100, "zsh", 0.0, 3.0),
            child(111, 100, "zsh", 0.0, 3.0),
            child(120, 111, "cargo", 2.0, 50.0),
            child(121, 120, "rustc", 90.0, 400.0),
            child(200, 1, "mds", 5.0, 80.0),
        ]
    }

    fn refs(v: &[Process]) -> Vec<&Process> {
        v.iter().collect()
    }

    fn pids(t: &Tree, rows: &[Row]) -> Vec<(u32, u8)> {
        rows.iter()
            .map(|r| (t.nodes[r.group].pid, r.depth))
            .collect()
    }

    fn all_open(t: &Tree) -> HashSet<u32> {
        t.nodes.iter().map(|n| n.pid).collect()
    }

    #[test]
    fn launchds_children_and_orphans_are_the_roots() {
        let mut v = sample();
        v.push(child(300, 9999, "orphan", 0.0, 1.0));
        let t = Tree::build(&refs(&v), Sort::default());
        let mut roots: Vec<u32> = t.roots.iter().map(|&i| t.nodes[i].pid).collect();
        roots.sort();
        assert_eq!(
            roots,
            [1, 100, 200, 300],
            "launchd, its children, and a parent the list lacks"
        );
        assert!(
            t.node(1).unwrap().children.is_empty(),
            "launchd holds nothing"
        );
        assert_eq!(t.parent_of(121), Some(120));
        assert_eq!(t.parent_of(100), None);
    }

    #[test]
    fn a_branch_sums_everything_under_it() {
        let v = sample();
        let t = Tree::build(&refs(&v), Sort::default());
        let term = t.node(100).unwrap();
        assert_eq!(term.cpu, 1.0, "its own");
        assert_eq!(term.sub_cpu, 93.0);
        assert_eq!(
            term.sub_rss_kib,
            (100.0 + 3.0 + 3.0 + 50.0 + 400.0) * 1024.0
        );
        assert_eq!(term.count, 5);
        let rustc = t.node(121).unwrap();
        assert_eq!((rustc.sub_cpu, rustc.count), (90.0, 1), "a leaf is itself");
    }

    #[test]
    fn a_loop_or_a_process_that_is_its_own_parent_does_not_hang() {
        let v = vec![
            child(5, 5, "self", 1.0, 1.0),
            child(10, 12, "a", 1.0, 1.0),
            child(11, 10, "b", 1.0, 1.0),
            child(12, 11, "c", 1.0, 1.0),
            child(13, 12, "under-the-loop", 1.0, 1.0),
            child(13, 1, "listed twice", 1.0, 1.0),
        ];
        let t = Tree::build(&refs(&v), Sort::default());
        assert_eq!(t.nodes.len(), 5, "a pid listed twice counts once");
        let mut roots: Vec<u32> = t.roots.iter().map(|&i| t.nodes[i].pid).collect();
        roots.sort();
        assert_eq!(roots, [5, 10], "the loop is cut at its lowest pid");
        let rows = t.flatten(&all_open(&t), None, &HashSet::new());
        assert_eq!(rows.len(), 5, "every process once");
        assert_eq!(t.node(10).unwrap().count, 4);
        let (keep, hits) = t.keep(|pid| pid == 13);
        assert_eq!(hits, 1);
        assert_eq!(
            t.flatten(&HashSet::new(), Some(&keep), &HashSet::new())
                .len(),
            4
        );
    }

    #[test]
    fn closed_nodes_hide_their_branch() {
        let v = sample();
        let t = Tree::build(&refs(&v), Sort::default());
        let none = HashSet::new();
        assert_eq!(
            pids(&t, &t.flatten(&none, None, &none)),
            [(100, 0), (200, 0), (1, 0)]
        );
        let open: HashSet<u32> = [100, 120].into();
        // 111 is closed, so 120 stays hidden though it's open.
        assert_eq!(
            pids(&t, &t.flatten(&open, None, &none)),
            [(100, 0), (111, 1), (110, 1), (200, 0), (1, 0)]
        );
    }

    #[test]
    fn sort_orders_siblings_by_their_branch() {
        let v = sample();
        let t = Tree::build(&refs(&v), Sort::default());
        let rows = pids(&t, &t.flatten(&all_open(&t), None, &HashSet::new()));
        assert_eq!(
            rows,
            [
                (100, 0),
                (111, 1),
                (120, 2),
                (121, 3),
                (110, 1),
                (200, 0),
                (1, 0)
            ],
            "the zsh running the build first, by its branch's CPU"
        );
        let t = Tree::build(
            &refs(&v),
            Sort {
                key: SortKey::Name,
                descending: false,
            },
        );
        let rows = pids(&t, &t.flatten(&all_open(&t), None, &HashSet::new()));
        assert_eq!(
            rows,
            [
                (1, 0),
                (200, 0),
                (100, 0),
                (110, 1),
                (111, 1),
                (120, 2),
                (121, 3)
            ],
            "launchd, mds, Terminal; zsh ties by pid"
        );
        let t = Tree::build(
            &refs(&v),
            Sort {
                key: SortKey::Memory,
                descending: false,
            },
        );
        let roots: Vec<u32> = t.roots.iter().map(|&i| t.nodes[i].pid).collect();
        assert_eq!(roots, [1, 200, 100], "smallest branch first");
    }

    #[test]
    fn a_search_keeps_matches_and_where_they_live() {
        let v = sample();
        let t = Tree::build(&refs(&v), Sort::default());
        let (keep, hits) = t.keep(|pid| pid == 121);
        assert_eq!(hits, 1);
        let none = HashSet::new();
        let rows = pids(&t, &t.flatten(&none, Some(&keep), &none));
        assert_eq!(
            rows,
            [(100, 0), (111, 1), (120, 2), (121, 3)],
            "opened down to the match, nothing else"
        );
        let closed: HashSet<u32> = [111].into();
        assert_eq!(
            pids(&t, &t.flatten(&none, Some(&keep), &closed)),
            [(100, 0), (111, 1)],
            "closing a node in a search hides its branch"
        );
        let (keep, hits) = t.keep(|pid| pid == 4242);
        assert_eq!(hits, 0);
        assert!(t.flatten(&none, Some(&keep), &none).is_empty());
    }

    #[test]
    fn two_thousand_processes_build_and_flatten_quickly() {
        // launchd's children, each with a chain and a fan of helpers: 2000 processes, deep and wide.
        let mut v = vec![child(1, 0, "launchd", 0.0, 1.0)];
        let mut pid = 2;
        while v.len() < 2000 {
            let root = pid;
            v.push(child(root, 1, &format!("app{root}"), 1.0, 10.0));
            pid += 1;
            let mut up = root;
            for d in 0..8 {
                if v.len() >= 2000 {
                    break;
                }
                v.push(child(pid, up, &format!("helper{d}"), 0.5, 2.0));
                up = pid;
                pid += 1;
            }
        }
        let open: HashSet<u32> = v.iter().map(|p| p.pid).collect();
        let start = std::time::Instant::now();
        let t = Tree::build(&refs(&v), Sort::default());
        let rows = t.flatten(&open, None, &HashSet::new());
        let (keep, _) = t.keep(|pid| pid % 7 == 0);
        let searched = t.flatten(&HashSet::new(), Some(&keep), &HashSet::new());
        let took = start.elapsed();
        assert_eq!(rows.len(), 2000);
        assert!(!searched.is_empty());
        // A debug build on a loaded CI machine; a release build takes about a millisecond.
        assert!(
            took.as_millis() < 250,
            "building and flattening 2000 processes took {took:?}"
        );
    }
}
