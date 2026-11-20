package hostwatch

import (
	"errors"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/logger"
)

type countLog struct {
	logger.Logger
	warns int
}

func (c *countLog) Warn(string, ...any) { c.warns++ }

func TestBootTimeRetriesUntilReadable(t *testing.T) {
	calls := 0
	log := &countLog{Logger: logger.Discard()}
	bt := &bootTimer{log: log, read: func() ([]byte, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("transient")
		}
		return []byte("cpu 1\nbtime 1700000000\n"), nil
	}}
	for range 2 {
		if !bt.get().IsZero() {
			t.Fatal("expected zero while reads fail")
		}
	}
	if log.warns != 1 {
		t.Fatalf("warns = %d, want 1", log.warns)
	}
	if got := bt.get(); !got.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("btime = %v", got)
	}
	bt.get()
	if calls != 3 {
		t.Fatalf("reads after success: %d", calls)
	}
}
