package owner

import (
	"context"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestUnixSocketPathWithSpaces(t *testing.T) {
	m := unixSocketLinks(procTree(), "/proc", []string{"/tmp/my app/service.sock"})
	if !m["socket:[780]"] || len(m) != 1 {
		t.Errorf("socks = %v", m)
	}
	if _, p, ok := parseUnixLine("0004: 1 0 0 0001 01 5 @abstract name"); !ok || p != "@abstract name" {
		t.Errorf("abstract path = %q ok=%v", p, ok)
	}
}

func TestProcFDLinkDenied(t *testing.T) {
	dirs := map[string][]string{"/proc": {"50", "51"}, "/proc/50/fd": {"3"}, "/proc/51/fd": {"4"}}
	denyFS := fakeFS{exist: map[string]bool{"/x": true}, dirs: dirs, deny: map[string]bool{"/proc/50/fd/3": true}}
	if _, denied := procPathHolders(denyFS, "/proc", []string{"/x"}); !denied {
		t.Error("permission failure on an fd link must mark the scan incomplete")
	}
	// Both fd links are simply gone (ENOENT): normal, not a denial.
	if _, denied := procPathHolders(fakeFS{dirs: dirs}, "/proc", []string{"/x"}); denied {
		t.Error("vanished fd link must not mark the scan incomplete")
	}
	f := Finder{OS: hostos.Linux, FS: denyFS, Procs: fakeProcs, IsRoot: func() bool { return true }}
	res, err := f.Find(context.Background(), Target{Kind: KindPath, Path: "/x"})
	if err != nil || !res.Incomplete {
		t.Errorf("root with denied link: res = %+v, err = %v", res, err)
	}
}
