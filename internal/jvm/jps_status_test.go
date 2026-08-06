package jvm

import (
	"context"
	"fmt"
	"testing"
)

func TestCollectAllStatusReportsJPSAvailability(t *testing.T) {
	// jps errors (not on PATH) => unavailable.
	if _, avail := CollectAllStatus(context.Background(), CollectOptions{
		CmdRunner: func(string, ...string) ([]byte, error) { return nil, fmt.Errorf("jps: not found") },
	}); avail {
		t.Fatal("jpsAvailable = true, want false when jps errors")
	}

	// jps runs but reports no JVMs => available, empty result.
	infos, avail := CollectAllStatus(context.Background(), CollectOptions{
		CmdRunner: func(string, ...string) ([]byte, error) { return []byte(""), nil },
	})
	if !avail {
		t.Fatal("jpsAvailable = false, want true when jps runs cleanly")
	}
	if len(infos) != 0 {
		t.Fatalf("infos = %d, want 0", len(infos))
	}
}
