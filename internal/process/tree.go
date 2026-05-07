package process

import "context"

// Collector collects process inventory for higher-level profiling services.
type Collector interface {
	Collect(ctx context.Context, opts CollectOptions) []Info
}

// CollectorFunc adapts a function into a Collector.
type CollectorFunc func(ctx context.Context, opts CollectOptions) []Info

// Collect calls f(ctx, opts).
func (f CollectorFunc) Collect(ctx context.Context, opts CollectOptions) []Info {
	return f(ctx, opts)
}

// SystemCollector collects process inventory from the local system.
type SystemCollector struct{}

// Collect returns all visible local processes.
func (SystemCollector) Collect(ctx context.Context, opts CollectOptions) []Info {
	return CollectAll(ctx, opts)
}

// TreeOptions controls tree collection.
type TreeOptions struct {
	Bundles []string
	Deep    bool
}

// TreeService builds process trees from an injected collector.
type TreeService struct {
	collector Collector
}

// NewTreeService constructs a tree service. Nil collector uses SystemCollector.
func NewTreeService(collector Collector) *TreeService {
	if collector == nil {
		collector = SystemCollector{}
	}
	return &TreeService{collector: collector}
}

// Build collects processes and returns a parent-child tree.
func (s *TreeService) Build(ctx context.Context, opts TreeOptions) []*TreeNode {
	procs := s.collector.Collect(ctx, CollectOptions{
		BundlePaths: opts.Bundles,
		Deep:        opts.Deep,
	})
	if len(opts.Bundles) > 0 {
		procs = ScopeTreeProcesses(procs)
	}
	return BuildTree(procs)
}

// ScopeTreeProcesses keeps app-attributed processes and their ancestors so a
// scoped tree still explains parentage without returning unrelated branches.
func ScopeTreeProcesses(procs []Info) []Info {
	if len(procs) == 0 {
		return nil
	}
	byPID := make(map[int]Info, len(procs))
	keep := make(map[int]bool)
	for _, p := range procs {
		byPID[p.PID] = p
		if p.AppPath != "" {
			keep[p.PID] = true
		}
	}
	for _, p := range procs {
		if p.AppPath == "" {
			continue
		}
		for parent, ok := byPID[p.PPID]; ok && parent.PID != p.PID; parent, ok = byPID[parent.PPID] {
			keep[parent.PID] = true
		}
	}
	out := make([]Info, 0, len(keep))
	for _, p := range procs {
		if keep[p.PID] {
			out = append(out, p)
		}
	}
	return out
}
