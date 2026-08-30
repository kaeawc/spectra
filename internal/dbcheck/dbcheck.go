// Package dbcheck inspects the health of embedded SQLite databases: physical
// integrity, un-checkpointed WAL bloat, and free-page fragmentation. It opens
// databases read-only and never writes to the file under inspection.
package dbcheck

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite" // register the pure-Go "sqlite" driver
)

// sqliteMagic is the 16-byte header prefix of every SQLite 3 database file.
const sqliteMagic = "SQLite format 3\x00"

// maxIntegrityErrors bounds how many quick_check rows are retained per database.
const maxIntegrityErrors = 10

// Report is the result of checking a set of databases.
type Report struct {
	Databases []DB `json:"databases"`
	Scanned   int  `json:"scanned"`
	Problems  int  `json:"problems"`
}

// DB is the health of a single SQLite database.
type DB struct {
	Path             string   `json:"path"`
	SizeBytes        int64    `json:"size_bytes"`
	PageSize         int      `json:"page_size,omitempty"`
	PageCount        int64    `json:"page_count,omitempty"`
	FreelistCount    int64    `json:"freelist_count,omitempty"`
	FragmentationPct float64  `json:"fragmentation_pct,omitempty"`
	JournalMode      string   `json:"journal_mode,omitempty"`
	WALBytes         int64    `json:"wal_bytes,omitempty"`
	IntegrityOK      bool     `json:"integrity_ok"`
	IntegrityErrors  []string `json:"integrity_errors,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// walBloatThreshold marks a WAL sidecar that has grown large enough to warrant
// a checkpoint (32 MiB).
const walBloatThreshold = 32 << 20

// fragmentationProblemPct marks a database whose freelist is a large share of
// its pages.
const fragmentationProblemPct = 25.0

// IsSQLite reports whether b begins with the SQLite 3 file header.
func IsSQLite(b []byte) bool {
	return len(b) >= len(sqliteMagic) && string(b[:len(sqliteMagic)]) == sqliteMagic
}

// Opener opens a database file for read-only inspection.
type Opener func(path string) (*sql.DB, error)

// StatSizer returns the size in bytes of a file, or an error if it is absent.
type StatSizer func(path string) (int64, error)

// Deps injects the file and database access dbcheck needs, so the analysis can
// be exercised against temporary databases in tests.
type Deps struct {
	Open Opener
	Stat StatSizer
}

// DefaultDeps opens databases read-only via the vendored sqlite driver and
// stats files from the real filesystem.
func DefaultDeps() Deps {
	return Deps{
		Open: func(path string) (*sql.DB, error) {
			return sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
		},
		Stat: func(path string) (int64, error) {
			fi, err := os.Stat(path)
			if err != nil {
				return 0, err
			}
			return fi.Size(), nil
		},
	}
}

// Check inspects each database path and returns their combined health. A path
// that cannot be opened is recorded with its error rather than aborting.
func Check(paths []string, deps Deps) Report {
	rep := Report{}
	for _, p := range paths {
		db := inspect(p, deps)
		if db.Error != "" || !db.IntegrityOK || isBloated(db) {
			rep.Problems++
		}
		rep.Databases = append(rep.Databases, db)
	}
	rep.Scanned = len(rep.Databases)
	return rep
}

func isBloated(db DB) bool {
	return db.WALBytes >= walBloatThreshold || db.FragmentationPct >= fragmentationProblemPct
}

func inspect(path string, deps Deps) DB {
	out := DB{Path: path}
	if size, err := deps.Stat(path); err == nil {
		out.SizeBytes = size
	}
	if size, err := deps.Stat(path + "-wal"); err == nil {
		out.WALBytes = size
	}

	conn, err := deps.Open(path)
	if err != nil {
		out.Error = fmt.Sprintf("open: %v", err)
		return out
	}
	defer conn.Close()

	out.PageSize = intPragma(conn, "page_size")
	out.PageCount = int64Pragma(conn, "page_count")
	out.FreelistCount = int64Pragma(conn, "freelist_count")
	if out.PageCount > 0 {
		out.FragmentationPct = fragmentationPct(out.FreelistCount, out.PageCount)
	}
	out.JournalMode = stringPragma(conn, "journal_mode")
	out.IntegrityOK, out.IntegrityErrors = quickCheck(conn)
	return out
}

// fragmentationPct is the freelist share of the total page count, rounded to
// one decimal place.
func fragmentationPct(freelist, pageCount int64) float64 {
	if pageCount <= 0 {
		return 0
	}
	pct := float64(freelist) / float64(pageCount) * 100
	return float64(int64(pct*10+0.5)) / 10
}

func quickCheck(conn *sql.DB) (bool, []string) {
	rows, err := conn.Query("PRAGMA quick_check")
	if err != nil {
		return false, []string{fmt.Sprintf("quick_check: %v", err)}
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return false, []string{fmt.Sprintf("scan: %v", err)}
		}
		if s == "ok" {
			continue
		}
		if len(msgs) < maxIntegrityErrors {
			msgs = append(msgs, s)
		}
	}
	if err := rows.Err(); err != nil {
		return false, []string{fmt.Sprintf("quick_check: %v", err)}
	}
	sort.Strings(msgs)
	return len(msgs) == 0, msgs
}

func stringPragma(conn *sql.DB, name string) string {
	var v string
	if err := conn.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		return ""
	}
	return v
}

func intPragma(conn *sql.DB, name string) int {
	var v int
	if err := conn.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		return 0
	}
	return v
}

func int64Pragma(conn *sql.DB, name string) int64 {
	var v int64
	if err := conn.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		return 0
	}
	return v
}

// Discover walks root and returns the paths of files whose header identifies
// them as SQLite databases. readHeader reads up to n leading bytes of a file.
func Discover(root string, readHeader func(path string) ([]byte, error)) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		// Tolerate unreadable entries and only collect regular files whose
		// header identifies them as SQLite; always return nil to keep scanning.
		if walkErr == nil && !d.IsDir() {
			if hdr, readErr := readHeader(path); readErr == nil && IsSQLite(hdr) {
				found = append(found, path)
			}
		}
		return nil
	})
	sort.Strings(found)
	return found, err
}
