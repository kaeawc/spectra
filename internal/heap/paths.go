package heap

import (
	"fmt"
	"strings"
)

// PathStep is one object on a reference path.
type PathStep struct {
	ID        uint64 `json:"id"`
	ClassName string `json:"class_name"`
}

// RootPath is a reference chain from a GC root to a target object. Steps[0] is
// the rooted object (Roots lists why it is a root) and the last step is the
// target.
type RootPath struct {
	Roots []GCRoot   `json:"roots"`
	Steps []PathStep `json:"steps"`
}

// predecessors lazily builds the whole-graph reverse adjacency (CSR). It costs
// 8 bytes per object plus 4 per reference and is only built for path queries.
func (g *ObjectGraph) predecessors() ([]int, []int32) {
	if g.predStart != nil {
		return g.predStart, g.preds
	}
	n := g.Len()
	start := make([]int, n+1)
	for _, w := range g.out {
		start[w+1]++
	}
	for i := 1; i <= n; i++ {
		start[i] += start[i-1]
	}
	preds := make([]int32, len(g.out))
	fill := append([]int(nil), start[:n]...)
	for v := 0; v < n; v++ {
		for _, w := range g.Out(int32(v)) {
			preds[fill[w]] = int32(v)
			fill[w]++
		}
	}
	g.predStart, g.preds = start, preds
	return start, preds
}

// PathsToGCRoots returns up to maxPaths shortest reference paths from distinct
// GC roots to any of targets, shortest first. It runs a breadth-first search
// backwards over references from the targets and stops once maxPaths rooted
// objects are reached. Unreachable targets yield no paths.
func (g *ObjectGraph) PathsToGCRoots(targets []int32, maxPaths int) []RootPath {
	if maxPaths <= 0 || len(targets) == 0 {
		return nil
	}
	predStart, preds := g.predecessors()
	const unvisited, target = -2, -1
	next := make([]int32, g.Len()) // toward the target; -1 marks a target
	for i := range next {
		next[i] = unvisited
	}
	queue := make([]int32, 0, len(targets))
	for _, t := range targets {
		if next[t] == unvisited {
			next[t] = target
			queue = append(queue, t)
		}
	}
	var paths []RootPath
	for head := 0; head < len(queue) && len(paths) < maxPaths; head++ {
		v := queue[head]
		if roots := g.RootsOf(v); len(roots) > 0 {
			paths = append(paths, g.pathFrom(v, next, roots))
			continue
		}
		for _, u := range preds[predStart[v]:predStart[v+1]] {
			if next[u] == unvisited {
				next[u] = v
				queue = append(queue, u)
			}
		}
	}
	return paths
}

func (g *ObjectGraph) pathFrom(v int32, next []int32, roots []GCRoot) RootPath {
	p := RootPath{Roots: append([]GCRoot(nil), roots...)}
	for ; v >= 0; v = next[v] {
		p.Steps = append(p.Steps, PathStep{ID: g.ids[v], ClassName: g.DisplayName(v)})
	}
	return p
}

// InstancesOf returns the objects whose class is named className.
func (g *ObjectGraph) InstancesOf(className string) []int32 {
	match := make([]bool, len(g.classNames))
	found := false
	for c, name := range g.classNames {
		if name == className {
			match[c] = true
			found = true
		}
	}
	if !found {
		return nil
	}
	var out []int32
	for i, c := range g.class {
		if match[c] {
			out = append(out, int32(i))
		}
	}
	return out
}

// String renders the path as "[root kinds] A (0x1) → B (0x2)".
func (p RootPath) String() string {
	kinds := make([]string, 0, len(p.Roots))
	seen := map[RootKind]bool{}
	for _, r := range p.Roots {
		if !seen[r.Kind] {
			seen[r.Kind] = true
			kinds = append(kinds, string(r.Kind))
		}
	}
	steps := make([]string, len(p.Steps))
	for i, st := range p.Steps {
		steps[i] = fmt.Sprintf("%s (0x%x)", st.ClassName, st.ID)
	}
	return "[" + strings.Join(kinds, ", ") + "] " + strings.Join(steps, " → ")
}
