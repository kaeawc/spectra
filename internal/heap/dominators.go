package heap

import (
	"bufio"
	"container/heap"
	"fmt"
	"os"
	"sort"
)

// RetainedSuspect is one object ranked by retained size — the memory that would
// be freed if it were collected.
type RetainedSuspect struct {
	ID            uint64  `json:"id"`
	ClassName     string  `json:"class_name"`
	ShallowBytes  int64   `json:"shallow_bytes"`
	RetainedBytes int64   `json:"retained_bytes"`
	PercentOfHeap float64 `json:"percent_of_heap"`
}

// ClassRetained aggregates the reachable instances of one class. RetainedBytes
// is the memory dominated by the class's instances: the sum of retained sizes
// of instances not themselves dominated by another instance of the class, so
// nested instances (linked-list nodes, tree nodes) are not double counted. It
// is a lower bound on the retained size of the instance set as a whole.
type ClassRetained struct {
	ClassName     string  `json:"class_name"`
	Instances     int64   `json:"instances"`
	ShallowBytes  int64   `json:"shallow_bytes"`
	RetainedBytes int64   `json:"retained_bytes"`
	PercentOfHeap float64 `json:"percent_of_heap"`
}

// DominatorResult is the ranked retained-size analysis of an object graph.
type DominatorResult struct {
	TotalShallowBytes int64             `json:"total_shallow_bytes"`
	ReachableObjects  int               `json:"reachable_objects"`
	ReachableBytes    int64             `json:"reachable_bytes"`
	Suspects          []RetainedSuspect `json:"suspects"`
	Classes           []ClassRetained   `json:"classes,omitempty"`
}

// RetainedSnapshot is an object graph plus its dominator tree and retained
// sizes. The dominator tree is rooted at a virtual super-root whose children
// are the GC roots. Internally nodes are numbered by DFS preorder from the
// super-root (0 = super-root), which guarantees idom(v) < v.
type RetainedSnapshot struct {
	Graph             *ObjectGraph
	TotalShallowBytes int64
	ReachableObjects  int
	ReachableBytes    int64

	dfnum      []int32 // object index -> preorder number; -1 if unreachable
	vertex     []int32 // preorder number -> object index; vertex[0] = -1
	idom       []int32 // preorder number -> preorder number of immediate dominator
	retained   []int64 // preorder number -> retained bytes
	childStart []int32 // dominator-tree children CSR, preorder space
	children   []int32
	classes    []ClassRetained // class index -> aggregate
}

// ComputeRetained builds the dominator tree of g and every reachable object's
// retained size using the Lengauer-Tarjan algorithm (simple version with path
// compression, O(E log N)). All working state is int32 arrays indexed by DFS
// preorder number.
func ComputeRetained(g *ObjectGraph) *RetainedSnapshot {
	s := &RetainedSnapshot{Graph: g}
	for i := range g.shallow {
		s.TotalShallowBytes += g.shallow[i]
	}
	var parent []int32
	s.dfnum, s.vertex, parent = dfsPreorder(g)
	m := len(s.vertex)
	s.ReachableObjects = m - 1

	predStart, preds := s.predecessors()
	s.idom = lengauerTarjan(m, parent, predStart, preds)

	s.retained = make([]int64, m)
	for d := 1; d < m; d++ {
		s.retained[d] = g.shallow[s.vertex[d]]
	}
	for d := m - 1; d >= 1; d-- {
		s.retained[s.idom[d]] += s.retained[d]
	}
	s.ReachableBytes = s.retained[0]

	s.childStart, s.children = dominatorChildren(s.idom)
	s.classes = s.aggregateClasses()
	return s
}

// dfsPreorder numbers the objects reachable from the GC roots in DFS preorder
// from the virtual super-root (number 0), returning object->number,
// number->object, and the DFS-tree parent of each number.
func dfsPreorder(g *ObjectGraph) (dfnum, vertex, parent []int32) {
	dfnum = make([]int32, g.Len())
	for i := range dfnum {
		dfnum[i] = -1
	}
	vertex = make([]int32, 1, g.Len()+1)
	parent = make([]int32, 1, g.Len()+1)
	vertex[0], parent[0] = -1, -1
	type frame struct {
		v    int32
		next int
	}
	var stack []frame
	visit := func(v, from int32) {
		dfnum[v] = int32(len(vertex))
		vertex = append(vertex, v)
		parent = append(parent, from)
		stack = append(stack, frame{v, g.outStart[v]})
	}
	for _, r := range g.roots {
		if dfnum[r] >= 0 {
			continue
		}
		visit(r, 0)
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.next == g.outStart[top.v+1] {
				stack = stack[:len(stack)-1]
				continue
			}
			w := g.out[top.next]
			top.next++
			if dfnum[w] < 0 {
				visit(w, dfnum[top.v])
			}
		}
	}
	return dfnum, vertex, parent
}

