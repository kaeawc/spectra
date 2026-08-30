package dbcheck

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func makeDB(t *testing.T, path string, setup func(*sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if setup != nil {
		setup(db)
	}
}

func TestIsSQLite(t *testing.T) {
	if !IsSQLite([]byte(sqliteMagic + "rest")) {
		t.Error("valid header not recognized")
	}
	if IsSQLite([]byte("not a database")) {
		t.Error("non-sqlite header matched")
	}
	if IsSQLite([]byte("short")) {
		t.Error("too-short header matched")
	}
}

func TestFragmentationPct(t *testing.T) {
	cases := []struct {
		free, pages int64
		want        float64
	}{
		{0, 100, 0},
		{25, 100, 25},
		{1, 3, 33.3},
		{5, 0, 0},
	}
	for _, c := range cases {
		if got := fragmentationPct(c.free, c.pages); got != c.want {
			t.Errorf("fragmentationPct(%d,%d)=%v want %v", c.free, c.pages, got, c.want)
		}
	}
}

func TestCheckHealthyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "good.db")
	makeDB(t, path, func(db *sql.DB) {
		if _, err := db.Exec("INSERT INTO t(v) VALUES (zeroblob(64))"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	})

	rep := Check([]string{path}, DefaultDeps())
	if rep.Scanned != 1 || rep.Problems != 0 {
		t.Fatalf("scanned=%d problems=%d, want 1/0", rep.Scanned, rep.Problems)
	}
	db := rep.Databases[0]
	if !db.IntegrityOK {
		t.Errorf("healthy DB should pass integrity: %v", db.IntegrityErrors)
	}
	if db.PageSize == 0 || db.PageCount == 0 {
		t.Errorf("expected page geometry, got size=%d count=%d", db.PageSize, db.PageCount)
	}
	if db.SizeBytes == 0 {
		t.Error("expected non-zero file size")
	}
}

func TestCheckReportsFreePages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frag.db")
	makeDB(t, path, func(db *sql.DB) {
		for i := 0; i < 2000; i++ {
			if _, err := db.Exec("INSERT INTO t(v) VALUES (zeroblob(256))"); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
		if _, err := db.Exec("DELETE FROM t"); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})

	rep := Check([]string{path}, DefaultDeps())
	db := rep.Databases[0]
	if db.FreelistCount == 0 || db.FragmentationPct == 0 {
		t.Errorf("expected free pages after bulk delete, got free=%d frag=%.1f", db.FreelistCount, db.FragmentationPct)
	}
	if !db.IntegrityOK {
		t.Errorf("fragmented DB is still structurally valid: %v", db.IntegrityErrors)
	}
}

func TestCheckOpenErrorIsRecorded(t *testing.T) {
	deps := Deps{
		Open: func(string) (*sql.DB, error) { return nil, errors.New("boom") },
		Stat: func(string) (int64, error) { return 0, errors.New("nope") },
	}
	rep := Check([]string{"/whatever.db"}, deps)
	if rep.Problems != 1 || rep.Databases[0].Error == "" {
		t.Fatalf("expected recorded open error as a problem, got %+v", rep.Databases[0])
	}
}

func TestCheckFlagsWALBloat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	makeDB(t, path, nil)
	deps := DefaultDeps()
	real := deps.Stat
	deps.Stat = func(p string) (int64, error) {
		if p == path+"-wal" {
			return walBloatThreshold + 1, nil
		}
		return real(p)
	}
	rep := Check([]string{path}, deps)
	if rep.Problems != 1 {
		t.Fatalf("expected WAL bloat to count as a problem, got %d", rep.Problems)
	}
	if rep.Databases[0].WALBytes <= walBloatThreshold {
		t.Errorf("WALBytes not captured: %d", rep.Databases[0].WALBytes)
	}
}

func TestExactBoundariesAreNotProblems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boundary.db")
	makeDB(t, path, nil)
	deps := DefaultDeps()
	real := deps.Stat
	deps.Stat = func(p string) (int64, error) {
		if p == path+"-wal" {
			return walBloatThreshold, nil // exactly at the threshold, not over
		}
		return real(p)
	}
	rep := Check([]string{path}, deps)
	if rep.Problems != 0 {
		t.Errorf("WAL exactly at threshold must not be a problem, got %d", rep.Problems)
	}
	if isBloated(DB{FragmentationPct: fragmentationProblemPct}) {
		t.Error("fragmentation exactly at threshold must not be a problem")
	}
}

func TestCheckNonDatabaseFileIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("this is not a database, just some text bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rep := Check([]string{path}, DefaultDeps())
	db := rep.Databases[0]
	if db.Error == "" {
		t.Errorf("a non-database file must be reported as an error, not integrity: %+v", db)
	}
	if len(db.IntegrityErrors) != 0 {
		t.Errorf("inspection failure must not be recorded as integrity errors: %v", db.IntegrityErrors)
	}
	if rep.Problems != 1 {
		t.Errorf("problems = %d, want 1", rep.Problems)
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	makeDB(t, dbPath, nil)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	readHeader := func(p string) ([]byte, error) {
		b, err := os.ReadFile(p) // #nosec G304 -- test reads temp files
		if err != nil {
			return nil, err
		}
		if len(b) > 16 {
			b = b[:16]
		}
		return b, nil
	}
	found, err := Discover(dir, readHeader)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(found) != 1 || found[0] != dbPath {
		t.Errorf("discover = %v, want [%s]", found, dbPath)
	}
}
