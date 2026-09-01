package heap

import (
	"bufio"
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

// DominatorResult is the retained-size analysis of an object graph.
type DominatorResult struct {
	TotalShallowBytes int64             `json:"total_shallow_bytes"`
	ReachableObjects  int               `json:"reachable_objects"`
	ReachableBytes    int64             `json:"reachable_bytes"`
	Suspects          []RetainedSuspect `json:"suspects"`
}

// Dominators computes the dominator tree of g (rooted at a synthetic super-root
// over all GC roots), each reachable object's retained size, and the top N
// objects by retained size. Cooper-Harvey-Kennedy iterative dominators; cost is
// roughly O(nodes × edges) worst case, adequate for moderate heaps.
func Dominators(g *ObjectGraph, topN int) DominatorResult {
	var res DominatorResult
	for _, n := range g.Nodes {
		res.TotalShallowBytes += n.ShallowSize
	}

	// Index the reachable subgraph. Node 0 is the synthetic super-root, whose
	// successors are the GC roots.
	idx := map[uint64]int{}
	var ids []uint64     // dense index (>=1) -> object id
	succ := [][]int{nil} // succ[0] filled after roots are indexed
	ids = append(ids, 0) // placeholder for the super-root at index 0
	var visit func(id uint64) int
	visit = func(id uint64) int {
		if i, ok := idx[id]; ok {
			return i
		}
		node, ok := g.Nodes[id]
		if !ok {
			return -1
		}
		i := len(ids)
		idx[id] = i
		ids = append(ids, id)
		succ = append(succ, nil)
		for _, o := range node.Out {
			if child := visit(o); child >= 0 {
				succ[i] = append(succ[i], child)
			}
		}
		return i
	}
	for _, r := range g.Roots {
		if child := visit(r); child >= 0 {
			succ[0] = append(succ[0], child)
		}
	}

	n := len(ids)
	res.ReachableObjects = n - 1
	if n == 1 {
		return res // no reachable objects
	}
	for i := 1; i < n; i++ {
		res.ReachableBytes += g.Nodes[ids[i]].ShallowSize
	}

	idom := computeIdom(n, succ)
	retained := computeRetained(n, idom, ids, g)

	suspects := make([]RetainedSuspect, 0, n-1)
	for i := 1; i < n; i++ {
		node := g.Nodes[ids[i]]
		pct := 0.0
		if res.ReachableBytes > 0 {
			pct = 100 * float64(retained[i]) / float64(res.ReachableBytes)
		}
		suspects = append(suspects, RetainedSuspect{
			ID: node.ID, ClassName: node.ClassName,
			ShallowBytes: node.ShallowSize, RetainedBytes: retained[i], PercentOfHeap: pct,
		})
	}
	sort.SliceStable(suspects, func(a, b int) bool {
		if suspects[a].RetainedBytes != suspects[b].RetainedBytes {
			return suspects[a].RetainedBytes > suspects[b].RetainedBytes
		}
		return suspects[a].ID < suspects[b].ID
	})
	if topN > 0 && len(suspects) > topN {
		suspects = suspects[:topN]
	}
	res.Suspects = suspects
	return res
}

// computeIdom returns the immediate dominator index of each node (idom[0]=0 for
// the super-root), using the CHK iterative algorithm over a reverse-postorder.
func computeIdom(n int, succ [][]int) []int {
	order, post := reversePostorder(n, succ)
	pred := make([][]int, n)
	for u := 0; u < n; u++ {
		for _, v := range succ[u] {
			pred[v] = append(pred[v], u)
		}
	}

	idom := make([]int, n)
	for i := range idom {
		idom[i] = -1
	}
	idom[0] = 0

	changed := true
	for changed {
		changed = false
		for _, b := range order {
			if b == 0 {
				continue
			}
			newIdom := -1
			for _, p := range pred[b] {
				if idom[p] == -1 {
					continue
				}
				if newIdom == -1 {
					newIdom = p
				} else {
					newIdom = intersect(p, newIdom, idom, post)
				}
			}
			if newIdom != -1 && idom[b] != newIdom {
				idom[b] = newIdom
				changed = true
			}
		}
	}
	return idom
}

func intersect(a, b int, idom, post []int) int {
	for a != b {
		for post[a] < post[b] {
			a = idom[a]
		}
		for post[b] < post[a] {
			b = idom[b]
		}
	}
	return a
}

// reversePostorder returns node indices in reverse postorder (root first) and a
// postorder-number array (higher = later in postorder; the root is highest).
func reversePostorder(n int, succ [][]int) (order, post []int) {
	post = make([]int, n)
	for i := range post {
		post[i] = -1
	}
	visited := make([]bool, n)
	counter := 0
	// Iterative DFS postorder from the super-root (0).
	type frame struct {
		node int
		next int
	}
	stack := []frame{{0, 0}}
	visited[0] = true
	var postList []int
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if top.next < len(succ[top.node]) {
			child := succ[top.node][top.next]
			top.next++
			if !visited[child] {
				visited[child] = true
				stack = append(stack, frame{child, 0})
			}
			continue
		}
		post[top.node] = counter
		postList = append(postList, top.node)
		counter++
		stack = stack[:len(stack)-1]
	}
	// Reverse postorder.
	order = make([]int, 0, len(postList))
	for i := len(postList) - 1; i >= 0; i-- {
		order = append(order, postList[i])
	}
	return order, post
}

// computeRetained sums shallow sizes over the dominator tree: retained[n] =
// shallow[n] + sum(retained[children]).
func computeRetained(n int, idom []int, ids []uint64, g *ObjectGraph) []int64 {
	children := make([][]int, n)
	for i := 1; i < n; i++ {
		if idom[i] >= 0 && idom[i] != i {
			children[idom[i]] = append(children[idom[i]], i)
		}
	}
	retained := make([]int64, n)
	for i := 1; i < n; i++ {
		retained[i] = g.Nodes[ids[i]].ShallowSize
	}
	// Post-order over the dominator tree (root 0), iterative.
	type frame struct {
		node int
		next int
	}
	stack := []frame{{0, 0}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if top.next < len(children[top.node]) {
			c := children[top.node][top.next]
			top.next++
			stack = append(stack, frame{c, 0})
			continue
		}
		if p := idom[top.node]; top.node != 0 && p >= 0 {
			retained[p] += retained[top.node]
		}
		stack = stack[:len(stack)-1]
	}
	return retained
}

// ParseObjectGraphFile streams and parses the object reference graph of the
// .hprof file at path.
func ParseObjectGraphFile(path string) (*ObjectGraph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseObjectGraph(bufio.NewReaderSize(f, 1<<20))
}
