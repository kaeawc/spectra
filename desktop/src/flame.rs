//! A CPU sample's stacks as a call tree, laid out as an icicle chart: the whole process on top,
//! each row one frame deeper, each frame as wide as its share of the samples.
//!
//! `spectra sample --json` lists the busiest folded stacks (outermost frame first, the count is
//! the leaf's self time), capped at 50 with `total_samples` covering all of them. The tree is built
//! once per result, off the UI thread (`Tool::parse`), and laid out then; zooming lays out again
//! on the press, never per frame. Frames too narrow to see merge into one "…" block, and rows and
//! blocks are capped, so a deep or wide sample draws as cheaply as a small one.

use std::collections::{HashMap, VecDeque};

use crate::tools::Stack;

/// Rows drawn below the view's top bar.
pub const MAX_ROWS: usize = 28;
/// Blocks drawn in one view.
pub const MAX_CELLS: usize = 400;
/// A frame narrower than this share of the view merges with its small siblings into "…".
pub const MIN_FRAC: f64 = 0.005;
/// Functions listed by self time.
pub const HEAVIEST: usize = 15;
/// Guards against a pathological sample: tree nodes kept, frames read per stack.
const MAX_NODES: usize = 20_000;
const MAX_FRAMES: usize = 256;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Kind {
    /// The whole process.
    #[default]
    Root,
    /// A thread, the outermost frame of each stack.
    Thread,
    Frame,
    /// A frame with no symbol name: `???` or a bare address.
    Unnamed,
    /// The samples in stacks Spectra didn't list.
    Unlisted,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Node {
    pub name: String,
    pub kind: Kind,
    /// Samples in which this frame was on the stack.
    pub total: u64,
    /// Samples in which this frame was the one running.
    pub self_samples: u64,
    pub parent: Option<usize>,
    /// Heaviest first.
    pub children: Vec<usize>,
}

/// What a block stands for.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum What {
    Node(usize),
    /// Sibling frames too narrow to draw, merged.
    More {
        samples: u64,
        frames: usize,
    },
}

/// One block: its row (0 is the view's top bar) and where it sits, as shares of the view's width.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Cell {
    pub what: What,
    pub row: usize,
    pub x: f64,
    pub w: f64,
}

/// A function and the samples it was running in, across the view.
#[derive(Clone, Debug, PartialEq)]
pub struct Heavy {
    pub name: String,
    pub self_samples: u64,
    pub unnamed: bool,
}

/// The chart as laid out under one frame.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct View {
    pub root: usize,
    /// By row, then left to right.
    pub cells: Vec<Cell>,
    pub rows: usize,
    /// Some frames weren't drawn: too deep, or too many blocks.
    pub clipped: bool,
    pub heaviest: Vec<Heavy>,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Flame {
    /// Node 0 is the whole process; empty when the sample had no stacks.
    pub nodes: Vec<Node>,
    /// Samples in stacks Spectra didn't list.
    pub unlisted: u64,
    /// Frames with no symbol name.
    pub unnamed: usize,
    pub view: View,
    /// The views zoomed out of, for Back.
    trail: Vec<usize>,
}

/// Whether a frame has no symbol name: `???`, empty, or a bare hex address.
pub fn unnamed(name: &str) -> bool {
    let n = name.trim();
    n.is_empty()
        || n.chars().all(|c| c == '?')
        || n.strip_prefix("0x")
            .is_some_and(|h| !h.is_empty() && h.chars().all(|c| c.is_ascii_hexdigit()))
}

