package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"
)

func fixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tcc.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE access (service TEXT, client TEXT, auth_value INTEGER)`,
		`INSERT INTO access VALUES ('kTCCServiceCamera', 'org.example.sample', 2)`,
		`INSERT INTO access VALUES ('kTCCServiceMicrophone', 'org.example.sample', 2)`,
		`INSERT INTO access VALUES ('kTCCServiceCamera', 'org.other.app', 0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func runShim(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func TestSingleColumnRows(t *testing.T) {
	// The exact query shape internal/detect issues for TCC reads.
	stdout, _, code := runShim(t, fixtureDB(t),
		"SELECT service FROM access WHERE client = 'org.example.sample' AND auth_value >= 2;")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := "kTCCServiceCamera\nkTCCServiceMicrophone\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestMultiColumnRowsUsePipes(t *testing.T) {
	stdout, _, code := runShim(t, fixtureDB(t),
		"SELECT service, auth_value FROM access WHERE client = 'org.other.app'")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "kTCCServiceCamera|0\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestMissingDatabaseFails(t *testing.T) {
	_, stderr, code := runShim(t, filepath.Join(t.TempDir(), "nope.db"), "SELECT 1")
	if code != 1 || stderr == "" {
		t.Errorf("exit = %d, stderr = %q, want failure", code, stderr)
	}
}

func TestBadQueryFails(t *testing.T) {
	_, _, code := runShim(t, fixtureDB(t), "SELECT nope FROM missing")
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}
