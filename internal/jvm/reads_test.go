package jvm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReadNMTErrorsCarryCLIText(t *testing.T) {
	off := func(string, ...string) ([]byte, error) {
		return []byte("42:\nNative memory tracking is not enabled\n"), nil
	}
	for _, mode := range []string{NMTModeSummary, NMTModeDetail} {
		_, err := ReadNMT(42, mode, off)
		if !errors.Is(err, ErrNMTDisabled) || !strings.Contains(err.Error(), "restart the JVM") {
			t.Errorf("%s: err = %v", mode, err)
		}
	}
	if _, err := ReadNMT(42, NMTModeDiff, off); err == nil || !strings.HasPrefix(err.Error(), "nmt diff: ") {
		t.Errorf("diff: err = %v", err)
	}
	failing := func(string, ...string) ([]byte, error) { return nil, errors.New("exit status 1") }
	if _, err := ReadNMT(42, NMTModeDiff, failing); err == nil || !strings.HasPrefix(err.Error(), "nmt summary.diff failed for PID 42: ") {
		t.Errorf("failing diff: err = %v", err)
	}
	if _, err := ReadNMT(42, "baseline", off); err == nil {
		t.Error("baseline must not be a read mode")
	}
}

func TestCollectTrendStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	run := func(string, ...string) ([]byte, error) {
		calls++
		cancel()
		return nil, errors.New("no jstat")
	}
	start := time.Now()
	trend := collectTrend(ctx, 42, 5, time.Hour, run)
	if time.Since(start) > 5*time.Second || calls != 1 {
		t.Fatalf("calls = %d after %s; want one sample and no hour-long sleep", calls, time.Since(start))
	}
	if trend == nil || trend.Samples != 5 {
		t.Fatalf("trend = %+v", trend)
	}
}
