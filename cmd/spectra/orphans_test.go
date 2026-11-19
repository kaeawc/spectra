package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

var orphansNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func orphansFakeDeps() orphansDeps {
	old := orphansNow.Add(-time.Hour)
	return orphansDeps{
		Procs: func(context.Context) []process.Info {
			return []process.Info{
				{PID: 1, Command: "launchd"},
				{PID: 10, PPID: 1, UID: 501, User: "me", Command: "node", RSSKiB: 2048, StartTime: old},
				{PID: 11, PPID: 1, UID: 501, User: "me", Command: "gradle", RSSKiB: 4096, StartTime: old},
				{PID: 12, PPID: 50, UID: 501, Command: "child", RSSKiB: 1, StartTime: old},
				{PID: 13, PPID: 1, UID: 501, Command: "other", RSSKiB: 1, StartTime: old},
				{PID: 14, PPID: 1, UID: 501, Command: "young", RSSKiB: 1, StartTime: orphansNow.Add(-time.Second)},
				{PID: 15, PPID: 1, UID: 501, Command: "noperm", RSSKiB: 1, StartTime: old},
				{PID: 50, PPID: 1, UID: 501, Command: "me", StartTime: old},
				{PID: 99, PPID: 50, UID: 501, Command: "spectra", StartTime: old},
			}
		},
		Cwds: func(context.Context, []int) map[int]string {
			return map[int]string{
				10: "/private/tmp/work/wt1/sub", 11: "/private/tmp/work/gone", 12: "/private/tmp/work/wt1",
				13: "/elsewhere", 14: "/private/tmp/work/wt1", 50: "/private/tmp/work/wt1",
			}
		},
		Stat: func(p string) error {
			if p == "/private/tmp/work/wt1/.git" || p == "/private/tmp/work/wt1/sub" {
				return nil
			}
			return fs.ErrNotExist
		},
		Now:   func() time.Time { return orphansNow },
		Getwd: func() (string, error) { return "/private/tmp/work", nil },
		Pid:   func() int { return 99 },
		Abs: func(p string) (string, error) {
			if strings.HasPrefix(p, "/") {
				return p, nil
			}
			return "/home/u/" + p, nil
		},
		EvalSymlinks: func(p string) (string, error) {
			switch p {
			case "/tmp/work":
				return "/private/tmp/work", nil
			case "/home/u/rel":
				return "/private/tmp/work", nil
			}
			return p, nil
		},
	}
}

func TestRunOrphansFindsAndGroups(t *testing.T) {
	for _, root := range []string{"/tmp/work", "rel", "/private/tmp/work"} {
		var out, errb bytes.Buffer
		code := runOrphansWithIO([]string{"--root", root, "--json"}, &out, &errb, orphansFakeDeps())
		if code != 3 {
			t.Fatalf("root %s: code %d: %s", root, code, errb.String())
		}
		var rep struct {
			Roots  []string `json:"roots"`
			Total  int      `json:"total"`
			Groups []struct {
				Worktree  string `json:"worktree"`
				RSSKiB    int64  `json:"rss_kib"`
				Processes []struct {
					PID        int  `json:"pid"`
					CwdMissing bool `json:"cwd_missing"`
				} `json:"processes"`
			} `json:"groups"`
			Limitations []string `json:"limitations"`
		}
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.Total != 2 || len(rep.Groups) != 2 || len(rep.Limitations) != 3 || rep.Roots[0] != root {
			t.Fatalf("root %s: %+v", root, rep)
		}
		if g := rep.Groups[0]; g.Worktree != "/private/tmp/work/gone" || g.RSSKiB != 4096 || !g.Processes[0].CwdMissing {
			t.Fatalf("root %s: first group %+v", root, g)
		}
		if rep.Groups[1].Processes[0].PID != 10 || rep.Groups[1].Processes[0].CwdMissing {
			t.Fatalf("root %s: second group %+v", root, rep.Groups[1])
		}
	}
}

