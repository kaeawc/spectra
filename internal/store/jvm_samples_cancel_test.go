package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// In rollback-journal mode COMMIT needs an exclusive lock and waits on the busy
// handler while a reader holds a shared lock. database/sql's Tx.Commit runs the
// driver commit with context.Background, so it used to outlive ctx; the
// persistence path must instead abort promptly once ctx is cancelled.
func TestSaveJVMSamples_CommitBlockedOnReaderHonorsCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spectra.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, p := range []string{"PRAGMA journal_mode=DELETE", "PRAGMA busy_timeout=60000"} {
		if _, err := db.db.Exec(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}

	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	rtx, err := reader.Begin()
	if err != nil {
		t.Fatalf("reader begin: %v", err)
	}
	t.Cleanup(func() { rtx.Rollback() })
	var n int
	if err := rtx.QueryRow(`SELECT count(*) FROM jvm_samples`).Scan(&n); err != nil {
		t.Fatalf("reader select: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- db.SaveJVMSamples(ctx, []snapshot.JVMSample{{PID: 1, At: time.Unix(1, 0), OldGenPct: 1}})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SaveJVMSamples succeeded despite a blocked commit")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SaveJVMSamples still blocked 5s after ctx expired")
	}

	// The aborted commit must roll back: nothing persisted, connection usable.
	rtx.Rollback()
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM jvm_samples`).Scan(&count); err != nil {
		t.Fatalf("count after abort: %v", err)
	}
	if count != 0 {
		t.Fatalf("rows after aborted commit = %d, want 0", count)
	}
}

// A commit that is merely delayed by a reader still succeeds once the reader
// finishes, and the connection's busy_timeout is left as configured.
func TestSaveJVMSamples_CommitWaitsForReaderThenSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spectra.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, p := range []string{"PRAGMA journal_mode=DELETE", "PRAGMA busy_timeout=60000"} {
		if _, err := db.db.Exec(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	rtx, err := reader.Begin()
	if err != nil {
		t.Fatalf("reader begin: %v", err)
	}
	var n int
	if err := rtx.QueryRow(`SELECT count(*) FROM jvm_samples`).Scan(&n); err != nil {
		t.Fatalf("reader select: %v", err)
	}
	time.AfterFunc(200*time.Millisecond, func() { rtx.Rollback() })

	if err := db.SaveJVMSamples(context.Background(), []snapshot.JVMSample{{PID: 1, At: time.Unix(1, 0), OldGenPct: 1}}); err != nil {
		t.Fatalf("SaveJVMSamples: %v", err)
	}
	var got int
	if err := db.db.QueryRow(`PRAGMA busy_timeout`).Scan(&got); err != nil || got != 60000 {
		t.Fatalf("busy_timeout = %d, %v; want 60000 restored", got, err)
	}
}
