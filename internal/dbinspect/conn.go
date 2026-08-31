package dbinspect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// readOnlyParams are forced onto every session so inspection cannot write to
// or stall the server: transactions default to read-only, statement and lock
// waits are bounded, and the session is identifiable in pg_stat_activity.
var readOnlyParams = map[string]string{
	"default_transaction_read_only":       "on",
	"application_name":                    "spectra-dbinspect",
	"statement_timeout":                   "5000",
	"lock_timeout":                        "2000",
	"idle_in_transaction_session_timeout": "5000",
}

// ConnectPostgres opens a single read-only pgx connection. The DSN may be a
// URL or libpq keyword form; unset fields fall back to PG* env vars.
func ConnectPostgres(ctx context.Context, dsn string) (Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: parse dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	for k, v := range readOnlyParams {
		cfg.RuntimeParams[k] = v
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: connect %s: %w", RedactDSN(dsn), err)
	}
	return pgxConn{conn}, nil
}

type pgxConn struct{ conn *pgx.Conn }

func (c pgxConn) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgxRows{rows}, nil
}

func (c pgxConn) Close(ctx context.Context) error { return c.conn.Close(ctx) }

type pgxRows struct{ rows pgx.Rows }

func (r pgxRows) Next() bool             { return r.rows.Next() }
func (r pgxRows) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r pgxRows) Values() ([]any, error) { return r.rows.Values() }
func (r pgxRows) Err() error             { return r.rows.Err() }
func (r pgxRows) Close()                 { r.rows.Close() }

func (r pgxRows) Columns() []string {
	fds := r.rows.FieldDescriptions()
	cols := make([]string, len(fds))
	for i, fd := range fds {
		cols[i] = fd.Name
	}
	return cols
}
