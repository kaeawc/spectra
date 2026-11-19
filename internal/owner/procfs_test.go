package owner

import (
	"context"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestUnixSocketPathWithSpaces(t *testing.T) {
	m, _ := unixSocketLinks(procTree(), "/proc", []string{"/tmp/my app/service.sock"})
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

func relSockFS(cwd string, deny bool) fakeFS {
	return fakeFS{
		dirs:  map[string][]string{"/proc": {"60", "61", "62", "63"}, "/proc/60/fd": {"3"}, "/proc/61/fd": {"3"}, "/proc/62/fd": {"3"}, "/proc/63/fd": {"3"}},
		links: map[string]string{"/proc/60/cwd": cwd, "/proc/60/fd/3": "socket:[900]", "/proc/61/cwd": "/other", "/proc/61/fd/3": "socket:[901]", "/proc/62/cwd": "/tmp", "/proc/62/fd/3": "socket:[902]", "/proc/63/cwd": "/srv/run", "/proc/63/fd/3": "socket:[903]"},
		files: map[string]string{"/proc/net/unix": "Num RefCount Protocol Flags Type St Inode Path\n" +
			"0001: 2 0 10000 0001 01 900 service.sock\n" +
			"0002: 2 0 10000 0001 01 901 service.sock\n" +
			"0003: 2 0 10000 0001 01 902 my app/service.sock\n" +
			"0004: 2 0 10000 0001 01 903 @service.sock\n"},
		deny: map[string]bool{"/proc/60/cwd": deny},
	}
}

func TestRelativeSocketMatchesCwd(t *testing.T) {
	got, denied := procPathHolders(relSockFS("/srv/run", false), "/proc", []string{"/srv/run/service.sock"})
	if denied || len(got) != 1 || got[0].PID != 60 || got[0].Hold != HoldFD {
		t.Errorf("got %+v denied=%v; want only pid 60", got, denied)
	}
}

func TestRelativeSocketOtherDirNotMatched(t *testing.T) {
	got, _ := procPathHolders(relSockFS("/elsewhere", false), "/proc", []string{"/srv/run/service.sock"})
	if len(got) != 0 {
		t.Errorf("same base name in another directory matched: %+v", got)
	}
}

func TestRelativeSocketWithSpaces(t *testing.T) {
	got, _ := procPathHolders(relSockFS("/elsewhere", false), "/proc", []string{"/tmp/my app/service.sock"})
	if len(got) != 1 || got[0].PID != 62 {
		t.Errorf("got %+v; want pid 62", got)
	}
}

func TestRelativeSocketCwdDenied(t *testing.T) {
	_, denied := procPathHolders(relSockFS("/srv/run", true), "/proc", []string{"/srv/run/service.sock"})
	if !denied {
		t.Error("unreadable cwd link must mark the scan incomplete")
	}
}

func TestRelativeSocketAbsoluteAndAbstractUnchanged(t *testing.T) {
	fsys := relSockFS("/elsewhere", false)
	fsys.files["/proc/net/unix"] += "0005: 2 0 10000 0001 01 904 /srv/run/service.sock\n"
	fsys.links["/proc/63/fd/3"] = "socket:[904]"
	got, _ := procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"})
	if len(got) != 1 || got[0].PID != 63 {
		t.Errorf("absolute entry: got %+v; want pid 63 only", got)
	}
	// The abstract "@service.sock" (inode 903) is never a relative candidate.
	if _, rel := unixSocketLinks(fsys, "/proc", []string{"/x/service.sock"}); rel["socket:[903]"] != "" {
		t.Errorf("abstract entry treated as relative: %v", rel)
	}
}
