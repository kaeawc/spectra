package heap

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// DefaultLeakThresholdPercent is the share of the reachable heap a
	// dominator subtree (or a class's top-level instances together) must
	// retain to be reported as a leak suspect.
	DefaultLeakThresholdPercent = 10.0
	defaultMaxLeakSuspects      = 10
	// accumulationRatio: descend the dominator tree while one child retains at
	// least this share of its parent; where no child does, memory fans out
	// into many objects — the accumulation point (typically a collection's
	// backing array).
	accumulationRatio = 0.8
)

// Leak suspect kinds.
const (
	LeakSuspectObject = "object"
	LeakSuspectClass  = "class"
)

// LeakOptions tunes LeakSuspects. Zero values select the defaults.
type LeakOptions struct {
	ThresholdPercent float64
	MaxSuspects      int
}

// AccumulationPoint is the object inside a suspect's dominator subtree where
// retained memory stops flowing through a single child and fans out.
type AccumulationPoint struct {
	ID               uint64  `json:"id"`
	ClassName        string  `json:"class_name"`
	RetainedBytes    int64   `json:"retained_bytes"`
	PercentOfHeap    float64 `json:"percent_of_heap"`
	DominatedObjects int     `json:"dominated_objects"`
	Depth            int     `json:"depth"`
}

// LeakSuspect is one automatically detected leak suspect. Object suspects are
// a single dominator subtree; class suspects are many top-level instances of
// one class that only together exceed the threshold.
type LeakSuspect struct {
	Kind          string             `json:"kind"`
	ClassName     string             `json:"class_name"`
	ID            uint64             `json:"id,omitempty"`
	Instances     int64              `json:"instances"`
	RetainedBytes int64              `json:"retained_bytes"`
	PercentOfHeap float64            `json:"percent_of_heap"`
	Accumulation  *AccumulationPoint `json:"accumulation_point,omitempty"`
	// PathToRoot is the shortest path from a GC root to the accumulation point
	// (object suspects) or to the largest instance (class suspects).
	PathToRoot  *RootPath `json:"path_to_root,omitempty"`
	Description string    `json:"description"`
}

// LeakReport is the automated leak-suspects report for one heap dump.
type LeakReport struct {
	TotalShallowBytes int64         `json:"total_shallow_bytes"`
	ReachableObjects  int           `json:"reachable_objects"`
	ReachableBytes    int64         `json:"reachable_bytes"`
	ThresholdPercent  float64       `json:"threshold_percent"`
	Suspects          []LeakSuspect `json:"suspects"`
}

// LeakSuspects ranks the dominator tree's top-level subtrees (children of the
// virtual super-root) by retained size. A subtree retaining at least the
// threshold share of the reachable heap is an object suspect; smaller
// top-level subtrees are grouped by class, and a class whose instances
// together reach the threshold is a class suspect. Each suspect is attributed
// to its accumulation point and the shortest path from a GC root.
func LeakSuspects(s *RetainedSnapshot, opts LeakOptions) LeakReport {
	if opts.ThresholdPercent <= 0 {
		opts.ThresholdPercent = DefaultLeakThresholdPercent
	}
	if opts.MaxSuspects <= 0 {
		opts.MaxSuspects = defaultMaxLeakSuspects
	}
	rep := LeakReport{
		TotalShallowBytes: s.TotalShallowBytes,
		ReachableObjects:  s.ReachableObjects,
		ReachableBytes:    s.ReachableBytes,
		ThresholdPercent:  opts.ThresholdPercent,
	}
	if s.ReachableBytes <= 0 {
		return rep
	}
	threshold := int64(float64(s.ReachableBytes) * opts.ThresholdPercent / 100)
	if threshold < 1 {
		threshold = 1
	}
	groups := map[int32]*classGroup{}
	for _, d := range s.children[s.childStart[0]:s.childStart[1]] {
		if s.retained[d] >= threshold {
			rep.Suspects = append(rep.Suspects, s.objectSuspect(d))
			continue
		}
		c := s.Graph.class[s.vertex[d]]
		grp := groups[c]
		if grp == nil {
			grp = &classGroup{largest: d}
			groups[c] = grp
		}
		grp.add(s, d)
	}
	for _, grp := range groups {
		if grp.count > 1 && grp.retained >= threshold {
			rep.Suspects = append(rep.Suspects, s.classSuspect(grp))
		}
	}
	sort.Slice(rep.Suspects, func(a, b int) bool {
		x, y := rep.Suspects[a], rep.Suspects[b]
		if x.RetainedBytes != y.RetainedBytes {
			return x.RetainedBytes > y.RetainedBytes
		}
		if x.ClassName != y.ClassName {
			return x.ClassName < y.ClassName
		}
		return x.ID < y.ID
	})
	if len(rep.Suspects) > opts.MaxSuspects {
		rep.Suspects = rep.Suspects[:opts.MaxSuspects]
	}
	return rep
}

