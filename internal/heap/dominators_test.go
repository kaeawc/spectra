package heap

import (
	"math/rand"
	"testing"
)

type tnode struct {
	id      uint64
	class   string
	shallow int64
	out     []uint64
}

func node(id uint64, class string, shallow int64, out ...uint64) tnode {
	return tnode{id: id, class: class, shallow: shallow, out: out}
}

// graph builds an ObjectGraph directly (no HPROF encoding) for algorithm tests.
func graph(roots []uint64, nodes ...tnode) *ObjectGraph {
	b := newGraphBuilder(8)
	classes := map[string]int32{}
	for _, n := range nodes {
		c, ok := classes[n.class]
		if !ok {
			c = b.classFor(classKey{classID: 1<<62 + uint64(len(classes))}, n.class)
			classes[n.class] = c
		}
		b.addObject(n.id, c, n.shallow, n.out)
	}
	b.roots = roots
	return b.build(func(classKey) string { return "" })
}

func retainedByID(res DominatorResult, id uint64) int64 {
	for _, s := range res.Suspects {
		if s.ID == id {
			return s.RetainedBytes
		}
	}
	return -1
}

func TestDominatorsDiamond(t *testing.T) {
	// R -> a; a -> b, a -> c; b -> d; c -> d. idom(d) = a, so a retains all.
	g := graph([]uint64{1},
		node(1, "A", 10, 2, 3),
		node(2, "B", 10, 4),
		node(3, "C", 10, 4),
		node(4, "D", 10),
	)
	res := Dominators(g, 0)
	if res.ReachableObjects != 4 || res.ReachableBytes != 40 {
		t.Fatalf("reachable = %d objs / %d bytes", res.ReachableObjects, res.ReachableBytes)
	}
	if got := retainedByID(res, 1); got != 40 {
		t.Errorf("retained(a) = %d, want 40 (a dominates the whole graph)", got)
	}
	for _, id := range []uint64{2, 3, 4} {
		if got := retainedByID(res, id); got != 10 {
			t.Errorf("retained(%d) = %d, want 10", id, got)
		}
	}
	if res.Suspects[0].ID != 1 || res.Suspects[0].PercentOfHeap != 100 {
		t.Errorf("top suspect = %+v, want id 1 at 100%%", res.Suspects[0])
	}
}

func TestDominatorsCycle(t *testing.T) {
	// R -> a; a <-> b. a retains a+b; b retains itself.
	g := graph([]uint64{1},
		node(1, "A", 10, 2),
		node(2, "B", 10, 1),
	)
	res := Dominators(g, 0)
	if got := retainedByID(res, 1); got != 20 {
		t.Errorf("retained(a) = %d, want 20", got)
	}
	if got := retainedByID(res, 2); got != 10 {
		t.Errorf("retained(b) = %d, want 10", got)
	}
}

func TestDominatorsUnreachable(t *testing.T) {
	// orphan (5) is neither a root nor referenced; it counts toward total heap
	// but not the reachable set or the suspects.
	g := graph([]uint64{1},
		node(1, "A", 10),
		node(5, "Orphan", 7),
	)
	res := Dominators(g, 0)
	if res.TotalShallowBytes != 17 {
		t.Errorf("total = %d, want 17", res.TotalShallowBytes)
	}
	if res.ReachableObjects != 1 || res.ReachableBytes != 10 {
		t.Errorf("reachable = %d/%d, want 1/10", res.ReachableObjects, res.ReachableBytes)
	}
	if retainedByID(res, 5) != -1 {
		t.Error("orphan must not appear in suspects")
	}
	for _, c := range res.Classes {
		if c.ClassName == "Orphan" {
			t.Errorf("unreachable class listed: %+v", c)
		}
	}
}

