package daemonprocess

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

func TestListServesStaleSnapshotOnOverrunAndFailure(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var fail atomic.Bool
	collect := func(ctx context.Context) ([]process.Info, error) {
		n := calls.Add(1)
		if n == 1 {
			return []process.Info{{PID: 1}}, nil
		}
		if fail.Load() {
			return nil, errors.New("ps: exit 1")
		}
		select {
		case <-release:
			return []process.Info{{PID: int(n)}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	clk := clock.NewFake(time.Unix(1000, 0))
	s := NewService(Options{Collect: collect, Clock: clk, AnswerWithin: 20 * time.Millisecond})
	ctx := context.Background()
	first, err := s.List(ctx, false)
	if err != nil || first.Stale {
		t.Fatalf("first = %+v %v", first, err)
	}
	clk.Advance(time.Minute)
	snap, err := s.List(ctx, false)
	if err != nil || !snap.Stale || snap.Processes[0].PID != 1 || !snap.SampledAt.Equal(first.SampledAt) || snap.StaleReason == "" {
		t.Fatalf("overrun = %+v %v, want the first snapshot marked stale", snap, err)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err = s.List(ctx, false)
		if err == nil && !snap.Stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never served the finished collection: %+v %v", snap, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	fail.Store(true)
	clk.Advance(time.Minute)
	snap, err = s.List(ctx, true)
	if err != nil || !snap.Stale || snap.StaleReason != "ps: exit 1" {
		t.Fatalf("failure = %+v %v, want the last snapshot marked stale", snap, err)
	}
}
