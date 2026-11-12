package store

import (
	"context"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// A panic inside the open transaction must not leave the single pooled
// connection inside it, where later writes would silently join it.
func TestSaveJVMSamples_PanicRollsBackAndPoolStaysUsable(t *testing.T) {
	db := openTestDB(t)
	testHookAfterBegin = func() { panic("boom") }
	func() {
		defer func() { _ = recover() }()
		_ = db.SaveJVMSamples(context.Background(), []snapshot.JVMSample{{PID: 1, At: time.Unix(1, 0)}})
	}()
	testHookAfterBegin = nil

	// BEGIN fails with "within a transaction" if the conn is still inside one.
	if _, err := db.db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("conn returned to pool inside a transaction: %v", err)
	}
	if _, err := db.db.Exec("ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := db.SaveJVMSamples(context.Background(), []snapshot.JVMSample{{PID: 2, At: time.Unix(2, 0), OldGenPct: 1}}); err != nil {
		t.Fatalf("SaveJVMSamples after panic: %v", err)
	}
}

// discardConn must close the connection rather than pool it (the branch taken
// when ROLLBACK or the busy_timeout restore fails).
func TestDiscardConnDropsConnectionFromPool(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	conn, err := db.db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	discardConn(conn)
	conn.Close()
	if n := db.db.Stats().OpenConnections; n != 0 {
		t.Fatalf("open connections after discard = %d, want 0", n)
	}
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{{PID: 3, At: time.Unix(3, 0)}}); err != nil {
		t.Fatalf("SaveJVMSamples on fresh conn: %v", err)
	}
	// The replacement connection must carry the same per-connection pragmas.
	var fk, sync int
	if err := db.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys after discard = %d, %v; want 1", fk, err)
	}
	if err := db.db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil || sync != 1 {
		t.Fatalf("synchronous after discard = %d, %v; want 1 (NORMAL)", sync, err)
	}
}
