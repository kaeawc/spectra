package store

import (
	"context"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// If ctx is cancelled while COMMIT is running and SQLite commits anyway, the
// driver would report ctx.Err() for a transaction that persisted. The save must
// report the real outcome: success here.
func TestSaveJVMSamples_CancelDuringSuccessfulCommitReportsSuccess(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := db.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	err = conn.Raw(func(dc any) error {
		dc.(interface{ RegisterCommitHook(sqlite.CommitHookFn) }).RegisterCommitHook(func() int32 {
			cancel()
			time.Sleep(100 * time.Millisecond) // let the driver observe the cancel
			return 0                           // allow the commit
		})
		return nil
	})
	if err != nil {
		t.Fatalf("register commit hook: %v", err)
	}
	conn.Close()

	err = db.SaveJVMSamples(ctx, []snapshot.JVMSample{{PID: 9, At: time.Unix(9, 0), OldGenPct: 1}})

	var n int
	if qerr := db.db.QueryRow(`SELECT count(*) FROM jvm_samples WHERE pid=9`).Scan(&n); qerr != nil {
		t.Fatalf("count: %v", qerr)
	}
	if n != 1 {
		t.Fatalf("rows persisted = %d, want 1 (commit hook allowed it)", n)
	}
	if err != nil {
		t.Fatalf("SaveJVMSamples = %v, but the commit persisted", err)
	}
}
