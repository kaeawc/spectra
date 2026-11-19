package owner

import (
	"context"
	"os"
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
	if _, denied, _ := procPathHolders(denyFS, "/proc", []string{"/x"}); !denied {
		t.Error("permission failure on an fd link must mark the scan incomplete")
	}
	// Both fd links are simply gone (ENOENT): normal, not a denial.
	if _, denied, _ := procPathHolders(fakeFS{dirs: dirs}, "/proc", []string{"/x"}); denied {
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
		deny:  map[string]bool{"/proc/60/cwd": deny},
		ids:   map[string]string{"/srv/run/service.sock": "run-sock", "/tmp/my app/service.sock": "app-sock"},
		modes: map[string]os.FileMode{"/srv/run/service.sock": os.ModeSocket, "/tmp/my app/service.sock": os.ModeSocket},
	}
}

func TestRelativeSocketMatchesCwd(t *testing.T) {
	got, denied, _ := procPathHolders(relSockFS("/srv/run", false), "/proc", []string{"/srv/run/service.sock"})
	if denied || len(got) != 1 || got[0].PID != 60 || got[0].Hold != HoldFD {
		t.Errorf("got %+v denied=%v; want only pid 60", got, denied)
	}
}

func TestRelativeSocketOtherDirNotMatched(t *testing.T) {
	got, _, _ := procPathHolders(relSockFS("/elsewhere", false), "/proc", []string{"/srv/run/service.sock"})
	if len(got) != 0 {
		t.Errorf("same base name in another directory matched: %+v", got)
	}
}

func TestRelativeSocketWithSpaces(t *testing.T) {
	got, _, _ := procPathHolders(relSockFS("/elsewhere", false), "/proc", []string{"/tmp/my app/service.sock"})
	if len(got) != 1 || got[0].PID != 62 {
		t.Errorf("got %+v; want pid 62", got)
	}
}

func TestRelativeSocketCwdDenied(t *testing.T) {
	_, denied, _ := procPathHolders(relSockFS("/srv/run", true), "/proc", []string{"/srv/run/service.sock"})
	if !denied {
		t.Error("unreadable cwd link must mark the scan incomplete")
	}
}

func TestRelativeSocketAbsoluteAndAbstractUnchanged(t *testing.T) {
	fsys := relSockFS("/elsewhere", false)
	fsys.files["/proc/net/unix"] += "0005: 2 0 10000 0001 01 904 /srv/run/service.sock\n"
	fsys.links["/proc/63/fd/3"] = "socket:[904]"
	got, _, _ := procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"})
	if len(got) != 1 || got[0].PID != 63 {
		t.Errorf("absolute entry: got %+v; want pid 63 only", got)
	}
	// The abstract "@service.sock" (inode 903) is never a relative candidate.
	if _, rel := unixSocketLinks(fsys, "/proc", []string{"/x/service.sock"}); rel["socket:[903]"] != "" {
		t.Errorf("abstract entry treated as relative: %v", rel)
	}
}

func dotDotFS(name string) fakeFS {
	fsys := relSockFS("/srv/run", false)
	fsys.files["/proc/net/unix"] = "Num RefCount Protocol Flags Type St Inode Path\n0001: 2 0 10000 0001 01 900 " + name + "\n"
	return fsys
}

func TestRelativeSocketDotDotAfterSymlink(t *testing.T) {
	const name = "link/../service.sock"
	fsys := dotDotFS(name)
	// link -> else/deep, so the kernel binds /srv/run/else/service.sock.
	fsys.ids = map[string]string{"/srv/run/else/service.sock": "real", "/srv/run/" + name: "real"}
	fsys.modes = map[string]os.FileMode{"/srv/run/" + name: os.ModeSocket, "/srv/run/else/service.sock": os.ModeSocket}
	got, _, _ := procPathHolders(fsys, "/proc", []string{"/srv/run/else/service.sock"})
	if len(got) != 1 || got[0].PID != 60 {
		t.Errorf("symlink/.. socket not found by identity: %+v", got)
	}
	// An unrelated regular file at the lexically cleaned path is not the socket.
	fsys.ids = map[string]string{"/srv/run/service.sock": "plain", "/srv/run/" + name: "real"}
	fsys.modes = map[string]os.FileMode{"/srv/run/" + name: os.ModeSocket}
	got, _, _ = procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"})
	if len(got) != 0 {
		t.Errorf("cleaned-path regular file matched: %+v", got)
	}
}

func TestRelativeSocketDotDotNeedsSocketIdentity(t *testing.T) {
	const name = "link/../service.sock"
	fsys := dotDotFS(name)
	if got, _, _ := procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"}); len(got) != 0 {
		t.Errorf("no identity available but matched: %+v", got)
	}
	fsys.ids = map[string]string{"/srv/run/service.sock": "x", "/srv/run/" + name: "x"}
	fsys.modes = map[string]os.FileMode{"/srv/run/service.sock": os.ModeSocket}
	if got, _, _ := procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"}); len(got) != 0 {
		t.Errorf("non-socket identity match accepted: %+v", got)
	}
}

func TestRelativeSocketRegularFileAtCwdPathNotMatched(t *testing.T) {
	fsys := relSockFS("/srv/run", false)
	fsys.modes = nil // /srv/run/service.sock is an unrelated regular file
	if got, _, _ := procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"}); len(got) != 0 {
		t.Errorf("regular file matched as socket holder: %+v", got)
	}
	delete(fsys.ids, "/srv/run/service.sock") // unstattable target
	if got, _, _ := procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"}); len(got) != 0 {
		t.Errorf("unstattable target matched: %+v", got)
	}
}

func TestRelativeSocketAmbiguousCandidates(t *testing.T) {
	fsys := relSockFS("/srv/run", false)
	// pid 61 bound in /other, then moved into /srv/run: same cwd-derived path.
	fsys.links["/proc/61/cwd"] = "/srv/run"
	got, _, ambiguous := procPathHolders(fsys, "/proc", []string{"/srv/run/service.sock"})
	if !ambiguous || len(got) != 2 || got[0].PID != 60 || got[1].PID != 61 {
		t.Errorf("got %+v ambiguous=%v; want pids 60 and 61, ambiguous", got, ambiguous)
	}
	f := Finder{OS: hostos.Linux, FS: fsys, Procs: fakeProcs, IsRoot: func() bool { return true }}
	res, err := f.Find(context.Background(), Target{Kind: KindPath, Path: "/srv/run/service.sock"})
	if err != nil || !res.Incomplete || res.Hint != ambiguousHint || len(res.Holders) != 2 {
		t.Errorf("res = %+v, err = %v; want incomplete with ambiguous hint", res, err)
	}
}

func TestRelativeSocketSingleCandidateNotAmbiguous(t *testing.T) {
	got, _, ambiguous := procPathHolders(relSockFS("/srv/run", false), "/proc", []string{"/srv/run/service.sock"})
	if ambiguous || len(got) != 1 {
		t.Errorf("got %+v ambiguous=%v; want one holder, not ambiguous", got, ambiguous)
	}
	f := Finder{OS: hostos.Linux, FS: relSockFS("/srv/run", false), Procs: fakeProcs, IsRoot: func() bool { return true }}
	res, err := f.Find(context.Background(), Target{Kind: KindPath, Path: "/srv/run/service.sock"})
	if err != nil || res.Incomplete || res.Hint != "" {
		t.Errorf("res = %+v, err = %v; want complete", res, err)
	}
}
