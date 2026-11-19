package orphans

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/process"
)

var now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func proc(pid, ppid int, rss int64, age time.Duration) process.Info {
	return process.Info{PID: pid, PPID: ppid, UID: 501, User: "me", Command: "cmd", RSSKiB: rss, StartTime: now.Add(-age)}
}

func opts(roots ...string) Options {
	var rs []hostload.Root
	for _, r := range roots {
		rs = append(rs, hostload.Root{Path: r, Resolved: r})
	}
	return Options{Roots: rs, MinAge: time.Minute, Now: now, SelfPID: 999}
}

func TestFindBasics(t *testing.T) {
	procs := []process.Info{
		{PID: 1, PPID: 0, Command: "launchd"},
		proc(10, 1, 100, time.Hour),  // orphan under root
		proc(11, 10, 50, time.Hour),  // its child
		proc(20, 30, 100, time.Hour), // child of live non-init parent
		proc(30, 5, 10, time.Hour),
		proc(40, 1, 100, time.Hour), // outside roots
		proc(50, 1, 100, time.Hour), // unreadable cwd
		proc(60, 1, 100, time.Second),
	}
	cwds := map[int]string{10: "/r/wt1/sub", 11: "/r/wt1", 20: "/r/wt1", 40: "/elsewhere", 60: "/r/wt1"}
	res := Find(procs, cwds, opts("/r"))
	if res.Total != 1 || res.Skipped != 1 {
		t.Fatalf("total=%d skipped=%d, want 1/1: %+v", res.Total, res.Skipped, res)
	}
	p := res.Groups[0].Processes[0]
	if p.PID != 10 || p.Descendants != 1 || p.SubtreeRSSKiB != 150 || p.AgeSeconds != 3600 || p.CwdMissing {
		t.Fatalf("unexpected row %+v", p)
	}
}

func TestFindSelfAndAncestorsExcluded(t *testing.T) {
	procs := []process.Info{
		proc(10, 1, 1, time.Hour), proc(11, 10, 1, time.Hour), proc(999, 11, 1, time.Hour),
	}
	cwds := map[int]string{10: "/r", 11: "/r", 999: "/r"}
	if res := Find(procs, cwds, opts("/r")); res.Total != 0 {
		t.Fatalf("self/ancestors listed: %+v", res)
	}
}

func TestFindCwdMissingAndGrouping(t *testing.T) {
	procs := []process.Info{
		proc(10, 1, 100, time.Hour), proc(11, 1, 300, time.Hour), proc(12, 1, 200, time.Hour), proc(13, 1, 50, time.Hour),
	}
	cwds := map[int]string{10: "/r/a", 11: "/r/b/src", 12: "/r/b/src", 13: "/r/gone"}
	o := opts("/r")
	o.FS = hostload.FS{Exists: func(p string) bool { return p == "/r/a" || p == "/r/b/.git" }}
	res := Find(procs, cwds, o)
	if len(res.Groups) != 3 {
		t.Fatalf("groups = %+v", res.Groups)
	}
	if res.Groups[0].Worktree != "/r/b" || res.Groups[0].RSSKiB != 500 || res.Groups[0].Processes[0].PID != 11 {
		t.Fatalf("order/sort wrong: %+v", res.Groups)
	}
	var missing []int
	for _, g := range res.Groups {
		for _, p := range g.Processes {
			if p.CwdMissing {
				missing = append(missing, p.PID)
			}
		}
	}
	if len(missing) != 3 {
		t.Fatalf("missing = %v (b/src x2 and gone are flagged; a exists)", missing)
	}
}

func TestMinAgeUnknownAgeKept(t *testing.T) {
	p := process.Info{PID: 10, PPID: 1, UID: 501, Command: "x"}
	if res := Find([]process.Info{p}, map[int]string{10: "/r"}, opts("/r")); res.Total != 1 {
		t.Fatalf("unknown age filtered: %+v", res)
	}
	p.Elapsed = 10 * time.Second
	if res := Find([]process.Info{p}, map[int]string{10: "/r"}, opts("/r")); res.Total != 0 {
		t.Fatalf("young by elapsed listed: %+v", res)
	}
}

func TestRootUserNotReparented(t *testing.T) {
	p := proc(10, 1, 1, time.Hour)
	p.UID = 0
	if res := Find([]process.Info{p}, map[int]string{10: "/r"}, opts("/r")); res.Total != 0 || res.Skipped != 0 {
		t.Fatalf("root daemon listed: %+v", res)
	}
}