// predecessors returns the reverse reachable graph in preorder space as CSR.
// The super-root (0) precedes every GC root.
func (s *RetainedSnapshot) predecessors() (start []int, preds []int32) {
	g := s.Graph
	m := len(s.vertex)
	start = make([]int, m+1)
	for d := 1; d < m; d++ {
		for _, w := range g.Out(s.vertex[d]) {
			start[s.dfnum[w]+1]++
		}
	}
	for _, r := range g.roots {
		start[s.dfnum[r]+1]++
	}
	for i := 1; i <= m; i++ {
		start[i] += start[i-1]
	}
	preds = make([]int32, start[m])
	fill := append([]int(nil), start[:m]...)
	add := func(to, from int32) {
		preds[fill[to]] = from
		fill[to]++
	}
	for d := 1; d < m; d++ {
		for _, w := range g.Out(s.vertex[d]) {
			add(s.dfnum[w], int32(d))
		}
	}
	for _, r := range g.roots {
		add(s.dfnum[r], 0)
	}
	return start, preds
}

// ltState is the Lengauer-Tarjan forest (ancestor/label) with semidominators.
type ltState struct {
	semi, ancestor, label []int32
	path                  []int32
}

func (lt *ltState) eval(v int32) int32 {
	if lt.ancestor[v] == -1 {
		return v
	}
	lt.compress(v)
	return lt.label[v]
}

// compress is the iterative form of the textbook recursive path compression.
func (lt *ltState) compress(v int32) {
	lt.path = lt.path[:0]
	for x := v; lt.ancestor[lt.ancestor[x]] != -1; x = lt.ancestor[x] {
		lt.path = append(lt.path, x)
	}
	for i := len(lt.path) - 1; i >= 0; i-- {
		y := lt.path[i]
		a := lt.ancestor[y]
		if lt.semi[lt.label[a]] < lt.semi[lt.label[y]] {
			lt.label[y] = lt.label[a]
		}
		lt.ancestor[y] = lt.ancestor[a]
	}
}

// lengauerTarjan returns idom (preorder space) for a graph of m nodes numbered
// in DFS preorder from root 0.
func lengauerTarjan(m int, parent []int32, predStart []int, preds []int32) []int32 {
	lt := &ltState{
		semi:     make([]int32, m),
		ancestor: make([]int32, m),
		label:    make([]int32, m),
	}
	idom := make([]int32, m)
	bucketHead := make([]int32, m)
	bucketNext := make([]int32, m)
	for i := 0; i < m; i++ {
		lt.semi[i] = int32(i)
		lt.label[i] = int32(i)
		lt.ancestor[i] = -1
		bucketHead[i] = -1
	}
	for w := int32(m - 1); w >= 1; w-- {
		for _, v := range preds[predStart[w]:predStart[w+1]] {
			if u := lt.eval(v); lt.semi[u] < lt.semi[w] {
				lt.semi[w] = lt.semi[u]
			}
		}
		sw := lt.semi[w]
		bucketNext[w] = bucketHead[sw]
		bucketHead[sw] = w
		p := parent[w]
		lt.ancestor[w] = p
		for v := bucketHead[p]; v != -1; v = bucketNext[v] {
			if u := lt.eval(v); lt.semi[u] < lt.semi[v] {
				idom[v] = u
			} else {
				idom[v] = p
			}
		}
		bucketHead[p] = -1
	}
	for w := 1; w < m; w++ {
		if idom[w] != lt.semi[w] {
			idom[w] = idom[idom[w]]
		}
	}
	return idom
}

// dominatorChildren returns the dominator tree's child lists as CSR.
func dominatorChildren(idom []int32) (start, children []int32) {
	m := len(idom)
	start = make([]int32, m+1)
	for d := 1; d < m; d++ {
		start[idom[d]+1]++
	}
	for i := 1; i <= m; i++ {
		start[i] += start[i-1]
	}
	children = make([]int32, start[m])
	fill := append([]int32(nil), start[:m]...)
	for d := 1; d < m; d++ {
		p := idom[d]
		children[fill[p]] = int32(d)
		fill[p]++
	}
	return start, children
}

// aggregateClasses walks the dominator tree, crediting a class with an
// instance's retained size only when no dominator ancestor is of the same
// class.
func (s *RetainedSnapshot) aggregateClasses() []ClassRetained {
	g := s.Graph
	classes := make([]ClassRetained, len(g.classNames))
	for i := range classes {
		classes[i].ClassName = g.classNames[i]
	}
	onPath := make([]int32, len(g.classNames))
	type frame struct{ d, next int32 }
	stack := []frame{{0, s.childStart[0]}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if top.next == s.childStart[top.d+1] {
			if top.d != 0 {
				onPath[g.class[s.vertex[top.d]]]--
			}
			stack = stack[:len(stack)-1]
			continue
		}
		d := s.children[top.next]
		top.next++
		obj := s.vertex[d]
		c := g.class[obj]
		classes[c].Instances++
		classes[c].ShallowBytes += g.shallow[obj]
		if onPath[c] == 0 {
			classes[c].RetainedBytes += s.retained[d]
		}
		onPath[c]++
		stack = append(stack, frame{d, s.childStart[d]})
	}
	return classes
}

