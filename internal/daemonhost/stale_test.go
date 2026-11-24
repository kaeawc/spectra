package daemonhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type reading struct {
	N int `json:"n"`
}

// blockingAfterFirst answers the first collection at once and then behaves
// as next says: block until release, or fail.
func blockingAfterFirst(calls *atomic.Int32, release <-chan struct{}, fail bool) CollectFunc {
	return func(ctx context.Context) (any, error) {
		n := int(calls.Add(1))
		if n == 1 {
			return reading{N: n}, nil
		}
		if fail {
			return nil, errors.New("vm_stat: exit 1")
		}
		select {
		case <-release:
			return reading{N: n}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func staleFields(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOverrunAnswersWithStaleReading(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	s, clk := newSvc(nil, func(o *Options) {
		o.Memory = blockingAfterFirst(&calls, release, false)
		o.AnswerWithin = 20 * time.Millisecond
	})
	ctx := context.Background()
	if _, err := s.Get(ctx, MethodMemory, false); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	start := time.Now()
	v, err := s.Get(ctx, MethodMemory, false)
	if err != nil {
		t.Fatalf("err = %v, want the stale reading", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waited on the collection instead of answering at the deadline")
	}
	m := staleFields(t, v)
	if m["stale"] != true || m["n"] != float64(1) || !strings.Contains(m["stale_reason"].(string), "still running") {
		t.Fatalf("reply = %v, want reading 1 marked stale", m)
	}
	if m["sampled_at"] != time.Unix(1000, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("sampled_at = %v, want when reading 1 was taken", m["sampled_at"])
	}
	// The collection carried on; once it lands the next call is fresh.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, err = s.Get(ctx, MethodMemory, false)
		if err == nil {
			if r, ok := v.(reading); ok && r.N == 2 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never served the finished collection; last %v %v", v, err)
}

func TestFailedCollectionAnswersWithStaleReading(t *testing.T) {
	var calls atomic.Int32
	s, clk := newSvc(nil, func(o *Options) { o.Memory = blockingAfterFirst(&calls, nil, true) })
	ctx := context.Background()
	_, _ = s.Get(ctx, MethodMemory, false)
	clk.Advance(time.Minute)
	for _, refresh := range []bool{false, true} {
		v, err := s.Get(ctx, MethodMemory, refresh)
		if err != nil {
			t.Fatalf("refresh=%v err = %v, want the stale reading", refresh, err)
		}
		m := staleFields(t, v)
		if m["stale"] != true || m["stale_reason"] != "vm_stat: exit 1" {
			t.Fatalf("refresh=%v reply = %v", refresh, m)
		}
	}
}

func TestNoReadingYetStillErrors(t *testing.T) {
	s, _ := newSvc(nil, func(o *Options) {
		o.Memory = func(context.Context) (any, error) { return nil, errors.New("boom") }
	})
	if _, err := s.Get(context.Background(), MethodMemory, false); err == nil {
		t.Fatal("no cached reading, want the collection error")
	}
}

func TestStaleKeepsPayloadSampledAt(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	v, err := markStale(map[string]any{"sampled_at": "2025-12-31T00:00:00Z"}, at, "x")
	if err != nil {
		t.Fatal(err)
	}
	if m := staleFields(t, v); m["sampled_at"] != "2025-12-31T00:00:00Z" {
		t.Fatalf("sampled_at = %v, want the payload's own", m["sampled_at"])
	}
	if v, _ := markStale(7, at, "x"); v != 7 {
		t.Fatalf("non-object payload = %v, want unchanged", v)
	}
}