impl Flame {
    /// Folds `stacks` (outermost frame first, counted at the leaf) into a tree. Samples
    /// `total_samples` counts beyond the listed stacks become one "not listed" block.
    pub fn build(stacks: &[Stack], total_samples: u64) -> Flame {
        let mut nodes = vec![Node {
            name: "Whole process".into(),
            kind: Kind::Root,
            total: 0,
            self_samples: 0,
            parent: None,
            children: Vec::new(),
        }];
        let mut index: HashMap<(usize, String), usize> = HashMap::new();
        let mut listed = 0u64;
        for st in stacks.iter().filter(|s| s.count > 0) {
            listed = listed.saturating_add(st.count);
            nodes[0].total = nodes[0].total.saturating_add(st.count);
            let mut at = 0;
            for (depth, frame) in st.frames.iter().take(MAX_FRAMES).enumerate() {
                let name = match frame.trim() {
                    "" => "???",
                    n => n,
                };
                let next = match index.get(&(at, name.to_owned())) {
                    Some(&i) => i,
                    None if nodes.len() >= MAX_NODES => break,
                    None => {
                        let kind = if unnamed(name) {
                            Kind::Unnamed
                        } else if depth == 0 && name.starts_with("Thread_") {
                            Kind::Thread
                        } else {
                            Kind::Frame
                        };
                        let i = nodes.len();
                        nodes.push(Node {
                            name: name.to_owned(),
                            kind,
                            total: 0,
                            self_samples: 0,
                            parent: Some(at),
                            children: Vec::new(),
                        });
                        nodes[at].children.push(i);
                        index.insert((at, name.to_owned()), i);
                        i
                    }
                };
                nodes[next].total = nodes[next].total.saturating_add(st.count);
                at = next;
            }
            nodes[at].self_samples = nodes[at].self_samples.saturating_add(st.count);
        }
        let unlisted = total_samples.saturating_sub(listed);
        if unlisted > 0 {
            let i = nodes.len();
            nodes.push(Node {
                name: "Stacks Spectra didn't list".into(),
                kind: Kind::Unlisted,
                total: unlisted,
                self_samples: unlisted,
                parent: Some(0),
                children: Vec::new(),
            });
            nodes[0].children.push(i);
            nodes[0].total += unlisted;
        }
        if nodes[0].total == 0 {
            return Flame::default();
        }
        for i in 0..nodes.len() {
            let mut kids = std::mem::take(&mut nodes[i].children);
            kids.sort_by(|a, b| {
                nodes[*b]
                    .total
                    .cmp(&nodes[*a].total)
                    .then_with(|| nodes[*a].name.cmp(&nodes[*b].name))
            });
            nodes[i].children = kids;
        }
        let unnamed = nodes.iter().filter(|n| n.kind == Kind::Unnamed).count();
        let view = layout(&nodes, 0);
        Flame {
            nodes,
            unlisted,
            unnamed,
            view,
            trail: Vec::new(),
        }
    }

    pub fn is_empty(&self) -> bool {
        self.nodes.is_empty()
    }

    /// Every sample in the process.
    pub fn total(&self) -> u64 {
        self.nodes.first().map_or(0, |n| n.total)
    }

    pub fn zoomed(&self) -> bool {
        self.view.root != 0
    }

    /// `samples` as a percentage of the whole process.
    pub fn share(&self, samples: u64) -> f64 {
        match self.total() {
            0 => 0.0,
            t => samples as f64 * 100.0 / t as f64,
        }
    }

    /// The frames from the thread down to `n`, outermost first.
    pub fn path(&self, n: usize) -> Vec<&str> {
        let mut out = Vec::new();
        let mut at = Some(n);
        while let Some(i) = at.filter(|i| *i != 0 && *i < self.nodes.len()) {
            out.push(self.nodes[i].name.as_str());
            at = self.nodes[i].parent;
        }
        out.reverse();
        out
    }

    /// A press on frame `n`: zooms into it, or, on the view's top bar, goes back.
    pub fn press(&mut self, n: usize) {
        if n >= self.nodes.len() {
            return;
        }
        if n == self.view.root {
            self.back();
        } else if n == 0 {
            self.whole();
        } else {
            self.trail.push(self.view.root);
            self.view = layout(&self.nodes, n);
        }
    }

    /// Back to the view zoomed in from.
    pub fn back(&mut self) {
        if self.is_empty() || !self.zoomed() {
            return;
        }
        let to = self.trail.pop().unwrap_or(0);
        self.view = layout(&self.nodes, to);
    }