// Retained returns object i's retained size, or 0 if it is unreachable.
func (s *RetainedSnapshot) Retained(i int32) int64 {
	if d := s.dfnum[i]; d > 0 {
		return s.retained[d]
	}
	return 0
}

// Reachable reports whether object i is reachable from a GC root.
func (s *RetainedSnapshot) Reachable(i int32) bool { return s.dfnum[i] > 0 }

// ImmediateDominator returns object i's immediate dominator. ok is false when
// i is unreachable or is dominated only by the virtual super-root.
func (s *RetainedSnapshot) ImmediateDominator(i int32) (int32, bool) {
	d := s.dfnum[i]
	if d <= 0 || s.idom[d] == 0 {
		return -1, false
	}
	return s.vertex[s.idom[d]], true
}

// percentOf returns b as a percentage of the reachable heap.
func (s *RetainedSnapshot) percentOf(b int64) float64 {
	if s.ReachableBytes <= 0 {
		return 0
	}
	return 100 * float64(b) / float64(s.ReachableBytes)
}

func (s *RetainedSnapshot) suspect(d int32) RetainedSuspect {
	g := s.Graph
	obj := s.vertex[d]
	return RetainedSuspect{
		ID:            g.ids[obj],
		ClassName:     g.DisplayName(obj),
		ShallowBytes:  g.shallow[obj],
		RetainedBytes: s.retained[d],
		PercentOfHeap: s.percentOf(s.retained[d]),
	}
}

// RankRetained returns the top limit objects and classes by retained size
// (limit <= 0 returns all). Ties break by object id / class name.
func RankRetained(s *RetainedSnapshot, limit int) DominatorResult {
	res := DominatorResult{
		TotalShallowBytes: s.TotalShallowBytes,
		ReachableObjects:  s.ReachableObjects,
		ReachableBytes:    s.ReachableBytes,
	}
	for _, d := range s.topRetained(limit) {
		res.Suspects = append(res.Suspects, s.suspect(d))
	}
	res.Classes = s.rankClasses(limit)
	return res
}

// topRetained selects the preorder numbers of the limit largest retained
// objects with a bounded min-heap, so ranking a huge heap is O(N log limit).
func (s *RetainedSnapshot) topRetained(limit int) []int32 {
	n := len(s.vertex) - 1
	if limit <= 0 || limit > n {
		limit = n
	}
	h := &retainedHeap{s: s}
	for d := int32(1); d <= int32(n); d++ {
		if h.Len() < limit {
			heap.Push(h, d)
		} else if limit > 0 && h.less(h.items[0], d) {
			h.items[0] = d
			heap.Fix(h, 0)
		}
	}
	out := h.items
	sort.Slice(out, func(a, b int) bool { return h.less(out[b], out[a]) })
	return out
}

func (s *RetainedSnapshot) rankClasses(limit int) []ClassRetained {
	var out []ClassRetained
	for _, c := range s.classes {
		if c.Instances == 0 {
			continue
		}
		c.PercentOfHeap = s.percentOf(c.RetainedBytes)
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].RetainedBytes != out[b].RetainedBytes {
			return out[a].RetainedBytes > out[b].RetainedBytes
		}
		return out[a].ClassName < out[b].ClassName
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// retainedHeap is a min-heap of preorder numbers ordered by retained size, so
// the root is the smallest of the current top set.
type retainedHeap struct {
	s     *RetainedSnapshot
	items []int32
}

// less orders a before b when a ranks lower: smaller retained, then larger id.
func (h *retainedHeap) less(a, b int32) bool {
	ra, rb := h.s.retained[a], h.s.retained[b]
	if ra != rb {
		return ra < rb
	}
	return h.s.Graph.ids[h.s.vertex[a]] > h.s.Graph.ids[h.s.vertex[b]]
}

func (h *retainedHeap) Len() int           { return len(h.items) }
func (h *retainedHeap) Less(i, j int) bool { return h.less(h.items[i], h.items[j]) }
func (h *retainedHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *retainedHeap) Push(x any)         { h.items = append(h.items, x.(int32)) }
func (h *retainedHeap) Pop() any {
	x := h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	return x
}

// Dominators computes the retained-size analysis of g and ranks the top N
// objects and classes by retained size.
func Dominators(g *ObjectGraph, topN int) DominatorResult {
	return RankRetained(ComputeRetained(g), topN)
}

// ParseObjectGraphFile streams and parses the object reference graph of the
// .hprof file at path.
func ParseObjectGraphFile(path string) (*ObjectGraph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open heap dump: %w", err)
	}
	defer func() { _ = f.Close() }()
	return ParseObjectGraph(bufio.NewReaderSize(f, 1<<20))
}
