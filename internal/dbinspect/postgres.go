package dbinspect

import (
	"context"
	"fmt"
	"strings"
)

// All catalog queries below read only pg_catalog and use bind parameters for
// every caller-supplied filter. Identifiers interpolated into SQL (sampling)
// are resolved against pg_class first and then quote-escaped.

// userSchemaFilter excludes system schemas; pg_%-prefixed schema names are
// reserved by postgres, so the LIKE cannot hide user tables.
const userSchemaFilter = `n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'`

// withConn opens a bounded read-only session, runs fn, and closes it.
func withConn(ctx context.Context, dsn string, o Options, fn func(ctx context.Context, conn Conn) error) error {
	o = withDefaults(o)
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	conn, err := o.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	return fn(ctx, conn)
}

const overviewQuery = `
SELECT current_setting('server_version'),
       current_database()::text,
       current_user::text,
       current_setting('transaction_read_only') = 'on',
       pg_database_size(current_database()),
       (SELECT count(*) FROM pg_stat_activity),
       current_setting('max_connections')::int`

const schemaSummaryQuery = `
SELECT n.nspname, count(c.oid)::int
FROM pg_namespace n
LEFT JOIN pg_class c ON c.relnamespace = n.oid AND c.relkind IN ('r','p')
WHERE ` + userSchemaFilter + `
GROUP BY n.nspname
ORDER BY n.nspname`

