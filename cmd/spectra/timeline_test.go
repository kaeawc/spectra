package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func fixedNow() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }

func fakeTimelineProcs() []process.Info {
	return []process.Info{
		{PID: 10, Command: "recent", StartTime: time.Date(2026, 1, 1, 11, 30, 0, 0, time.UTC)},
		{PID: 20, Command: "old", StartTime: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)},
		{PID: 30, Command: "nostart"}, // zero StartTime -> excluded
	}
}

func TestRunTimelineWindowsAndSummary(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTimelineWithIO([]string{"--since", "1h"}, &out, &errBuf, fixedNow, fakeTimelineProcs)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "recent [10] started") {
		t.Errorf("expected the recent process event, got:\n%s", s)
	}
	if strings.Contains(s, "old [20]") || strings.Contains(s, "nostart") {
		t.Errorf("out-of-window / zero-start processes should be excluded:\n%s", s)
	}
}

func TestRunTimelineJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runTimelineWithIO([]string{"--since", "6h", "--json"}, &out, &errBuf, fixedNow, fakeTimelineProcs); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"events"`) || !strings.Contains(out.String(), `"source"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunTimelineArgValidation(t *testing.T) {
	for _, args := range [][]string{{"--since", "0"}, {"extra"}, {"--since", "-5m"}} {
		var out, errBuf bytes.Buffer
		if code := runTimelineWithIO(args, &out, &errBuf, fixedNow, fakeTimelineProcs); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}
