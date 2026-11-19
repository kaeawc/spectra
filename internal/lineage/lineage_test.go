package lineage

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/process"
)

func p(pid, ppid int, cmd string, rss int64, cpu float64) process.Info {
	return process.Info{PID: pid, PPID: ppid, Command: cmd, FullCommandLine: cmd, RSSKiB: rss, CPUPct: cpu, UID: 501, User: "me", VSizeKiB: 100}
}

func pids(a []Ancestor) []int {
	var out []int
	for _, x := range a {
		out = append(out, x.PID)
	}
	return out
}

func TestAncestorChain(t *testing.T) {
	root := p(1, 0, "launchd", 1, 0)
	root.UID = 0
	tests := []struct {
		name  string
		procs []process.Info
		pid   int
		want  []int
	}{
		{"stops at pid 1", []process.Info{root, p(10, 1, "zsh", 1, 0), p(20, 10, "make", 1, 0)}, 20, []int{20, 10, 1}},
		{"pid 1 itself", []process.Info{root}, 1, []int{1}},
		{"missing parent", []process.Info{p(20, 99, "x", 1, 0)}, 20, []int{20}},
		{"self parent", []process.Info{p(20, 20, "x", 1, 0)}, 20, []int{20}},
		{"pid0 parent", []process.Info{p(20, 0, "x", 1, 0), p(0, 0, "kernel", 1, 0)}, 20, []int{20}},
		{"cycle", []process.Info{p(2, 3, "a", 1, 0), p(3, 4, "b", 1, 0), p(4, 2, "c", 1, 0)}, 2, []int{2, 3, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Build(tt.procs, tt.pid)
			if err != nil {
				t.Fatal(err)
			}
			if got := pids(r.Ancestors); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ancestors = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDescendantsAndTotals(t *testing.T) {
	procs := []process.Info{
		p(1, 0, "launchd", 1, 0),
		p(10, 1, "root", 100, 1.5),
		p(11, 10, "a", 200, 2),
		p(12, 10, "b", 300, 3),
		p(13, 11, "c", 400, 4),
		p(99, 1, "other", 9999, 9),
	}
	r, err := Build(procs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tree.Count != 4 || r.Tree.SubtreeRSSKiB != 1000 {
		t.Errorf("totals = %d/%d", r.Tree.Count, r.Tree.SubtreeRSSKiB)
	}
	if r.Tree.SubtreeCPUPct != 10.5 {
		t.Errorf("cpu = %v", r.Tree.SubtreeCPUPct)
	}
	if len(r.Tree.Children) != 2 || r.Tree.Children[0].PID != 11 || r.Tree.Children[0].Children[0].PID != 13 {
		t.Errorf("tree shape wrong: %+v", r.Tree)
	}
	if r.Tree.Children[0].RSSKiB != 200 || r.Tree.Children[0].SubtreeRSSKiB != 600 {
		t.Errorf("node rss wrong: %+v", r.Tree.Children[0])
	}
}

func TestDescendantCycleTerminates(t *testing.T) {
	procs := []process.Info{p(2, 3, "a", 1, 0), p(3, 2, "b", 1, 0)}
	r, err := Build(procs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tree.Count != 2 {
		t.Errorf("count = %d", r.Tree.Count)
	}
}

func TestZombieFlag(t *testing.T) {
	tests := []struct {
		name string
		info process.Info
		want bool
	}{
		{"macos live parens", process.Info{PID: 5, PPID: 10, Command: "(bash)", FullCommandLine: "(bash)"}, false},
		{"sd-pam", process.Info{PID: 5, PPID: 10, Command: "(sd-pam)", FullCommandLine: "(sd-pam)"}, false},
		{"defunct", process.Info{PID: 5, PPID: 10, Command: "sh <defunct>", VSizeKiB: 0}, true},
		{"linux zombie", process.Info{PID: 5, PPID: 1234, Command: "sh", FullCommandLine: "[sh]"}, true},
		{"kernel thread ppid 2", process.Info{PID: 5, PPID: 2, Command: "kworker/0:1", FullCommandLine: "[kworker/0:1]"}, false},
		{"kernel thread ppid 0", process.Info{PID: 5, PPID: 0, Command: "kthreadd", FullCommandLine: "[kthreadd]"}, false},
		{"bracket with vsize", process.Info{PID: 5, PPID: 10, Command: "sh", FullCommandLine: "[sh]", VSizeKiB: 5}, false},
		{"normal", process.Info{PID: 5, PPID: 10, Command: "sh", FullCommandLine: "sh -c (x)"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsZombie(tt.info); got != tt.want {
				t.Errorf("IsZombie = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestZombieFlagOnNode(t *testing.T) {
	z := process.Info{PID: 11, PPID: 10, Command: "sh <defunct>"}
	r, err := Build([]process.Info{p(10, 1, "x", 1, 0), z}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Tree.Children[0].Flags; !reflect.DeepEqual(got, []string{FlagZombie}) {
		t.Errorf("flags = %v", got)
	}
}

func TestReparented(t *testing.T) {
	root := p(1, 0, "launchd", 1, 0)
	root.UID = 0
	user := p(10, 1, "daemonized", 1, 0)
	rootOwned := p(11, 1, "sshd", 1, 0)
	rootOwned.UID = 0
	child := p(12, 10, "kid", 1, 0)
	r, _ := Build([]process.Info{root, user, rootOwned, child}, 10)
	if !reflect.DeepEqual(r.Flags, []string{FlagReparented}) {
		t.Errorf("user flags = %v", r.Flags)
	}
	r, _ = Build([]process.Info{root, user, rootOwned, child}, 11)
	if len(r.Flags) != 0 {
		t.Errorf("root flags = %v", r.Flags)
	}
	r, _ = Build([]process.Info{root, user, rootOwned, child}, 12)
	if len(r.Flags) != 0 {
		t.Errorf("child flags = %v", r.Flags)
	}
}

func TestUnknownPID(t *testing.T) {
	_, err := Build([]process.Info{p(1, 0, "launchd", 1, 0)}, 42)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestJSONShape(t *testing.T) {
	r, _ := Build([]process.Info{p(1, 0, "launchd", 1, 0), p(10, 1, "a", 5, 1), p(11, 10, "b", 7, 2)}, 10)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pid", "ancestors", "tree"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	tree := m["tree"].(map[string]any)
	for _, k := range []string{"pid", "rss_kib", "cpu_pct", "count", "subtree_rss_kib", "subtree_cpu_pct", "children"} {
		if _, ok := tree[k]; !ok {
			t.Errorf("tree missing %q in %s", k, b)
		}
	}
	if !strings.Contains(string(b), `"ancestors":[{"pid":10`) {
		t.Errorf("ancestors should start at target: %s", b)
	}
}
