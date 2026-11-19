package fswatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
)

func dirEntry(name string) Entry     { return Entry{Name: name, IsDir: true} }
func linkEntry(name string) Entry    { return Entry{Name: name, IsSymlink: true} }
func linkDirEntry(name string) Entry { return Entry{Name: name, IsSymlink: true, IsDir: true} }

func TestTypeChangesAreRemovedThenCreated(t *testing.T) {
	cases := []struct {
		name     string
		from, to Entry
		want     []string
	}{
		{"file to dir", file("x", 1, 0), dirEntry("x"), []string{"removed:file:/r/x", "created:dir:/r/x"}},
		{"dir to file", dirEntry("x"), file("x", 1, 0), []string{"removed:dir:/r/x", "created:file:/r/x"}},
		{"file to symlink", file("x", 1, 0), linkEntry("x"), []string{"removed:file:/r/x", "created:symlink:/r/x"}},
		{"symlink to dir", linkDirEntry("x"), dirEntry("x"), []string{"removed:symlink:/r/x", "created:dir:/r/x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fsys := fakeFS{"/r": {c.from}, "/r/x": nil}
			res := harness(t, fsys, []func(){func() { fsys["/r"] = []Entry{c.to} }}, Config{})
			var got []string
			for _, e := range res.Events {
				got = append(got, e.Kind+":"+e.EntryKind+":"+e.Path)
			}
			if len(got) != 2 || got[0] != c.want[0] || got[1] != c.want[1] {
				t.Fatalf("events = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSymlinkBusiestFileDoesNotExistAsFile(t *testing.T) {
	fsys := fakeFS{"/r": {file("g", 1, 0)}}
	res := harness(t, fsys, []func(){func() { fsys["/r"] = []Entry{linkEntry("g")} }}, Config{})
	f := res.Summary.BusiestFiles
	if len(f) != 1 || f[0].Exists {
		t.Fatalf("symlink must not count as an existing file: %+v", f)
	}
}

func TestTruncationDoesNotInventEvents(t *testing.T) {
	fsys := fakeFS{"/r": {file("a", 1, 0), file("b", 1, 0), file("c", 1, 0), file("d", 1, 0), file("e", 1, 0)}}
	res := harness(t, fsys, []func(){
		func() { fsys["/r"] = []Entry{file("b", 1, 0), file("c", 1, 0), file("d", 1, 0), file("e", 1, 0)} },
	}, Config{MaxEntries: 3})
	got := kinds(res)
	if len(got) != 1 || got[0] != "removed:/r/a" {
		t.Fatalf("events = %v, want only removed:/r/a (d slid into the window, it was not created)", got)
	}
	if !res.Summary.Truncated || res.Summary.TrackedEntries != 3 || res.Summary.TruncationNote == "" {
		t.Fatalf("summary = %+v", res.Summary)
	}
}

func TestTruncatedLastFollowsTreeOrder(t *testing.T) {
	// "a.b" sorts before "a/x" as a string but after it in tree order.
	fsys := fakeFS{"/r": {dirEntry("a"), file("a.b", 1, 0)}, "/r/a": {file("x", 1, 0)}}
	snap, err := Take(context.Background(), fsys, "/r", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Entries["/r/a/x"]; !ok || snap.Last != "/r/a/x" || !snap.Truncated {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestUnreadableSubdirWarnsOnceAndKeepsEntries(t *testing.T) {
	fsys := fakeFS{"/r": {dirEntry("d"), file("a", 1, 0)}, "/r/d": {file("x", 1, 0)}}
	var warned []string
	res := harness(t, fsys, []func(){
		func() { delete(fsys, "/r/d") },
		func() {},
		func() { fsys["/r/d"] = []Entry{file("x", 1, 0)} },
	}, Config{Warn: func(m string) { warned = append(warned, m) }})
	if len(warned) != 1 || len(res.Events) != 0 || len(res.Warnings) != 1 {
		t.Fatalf("warned=%v events=%v warnings=%v", warned, res.Events, res.Warnings)
	}
}

type rootErrFS struct {
	fakeFS
	err error
}

func (f *rootErrFS) ReadDir(p string) ([]Entry, error) {
	if p == "/r" && f.err != nil {
		return nil, f.err
	}
	return f.fakeFS.ReadDir(p)
}

func TestTransientRootErrorSkipsPoll(t *testing.T) {
	fsys := &rootErrFS{fakeFS: fakeFS{"/r": {file("a", 1, 0), file("b", 1, 0)}}}
	clk := clock.NewFake(t0)
	n := 0
	out, err := Run(context.Background(), Config{
		Root: "/r", Interval: time.Second, Duration: 3 * time.Second, Top: 5, MaxEntries: 100, FS: fsys, Clock: clk,
		Sleep: func(_ context.Context, d time.Duration) error {
			n++
			fsys.err = nil
			if n == 1 {
				fsys.err = errors.New("permission blip")
			}
			clk.Advance(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.PollsFailed != 1 || out.Polls != 2 || len(out.Events) != 0 {
		t.Fatalf("polls=%d failed=%d events=%v", out.Polls, out.PollsFailed, out.Events)
	}
}

func TestRootGoneEndsWatch(t *testing.T) {
	fsys := &rootErrFS{fakeFS: fakeFS{"/r": {file("a", 1, 0)}}}
	clk := clock.NewFake(t0)
	out, err := Run(context.Background(), Config{
		Root: "/r", Interval: time.Second, Duration: time.Minute, Top: 5, MaxEntries: 100, FS: fsys, Clock: clk,
		Sleep: func(_ context.Context, d time.Duration) error {
			fsys.err = fmt.Errorf("read dir /r: %w", fs.ErrNotExist)
			clk.Advance(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.EndedReason == "" || out.Polls != 0 || len(out.Events) != 0 || out.DurationMS != 1000 {
		t.Fatalf("result = %+v", out)
	}
}

type cancelFS struct {
	fakeFS
	cancel func()
	armed  *bool
}

func (f cancelFS) ReadDir(p string) ([]Entry, error) {
	if *f.armed && p == "/r/d" {
		f.cancel()
	}
	return f.fakeFS.ReadDir(p)
}

func TestCancelledScanIsAbandoned(t *testing.T) {
	armed := false
	ctx, cancel := context.WithCancel(context.Background())
	base := fakeFS{"/r": {dirEntry("d"), dirEntry("e")}, "/r/d": {file("x", 1, 0)}, "/r/e": {file("y", 1, 0)}}
	fsys := cancelFS{fakeFS: base, cancel: cancel, armed: &armed}
	clk := clock.NewFake(t0)
	out, err := Run(ctx, Config{
		Root: "/r", Interval: time.Second, Duration: time.Minute, Top: 5, MaxEntries: 100, FS: fsys, Clock: clk,
		Sleep: func(_ context.Context, d time.Duration) error {
			armed = true
			base["/r"] = []Entry{dirEntry("d")} // a diff of the partial scan would report e removed
			clk.Advance(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 0 || out.Polls != 0 {
		t.Fatalf("partial scan was diffed: %+v", out)
	}
	if _, err := Take(ctx, fsys, "/r", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("Take on cancelled ctx err = %v", err)
	}
}

func TestOSFSUnstattableChildrenAreUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if es, err := (OSFS{}).ReadDir(dir); err == nil {
		t.Fatalf("expected error for unstattable children, got %v", es)
	}
	snap, err := Take(context.Background(), OSFS{}, filepath.Dir(dir), 100)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Unreadable[dir] {
		t.Fatalf("dir not marked unreadable: %+v", snap.Unreadable)
	}
}
