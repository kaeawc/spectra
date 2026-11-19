package orphans

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
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
	o.Stat = statOf("/r/a", "/r/b/.git")
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

func statOf(existing ...string) func(string) error {
	return func(p string) error {
		for _, e := range existing {
			if e == p {
				return nil
			}
		}
		return fs.ErrNotExist
	}
}

func TestDaemonByDesignExcluded(t *testing.T) {
	mk := func(pid int, cmd, line string) process.Info {
		p := proc(pid, 1, 100, time.Hour)
		p.Command, p.FullCommandLine = cmd, line
		return p
	}
	procs := []process.Info{
		mk(10, "tmux", "tmux new"), mk(11, "qemu-system-aarch64-headless", "qemu x"),
		mk(12, "java", "java -cp x org.gradle.launcher.daemon.bootstrap.GradleDaemon"),
		mk(13, "java", "java -jar app.jar"), mk(14, "bun", "bun proxy-session.ts serve"),
		mk(15, "limactl", "limactl hostagent"), mk(16, "caffeinate", "caffeinate -i"),
	}
	cwds := map[int]string{}
	for _, p := range procs {
		cwds[p.PID] = "/r/wt"
	}
	res := Find(procs, cwds, opts("/r"))
	if res.Total != 2 || res.DaemonTotal != 5 {
		t.Fatalf("total=%d daemons=%d: %+v", res.Total, res.DaemonTotal, res)
	}
	if got := res.PIDs(); len(got) != 2 || got[0] == 10 {
		t.Fatalf("pids = %v, want only 13 and 14", got)
	}
	g := res.Groups[0]
	if g.RSSKiB != 200 {
		t.Fatalf("group rss = %d, want 200 (non-daemon only)", g.RSSKiB)
	}
	for _, p := range g.Processes {
		if want := p.PID != 13 && p.PID != 14; p.DaemonByDesign != want {
			t.Errorf("pid %d daemon=%v", p.PID, p.DaemonByDesign)
		}
	}
}

func TestCommandLineFallsBackToCommand(t *testing.T) {
	a := proc(10, 1, 1, time.Hour)
	b := proc(11, 1, 1, time.Hour)
	b.FullCommandLine = "cmd --flag"
	res := Find([]process.Info{a, b}, map[int]string{10: "/r", 11: "/r"}, opts("/r"))
	got := map[int]string{}
	for _, p := range res.Groups[0].Processes {
		got[p.PID] = p.CommandLine
	}
	if got[10] != "cmd" || got[11] != "cmd --flag" {
		t.Fatalf("command lines = %v", got)
	}
}

func TestRemovedWorktreeGrouping(t *testing.T) {
	procs := []process.Info{proc(10, 1, 100, time.Hour), proc(11, 1, 100, time.Hour), proc(12, 1, 100, time.Hour)}
	cwds := map[int]string{
		10: "/r/repo/.claude/worktrees/gone/src/x", // worktree dir lost its .git
		11: "/r/repo/.claude/worktrees/live/src",   // live linked worktree
		12: "/r/repo/src",                          // main repo
	}
	o := opts("/r")
	o.Stat = statOf("/r/repo/.git", "/r/repo/.claude/worktrees/live/.git", "/r/repo/.claude/worktrees/gone/src")
	res := Find(procs, cwds, o)
	got := map[string]bool{}
	for _, g := range res.Groups {
		got[g.Worktree] = g.WorktreeRemoved
	}
	want := map[string]bool{
		"/r/repo/.claude/worktrees/gone": true, "/r/repo/.claude/worktrees/live": false, "/r/repo": false,
	}
	if len(got) != 3 {
		t.Fatalf("groups = %v", got)
	}
	for k, v := range want {
		if rv, ok := got[k]; !ok || rv != v {
			t.Fatalf("groups = %v, want %v", got, want)
		}
	}
}

func TestCwdMissingVersusUnreadable(t *testing.T) {
	procs := []process.Info{proc(10, 1, 1, time.Hour), proc(11, 1, 1, time.Hour), proc(12, 1, 1, time.Hour), proc(13, 1, 1, time.Hour)}
	cwds := map[int]string{10: "/r/gone", 11: "/r/denied", 12: "/r/ok", 13: "/r/dead (deleted)"}
	o := opts("/r")
	o.Stat = func(p string) error {
		switch p {
		case "/r/gone":
			return &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
		case "/r/denied":
			return &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrPermission}
		case "/r/ok":
			return nil
		}
		return errors.New("unexpected " + p)
	}
	res := Find(procs, cwds, o)
	type flags struct{ missing, unreadable bool }
	got := map[int]flags{}
	for _, g := range res.Groups {
		for _, p := range g.Processes {
			got[p.PID] = flags{p.CwdMissing, p.CwdUnreadable}
		}
	}
	want := map[int]flags{10: {true, false}, 11: {false, true}, 12: {false, false}, 13: {true, false}}
	for pid, w := range want {
		if got[pid] != w {
			t.Errorf("pid %d: %+v, want %+v (all %v)", pid, got[pid], w, got)
		}
	}
}

func TestRemovedWorktreeOutsideRepo(t *testing.T) {
	procs := []process.Info{proc(10, 1, 100, time.Hour), proc(11, 1, 100, time.Hour), proc(12, 1, 100, time.Hour)}
	cwds := map[int]string{
		10: "/r/worktrees/feature/a",
		11: "/r/worktrees/feature/b/c",
		12: "/r/other/x", // parent is not named worktrees
	}
	o := opts("/r")
	o.Stat = statOf("/r/worktrees/feature/a", "/r/worktrees/feature/b/c", "/r/other/x")
	res := Find(procs, cwds, o)
	got := map[string]bool{}
	for _, g := range res.Groups {
		got[g.Worktree] = g.WorktreeRemoved
	}
	want := map[string]bool{"/r/worktrees/feature": true, "/r/other/x": false}
	if len(got) != len(want) {
		t.Fatalf("groups = %v", got)
	}
	for k, v := range want {
		if rv, ok := got[k]; !ok || rv != v {
			t.Fatalf("groups = %v, want %v", got, want)
		}
	}
}

// Goes through the real Linux cwd reader: the deleted marker must survive it
// even when a directory was recreated at the same path.
func TestLinuxDeletedCwdThroughHost(t *testing.T) {
	h := hostload.Host{OS: hostos.Linux, Readlink: func(p string) (string, error) {
		if p == "/proc/10/cwd" {
			return "/r/job (deleted)", nil
		}
		return "", errors.New("denied")
	}}
	o := opts("/r")
	o.Stat = func(string) error { return nil } // path exists again
	res := Find([]process.Info{proc(10, 1, 100, time.Hour)}, h.CwdsRaw(context.Background(), []int{10}), o)
	if len(res.Groups) != 1 || len(res.Groups[0].Processes) != 1 {
		t.Fatalf("res = %+v", res)
	}
	p := res.Groups[0].Processes[0]
	if !p.CwdMissing || p.Cwd != "/r/job" {
		t.Fatalf("process = %+v", p)
	}
}

func TestGroupRSSIsSubtree(t *testing.T) {
	procs := []process.Info{proc(10, 1, 100, time.Hour), proc(11, 10, 400, time.Hour), proc(12, 1, 50, time.Hour)}
	res := Find(procs, map[int]string{10: "/r/a", 12: "/r/a"}, opts("/r"))
	if g := res.Groups[0]; g.RSSKiB != 550 {
		t.Fatalf("group rss = %d, want 550", g.RSSKiB)
	}
}
