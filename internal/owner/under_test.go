package owner

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

type dirInfo struct{ fakeInfo }

func (dirInfo) IsDir() bool { return true }

// dirFS is fakeFS whose existing paths are directories.
type dirFS struct{ fakeFS }

func (d dirFS) Stat(n string) (os.FileInfo, error) {
	fi, err := d.fakeFS.Stat(n)
	if err != nil {
		return nil, err
	}
	return dirInfo{fi.(fakeInfo)}, nil
}

var underStart = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func underProcs(context.Context) []process.Info {
	return []process.Info{
		{PID: 1, PPID: 0, UID: 0, Command: "init"},
		{PID: 10, PPID: 1, UID: 501, Command: "zsh"},
		{PID: 20, PPID: 10, UID: 501, Command: "gradle", StartTime: underStart},
		{PID: 30, PPID: 10, UID: 501, Command: "vim"},
		{PID: 40, PPID: 1, UID: 0, Command: "cron"},
		{PID: 99, PPID: 10, UID: 501, Command: "spectra"},
		{PID: 100, PPID: 99, UID: 501, Command: "lsof"},
	}
}

func underFS() dirFS {
	return dirFS{fakeFS{
		exist: map[string]bool{"/work/repo": true, "/work/idle": true, "/link/repo": true},
		eval:  map[string]string{"/link/repo": "/work/repo"},
		links: map[string]string{
			"/proc/10/cwd":  "/home/me",
			"/proc/20/cwd":  "/work/repo/app/build",
			"/proc/30/cwd":  "/work/repository", // a sibling with a shared prefix
			"/proc/99/cwd":  "/work/repo",
			"/proc/100/cwd": "/work/repo",
		},
		deny: map[string]bool{"/proc/40/cwd": true},
	}}
}

func underFinder(k hostos.Kind, run proc.Runner) Finder {
	return Finder{
		OS: k, FS: underFS(), Runner: run, Procs: underProcs,
		IsRoot: func() bool { return false }, UID: func() int { return 501 }, SelfPID: func() int { return 99 },
	}
}

func TestUnderLinux(t *testing.T) {
	tests := []struct {
		name       string
		dirs       []string
		wantPIDs   []int
		wantCwd    string
		wantErr    string
		incomplete bool
	}{
		{name: "subdirectory cwd", dirs: []string{"/work/repo"}, wantPIDs: []int{20}, wantCwd: "/work/repo/app/build", incomplete: true},
		{name: "idle tree", dirs: []string{"/work/idle"}, incomplete: true},
		{name: "symlinked dir", dirs: []string{"/link/repo"}, wantPIDs: []int{20}, incomplete: true},
		{name: "missing dir", dirs: []string{"/nope"}, wantErr: "/nope"},
		{name: "relative dir", dirs: []string{"repo"}, wantErr: "not absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := underFinder(hostos.Linux, proc.NewFake()).Under(context.Background(), tt.dirs)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := pidsOf(res.Holders); !equalInts(got, tt.wantPIDs) {
				t.Fatalf("pids = %v, want %v", got, tt.wantPIDs)
			}
			if res.Incomplete != tt.incomplete || (tt.incomplete && res.Hint == "") {
				t.Errorf("incomplete = %v hint %q", res.Incomplete, res.Hint)
			}
			if tt.wantCwd != "" {
				h := res.Holders[0]
				if h.Cwd != tt.wantCwd || h.Hold != HoldCwd || !h.Started.Equal(underStart) || len(h.Chain) != 2 || h.Chain[0].PID != 10 {
					t.Errorf("holder = %+v", h)
				}
			}
		})
	}
}

func TestUnderResolvesProcessCwd(t *testing.T) {
	fsys := underFS()
	fsys.links["/proc/30/cwd"] = "/alias/x"
	fsys.eval["/alias/x"] = "/work/repo/x"
	f := underFinder(hostos.Linux, nil)
	f.FS = fsys
	res, err := f.Under(context.Background(), []string{"/work/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pidsOf(res.Holders); !equalInts(got, []int{20, 30}) {
		t.Errorf("pids = %v", got)
	}
}

func TestUnderDarwin(t *testing.T) {
	lsofOut := "p10\nfcwd\nn/Users/me\np20\nfcwd\nn/work/repo/app\np30\nfcwd\nn/work/repository\np99\nfcwd\nn/work/repo\np100\nfcwd\nn/work/repo\n"
	run := proc.NewFake().On(proc.MatchPrefix("lsof", "-w", "-nP", "-a", "-d", "cwd"), proc.Response{Result: proc.Result{ExitCode: 1, Stdout: []byte(lsofOut)}})
	f := underFinder(hostos.Darwin, run)
	res, err := f.Under(context.Background(), []string{"/work/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pidsOf(res.Holders); !equalInts(got, []int{20}) {
		t.Fatalf("pids = %v", got)
	}
	// init (pid 1) and cron are root-owned and unreadable without root.
	if !res.Incomplete {
		t.Error("want incomplete: other users' cwds were not listed")
	}
	f.IsRoot = func() bool { return true }
	if res, _ := f.Under(context.Background(), []string{"/work/repo"}); res.Incomplete {
		t.Error("root sees everything; want complete")
	}
}

func TestUnderDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := underFinder(hostos.Linux, nil).Under(ctx, []string{"/work/repo"}); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Errorf("err = %v, want context error", err)
	}
}

func TestPathUnder(t *testing.T) {
	for _, tt := range []struct {
		p, root string
		want    bool
	}{
		{"/a/b", "/a/b", true}, {"/a/b/c", "/a/b", true}, {"/a/bc", "/a/b", false}, {"/a", "/a/b", false}, {"/x", "/", true},
	} {
		if got := PathUnder(tt.p, tt.root); got != tt.want {
			t.Errorf("PathUnder(%q, %q) = %v", tt.p, tt.root, got)
		}
	}
}

func pidsOf(hs []Holder) []int {
	var out []int
	for _, h := range hs {
		out = append(out, h.PID)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