func TestDominatorsTopN(t *testing.T) {
	g := graph([]uint64{1, 2, 3, 4},
		node(1, "A", 30),
		node(2, "B", 20),
		node(3, "C", 10),
		node(4, "D", 20),
	)
	res := Dominators(g, 2)
	if len(res.Suspects) != 2 || len(res.Classes) != 2 {
		t.Fatalf("topN=2 gave %d objects / %d classes", len(res.Suspects), len(res.Classes))
	}
	// Equal retained sizes tie-break by ascending id.
	if res.Suspects[0].ID != 1 || res.Suspects[1].ID != 2 {
		t.Errorf("top-2 by retained = %d,%d want 1,2", res.Suspects[0].ID, res.Suspects[1].ID)
	}
	if res.Classes[0].ClassName != "A" || res.Classes[1].ClassName != "B" {
		t.Errorf("top-2 classes = %+v", res.Classes)
	}
}

func TestDominatorsEmpty(t *testing.T) {
	res := Dominators(graph(nil), 5)
	if res.ReachableObjects != 0 || len(res.Suspects) != 0 || len(res.Classes) != 0 {
		t.Errorf("empty graph = %+v", res)
	}
}

func TestClassRetainedNotDoubleCounted(t *testing.T) {
	// A linked list of three Node objects under a holder: the Node class retains
	// the list once (the head's retained size), not head+mid+tail.
	g := graph([]uint64{1},
		node(1, "Holder", 5, 2),
		node(2, "Node", 10, 3),
		node(3, "Node", 10, 4),
		node(4, "Node", 10, 5),
		node(5, "Leaf", 1),
	)
	res := Dominators(g, 0)
	var nodeClass ClassRetained
	for _, c := range res.Classes {
		if c.ClassName == "Node" {
			nodeClass = c
		}
	}
	if nodeClass.Instances != 3 || nodeClass.ShallowBytes != 30 || nodeClass.RetainedBytes != 31 {
		t.Errorf("Node class = %+v, want 3 instances / 30 shallow / 31 retained", nodeClass)
	}
	if res.Classes[0].ClassName != "Holder" || res.Classes[0].RetainedBytes != 36 {
		t.Errorf("top class = %+v, want Holder retaining 36", res.Classes[0])
	}
}

func TestDominatorsDeepChain(t *testing.T) {
	// A long chain exercises the iterative DFS and path compression without
	// recursion; the head retains everything.
	const n = 200000
	nodes := make([]tnode, n)
	for i := 0; i < n; i++ {
		var out []uint64
		if i+1 < n {
			out = []uint64{uint64(i + 2)}
		}
		nodes[i] = node(uint64(i+1), "N", 1, out...)
	}
	res := Dominators(graph([]uint64{1}, nodes...), 1)
	if res.Suspects[0].ID != 1 || res.Suspects[0].RetainedBytes != n {
		t.Fatalf("head = %+v, want retained %d", res.Suspects[0], n)
	}
}

func TestImmediateDominatorAccessors(t *testing.T) {
	g := graph([]uint64{1, 2},
		node(1, "A", 1, 3),
		node(2, "B", 1, 3),
		node(3, "C", 1, 4),
		node(4, "D", 1),
		node(9, "Orphan", 1),
	)
	s := ComputeRetained(g)
	idx := func(id uint64) int32 {
		i, ok := g.Index(id)
		if !ok {
			t.Fatalf("id %d missing", id)
		}
		return i
	}
	if _, ok := s.ImmediateDominator(idx(3)); ok {
		t.Error("C is reachable from two roots; only the super-root dominates it")
	}
	if d, ok := s.ImmediateDominator(idx(4)); !ok || g.ID(d) != 3 {
		t.Errorf("idom(D) = %d,%v want C", d, ok)
	}
	if s.Reachable(idx(9)) || s.Retained(idx(9)) != 0 {
		t.Error("orphan must be unreachable with zero retained size")
	}
}