    /// The whole process again.
    pub fn whole(&mut self) {
        if self.is_empty() {
            return;
        }
        self.trail.clear();
        if self.zoomed() {
            self.view = layout(&self.nodes, 0);
        }
    }
}

/// Lays out the tree under `root` across the view, breadth first so that when the block cap is
/// reached the shallow rows are whole.
pub fn layout(nodes: &[Node], root: usize) -> View {
    let Some(top) = nodes.get(root) else {
        return View::default();
    };
    let total = top.total.max(1) as f64;
    let mut cells = vec![Cell {
        what: What::Node(root),
        row: 0,
        x: 0.0,
        w: 1.0,
    }];
    let mut rows = 1;
    let mut clipped = false;
    let mut queue = VecDeque::from([(root, 0usize, 0.0f64)]);
    while let Some((n, row, x0)) = queue.pop_front() {
        let kids = &nodes[n].children;
        if kids.is_empty() {
            continue;
        }
        if row + 1 > MAX_ROWS {
            clipped = true;
            continue;
        }
        let mut x = x0;
        let (mut small, mut small_n) = (0u64, 0usize);
        for &c in kids {
            let w = nodes[c].total as f64 / total;
            if w < MIN_FRAC {
                small += nodes[c].total;
                small_n += 1;
                continue;
            }
            if cells.len() >= MAX_CELLS {
                clipped = true;
                break;
            }
            cells.push(Cell {
                what: What::Node(c),
                row: row + 1,
                x,
                w,
            });
            rows = rows.max(row + 2);
            queue.push_back((c, row + 1, x));
            x += w;
        }
        // Children are heaviest first, so the narrow ones sit together at the end.
        let w = small as f64 / total;
        if small_n > 0 && w >= MIN_FRAC && cells.len() < MAX_CELLS {
            cells.push(Cell {
                what: What::More {
                    samples: small,
                    frames: small_n,
                },
                row: row + 1,
                x,
                w,
            });
            rows = rows.max(row + 2);
        }
    }
    View {
        root,
        cells,
        rows,
        clipped,
        heaviest: heaviest(nodes, root),
    }
}