func TestRunOrphansTextDefaultRootAndPids(t *testing.T) {
	var out, errb bytes.Buffer
	code := runOrphansWithIO(nil, &out, &errb, orphansFakeDeps())
	if code != 3 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"[cwd missing]", "1 skipped", "PPID 1 is a heuristic", "evidence, not a kill list", "nohup/disown", "incl. descendants"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
}

func TestRunOrphansPidsFlagAndDaemons(t *testing.T) {
	deps := orphansFakeDeps()
	base := deps.Procs
	deps.Procs = func(ctx context.Context) []process.Info {
		ps := base(ctx)
		ps = append(ps, process.Info{PID: 16, PPID: 1, UID: 501, Command: "tmux", FullCommandLine: "tmux new -s " + strings.Repeat("x", 200), RSSKiB: 9, StartTime: orphansNow.Add(-time.Hour)})
		return ps
	}
	cwds := deps.Cwds
	deps.Cwds = func(ctx context.Context, p []int) map[int]string {
		m := cwds(ctx, p)
		m[16] = "/private/tmp/work/wt1"
		return m
	}
	var out, errb bytes.Buffer
	if code := runOrphansWithIO(nil, &out, &errb, deps); code != 3 {
		t.Fatalf("code %d", code)
	}
	s := out.String()
	if strings.Contains(s, "pids:") {
		t.Fatalf("pids line printed without --pids:\n%s", s)
	}
	idx := strings.Index(s, "Probably intentional (daemonize by design)")
	if idx < 0 || !strings.Contains(s[idx:], "tmux new") || strings.Contains(s[:idx], "tmux new") || !strings.Contains(s, "\u2026") {
		t.Fatalf("daemon section/truncation wrong:\n%s", s)
	}
	out.Reset()
	runOrphansWithIO([]string{"--pids"}, &out, &errb, deps)
	if !strings.Contains(out.String(), "pids: 11 10\n") {
		t.Fatalf("--pids output:\n%s", out.String())
	}
	out.Reset()
	runOrphansWithIO([]string{"--json"}, &out, &errb, deps)
	if !strings.Contains(out.String(), `"daemon_by_design": true`) || !strings.Contains(out.String(), strings.Repeat("x", 200)) {
		t.Fatalf("json missing daemon flag or full command_line:\n%s", out.String())
	}
	// Only daemons: exit 0.
	only := orphansFakeDeps()
	only.Procs = func(context.Context) []process.Info {
		return []process.Info{{PID: 16, PPID: 1, UID: 501, Command: "tmux", StartTime: orphansNow.Add(-time.Hour)}}
	}
	only.Cwds = func(context.Context, []int) map[int]string { return map[int]string{16: "/private/tmp/work/wt1"} }
	if code := runOrphansWithIO(nil, &bytes.Buffer{}, &errb, only); code != 0 {
		t.Fatalf("daemon-only code = %d, want 0", code)
	}
}

func TestRunOrphansNoneExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runOrphansWithIO([]string{"--root", "/nowhere"}, &out, &errb, orphansFakeDeps()); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "pids:") {
		t.Fatalf("pid line printed with no orphans:\n%s", out.String())
	}
}

func TestRunOrphansMinAgeFlag(t *testing.T) {
	var out, errb bytes.Buffer
	code := runOrphansWithIO([]string{"--min-age", "0s", "--json"}, &out, &errb, orphansFakeDeps())
	if code != 3 || !strings.Contains(out.String(), `"pid": 14`) {
		t.Fatalf("code %d, young process missing with --min-age 0:\n%s", code, out.String())
	}
}

func TestRunOrphansWriteErrorAndUsage(t *testing.T) {
	var errb bytes.Buffer
	if code := runOrphansWithIO(nil, failWriter{}, &errb, orphansFakeDeps()); code != 1 {
		t.Fatalf("write error code = %d, want 1", code)
	}
	for _, args := range [][]string{{"extra"}, {"--min-age", "-1s"}, {"--bogus"}} {
		errb.Reset()
		if code := runOrphansWithIO(args, &bytes.Buffer{}, &errb, orphansFakeDeps()); code != 2 {
			t.Fatalf("%v: code = %d, want 2", args, code)
		}
	}
}
