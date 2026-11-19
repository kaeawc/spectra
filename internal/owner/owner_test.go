package owner

import (
	"context"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in      string
		want    Target
		wantErr bool
	}{
		{in: ":8080", want: Target{Kind: KindPort, Raw: ":8080", Port: 8080}},
		{in: "tcp:8080", want: Target{Kind: KindPort, Raw: "tcp:8080", Proto: "tcp", Port: 8080}},
		{in: "UDP:53", want: Target{Kind: KindPort, Raw: "UDP:53", Proto: "udp", Port: 53}},
		{in: "/var/log/x.log", want: Target{Kind: KindPath, Raw: "/var/log/x.log", Path: "/var/log/x.log"}},
		{in: "./a/../b.txt", want: Target{Kind: KindPath, Raw: "./a/../b.txt", Path: "/work/b.txt"}},
		{in: "1234", want: Target{Kind: KindPID, Raw: "1234", PID: 1234}},
		{in: "garbage", wantErr: true},
		{in: "", wantErr: true},
		{in: "0", wantErr: true},
		{in: ":0", wantErr: true},
		{in: ":65536", wantErr: true},
		{in: "tcp:abc", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseTarget(tt.in, "/work")
		if (err != nil) != tt.wantErr {
			t.Errorf("%q: err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("%q: got %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseLsofPorts(t *testing.T) {
	out := "p100\ncnode\nLjason\nf20\ntIPv4\nPTCP\nn*:8080\nTST=LISTEN\nTQR=0\n" +
		"p200\ncchrome\nLjason\nf30\ntIPv4\nPTCP\nn127.0.0.1:5555->127.0.0.1:8080\nTST=ESTABLISHED\n" +
		"f31\ntIPv4\nPTCP\nn127.0.0.1:8080->127.0.0.1:5556\nTST=ESTABLISHED\n"
	got := lsofPortHolders(parseLsofF([]byte(out)), 8080)
	want := []Holder{
		{PID: 100, User: "jason", Command: "node", Hold: HoldListen, Detail: "tcp *:8080"},
		{PID: 200, User: "jason", Command: "chrome", Hold: HoldConnected, Detail: "tcp 127.0.0.1:8080->127.0.0.1:5556"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseLsofUDP(t *testing.T) {
	out := "p9\ncmdns\nLroot\nf5\ntIPv6\nPUDP\nn[::1]:53\n"
	got := lsofPortHolders(parseLsofF([]byte(out)), 53)
	if len(got) != 1 || got[0].Hold != HoldListen {
		t.Errorf("got %+v", got)
	}
}

func TestParseLsofPath(t *testing.T) {
	// Mixes target records with other files on the same volume, as lsof
	// does for a mount point.
	out := "p7\ncvim\nLjason\nfcwd\na \ntDIR\nn/tmp\nftxt\na \ntREG\nn/tmp\nf3w\na\ntREG\nn/tmp/x\n" +
		"f4u\nau\ntREG\nn/tmp/x\nfrtd\ntDIR\nn/\nf5r\nar\ntREG\nn/tmp/other\n" +
		"p8\ncsh\nLjason\nfcwd\ntDIR\nn/tmp/elsewhere\nf9r\nar\ntREG\nn/private/tmp/x\n"
	got := lsofPathHolders(parseLsofF([]byte(out)), []string{"/tmp", "/tmp/x"})
	want := []Holder{
		{PID: 7, User: "jason", Command: "vim", Hold: HoldCwd},
		{PID: 7, User: "jason", Command: "vim", Hold: HoldTxt, Detail: "txt"},
		{PID: 7, User: "jason", Command: "vim", Hold: HoldFD, Mode: "", Detail: "fd 3"},
		{PID: 7, User: "jason", Command: "vim", Hold: HoldFD, Mode: "rw", Detail: "fd 4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseSS(t *testing.T) {
	out := `tcp LISTEN 0 511 0.0.0.0:8080 0.0.0.0:* users:(("nginx",pid=10,fd=6),("nginx",pid=11,fd=6))
tcp LISTEN 0 511 [::]:8080 [::]:* users:(("nginx",pid=10,fd=7))
tcp ESTAB 0 0 10.0.0.2:8080 10.0.0.9:4444 users:(("nginx",pid=11,fd=9))
tcp LISTEN 0 4096 127.0.0.1:9090 0.0.0.0:*
tcp LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*
udp UNCONN 0 0 *:53 *:*
`
	got, hidden := parseSS([]byte(out), "tcp", 8080)
	if !hidden {
		t.Error("expected hidden=true for the line without users")
	}
	if len(got) != 4 {
		t.Fatalf("got %d holders: %+v", len(got), got)
	}
	if got[0].PID != 10 || got[1].PID != 11 || got[0].Command != "nginx" || got[0].Hold != HoldListen {
		t.Errorf("multi-user parse wrong: %+v", got[:2])
	}
	if got[2].Detail != "tcp [::]:8080 fd 7" {
		t.Errorf("ipv6 detail = %q", got[2].Detail)
	}
	if got[3].Hold != HoldConnected || got[3].Detail != "tcp 10.0.0.2:8080->10.0.0.9:4444 fd 9" {
		t.Errorf("connected = %+v", got[3])
	}
	if udp, _ := parseSS([]byte(out), "udp", 53); len(udp) != 0 {
		t.Errorf("udp without users should yield no holders: %+v", udp)
	}
}

type dent struct{ name string }

func (d dent) Name() string               { return d.name }
func (d dent) IsDir() bool                { return true }
func (d dent) Type() fs.FileMode          { return fs.ModeDir }
func (d dent) Info() (fs.FileInfo, error) { return nil, os.ErrNotExist }

type fakeInfo struct {
	os.FileInfo
	id string
}

func (f fakeFS) Stat(n string) (os.FileInfo, error) {
	if id, ok := f.ids[n]; ok {
		return fakeInfo{id: id}, nil
	}
	if f.exist[n] {
		return fakeInfo{id: n}, nil
	}
	return nil, fs.ErrNotExist
}

func (f fakeFS) SameFile(a, b os.FileInfo) bool {
	x, y := a.(fakeInfo), b.(fakeInfo)
	return x.id != "" && x.id == y.id
}

func (f fakeFS) EvalSymlinks(n string) (string, error) {
	if r, ok := f.eval[n]; ok {
		return r, nil
	}
	return n, nil
}

type fakeFS struct {
	exist map[string]bool
	ids   map[string]string // inode identity for Stat, overrides exist
	eval  map[string]string
	dirs  map[string][]string
	links map[string]string
	files map[string]string
	deny  map[string]bool
}

func (f fakeFS) ReadDir(n string) ([]os.DirEntry, error) {
	if f.deny[n] {
		return nil, fs.ErrPermission
	}
	names, ok := f.dirs[n]
	if !ok {
		return nil, fs.ErrNotExist
	}
	var out []os.DirEntry
	for _, s := range names {
		out = append(out, dent{s})
	}
	return out, nil
}

func (f fakeFS) Readlink(n string) (string, error) {
	if f.deny[n] {
		return "", fs.ErrPermission
	}
	if l, ok := f.links[n]; ok {
		return l, nil
	}
	return "", fs.ErrNotExist
}

func (f fakeFS) ReadFile(n string) ([]byte, error) {
	if s, ok := f.files[n]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func procTree() fakeFS {
	return fakeFS{
		exist: map[string]bool{"/nothing": true, "/srv/data/db.sqlite": true, "/var/run/app.sock": true},
		eval:  map[string]string{"/var/run/app.sock": "/run/app.sock"},
		// The path was recreated: fd 3 (deleted) is an older inode, and
		// pid 40 holds the live file through another hard link.
		ids: map[string]string{
			"/srv/data/db.sqlite": "ino-live", "/proc/20/fd/5": "ino-live",
			"/proc/30/fd/3": "ino-old", "/srv/data/hardlink.sqlite": "ino-live", "/proc/40/fd/7": "ino-live",
		},
		dirs: map[string][]string{
			"/proc":       {"1", "20", "30", "40", "self", "cpuinfo"},
			"/proc/20/fd": {"0", "5"},
			"/proc/30/fd": {"3", "4"},
			"/proc/40/fd": {"7"},
		},
		links: map[string]string{
			"/proc/30/fd/4": "socket:[777]",
			"/proc/40/fd/7": "/srv/data/hardlink.sqlite",
			"/proc/20/cwd":  "/srv/data",
			"/proc/20/exe":  "/usr/bin/app",
			"/proc/20/fd/5": "/srv/data/db.sqlite",
			"/proc/30/fd/3": "/srv/data/db.sqlite (deleted)",
			"/proc/30/exe":  "/srv/data/db.sqlite",
		},
		files: map[string]string{
			"/proc/20/fdinfo/5": "pos:\t0\nflags:\t0100002\nmnt_id:\t1\n",
			"/proc/30/fdinfo/3": "flags:\t02000000\n",
			"/proc/40/fdinfo/7": "flags:\t0100001\n",
			"/proc/net/unix": "Num       RefCount Protocol Flags    Type St Inode Path\n" +
				"0000: 00000002 00000000 00010000 0001 01 777 /run/app.sock\n" +
				"0001: 00000003 00000000 00000000 0001 03 778 /run/other.sock\n" +
				"0003: 00000002 00000000 00010000 0001 01 780 /tmp/my app/service.sock\n" +
				"0002: 00000002 00000000 00010000 0001 01 779\n",
		},
		deny: map[string]bool{"/proc/1/fd": true},
	}
}

func TestProcPathHolders(t *testing.T) {
	got, denied := procPathHolders(procTree(), "/proc", []string{"/srv/data/db.sqlite"})
	if !denied {
		t.Error("pid 1 fd dir is permission denied; want denied")
	}
	want := []Holder{
		{PID: 20, Hold: HoldFD, Detail: "fd 5", Mode: "rw"},
		{PID: 30, Hold: HoldTxt, Detail: "exe"},
		{PID: 40, Hold: HoldFD, Detail: "fd 7", Mode: "w"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	got, _ = procPathHolders(procTree(), "/proc", []string{"/srv/data"})
	if len(got) != 1 || got[0].Hold != HoldCwd || got[0].PID != 20 {
		t.Errorf("cwd match = %+v", got)
	}
}

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func fakeProcs(context.Context) []process.Info {
	return []process.Info{
		{PID: 1, PPID: 0, Command: "systemd", User: "root"},
		{PID: 10, PPID: 1, Command: "sshd", User: "root"},
		{PID: 20, PPID: 10, Command: "app", User: "jason", ExecutablePath: "/usr/bin/app", StartTime: t0},
		{PID: 30, PPID: 20, Command: "worker", User: "jason"},
	}
}

func TestParentChainAndEnrich(t *testing.T) {
	f := Finder{OS: hostos.Linux, FS: procTree(), IsRoot: func() bool { return true }, Procs: fakeProcs}
	res, err := f.Find(context.Background(), Target{Kind: KindPID, Raw: "30", PID: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Holders) != 1 {
		t.Fatalf("holders = %+v", res.Holders)
	}
	h := res.Holders[0]
	wantChain := []Link{{20, "app"}, {10, "sshd"}, {1, "systemd"}}
	if h.User != "jason" || h.Command != "worker" || !reflect.DeepEqual(h.Chain, wantChain) {
		t.Errorf("holder = %+v", h)
	}
	if h.Executable != "/srv/data/db.sqlite" {
		t.Errorf("exe should come from /proc/30/exe: %q", h.Executable)
	}
	res, _ = f.Find(context.Background(), Target{Kind: KindPID, Raw: "20", PID: 20})
	if res.Holders[0].Executable != "/usr/bin/app" || !res.Holders[0].Started.Equal(t0) {
		t.Errorf("pid holder = %+v", res.Holders[0])
	}
	if res, _ = f.Find(context.Background(), Target{Kind: KindPID, Raw: "999", PID: 999}); len(res.Holders) != 0 {
		t.Errorf("missing pid should have no holders: %+v", res)
	}
}

func TestParentChainCycle(t *testing.T) {
	byPID := map[int]process.Info{1: {PID: 1, PPID: 2, Command: "a"}, 2: {PID: 2, PPID: 1, Command: "b"}}
	if got := parentChain(byPID, 1); len(got) != 2 {
		t.Errorf("cycle chain = %+v", got)
	}
}

func TestFindLinuxPortNeedsRoot(t *testing.T) {
	ss := `tcp LISTEN 0 511 0.0.0.0:8080 0.0.0.0:* users:(("app",pid=20,fd=6))
tcp LISTEN 0 511 [::]:8080 [::]:*
`
	run := proc.NewFake().
		On(proc.MatchExact("ss", "-H", "-lntup"), proc.Response{Result: proc.Result{Stdout: []byte(ss)}}).
		On(proc.MatchExact("ss", "-H", "-ntup"), proc.Response{})
	tgt := Target{Kind: KindPort, Raw: ":8080", Port: 8080}
	f := Finder{OS: hostos.Linux, Runner: run, Procs: fakeProcs, IsRoot: func() bool { return false }}
	res, err := f.Find(context.Background(), tgt)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Incomplete || res.Hint == "" || len(res.Holders) != 1 || res.Holders[0].User != "jason" {
		t.Errorf("non-root result = %+v", res)
	}
	f.IsRoot = func() bool { return true }
	if res, _ = f.Find(context.Background(), tgt); res.Incomplete || res.Hint != "" {
		t.Errorf("root result should be complete: %+v", res)
	}
}

func TestFindDarwinPortAndPath(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	run := proc.NewFake().
		On(proc.MatchPrefix("lsof", "-nP", "-iTCP:8080"), proc.Response{Result: proc.Result{Stdout: []byte("p20\ncapp\nLjason\nf4\ntIPv4\nPTCP\nn*:8080\nTST=LISTEN\n")}}).
		On(proc.MatchPrefix("lsof", "-nP", "-iUDP:8080"), proc.Response{Result: proc.Result{ExitCode: 1}}).
		On(proc.MatchPrefix("lsof", "-nP", "-F", lsofPathFields, "--", "/srv/x"), proc.Response{Result: proc.Result{Stdout: []byte("p30\ncworker\nLjason\nf9\nar\ntREG\nn/srv/x\n")}})
	f := Finder{Runner: run, FS: fakeFS{exist: map[string]bool{"/srv/x": true}}, Procs: fakeProcs, IsRoot: func() bool { return false }}
	res, err := f.Find(context.Background(), Target{Kind: KindPort, Raw: ":8080", Port: 8080})
	if err != nil || len(res.Holders) != 1 || res.Holders[0].Hold != HoldListen || !res.Incomplete {
		t.Fatalf("port: %+v, %v", res, err)
	}
	if len(res.Holders[0].Chain) != 2 {
		t.Errorf("chain = %+v", res.Holders[0].Chain)
	}
	res, err = f.Find(context.Background(), Target{Kind: KindPath, Raw: "/srv/x", Path: "/srv/x"})
	if err != nil || len(res.Holders) != 1 || res.Holders[0].Mode != "r" || res.Holders[0].Hold != HoldFD {
		t.Fatalf("path: %+v, %v", res, err)
	}
}

func TestFindLsofError(t *testing.T) {
	run := proc.NewFake().On(proc.MatchPrefix("lsof"), proc.Response{Result: proc.Result{ExitCode: 1, Stderr: []byte("lsof: boom")}})
	f := Finder{OS: hostos.Darwin, Runner: run, FS: fakeFS{exist: map[string]bool{"/x": true}}, Procs: fakeProcs}
	_, err := f.Find(context.Background(), Target{Kind: KindPath, Path: "/x"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestFindMissingPath(t *testing.T) {
	for _, k := range []hostos.Kind{hostos.Darwin, hostos.Linux} {
		// No runner rules: lsof must never be invoked for a missing path.
		f := Finder{OS: k, Runner: proc.NewFake(), FS: fakeFS{}, Procs: fakeProcs}
		_, err := f.Find(context.Background(), Target{Kind: KindPath, Path: "/nonexistent"})
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Errorf("%v: err = %v", k, err)
		}
	}
}

func TestFindLinuxSymlinkedPath(t *testing.T) {
	f := Finder{OS: hostos.Linux, FS: procTree(), Procs: fakeProcs, IsRoot: func() bool { return true }}
	res, err := f.Find(context.Background(), Target{Kind: KindPath, Raw: "/var/run/app.sock", Path: "/var/run/app.sock"})
	if err != nil || len(res.Holders) != 1 || res.Holders[0].PID != 30 || res.Holders[0].Detail != "fd 4" {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestFindDarwinMountPointFiltered(t *testing.T) {
	run := proc.NewFake().On(proc.MatchPrefix("lsof"), proc.Response{Result: proc.Result{
		Stdout: []byte("p20\ncapp\nLjason\nfcwd\ntDIR\nn/private/tmp\nf5r\nar\ntREG\nn/private/tmp/other\np30\ncw\nLjason\nf6r\nar\ntREG\nn/etc/hosts\n")}})
	fsys := fakeFS{exist: map[string]bool{"/tmp": true}, eval: map[string]string{"/tmp": "/private/tmp"}}
	f := Finder{OS: hostos.Darwin, Runner: run, FS: fsys, Procs: fakeProcs}
	res, err := f.Find(context.Background(), Target{Kind: KindPath, Path: "/tmp"})
	if err != nil || len(res.Holders) != 1 || res.Holders[0].PID != 20 || res.Holders[0].Hold != HoldCwd {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestFindLinuxPathDenied(t *testing.T) {
	f := Finder{OS: hostos.Linux, FS: procTree(), Procs: fakeProcs, IsRoot: func() bool { return false }}
	res, err := f.Find(context.Background(), Target{Kind: KindPath, Raw: "/nothing", Path: "/nothing"})
	if err != nil || len(res.Holders) != 0 || !res.Incomplete {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}
