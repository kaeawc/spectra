package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/fswatch"
)

type fakeWatchFS struct{ dirs map[string][]fswatch.Entry }

func (f *fakeWatchFS) ReadDir(p string) ([]fswatch.Entry, error) {
	es, ok := f.dirs[p]
	if !ok {
		return nil, errors.New("no such dir")
	}
	return append([]fswatch.Entry(nil), es...), nil
}

var fsEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fswatchTestDeps grows /r/log by one byte on every poll.
func fswatchTestDeps() fswatchDeps {
	size := int64(1)
	fsys := &fakeWatchFS{dirs: map[string][]fswatch.Entry{"/r": {{Name: "log", Size: size}}}}
	clk := clock.NewFake(fsEpoch)
	return fswatchDeps{
		fs:    fsys,
		clock: clk,
		sleep: func(_ context.Context, d time.Duration) error {
			size++
			fsys.dirs["/r"] = []fswatch.Entry{{Name: "log", Size: size}}
			clk.Advance(d)
			return nil
		},
		holders: func(_ context.Context, p string) ([]fswatch.Holder, error) {
			return []fswatch.Holder{{PID: 42, Command: "writer", Mode: "fd w"}}, nil
		},
	}
}

func TestFswatchText(t *testing.T) {
	var out, errb bytes.Buffer
	code := runFswatchWithIO(context.Background(), []string{"--duration", "3s", "/r"}, &out, &errb, fswatchTestDeps())
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"modified", "/r/log", "3 events", "held by pid 42 writer", "polling misses files"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestFswatchFlagsAfterPath(t *testing.T) {
	var out, errb bytes.Buffer
	code := runFswatchWithIO(context.Background(), []string{"/r", "--duration", "2s", "--json"}, &out, &errb, fswatchTestDeps())
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
	var got struct {
		Root       string           `json:"root"`
		DurationMS int64            `json:"duration_ms"`
		IntervalMS int64            `json:"interval_ms"`
		Polls      int              `json:"polls"`
		Events     []map[string]any `json:"events"`
		Summary    struct {
			TotalEvents int `json:"total_events"`
		} `json:"summary"`
		Limitations []string `json:"limitations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out.String())
	}
	if got.Root != "/r" || got.DurationMS != 2000 || got.IntervalMS != 1000 || got.Polls != 2 ||
		len(got.Events) != 2 || got.Summary.TotalEvents != 2 || len(got.Limitations) == 0 {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestFswatchCancelPrintsSummary(t *testing.T) {
	deps := fswatchTestDeps()
	ctx, cancel := context.WithCancel(context.Background())
	inner := deps.sleep
	n := 0
	deps.sleep = func(c context.Context, d time.Duration) error {
		n++
		if n == 2 {
			cancel()
			return c.Err()
		}
		return inner(c, d)
	}
	var out, errb bytes.Buffer
	if code := runFswatchWithIO(ctx, []string{"--json", "/r"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"duration_ms": 1000`) {
		t.Fatalf("elapsed not reported:\n%s", out.String())
	}
}

func TestFswatchWriteError(t *testing.T) {
	var errb bytes.Buffer
	if code := runFswatchWithIO(context.Background(), []string{"--duration", "2s", "/r"}, failWriter{}, &errb, fswatchTestDeps()); code != 1 {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
}

func TestFswatchUnreadableRoot(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runFswatchWithIO(context.Background(), []string{"/missing"}, &out, &errb, fswatchTestDeps()); code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestFswatchUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"/r", "/s"},
		{"--duration", "0s", "/r"},
		{"--interval", "-1s", "/r"},
		{"--top", "0", "/r"},
		{"--max-entries", "0", "/r"},
		{"--duration", "1s", "--interval", "2s", "/r"},
		{"--bogus", "/r"},
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := runFswatchWithIO(context.Background(), args, &out, &errb, fswatchTestDeps()); code != 2 {
			t.Errorf("args %v: code=%d, want 2", args, code)
		}
	}
}
