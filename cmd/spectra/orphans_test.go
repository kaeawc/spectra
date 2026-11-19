package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
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
		FS:    hostload.FS{Exists: func(p string) bool { return p == "/private/tmp/work/wt1/.git" || p == "/private/tmp/work/wt1/sub" }},
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
		if rep.Total != 2 || len(rep.Groups) != 2 || len(rep.Limitations) != 2 || rep.Roots[0] != root {
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
	for _, want := range []string{"[cwd missing]", "1 skipped", "PPID 1 is a heuristic", "pids: 11 10\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
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
