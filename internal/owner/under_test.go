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

func (d dirFS) SameFile(a, b os.FileInfo) bool {
	x, y := a.(dirInfo), b.(dirInfo)
	return x.id != "" && x.id == y.id
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
	f := underFinder(hostos.Darwin, lsofCwdRunner(lsofOut, 0))
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

func lsofCwdRunner(out string, code int) proc.Runner {
	return proc.NewFake().On(proc.MatchPrefix("lsof", "-w", "-nP", "-a", "-d", "cwd"),
		proc.Response{Result: proc.Result{ExitCode: code, Stdout: []byte(out), Stderr: []byte("lsof: status error")}})
}

// lsof -F escapes the name field; these are the spellings it really prints
// for "grün", a tab, a newline and a backslash.
func TestUnderDarwinDecodesLsofEscapes(t *testing.T) {
	for _, tt := range []struct{ name, dir, lsofName string }{
		{"utf8", "/work/grün", `/work/gr\xc3\xbcn/sub`},
		{"tab and backslash", "/work/t\tb\\s", `/work/t\tb\\s`},
		{"newline", "/work/a\nb", `/work/a\nb/x`},
		{"control", "/work/c\x01d", `/work/c^Ad`},
		{"literal caret", "/work/x^Ay", `/work/x^Ay`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := underFinder(hostos.Darwin, lsofCwdRunner("p20\nR10\ncgradle\nfcwd\nn"+tt.lsofName+"\n", 0))
			f.FS = dirFS{fakeFS{exist: map[string]bool{tt.dir: true}}}
			res, err := f.Under(context.Background(), []string{tt.dir})
			if err != nil {
				t.Fatal(err)
			}
			if got := pidsOf(res.Holders); !equalInts(got, []int{20}) {
				t.Fatalf("pids = %v (lsof name %q, dir %q)", got, tt.lsofName, tt.dir)
			}
		})
	}
}

func TestLsofNameSpellings(t *testing.T) {
	for in, want := range map[string][]string{
		`/plain`:            {"/plain"},
		`/gr\xc3\xbcn`:      {"/grün"},
		`/a\\b\tc\nd\re\bf`: {"/a\\b\tc\nd\re\bf"},
		`/bad\xZZ\q`:        {`/bad\xZZ\q`},
		`/c^A^?`:            {"/c\x01\x7f", `/c^A^?`},
		`/caret^x`:          {`/caret^x`},
	} {
		if got := lsofNameSpellings(in); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("lsofNameSpellings(%q) = %q, want %q", in, got, want)
		}
	}
}

// On a case-insensitive volume lsof reports the spelling on disk, which can
// differ in case from the directory the caller typed.
func TestUnderDarwinCaseInsensitiveIdentity(t *testing.T) {
	fsys := dirFS{fakeFS{ids: map[string]string{
		"/work/WT": "ino-wt", "/work/wt": "ino-wt", "/work/wt/app": "ino-app", "/work": "ino-work", "/": "ino-root",
		"/work/other": "ino-other",
	}}}
	out := "p20\nR10\nfcwd\nn/work/wt/app\np30\nR10\nfcwd\nn/work/other\n"
	f := underFinder(hostos.Darwin, lsofCwdRunner(out, 0))
	f.FS = fsys
	res, err := f.Under(context.Background(), []string{"/work/WT"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pidsOf(res.Holders); !equalInts(got, []int{20}) {
		t.Fatalf("pids = %v, want [20]", got)
	}
	// Linux keeps exact path matching.
	m := Finder{OS: hostos.Linux, FS: fsys}.NewCwdMatcher()
	if m.Under("/work/wt/app", []string{"/work/WT"}) {
		t.Error("linux matched by identity")
	}
}

func TestUnderDarwinLsofFailureIsError(t *testing.T) {
	f := underFinder(hostos.Darwin, lsofCwdRunner("p20\nfcwd\nn/work/repo/app\n", 1))
	if _, err := f.Under(context.Background(), []string{"/work/repo"}); err == nil || !strings.Contains(err.Error(), "exited 1") {
		t.Fatalf("err = %v, want lsof failure", err)
	}
}

func TestUnderEmptyProcessListIsError(t *testing.T) {
	for _, k := range []hostos.Kind{hostos.Linux, hostos.Darwin} {
		f := underFinder(k, lsofCwdRunner("", 0))
		f.Procs = func(context.Context) []process.Info { return nil }
		if _, err := f.Under(context.Background(), []string{"/work/repo"}); err == nil || !strings.Contains(err.Error(), "process list is empty") {
			t.Errorf("%v: err = %v, want empty-process-list error", k, err)
		}
	}
}

// A pid the cwd source reports but the process table missed is still a
// holder; one whose parent is Spectra is still excluded.
func TestUnderHoldersFromCwdSource(t *testing.T) {
	out := "p555\nR10\ncninja\nfcwd\nn/work/repo/out\np556\nR99\nclsof\nfcwd\nn/work/repo\n"
	res, err := underFinder(hostos.Darwin, lsofCwdRunner(out, 0)).Under(context.Background(), []string{"/work/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Holders) != 1 || res.Holders[0].PID != 555 || res.Holders[0].Command != "ninja" ||
		len(res.Holders[0].Chain) != 2 || res.Holders[0].Chain[0].PID != 10 {
		t.Fatalf("holders = %+v", res.Holders)
	}

	fsys := underFS()
	fsys.dirs = map[string][]string{"/proc": {"1", "10", "20", "77", "self"}}
	fsys.links["/proc/77/cwd"] = "/work/repo/late"
	fsys.files = map[string]string{"/proc/77/stat": "77 (make (x)) S 10 77 77 0"}
	f := underFinder(hostos.Linux, nil)
	f.FS = fsys
	res, err = f.Under(context.Background(), []string{"/work/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pidsOf(res.Holders); !equalInts(got, []int{20, 77}) || res.Holders[1].Command != "make (x)" {
		t.Fatalf("holders = %+v", res.Holders)
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