func BenchmarkComputeRetained(b *testing.B) {
	const n = 1_000_000
	rng := rand.New(rand.NewSource(1))
	gb := newGraphBuilder(8)
	c := gb.classFor(classKey{classID: 1 << 62}, "X")
	refs := make([]uint64, 4)
	for i := 0; i < n; i++ {
		for k := range refs {
			refs[k] = uint64(1 + rng.Intn(n))
		}
		gb.addObject(uint64(i+1), c, 16, refs)
	}
	gb.roots = []uint64{1, 2, 3}
	g := gb.build(func(classKey) string { return "" })
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RankRetained(ComputeRetained(g), 20)
	}
}

// bruteDominators computes, for each reachable node, its immediate dominator
// (-1 for the super-root) and retained size by the definition: v dominates w
// iff removing v makes w unreachable from the roots.
func bruteDominators(n int, succ [][]int, roots []int, shallow []int64) (idom []int, retained []int64, reach []bool) {
	reachWithout := func(skip int) []bool {
		seen := make([]bool, n)
		var stack []int
		for _, r := range roots {
			if r != skip && !seen[r] {
				seen[r] = true
				stack = append(stack, r)
			}
		}
		for len(stack) > 0 {
			v := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, w := range succ[v] {
				if w != skip && !seen[w] {
					seen[w] = true
					stack = append(stack, w)
				}
			}
		}
		return seen
	}
	reach = reachWithout(-1)
	dom := make([][]bool, n) // dom[v][w]: v strictly dominates w
	for v := 0; v < n; v++ {
		dom[v] = make([]bool, n)
		if !reach[v] {
			continue
		}
		without := reachWithout(v)
		for w := 0; w < n; w++ {
			dom[v][w] = w != v && reach[w] && !without[w]
		}
	}
	idom = make([]int, n)
	retained = make([]int64, n)
	for w := 0; w < n; w++ {
		idom[w] = -1
		if !reach[w] {
			continue
		}
		retained[w] = shallow[w]
		for v := 0; v < n; v++ {
			if dom[w][v] {
				retained[w] += shallow[v]
			}
			// idom is the strict dominator that every other strict dominator dominates.
			if dom[v][w] && (idom[w] == -1 || dom[idom[w]][v]) {
				idom[w] = v
			}
		}
	}
	return idom, retained, reach
}

func TestLengauerTarjanMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 300; iter++ {
		n := 2 + rng.Intn(25)
		succ := make([][]int, n)
		shallow := make([]int64, n)
		nodes := make([]tnode, n)
		edges := rng.Intn(3 * n)
		for e := 0; e < edges; e++ {
			u, v := rng.Intn(n), rng.Intn(n)
			succ[u] = append(succ[u], v)
		}
		var roots []int
		var rootIDs []uint64
		for k := 1 + rng.Intn(3); k > 0; k-- {
			r := rng.Intn(n)
			roots = append(roots, r)
			rootIDs = append(rootIDs, uint64(r+1))
		}
		for i := 0; i < n; i++ {
			shallow[i] = int64(1 + rng.Intn(100))
			var out []uint64
			for _, w := range succ[i] {
				out = append(out, uint64(w+1))
			}
			nodes[i] = node(uint64(i+1), "X", shallow[i], out...)
		}
		wantIdom, wantRetained, reach := bruteDominators(n, succ, roots, shallow)

		g := graph(rootIDs, nodes...)
		s := ComputeRetained(g)
		for i := 0; i < n; i++ {
			obj, _ := g.Index(uint64(i + 1))
			if s.Reachable(obj) != reach[i] {
				t.Fatalf("iter %d node %d: reachable = %v, want %v", iter, i, s.Reachable(obj), reach[i])
			}
			if !reach[i] {
				continue
			}
			got := -1
			if d, ok := s.ImmediateDominator(obj); ok {
				got = int(g.ID(d)) - 1
			}
			if got != wantIdom[i] {
				t.Fatalf("iter %d node %d: idom = %d, want %d (succ %v roots %v)", iter, i, got, wantIdom[i], succ, roots)
			}
			if s.Retained(obj) != wantRetained[i] {
				t.Fatalf("iter %d node %d: retained = %d, want %d", iter, i, s.Retained(obj), wantRetained[i])
			}
		}
	}
}
