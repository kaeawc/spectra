// Package dbinspect connects to databases an application under debug talks
// to and reads schema, relationships, and health statistics without
// disturbing the running workload. Sessions are read-only by construction:
// each connector forces a read-only session with statement and lock
// timeouts, and only ever issues catalog or SELECT queries.
//
// PostgreSQL (via pgx) and MySQL/MariaDB (via go-sql-driver) are supported;
// the engine is inferred from the DSN. Discovery also recognizes SQLite so
// it can slot in later.
package dbinspect

import (
	"context"
	"time"
)

// Engine identifies a database engine spectra can inspect or discover.
type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
	EngineSQLite   Engine = "sqlite" // discovery only for now
)

// ConnectFn opens a read-only connection for a DSN. Injected so tests can
// fake the server; nil selects the built-in connector for the engine.
type ConnectFn func(ctx context.Context, dsn string) (Conn, error)

// Conn is the subset of a database connection the inspector needs.
type Conn interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	Close(ctx context.Context) error
}

// Rows is the subset of a result cursor the inspector needs. Each driver's
// adapter supplies whatever its native cursor lacks (Columns for pgx,
// Values for database/sql).
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Values() ([]any, error)
	Columns() []string
	Err() error
	Close()
}

// Options configures an inspection. The zero value infers the engine from
// the DSN and uses that engine's built-in connector with a 10s overall
// timeout.
type Options struct {
	// Connect opens the connection. Nil means the engine's built-in
	// connector (ConnectPostgres or ConnectMySQL).
	Connect ConnectFn
	// Engine forces the engine instead of inferring it from the DSN.
	Engine Engine
	// Timeout bounds the whole operation (connect + queries). Zero means 10s.
	Timeout time.Duration
}

const defaultTimeout = 10 * time.Second

func withDefaults(o Options) Options {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	return o
}

// Overview is a one-page summary of the server and database.
type Overview struct {
	Engine          Engine          `json:"engine"`
	ServerVersion   string          `json:"server_version"`
	Database        string          `json:"database"`
	User            string          `json:"user"`
	ReadOnlySession bool            `json:"read_only_session"`
	SizeBytes       int64           `json:"size_bytes"`
	Connections     int             `json:"connections"`
	MaxConnections  int             `json:"max_connections"`
	Schemas         []SchemaSummary `json:"schemas,omitempty"`
}

// SchemaSummary is one user schema and how many tables it holds.
type SchemaSummary struct {
	Name       string `json:"name"`
	TableCount int    `json:"table_count"`
}

// Column is one column of a table or view.
type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	Default    string `json:"default,omitempty"`
	PrimaryKey bool   `json:"primary_key,omitempty"`
}

// Index is one index on a table.
type Index struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
}

// Table is one relation (table, view, materialized view, foreign table).
type Table struct {
	Schema        string   `json:"schema"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	EstimatedRows int64    `json:"estimated_rows"`
	TotalBytes    int64    `json:"total_bytes"`
	Columns       []Column `json:"columns,omitempty"`
	Indexes       []Index  `json:"indexes,omitempty"`
}

// SchemaReport is the full structural picture of one database.
type SchemaReport struct {
	Engine Engine  `json:"engine"`
	Tables []Table `json:"tables"`
}

// ForeignKey is one foreign-key relationship between two tables.
type ForeignKey struct {
	Name        string   `json:"name"`
	FromSchema  string   `json:"from_schema"`
	FromTable   string   `json:"from_table"`
	FromColumns []string `json:"from_columns"`
	ToSchema    string   `json:"to_schema"`
	ToTable     string   `json:"to_table"`
	ToColumns   []string `json:"to_columns"`
	OnDelete    string   `json:"on_delete,omitempty"`
	OnUpdate    string   `json:"on_update,omitempty"`
}

// RelationsReport lists every foreign-key relationship in scope.
type RelationsReport struct {
	Engine      Engine       `json:"engine"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
}

// TableStats is planner and vacuum health for one table, from
// pg_stat_user_tables. Row counts are estimates — no COUNT(*) is issued.
type TableStats struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	SeqScans    int64  `json:"seq_scans"`
	IdxScans    int64  `json:"idx_scans"`
	LiveRows    int64  `json:"live_rows"`
	DeadRows    int64  `json:"dead_rows"`
	TotalBytes  int64  `json:"total_bytes"`
	LastVacuum  string `json:"last_vacuum,omitempty"`
	LastAnalyze string `json:"last_analyze,omitempty"`
}

// StatsReport lists per-table health, largest tables first.
type StatsReport struct {
	Engine Engine       `json:"engine"`
	Tables []TableStats `json:"tables"`
}

// SampleReport is a bounded row sample from one table. Row data may contain
// customer PII — callers gate this behind explicit confirmation.
type SampleReport struct {
	Engine  Engine   `json:"engine"`
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Limit   int      `json:"limit"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}
