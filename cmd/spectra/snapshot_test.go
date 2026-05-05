package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/store"
)

func tempDB(t *testing.T) *store.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestResolveSnapshotNotFoundReturnsError(t *testing.T) {
	db := tempDB(t)
	_, err := resolveSnapshot(context.Background(), db, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent snapshot ID, got nil")
	}
}
