package process

import (
	"context"
	"testing"
)

type fakeProcessCollector struct {
	procs []Info
	opts  []CollectOptions
}

func (c *fakeProcessCollector) Collect(_ context.Context, opts CollectOptions) []Info {
	c.opts = append(c.opts, opts)
	return append([]Info(nil), c.procs...)
}

func TestTreeServiceBuildPassesCollectionOptions(t *testing.T) {
	collector := &fakeProcessCollector{procs: []Info{{PID: 1, PPID: 0, Command: "launchd"}}}
	roots := NewTreeService(collector).Build(context.Background(), TreeOptions{
		Bundles: []string{"/Applications/App.app"},
		Deep:    true,
	})
	if len(roots) != 0 {
		t.Fatalf("roots = %d, want 0 because no process matched bundle scope", len(roots))
	}
	if len(collector.opts) != 1 {
		t.Fatalf("collector calls = %d, want 1", len(collector.opts))
	}
	if !collector.opts[0].Deep {
		t.Error("Deep = false, want true")
	}
	if len(collector.opts[0].BundlePaths) != 1 || collector.opts[0].BundlePaths[0] != "/Applications/App.app" {
		t.Errorf("BundlePaths = %v", collector.opts[0].BundlePaths)
	}
}

func TestScopeTreeProcessesKeepsMatchedProcessesAndAncestors(t *testing.T) {
	procs := []Info{
		{PID: 1, PPID: 0, Command: "launchd"},
		{PID: 10, PPID: 1, Command: "App", AppPath: "/Applications/App.app"},
		{PID: 11, PPID: 10, Command: "Helper", AppPath: "/Applications/App.app"},
		{PID: 99, PPID: 1, Command: "Other"},
	}
	scoped := ScopeTreeProcesses(procs)
	if len(scoped) != 3 {
		t.Fatalf("scoped len = %d, want 3", len(scoped))
	}
	for _, pid := range []int{1, 10, 11} {
		if !hasPID(scoped, pid) {
			t.Errorf("scoped missing PID %d", pid)
		}
	}
	if hasPID(scoped, 99) {
		t.Error("scoped includes unrelated PID 99")
	}
	roots := BuildTree(scoped)
	if len(roots) != 1 || roots[0].Info.PID != 1 {
		t.Fatalf("root = %+v, want PID 1", roots)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].Info.PID != 10 {
		t.Fatalf("root children = %+v, want PID 10", roots[0].Children)
	}
}

func hasPID(procs []Info, pid int) bool {
	for _, p := range procs {
		if p.PID == pid {
			return true
		}
	}
	return false
}