/// The functions under `root` that ran the most samples themselves, heaviest first.
pub fn heaviest(nodes: &[Node], root: usize) -> Vec<Heavy> {
    let mut by_name: HashMap<&str, (u64, bool)> = HashMap::new();
    let mut stack = vec![root];
    while let Some(n) = stack.pop() {
        let Some(node) = nodes.get(n) else { continue };
        stack.extend(node.children.iter().copied());
        if matches!(node.kind, Kind::Root | Kind::Unlisted) || node.self_samples == 0 {
            continue;
        }
        let e = by_name
            .entry(node.name.as_str())
            .or_insert((0, node.kind == Kind::Unnamed));
        e.0 += node.self_samples;
    }
    let mut out: Vec<Heavy> = by_name
        .into_iter()
        .map(|(name, (self_samples, unnamed))| Heavy {
            name: name.to_owned(),
            self_samples,
            unnamed,
        })
        .collect();
    out.sort_by(|a, b| {
        b.self_samples
            .cmp(&a.self_samples)
            .then_with(|| a.name.cmp(&b.name))
    });
    out.truncate(HEAVIEST);
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tools::{Report, SampleReport, Tool};

    /// Invented, shaped like `spectra sample --json`: a made-up app, 24 of 53 stacks listed.
    pub const APP: &str = include_str!("../tests/fixtures/flame/app-busy.json");
    pub const UNNAMED: &str = include_str!("../tests/fixtures/flame/unnamed.json");

    fn report(json: &str) -> SampleReport {
        match Tool::Sample.parse(json).unwrap() {
            Report::Sample(r) => r,
            _ => unreachable!(),
        }
    }

    fn stack(frames: &[&str], count: u64) -> Stack {
        Stack {
            frames: frames.iter().map(|f| f.to_string()).collect(),
            count,
            percent: 0.0,
        }
    }

    fn find(f: &Flame, name: &str) -> usize {
        f.nodes.iter().position(|n| n.name == name).unwrap()
    }

    #[test]
    fn stacks_fold_into_a_tree_with_totals_and_self_time() {
        let f = Flame::build(
            &[
                stack(&["T", "main", "work", "hash"], 6),
                stack(&["T", "main", "work"], 3),
                stack(&["T", "main", "idle"], 1),
            ],
            10,
        );
        assert_eq!(f.total(), 10);
        assert_eq!(f.unlisted, 0);
        let work = find(&f, "work");
        assert_eq!((f.nodes[work].total, f.nodes[work].self_samples), (9, 3));
        let main = find(&f, "main");
        assert_eq!((f.nodes[main].total, f.nodes[main].self_samples), (10, 0));
        // Heaviest child first.
        assert_eq!(f.nodes[main].children[0], work);
        assert_eq!(f.path(find(&f, "hash")), ["T", "main", "work", "hash"]);
    }

    #[test]
    fn samples_spectra_didnt_list_are_one_block_so_shares_stay_true() {
        let r = report(APP);
        let listed: u64 = r.stacks.iter().map(|s| s.count).sum();
        assert!(listed < r.total_samples);
        let f = &r.flame;
        assert_eq!(f.total(), 181);
        assert_eq!(f.unlisted, 181 - listed);
        let u = find(f, "Stacks Spectra didn't list");
        assert_eq!(f.nodes[u].kind, Kind::Unlisted);
        assert_eq!(f.nodes[u].parent, Some(0));
        // The two threads hold every listed sample between them.
        let main = find(f, "Thread_100");
        let worker = find(f, "Thread_104");
        assert_eq!(f.nodes[main].kind, Kind::Thread);
        assert_eq!(f.nodes[main].total + f.nodes[worker].total, listed);
    }

    #[test]
    fn the_layout_spans_the_view_and_each_child_sits_inside_its_parent() {
        let f = report(UNNAMED).flame;
        let v = &f.view;
        assert_eq!(v.cells[0].what, What::Node(0));
        assert_eq!((v.cells[0].x, v.cells[0].w), (0.0, 1.0));
        for c in &v.cells {
            assert!(
                c.x >= -1e-9 && c.x + c.w <= 1.0 + 1e-9,
                "{c:?} inside the view"
            );
            if let What::Node(n) = c.what {
                if c.row == 0 {
                    continue;
                }
                let p = f.nodes[n].parent.unwrap();
                let pc = v
                    .cells
                    .iter()
                    .find(|pc| pc.what == What::Node(p))
                    .expect("a drawn frame's parent is drawn");
                assert_eq!(pc.row + 1, c.row);
                assert!(c.x >= pc.x - 1e-9 && c.x + c.w <= pc.x + pc.w + 1e-9);
            }
        }
        // Rows ascend, and blocks run left to right within a row.
        for pair in v.cells.windows(2) {
            assert!(
                pair[0].row < pair[1].row
                    || (pair[0].row == pair[1].row && pair[0].x + pair[0].w <= pair[1].x + 1e-9)
            );
        }
    }

    #[test]
    fn frames_too_narrow_to_see_merge_into_one_block() {
        let f = report(UNNAMED).flame;
        // Four one-sample children of infer_body, each under MIN_FRAC of 412, merge into "…".
        let merged: Vec<&Cell> = f
            .view
            .cells
            .iter()
            .filter(|c| matches!(c.what, What::More { .. }))
            .collect();
        assert_eq!(merged.len(), 1);
        assert_eq!(
            merged[0].what,
            What::More {
                samples: 4,
                frames: 4
            }
        );
        assert!(
            !f.view.cells.iter().any(|c| c.what
                == What::Node(find(&f, "hir_ty::infer::pat::InferenceContext::infer_pat")))
        );
        // A lone one-sample thread is too narrow even merged, so it isn't drawn.
        assert!(
            !f.view
                .cells
                .iter()
                .any(|c| c.what == What::Node(find(&f, "Thread_2205")))
        );
    }

    #[test]
    fn deep_and_wide_samples_are_capped() {
        let deep: Vec<String> = (0..200).map(|i| format!("f{i}")).collect();
        let deep: Vec<&str> = deep.iter().map(String::as_str).collect();
        let f = Flame::build(&[stack(&deep, 5)], 5);
        assert_eq!(f.view.rows, MAX_ROWS + 1);
        assert!(f.view.clipped);

        let wide: Vec<Stack> = (0..2000)
            .map(|i| stack(&["T", &format!("a{i}"), &format!("b{i}")], 1))
            .collect();
        let f = Flame::build(&wide, 2000);
        assert!(f.view.cells.len() <= MAX_CELLS);
        // 2000 one-sample frames are each too narrow: one merged block per row under the thread.
        assert!(f.view.cells.len() < 10);
    }

    #[test]
    fn zooming_lays_out_the_subtree_and_back_and_whole_return() {
        let mut f = report(UNNAMED).flame;
        let whole = f.view.clone();
        let solve = find(&f, "chalk_solve::solve::Solver::solve");
        f.press(solve);
        assert!(f.zoomed());
        assert_eq!(f.view.root, solve);
        assert_eq!(f.view.cells[0].w, 1.0);
        // Its child solve_goal holds 40 of its 160 samples.
        let goal = find(
            &f,
            "chalk_recursive::fixed_point::RecursiveContext::solve_goal",
        );
        let c = f
            .view
            .cells
            .iter()
            .find(|c| c.what == What::Node(goal))
            .unwrap();
        assert!((c.w - 0.25).abs() < 1e-9);
        assert_eq!(f.view.heaviest[0].name, "chalk_solve::solve::Solver::solve");
        assert_eq!(f.view.heaviest.len(), 2);

        f.press(goal);
        assert_eq!(f.view.root, goal);
        f.back();
        assert_eq!(f.view.root, solve);
        // Pressing the top bar goes back too.
        f.press(solve);
        assert_eq!(f.view, whole);
        f.press(goal);
        f.press(goal);
        f.whole();
        assert_eq!(f.view, whole);
        // Out of range, and back at the top, change nothing.
        f.press(99_999);
        f.back();
        assert_eq!(f.view, whole);
    }

    #[test]
    fn heaviest_functions_sum_self_time_by_name_across_the_tree() {
        let f = report(APP).flame;
        let h = &f.view.heaviest;
        assert!(h.len() <= HEAVIEST);
        assert_eq!(h[0].name, "lumen::layout::Tree::measure");
        // 58 + 3 from two stacks that end at the same frame.
        assert_eq!(h[0].self_samples, 61);
        // free runs under four different callers: 8 + 6 + 2 + 2.
        let free = h.iter().find(|x| x.name == "free").unwrap();
        assert_eq!(free.self_samples, 18);
        assert!(h.windows(2).all(|w| w[0].self_samples >= w[1].self_samples));
        assert!(!h.iter().any(|x| x.name.starts_with("Stacks Spectra")));
    }

    #[test]
    fn unnamed_frames_are_counted_and_marked() {
        assert!(unnamed("???"));
        assert!(unnamed("0x10a3f2c40"));
        assert!(unnamed("  "));
        assert!(!unnamed("0xdeadbeef_thunk"));
        assert!(!unnamed("main"));
        let f = report(UNNAMED).flame;
        // ??? › ??? under fetch_cold, the address, and ??? under Thread_2205.
        assert_eq!(f.unnamed, 4);
        assert!(f.view.heaviest.iter().any(|h| h.unnamed && h.name == "???"));
    }

    #[test]
    fn a_sample_with_no_stacks_or_odd_fields_has_no_flame() {
        assert!(
            report(r#"{"pid":1,"total_samples":0,"stacks":[]}"#)
                .flame
                .is_empty()
        );
        assert!(report(r#"{"pid":1}"#).flame.is_empty());
        // Missing counts and frames don't fail the page.
        let r = report(
            r#"{"total_samples":3,"stacks":[{"frames":["T","a"]},{"count":2},{"frames":["T",""],"count":1}]}"#,
        );
        let f = &r.flame;
        assert_eq!(f.total(), 3);
        assert_eq!(f.nodes[0].self_samples, 2);
        assert_eq!(f.unnamed, 1);
    }
}
