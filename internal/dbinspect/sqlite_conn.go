package dbinspect

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// ConnectSQLite opens a single read-only connection to an SQLite database
// file through modernc.org/sqlite (pure Go, already spectra's own storage
// driver). The DSN may be sqlite://<path>, a file: URI, or a bare path.
// The connection is opened mode=ro with query_only forced, so nothing can
// write, and a short busy timeout so inspection fails fast instead of
// holding locks against the owning application.
func ConnectSQLite(ctx context.Context, dsn string) (Conn, error) {
	driverDSN, err := sqliteDriverDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", driverDSN)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: open %s: %w", RedactDSN(dsn), err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// Opening is lazy; ping now so a missing or unreadable file fails here
	// with a clear error instead of on the first catalog query.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dbinspect: open sqlite database %s: %w", RedactDSN(dsn), err)
	}
	return sqlConn{db}, nil
}

// sqliteReadOnlyParams force the read-only session. mode=ro refuses writes
// at the file layer; query_only refuses them at the statement layer; the
// busy timeout bounds lock waits against the owning app.
const sqliteReadOnlyParams = "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"

// sqliteDriverDSN converts sqlite:// URLs, file: URIs, and bare paths into
// a modernc.org/sqlite DSN with the read-only parameters forced on.
func sqliteDriverDSN(dsn string) (string, error) {
	path := dsn
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("dbinspect: parse dsn %s: %w", RedactDSN(dsn), err)
		}
		// sqlite:///abs/path parses as empty host + /abs/path; a relative
		// sqlite://some.db parses as host "some.db".
		path = u.Path
		if path == "" {
			path = u.Host
		} else if u.Host != "" {
			path = u.Host + path
		}
	case strings.HasPrefix(dsn, "file:"):
		path = strings.TrimPrefix(dsn, "file:")
		// Drop any caller query params: the forced read-only set below is
		// not negotiable, and mode=rw must not survive.
		if idx := strings.IndexByte(path, '?'); idx >= 0 {
			path = path[:idx]
		}
	}
	if path == "" {
		return "", fmt.Errorf("dbinspect: sqlite dsn %s has no file path", RedactDSN(dsn))
	}
	return "file:" + path + "?" + sqliteReadOnlyParams, nil
}
