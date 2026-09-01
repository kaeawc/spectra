package heap

import (
	"bytes"
	"testing"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func node(id uint64, class string, shallow int64, out ...uint64) *ObjectNode {
	return &ObjectNode{ID: id, ClassName: class, ShallowSize: shallow, Out: out}
}

func graph(roots []uint64, nodes ...*ObjectNode) *ObjectGraph {
	m := map[uint64]*ObjectNode{}
	for _, n := range nodes {
		m[n.ID] = n
	}
	return &ObjectGraph{Nodes: m, Roots: roots, IDSize: 8}
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
}

func TestDominatorsTopN(t *testing.T) {
	g := graph([]uint64{1, 2, 3},
		node(1, "A", 30),
		node(2, "B", 20),
		node(3, "C", 10),
	)
	res := Dominators(g, 2)
	if len(res.Suspects) != 2 {
		t.Fatalf("topN=2 gave %d", len(res.Suspects))
	}
	if res.Suspects[0].ID != 1 || res.Suspects[1].ID != 2 {
		t.Errorf("top-2 by retained = %d,%d want 1,2", res.Suspects[0].ID, res.Suspects[1].ID)
	}
}

func TestDominatorsEmpty(t *testing.T) {
	res := Dominators(&ObjectGraph{Nodes: map[uint64]*ObjectNode{}, IDSize: 8}, 5)
	if res.ReachableObjects != 0 || len(res.Suspects) != 0 {
		t.Errorf("empty graph = %+v", res)
	}
}

func TestDominatorsEndToEndFromHPROF(t *testing.T) {
	// Parse the graph the 9a builder emits, then run retained-size analysis.
	// Reachable from the root (a): a and b form a cycle; the arrays are neither
	// rooted nor referenced, so they are unreachable.
	g, err := ParseObjectGraph(bytesReader(buildGraphDump(8)))
	if err != nil {
		t.Fatalf("ParseObjectGraph: %v", err)
	}
	res := Dominators(g, 5)
	if res.ReachableObjects != 2 {
		t.Fatalf("reachable = %d, want 2 (a,b)", res.ReachableObjects)
	}
	// a shallow = idSize(8); b shallow = instSize(idSize+4=12). a retains both.
	if got := retainedByID(res, 1000); got != 20 {
		t.Errorf("retained(a) = %d, want 20", got)
	}
	if got := retainedByID(res, 1001); got != 12 {
		t.Errorf("retained(b) = %d, want 12", got)
	}
	if res.Suspects[0].ID != 1000 {
		t.Errorf("top suspect = 0x%x, want a(0x3e8)", res.Suspects[0].ID)
	}
}
