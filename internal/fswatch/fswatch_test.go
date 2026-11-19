package fswatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeFS maps a directory to its entries.
type fakeFS map[string][]Entry

func (f fakeFS) ReadDir(path string) ([]Entry, error) {
	es, ok := f[path]
	if !ok {
		return nil, errors.New("no such dir")
	}
	return append([]Entry(nil), es...), nil
}

func file(name string, size int64, mod int) Entry {
	return Entry{Name: name, Size: size, ModTime: t0.Add(time.Duration(mod) * time.Second)}
}

// harness runs the watcher with a fake clock; steps[i] mutates the fs before poll i+1.
func harness(t *testing.T, fsys fakeFS, steps []func(), cfg Config) Result {
	t.Helper()
	clk := clock.NewFake(t0)
	n := 0
	cfg.FS = fsys
	cfg.Clock = clk
	if cfg.Root == "" {
		cfg.Root = "/r"
	}
	if cfg.Interval == 0 {
		cfg.Interval = time.Second
	}
	if cfg.Duration == 0 {
		cfg.Duration = time.Duration(len(steps)) * cfg.Interval
	}
	if cfg.Top == 0 {
		cfg.Top = 10
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	cfg.Sleep = func(_ context.Context, d time.Duration) error {
		if n < len(steps) {
			steps[n]()
		}
		n++
		clk.Advance(d)
		return nil
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func kinds(res Result) []string {
	var out []string
	for _, e := range res.Events {
		out = append(out, e.Kind+":"+e.Path)
	}
	return out
}

func TestCreatedModifiedRemoved(t *testing.T) {
	fsys := fakeFS{"/r": {file("a", 1, 0), file("b", 1, 0)}}
	res := harness(t, fsys, []func(){
		func() { fsys["/r"] = []Entry{file("a", 2, 0), file("b", 1, 0), file("c", 5, 1)} },
		func() { fsys["/r"] = []Entry{file("a", 2, 0), file("c", 5, 1)} },
	}, Config{})
	got := kinds(res)
	want := []string{"modified:/r/a", "created:/r/c", "removed:/r/b"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
	if c := res.Summary.Counts; c.Created != 1 || c.Modified != 1 || c.Removed != 1 {
		t.Fatalf("counts = %+v", c)
	}
	if res.Polls != 2 || res.DurationMS != 2000 {
		t.Fatalf("polls=%d duration=%d", res.Polls, res.DurationMS)
	}
}

func TestMtimeOnlyChangeIsModified(t *testing.T) {
	fsys := fakeFS{"/r": {file("a", 1, 0)}}
	res := harness(t, fsys, []func(){func() { fsys["/r"] = []Entry{file("a", 1, 9)} }}, Config{})
	if got := kinds(res); len(got) != 1 || got[0] != "modified:/r/a" {
		t.Fatalf("events = %v", got)
	}
}

func TestUnchangedTreeEmitsNothing(t *testing.T) {
	fsys := fakeFS{"/r": {file("a", 1, 0), {Name: "d", IsDir: true}}, "/r/d": {file("x", 1, 0)}}
	emitted := 0
	res := harness(t, fsys, []func(){func() {}, func() {}, func() {}}, Config{Emit: func(Event) { emitted++ }})
	if emitted != 0 || len(res.Events) != 0 || res.Summary.TotalEvents != 0 || res.Polls != 3 {
		t.Fatalf("emitted=%d events=%v polls=%d", emitted, res.Events, res.Polls)
	}
}

func TestBusiestRanking(t *testing.T) {
	fsys := fakeFS{"/r": {{Name: "d", IsDir: true}, {Name: "e", IsDir: true}}, "/r/d": {file("hot", 1, 0), file("warm", 1, 0)}, "/r/e": {file("x", 1, 0)}}
	bump := func(hot, warm int64, x int64) func() {
		return func() {
			fsys["/r/d"] = []Entry{file("hot", hot, 0), file("warm", warm, 0)}
			fsys["/r/e"] = []Entry{file("x", x, 0)}
		}
	}
	res := harness(t, fsys, []func(){bump(2, 1, 2), bump(3, 2, 2), bump(4, 2, 2)}, Config{Top: 2})
	f := res.Summary.BusiestFiles
	if len(f) != 2 || f[0].Path != "/r/d/hot" || f[0].Events != 3 || f[1].Path != "/r/d/warm" || f[1].Events != 1 {
		t.Fatalf("files = %+v", f)
	}
	d := res.Summary.BusiestDirs
	if len(d) != 2 || d[0].Path != "/r/d" || d[0].Events != 4 || d[1].Path != "/r/e" || d[1].Events != 1 {
		t.Fatalf("dirs = %+v", d)
	}
	if !f[0].Exists || res.Summary.DistinctFiles != 3 {
		t.Fatalf("exists/distinct wrong: %+v", res.Summary)
	}
}

func TestRemovedFileNotExists(t *testing.T) {
	fsys := fakeFS{"/r": {file("a", 1, 0)}}
	res := harness(t, fsys, []func(){func() { fsys["/r"] = nil }}, Config{})
	if f := res.Summary.BusiestFiles; len(f) != 1 || f[0].Exists {
		t.Fatalf("files = %+v", f)
	}
}

func TestEarlyCancelReportsElapsed(t *testing.T) {
	fsys := fakeFS{"/r": {file("a", 1, 0)}}
	clk := clock.NewFake(t0)
	ctx, cancel := context.WithCancel(context.Background())
	polls := 0
	res, err := Run(ctx, Config{
		Root: "/r", Interval: time.Second, Duration: time.Minute, Top: 5, MaxEntries: 100,
		FS: fsys, Clock: clk,
		Sleep: func(ctx context.Context, d time.Duration) error {
			polls++
			if polls == 3 {
				cancel()
				return ctx.Err()
			}
			clk.Advance(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.DurationMS != 2000 || res.Polls != 2 {
		t.Fatalf("duration=%d polls=%d", res.DurationMS, res.Polls)
	}
}

func TestTruncation(t *testing.T) {
	fsys := fakeFS{"/r": {file("a", 1, 0), file("b", 1, 0), file("c", 1, 0), file("d", 1, 0)}}
	res := harness(t, fsys, []func(){func() {}}, Config{MaxEntries: 2})
	if !res.Summary.Truncated || res.Summary.TruncationNote == "" || res.Summary.TrackedEntries != 2 {
		t.Fatalf("summary = %+v", res.Summary)
	}
	if len(res.Events) != 0 {
		t.Fatalf("stable truncated tree produced events: %v", res.Events)
	}
}

func TestSymlinkNotFollowed(t *testing.T) {
	fsys := fakeFS{
		"/r":      {{Name: "link", IsDir: true, IsSymlink: true}},
		"/r/link": {file("secret", 1, 0)},
	}
	snap, err := Take(fsys, "/r", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Entries["/r/link/secret"]; ok {
		t.Fatal("followed symlink")
	}
	if m := snap.Entries["/r/link"]; m.IsDir {
		t.Fatal("symlink recorded as directory")
	}
}

func TestUnreadableRootIsError(t *testing.T) {
	_, err := Run(context.Background(), Config{Root: "/nope", FS: fakeFS{}, Clock: clock.NewFake(t0), MaxEntries: 1, Interval: time.Second, Duration: time.Second})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRealFilesystem(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(a, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "loop")); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(t0)
	n := 0
	res, err := Run(context.Background(), Config{
		Root: dir, Interval: time.Second, Duration: 2 * time.Second, Top: 5, MaxEntries: 100,
		FS: OSFS{}, Clock: clk,
		Sleep: func(_ context.Context, d time.Duration) error {
			n++
			if n == 1 {
				_ = os.WriteFile(a, []byte("longer"), 0o600)
				_ = os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o600)
			}
			clk.Advance(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Summary.Counts
	if c.Created != 1 || c.Modified != 1 || c.Removed != 0 {
		t.Fatalf("counts = %+v events=%v", c, res.Events)
	}
}