// CollectOverview returns server, database, and session facts plus a count
// of tables per user schema.
func CollectOverview(ctx context.Context, dsn string, o Options) (*Overview, error) {
	out := &Overview{Engine: EnginePostgres}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		if err := scanOverviewRow(ctx, conn, out); err != nil {
			return err
		}
		schemas, err := collectSchemaSummaries(ctx, conn)
		if err != nil {
			return err
		}
		out.Schemas = schemas
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanOverviewRow(ctx context.Context, conn Conn, out *Overview) error {
	rows, err := conn.Query(ctx, overviewQuery)
	if err != nil {
		return fmt.Errorf("dbinspect: overview query: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return fmt.Errorf("dbinspect: overview query returned no rows: %w", rows.Err())
	}
	var connections int64
	if err := rows.Scan(&out.ServerVersion, &out.Database, &out.User, &out.ReadOnlySession,
		&out.SizeBytes, &connections, &out.MaxConnections); err != nil {
		return fmt.Errorf("dbinspect: scan overview: %w", err)
	}
	out.Connections = int(connections)
	return rows.Err()
}

func collectSchemaSummaries(ctx context.Context, conn Conn) ([]SchemaSummary, error) {
	rows, err := conn.Query(ctx, schemaSummaryQuery)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: schema summary query: %w", err)
	}
	defer rows.Close()
	var out []SchemaSummary
	for rows.Next() {
		var s SchemaSummary
		if err := rows.Scan(&s.Name, &s.TableCount); err != nil {
			return nil, fmt.Errorf("dbinspect: scan schema summary: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const tablesQuery = `
SELECT n.nspname, c.relname,
       CASE c.relkind
            WHEN 'r' THEN 'table'
            WHEN 'p' THEN 'partitioned table'
            WHEN 'v' THEN 'view'
            WHEN 'm' THEN 'materialized view'
            WHEN 'f' THEN 'foreign table'
       END,
       GREATEST(c.reltuples, 0)::bigint,
       CASE WHEN c.relkind IN ('r','m') THEN pg_total_relation_size(c.oid) ELSE 0 END
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r','p','v','m','f')
  AND ` + userSchemaFilter + `
  AND ($1 = '' OR n.nspname = $1)
ORDER BY n.nspname, c.relname`

const columnsQuery = `
SELECT n.nspname, c.relname, a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod),
       NOT a.attnotnull,
       COALESCE(pg_get_expr(ad.adbin, ad.adrelid), ''),
       COALESCE((SELECT TRUE FROM pg_index i
                 WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)), FALSE)
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped
  AND c.relkind IN ('r','p','v','m','f')
  AND ` + userSchemaFilter + `
  AND ($1 = '' OR n.nspname = $1)
ORDER BY n.nspname, c.relname, a.attnum`

const indexesQuery = `
SELECT n.nspname, c.relname, ic.relname,
       pg_get_indexdef(i.indexrelid),
       i.indisunique, i.indisprimary
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE ` + userSchemaFilter + `
  AND ($1 = '' OR n.nspname = $1)
ORDER BY n.nspname, c.relname, ic.relname`

// CollectSchema returns every user relation with its columns and indexes.
// Pass schema to limit scope to one schema, or "" for all user schemas.
func CollectSchema(ctx context.Context, dsn, schema string, o Options) (*SchemaReport, error) {
	out := &SchemaReport{Engine: EnginePostgres}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		tables, err := collectTables(ctx, conn, schema)
		if err != nil {
			return err
		}
		if err := attachColumns(ctx, conn, schema, tables); err != nil {
			return err
		}
		if err := attachIndexes(ctx, conn, schema, tables); err != nil {
			return err
		}
		for _, t := range tables.ordered {
			out.Tables = append(out.Tables, *t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// tableSet indexes tables by schema.name while preserving catalog order.
type tableSet struct {
	byName  map[string]*Table
	ordered []*Table
}

func collectTables(ctx context.Context, conn Conn, schema string) (*tableSet, error) {
	rows, err := conn.Query(ctx, tablesQuery, schema)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: tables query: %w", err)
	}
	defer rows.Close()
	set := &tableSet{byName: map[string]*Table{}}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.Kind, &t.EstimatedRows, &t.TotalBytes); err != nil {
			return nil, fmt.Errorf("dbinspect: scan table: %w", err)
		}
		set.byName[t.Schema+"."+t.Name] = &t
		set.ordered = append(set.ordered, &t)
	}
	return set, rows.Err()
}

func attachColumns(ctx context.Context, conn Conn, schema string, set *tableSet) error {
	rows, err := conn.Query(ctx, columnsQuery, schema)
	if err != nil {
		return fmt.Errorf("dbinspect: columns query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tblSchema, tblName string
		var col Column
		if err := rows.Scan(&tblSchema, &tblName, &col.Name, &col.Type,
			&col.Nullable, &col.Default, &col.PrimaryKey); err != nil {
			return fmt.Errorf("dbinspect: scan column: %w", err)
		}
		if t := set.byName[tblSchema+"."+tblName]; t != nil {
			t.Columns = append(t.Columns, col)
		}
	}
	return rows.Err()
}

func attachIndexes(ctx context.Context, conn Conn, schema string, set *tableSet) error {
	rows, err := conn.Query(ctx, indexesQuery, schema)
	if err != nil {
		return fmt.Errorf("dbinspect: indexes query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tblSchema, tblName string
		var idx Index
		if err := rows.Scan(&tblSchema, &tblName, &idx.Name, &idx.Definition,
			&idx.Unique, &idx.Primary); err != nil {
			return fmt.Errorf("dbinspect: scan index: %w", err)
		}
		if t := set.byName[tblSchema+"."+tblName]; t != nil {
			t.Indexes = append(t.Indexes, idx)
		}
	}
	return rows.Err()
}

const foreignKeysQuery = `
SELECT con.conname,
       fn.nspname, fc.relname,
       (SELECT array_agg(a.attname ORDER BY x.ord)
          FROM unnest(con.conkey) WITH ORDINALITY AS x(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = x.attnum),
       tn.nspname, tc.relname,
       (SELECT array_agg(a.attname ORDER BY x.ord)
          FROM unnest(con.confkey) WITH ORDINALITY AS x(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = x.attnum),
       con.confdeltype::text, con.confupdtype::text
FROM pg_constraint con
JOIN pg_class fc ON fc.oid = con.conrelid
JOIN pg_namespace fn ON fn.oid = fc.relnamespace
JOIN pg_class tc ON tc.oid = con.confrelid
JOIN pg_namespace tn ON tn.oid = tc.relnamespace
WHERE con.contype = 'f'
  AND ($1 = '' OR fn.nspname = $1)
ORDER BY fn.nspname, fc.relname, con.conname`

// CollectRelations returns every foreign-key relationship whose referencing
// table is in scope. Pass schema to limit scope, or "" for all user schemas.
func CollectRelations(ctx context.Context, dsn, schema string, o Options) (*RelationsReport, error) {
	out := &RelationsReport{Engine: EnginePostgres}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		rows, err := conn.Query(ctx, foreignKeysQuery, schema)
		if err != nil {
			return fmt.Errorf("dbinspect: foreign keys query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var fk ForeignKey
			var onDelete, onUpdate string
			if err := rows.Scan(&fk.Name, &fk.FromSchema, &fk.FromTable, &fk.FromColumns,
				&fk.ToSchema, &fk.ToTable, &fk.ToColumns, &onDelete, &onUpdate); err != nil {
				return fmt.Errorf("dbinspect: scan foreign key: %w", err)
			}
			fk.OnDelete = fkAction(onDelete)
			fk.OnUpdate = fkAction(onUpdate)
			out.ForeignKeys = append(out.ForeignKeys, fk)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fkAction expands pg_constraint action codes to readable names.
func fkAction(code string) string {
	switch code {
	case "a":
		return "no action"
	case "r":
		return "restrict"
	case "c":
		return "cascade"
	case "n":
		return "set null"
	case "d":
		return "set default"
	}
	return code
}

const tableStatsQuery = `
SELECT s.schemaname, s.relname,
       s.seq_scan, COALESCE(s.idx_scan, 0),
       s.n_live_tup, s.n_dead_tup,
       pg_total_relation_size(s.relid),
       COALESCE(to_char(GREATEST(s.last_vacuum, s.last_autovacuum) AT TIME ZONE 'UTC',
                        'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
       COALESCE(to_char(GREATEST(s.last_analyze, s.last_autoanalyze) AT TIME ZONE 'UTC',
                        'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '')
FROM pg_stat_user_tables s
WHERE ($1 = '' OR s.schemaname = $1)
ORDER BY pg_total_relation_size(s.relid) DESC, s.schemaname, s.relname
LIMIT 500`

// CollectStats returns per-table planner and vacuum health, largest tables
// first. Row counts are pg_stat estimates — no COUNT(*) is issued.
func CollectStats(ctx context.Context, dsn, schema string, o Options) (*StatsReport, error) {
	out := &StatsReport{Engine: EnginePostgres}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		rows, err := conn.Query(ctx, tableStatsQuery, schema)
		if err != nil {
			return fmt.Errorf("dbinspect: table stats query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t TableStats
			if err := rows.Scan(&t.Schema, &t.Name, &t.SeqScans, &t.IdxScans,
				&t.LiveRows, &t.DeadRows, &t.TotalBytes, &t.LastVacuum, &t.LastAnalyze); err != nil {
				return fmt.Errorf("dbinspect: scan table stats: %w", err)
			}
			out.Tables = append(out.Tables, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveTableQuery maps caller input ("orders" or "billing.orders",
// quoting allowed) to the exact catalog identifiers via to_regclass, so the
// sample query below only ever interpolates names postgres itself resolved.
const resolveTableQuery = `
SELECT n.nspname, c.relname
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.oid = to_regclass($1) AND c.relkind IN ('r','p','v','m','f')`

const (
	defaultSampleLimit = 10
	maxSampleLimit     = 500
)

// SampleTable reads up to limit rows from one table. limit defaults to 10
// and is capped at 500. Callers must gate this behind explicit confirmation:
// row data may contain customer PII.
func SampleTable(ctx context.Context, dsn, table string, limit int, o Options) (*SampleReport, error) {
	if limit <= 0 {
		limit = defaultSampleLimit
	}
	if limit > maxSampleLimit {
		limit = maxSampleLimit
	}
	out := &SampleReport{Engine: EnginePostgres, Limit: limit}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		schema, name, err := resolveTable(ctx, conn, table)
		if err != nil {
			return err
		}
		out.Schema, out.Table = schema, name
		return sampleRows(ctx, conn, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func resolveTable(ctx context.Context, conn Conn, table string) (string, string, error) {
	rows, err := conn.Query(ctx, resolveTableQuery, table)
	if err != nil {
		return "", "", fmt.Errorf("dbinspect: resolve table %q: %w", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", "", fmt.Errorf("dbinspect: resolve table %q: %w", table, err)
		}
		return "", "", fmt.Errorf("dbinspect: table %q not found", table)
	}
	var schema, name string
	if err := rows.Scan(&schema, &name); err != nil {
		return "", "", fmt.Errorf("dbinspect: scan resolved table: %w", err)
	}
	return schema, name, nil
}

func sampleRows(ctx context.Context, conn Conn, out *SampleReport) error {
	// Identifiers come from pg_class via resolveTable, then are quote-escaped;
	// the limit is a bind parameter.
	query := "SELECT * FROM " + quoteIdent(out.Schema) + "." + quoteIdent(out.Table) + " LIMIT $1"
	rows, err := conn.Query(ctx, query, out.Limit)
	if err != nil {
		return fmt.Errorf("dbinspect: sample %s.%s: %w", out.Schema, out.Table, err)
	}
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return fmt.Errorf("dbinspect: read sample row: %w", err)
		}
		row := make([]any, len(values))
		for i, v := range values {
			row[i] = displayValue(v)
		}
		out.Rows = append(out.Rows, row)
	}
	out.Columns = rows.Columns()
	return rows.Err()
}

// quoteIdent double-quotes an identifier, escaping embedded quotes.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
