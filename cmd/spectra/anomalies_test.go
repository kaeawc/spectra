package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/store"
)

func metricRows() []store.ProcessMetricRow {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	var rows []store.ProcessMetricRow
	// pid 8123: noisy ~600MB baseline then a spike to ~2.1GB (in KiB)
	vals := []int64{600_000, 610_000, 595_000, 605_000, 600_000, 615_000, 590_000, 2_100_000}
	for i, v := range vals {
		rows = append(rows, store.ProcessMetricRow{PID: 8123, MinuteAt: base.Add(time.Duration(i) * time.Minute), AvgRSSKiB: v})
	}
	return rows
}

func TestRunAnomaliesFlagsSpike(t *testing.T) {
	load := func() ([]store.ProcessMetricRow, error) { return metricRows(), nil }
	names := func() map[int]string { return map[int]string{8123: "com.corp.helper"} }
	var out, errBuf bytes.Buffer
	if code := runAnomaliesWithIO(nil, &out, &errBuf, load, names); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "pid 8123 (com.corp.helper)") || !strings.Contains(s, "σ") {
		t.Errorf("output = %q", s)
	}
	if !strings.Contains(s, "GB") {
		t.Errorf("expected a GB-scale spike in output; got %q", s)
	}
}

func TestRunAnomaliesExitedLabel(t *testing.T) {
	load := func() ([]store.ProcessMetricRow, error) { return metricRows(), nil }
	names := func() map[int]string { return map[int]string{} } // pid no longer running
	var out, errBuf bytes.Buffer
	runAnomaliesWithIO(nil, &out, &errBuf, load, names)
	if !strings.Contains(out.String(), "pid 8123 (exited)") {
		t.Errorf("expected exited label; got %q", out.String())
	}
}

func TestRunAnomaliesNoneClean(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	flat := []store.ProcessMetricRow{}
	for i, v := range []int64{600_000, 610_000, 595_000, 605_000, 600_000, 615_000, 590_000, 600_000} {
		flat = append(flat, store.ProcessMetricRow{PID: 1, MinuteAt: base.Add(time.Duration(i) * time.Minute), AvgRSSKiB: v})
	}
	load := func() ([]store.ProcessMetricRow, error) { return flat, nil }
	names := func() map[int]string { return nil }
	var out, errBuf bytes.Buffer
	runAnomaliesWithIO(nil, &out, &errBuf, load, names)
	if !strings.Contains(out.String(), "No process RSS anomalies") {
		t.Errorf("expected clean message; got %q", out.String())
	}
}

func TestRunAnomaliesEmpty(t *testing.T) {
	load := func() ([]store.ProcessMetricRow, error) { return nil, nil }
	names := func() map[int]string { return nil }
	var out, errBuf bytes.Buffer
	if code := runAnomaliesWithIO(nil, &out, &errBuf, load, names); code != 1 {
		t.Fatalf("exit = %d, want 1 for empty metrics", code)
	}
}

func TestRunAnomaliesRejectsBadParams(t *testing.T) {
	load := func() ([]store.ProcessMetricRow, error) { return metricRows(), nil }
	names := func() map[int]string { return nil }
	for _, args := range [][]string{{"-z", "0"}, {"-z", "-2"}, {"--min-samples", "1"}} {
		var out, errBuf bytes.Buffer
		if code := runAnomaliesWithIO(args, &out, &errBuf, load, names); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestRunAnomaliesLoadError(t *testing.T) {
	load := func() ([]store.ProcessMetricRow, error) { return nil, errors.New("db locked") }
	names := func() map[int]string { return nil }
	var out, errBuf bytes.Buffer
	if code := runAnomaliesWithIO(nil, &out, &errBuf, load, names); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
