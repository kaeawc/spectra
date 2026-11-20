package idle

import (
	"context"
	"io/fs"
	"os"
	"reflect"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/proc"
)

type idInfo struct {
	os.FileInfo
	id string
}

func (idInfo) IsDir() bool { return true }

// idFS is an owner.FS whose Stat identity comes from ids, so two spellings
// of one directory (different case on a case-insensitive volume) share one.
type idFS struct{ ids map[string]string }

func (f idFS) Stat(n string) (os.FileInfo, error) {
	if id, ok := f.ids[n]; ok {
		return idInfo{id: id}, nil
	}
	return nil, fs.ErrNotExist
}
func (idFS) SameFile(a, b os.FileInfo) bool        { return a.(idInfo).id == b.(idInfo).id }
func (idFS) EvalSymlinks(n string) (string, error) { return n, nil }
func (idFS) Readlink(string) (string, error)       { return "", fs.ErrNotExist }
func (idFS) ReadDir(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }
func (idFS) ReadFile(string) ([]byte, error)       { return nil, fs.ErrNotExist }

// idle reads cwds through owner.Cwds and matches through owner's matcher, so
// lsof's escaped non-ASCII names and a case-only difference on macOS both
// still put the process in scope.
func TestFindUsesOwnerCwdDecodingAndIdentity(t *testing.T) {
	lsofOut := "p20\nR1\nfcwd\nn/work/gr\\xc3\\xbcn/wt\np30\nR1\nfcwd\nn/work/Lane\n"
	fsys := idFS{ids: map[string]string{
		"/work/grün/WT": "ino-wt", "/work/grün/wt": "ino-wt",
		"/work/lane": "ino-lane", "/work/Lane": "ino-lane",
	}}
	run := proc.NewFake().On(proc.MatchPrefix("lsof", "-w", "-nP", "-a", "-d", "cwd"), proc.Response{Result: proc.Result{Stdout: []byte(lsofOut)}})
	f := owner.Finder{OS: hostos.Darwin, Runner: run, FS: fsys, IsRoot: func() bool { return true }}
	s := sample(idleCPU())
	cwds, _, err := f.Cwds(context.Background(), s.Procs)
	if err != nil {
		t.Fatal(err)
	}
	o := opts()
	o.Roots = []Root{{Path: "/work/grün/WT"}, {Path: "/work/lane"}}
	o.Under = f.NewCwdMatcher().Under
	res := Find(s, cwds, o)
	if got := pids(res.Holders); !reflect.DeepEqual(got, []int{20, 30}) {
		t.Fatalf("holders = %v, want [20 30] (cwds %v)", got, cwds)
	}
	if res.Holders[0].Matched != "/work/grün/WT" {
		t.Errorf("matched = %q", res.Holders[0].Matched)
	}
}
