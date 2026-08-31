package dbinspect

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// ConnectMySQL opens a single read-only database/sql connection through
// go-sql-driver. The DSN may be a mysql:// URL or the driver's native
// "user:pass@tcp(host:port)/db" form. The session is forced read-only —
// connecting fails if the server refuses — and statement/lock waits are
// bounded best-effort (MySQL and MariaDB spell those variables differently).
func ConnectMySQL(ctx context.Context, dsn string) (Conn, error) {
	driverDSN, err := mysqlDriverDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg, err := mysql.ParseDSN(driverDSN)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: parse dsn %s: %w", RedactDSN(dsn), err)
	}
	cfg.ParseTime = true

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("dbinspect: open %s: %w", RedactDSN(dsn), err)
	}
	// One connection total, so the session SETs below govern every query.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := setupMySQLSession(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return sqlConn{db}, nil
}

// setupMySQLSession forces the read-only guarantee and bounds waits.
func setupMySQLSession(ctx context.Context, db *sql.DB) error {
	// Non-negotiable: if the server cannot make the session read-only,
	// refuse to inspect it at all.
	if _, err := db.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
		return fmt.Errorf("dbinspect: force read-only session: %w", err)
	}
	// Best-effort bounds; variable names differ between MySQL and MariaDB.
	if _, err := db.ExecContext(ctx, "SET SESSION max_execution_time = 5000"); err != nil {
		_, _ = db.ExecContext(ctx, "SET SESSION max_statement_time = 5")
	}
	_, _ = db.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 2")
	return nil
}

// mysqlDriverDSN converts a mysql:// URL into go-sql-driver's native DSN.
// Input already in native form (or anything without a scheme) passes through
// for mysql.ParseDSN to validate.
func mysqlDriverDSN(dsn string) (string, error) {
	if !strings.Contains(dsn, "://") {
		return dsn, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("dbinspect: parse dsn %s: %w", RedactDSN(dsn), err)
	}
	if !strings.EqualFold(u.Scheme, "mysql") {
		return "", fmt.Errorf("dbinspect: unsupported scheme %q for mysql connector", u.Scheme)
	}
	var b strings.Builder
	if u.User != nil {
		b.WriteString(u.User.Username())
		if pw, ok := u.User.Password(); ok {
			b.WriteString(":" + pw)
		}
		b.WriteString("@")
	}
	host := u.Host
	if u.Port() == "" {
		host += ":3306"
	}
	b.WriteString("tcp(" + host + ")")
	b.WriteString("/" + strings.TrimPrefix(u.Path, "/"))
	if u.RawQuery != "" {
		b.WriteString("?" + u.RawQuery)
	}
	return b.String(), nil
}

type sqlConn struct{ db *sql.DB }

func (c sqlConn) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return newSQLRows(rows), nil
}

func (c sqlConn) Close(context.Context) error { return c.db.Close() }

// sqlRows adapts *sql.Rows to the Rows interface. database/sql has no
// Values(), so it is synthesized by scanning into interface pointers;
// text-typed []byte values become strings, binary ones stay raw for
// displayValue's hex form.
type sqlRows struct {
	rows    *sql.Rows
	cols    []string
	rawCols map[int]bool
}

func newSQLRows(rows *sql.Rows) *sqlRows {
	r := &sqlRows{rows: rows, rawCols: map[int]bool{}}
	r.cols, _ = rows.Columns()
	if types, err := rows.ColumnTypes(); err == nil {
		for i, t := range types {
			name := strings.ToUpper(t.DatabaseTypeName())
			if strings.Contains(name, "BLOB") || strings.Contains(name, "BINARY") ||
				strings.Contains(name, "BIT") {
				r.rawCols[i] = true
			}
		}
	}
	return r
}

func (r *sqlRows) Next() bool             { return r.rows.Next() }
func (r *sqlRows) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r *sqlRows) Columns() []string      { return r.cols }
func (r *sqlRows) Err() error             { return r.rows.Err() }
func (r *sqlRows) Close()                 { _ = r.rows.Close() }

func (r *sqlRows) Values() ([]any, error) {
	values := make([]any, len(r.cols))
	ptrs := make([]any, len(r.cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := r.rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	for i, v := range values {
		if b, ok := v.([]byte); ok && !r.rawCols[i] {
			values[i] = string(b)
		}
	}
	return values, nil
}