type classGroup struct {
	count    int64
	retained int64
	largest  int32 // preorder number of the largest instance
}

func (grp *classGroup) add(s *RetainedSnapshot, d int32) {
	grp.count++
	grp.retained += s.retained[d]
	if s.retained[d] > s.retained[grp.largest] {
		grp.largest = d
	}
}

// accumulationPoint descends from d while a single dominator child retains at
// least accumulationRatio of its parent, returning the point and its preorder
// number.
func (s *RetainedSnapshot) accumulationPoint(d int32) (AccumulationPoint, int32) {
	depth := 0
	for {
		best := int32(-1)
		for _, c := range s.children[s.childStart[d]:s.childStart[d+1]] {
			if best == -1 || s.retained[c] > s.retained[best] {
				best = c
			}
		}
		if best == -1 || float64(s.retained[best]) < accumulationRatio*float64(s.retained[d]) {
			break
		}
		d = best
		depth++
	}
	obj := s.vertex[d]
	return AccumulationPoint{
		ID:               s.Graph.ids[obj],
		ClassName:        s.Graph.DisplayName(obj),
		RetainedBytes:    s.retained[d],
		PercentOfHeap:    s.percentOf(s.retained[d]),
		DominatedObjects: int(s.childStart[d+1] - s.childStart[d]),
		Depth:            depth,
	}, d
}

func (s *RetainedSnapshot) shortestPath(d int32) *RootPath {
	paths := s.Graph.PathsToGCRoots([]int32{s.vertex[d]}, 1)
	if len(paths) == 0 {
		return nil
	}
	return &paths[0]
}

func (s *RetainedSnapshot) objectSuspect(d int32) LeakSuspect {
	obj := s.vertex[d]
	acc, accD := s.accumulationPoint(d)
	ls := LeakSuspect{
		Kind:          LeakSuspectObject,
		ClassName:     s.Graph.DisplayName(obj),
		ID:            s.Graph.ids[obj],
		Instances:     1,
		RetainedBytes: s.retained[d],
		PercentOfHeap: s.percentOf(s.retained[d]),
		Accumulation:  &acc,
		PathToRoot:    s.shortestPath(accD),
	}
	ls.Description = describeObjectSuspect(ls)
	return ls
}

func (s *RetainedSnapshot) classSuspect(grp *classGroup) LeakSuspect {
	obj := s.vertex[grp.largest]
	ls := LeakSuspect{
		Kind:          LeakSuspectClass,
		ClassName:     s.Graph.ClassName(obj),
		Instances:     grp.count,
		RetainedBytes: grp.retained,
		PercentOfHeap: s.percentOf(grp.retained),
		PathToRoot:    s.shortestPath(grp.largest),
	}
	ls.Description = fmt.Sprintf("%d instances of %s together retain %s (%.1f%% of the reachable heap); none is dominated by another object, so each is held directly by GC roots or shared referrers.",
		ls.Instances, ls.ClassName, formatBytes(ls.RetainedBytes), ls.PercentOfHeap)
	if p := ls.PathToRoot; p != nil {
		ls.Description += " Largest instance path: " + p.String() + "."
	}
	return ls
}

func describeObjectSuspect(ls LeakSuspect) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (0x%x) retains %s (%.1f%% of the reachable heap).",
		ls.ClassName, ls.ID, formatBytes(ls.RetainedBytes), ls.PercentOfHeap)
	if acc := ls.Accumulation; acc != nil && acc.Depth > 0 {
		fmt.Fprintf(&b, " Memory accumulates in %s (0x%x), which retains %s",
			acc.ClassName, acc.ID, formatBytes(acc.RetainedBytes))
		if acc.DominatedObjects > 0 {
			fmt.Fprintf(&b, " across %d directly dominated objects", acc.DominatedObjects)
		}
		b.WriteString(".")
	} else if acc != nil && acc.DominatedObjects > 1 {
		fmt.Fprintf(&b, " It directly dominates %d objects.", acc.DominatedObjects)
	}
	if p := ls.PathToRoot; p != nil {
		fmt.Fprintf(&b, " Shortest path from a GC root: %s.", p.String())
	}
	return b.String()
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
