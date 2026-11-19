package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

func memtraceFakeDeps(rss ...int64) memtraceDeps {
	t0 := time.Unix(1_700_000_000, 0)
	clk := clock.NewFake(t0)
	calls := 0
	return memtraceDeps{
		clock: clk,
		collect: func(context.Context) []process.Info {
			i := min(calls, len(rss)-1)
			calls++
			return []process.Info{
				{PID: 100, PPID: 1, Command: "app", RSSKiB: rss[i], StartTime: t0},
				{PID: 101, PPID: 100, Command: "kid", RSSKiB: 10 + rss[i]/10, StartTime: t0},
			}
		},
		sleep: func(_ context.Context, d time.Duration) error { clk.Advance(d); return nil },
	}
}

func TestRunMemtraceText(t *testing.T) {
	var out, errb bytes.Buffer
	deps := memtraceFakeDeps(100, 200, 300, 400, 500, 600)
	code := runMemtraceWithIO(context.Background(), []string{"100", "--tree", "--duration", "5s", "--interval", "1s"}, &out, &errb, deps)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	for _, want := range []string{"subtree", "2 processes", "samples: 6", "verdict: growing", "kid"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestRunMemtraceJSON(t *testing.T) {
	var out, errb bytes.Buffer
	deps := memtraceFakeDeps(100, 100, 100, 100, 100)
	args := []string{"--json", "--duration", "4s", "--interval", "1s", "100"}
	if code := runMemtraceWithIO(context.Background(), args, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var got struct {
		PID        int   `json:"pid"`
		DurationMS int64 `json:"duration_ms"`
		IntervalMS int64 `json:"interval_ms"`
		Samples    []struct {
			ElapsedMS int64 `json:"elapsed_ms"`
		} `json:"samples"`
		Summary struct {
			Verdict string `json:"verdict"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PID != 100 || got.DurationMS != 4000 || got.IntervalMS != 1000 || len(got.Samples) != 5 || got.Summary.Verdict != "steady" {
		t.Fatalf("got %+v", got)
	}
}

func TestRunMemtraceUnknownPID(t *testing.T) {
	var out, errb bytes.Buffer
	deps := memtraceFakeDeps(1)
	if code := runMemtraceWithIO(context.Background(), []string{"999"}, &out, &errb, deps); code != 1 || !strings.Contains(errb.String(), "not found") {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
}

func TestRunMemtraceWriteError(t *testing.T) {
	var errb bytes.Buffer
	deps := memtraceFakeDeps(1, 2)
	if code := runMemtraceWithIO(context.Background(), []string{"100", "--duration", "1s"}, failWriter{}, &errb, deps); code != 1 {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
}

func TestRunMemtraceUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil, {"abc"}, {"-5"}, {"0"}, {"1", "2"}, {"100", "--interval", "0s"},
		{"100", "--duration", "-1s"}, {"--bogus", "100"},
	} {
		var out, errb bytes.Buffer
		if code := runMemtraceWithIO(context.Background(), args, &out, &errb, memtraceFakeDeps(1)); code != 2 {
			t.Errorf("args %v: code %d, want 2 (stderr=%s)", args, code, errb.String())
		}
	}
}
