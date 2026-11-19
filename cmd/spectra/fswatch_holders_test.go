package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/fswatch"
)

// growingDeps grows n files under /r by one byte per poll and also adds a
// symlink "g" that changes its mtime every poll.
func growingDeps(n int, lookup func(context.Context, string) ([]fswatch.Holder, bool, error)) fswatchDeps {
	fsys := &fakeWatchFS{dirs: map[string][]fswatch.Entry{}}
	clk := clock.NewFake(fsEpoch)
	tick := 0
	build := func() {
		var es []fswatch.Entry
		for i := 0; i < n; i++ {
			es = append(es, fswatch.Entry{Name: fmt.Sprintf("f%02d", i), Size: int64(tick)})
		}
		es = append(es, fswatch.Entry{Name: "g", IsSymlink: true, Size: int64(tick), ModTime: fsEpoch.Add(time.Duration(tick) * time.Second)})
		fsys.dirs["/r"] = es
	}
	build()
	return fswatchDeps{
		fs: fsys, clock: clk, holders: lookup,
		sleep: func(_ context.Context, d time.Duration) error {
			tick++
			build()
			clk.Advance(d)
			return nil
		},
	}
}

func TestFswatchHolderLookupSkipsSymlinksAndCapsAtFive(t *testing.T) {
	var seen []string
	deps := growingDeps(8, func(_ context.Context, p string) ([]fswatch.Holder, bool, error) {
		seen = append(seen, p)
		return nil, false, nil
	})
	var out, errb bytes.Buffer
	if code := runFswatchWithIO(context.Background(), []string{"--duration", "2s", "--top", "20", "/r"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code=%d %s", code, errb.String())
	}
	if len(seen) != 5 {
		t.Fatalf("looked up %d files, want 5: %v", len(seen), seen)
	}
	for _, p := range seen {
		if strings.HasSuffix(p, "/g") {
			t.Fatalf("symlink looked up: %v", seen)
		}
	}
	if !strings.Contains(out.String(), "symlink") {
		t.Fatalf("entry kind missing from text:\n%s", out.String())
	}
}

func TestFswatchHoldersTimeoutIsReportedIncomplete(t *testing.T) {
	deps := growingDeps(2, func(ctx context.Context, _ string) ([]fswatch.Holder, bool, error) {
		<-ctx.Done()
		return nil, false, ctx.Err()
	})
	var out, errb bytes.Buffer
	args := []string{"--duration", "2s", "--holders-timeout", "30ms", "--json", "/r"}
	if code := runFswatchWithIO(context.Background(), args, &out, &errb, deps); code != 0 {
		t.Fatalf("code=%d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"holders_incomplete": true`) || !strings.Contains(out.String(), `"kind": "file"`) {
		t.Fatalf("missing incomplete flag or kind:\n%s", out.String())
	}
	out.Reset()
	deps = growingDeps(1, func(ctx context.Context, _ string) ([]fswatch.Holder, bool, error) { return nil, true, nil })
	if code := runFswatchWithIO(context.Background(), []string{"--duration", "2s", "/r"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out.String(), "Holders may be incomplete") {
		t.Fatalf("text does not flag incomplete holders:\n%s", out.String())
	}
}

func TestFswatchHoldersTimeoutZeroDisablesLookup(t *testing.T) {
	var calls atomic.Int32
	deps := growingDeps(2, func(context.Context, string) ([]fswatch.Holder, bool, error) {
		calls.Add(1)
		return nil, false, nil
	})
	var out, errb bytes.Buffer
	if code := runFswatchWithIO(context.Background(), []string{"--duration", "2s", "--holders-timeout", "0", "/r"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if calls.Load() != 0 || strings.Contains(out.String(), "incomplete") {
		t.Fatalf("calls=%d out=%s", calls.Load(), out.String())
	}
	if code := runFswatchWithIO(context.Background(), []string{"--holders-timeout", "-1s", "/r"}, &out, &errb, deps); code != 2 {
		t.Fatalf("negative timeout code=%d", code)
	}
}
