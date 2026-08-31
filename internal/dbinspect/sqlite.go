package dbinspect

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SQLite catalog reads use sqlite_schema and pragma table-valued functions
// only. There is a single schema ("main"; ATTACH is never issued), so a
// schema filter other than "" or "main" yields an empty report. Identifiers
// interpolated into SQL (sampling) are resolved against sqlite_schema first
// and then double-quote-escaped.

const sqliteMainSchema = "main"

// sqliteSchemaInScope reports whether the caller's schema filter matches
// the only schema an un-attached SQLite database has.
func sqliteSchemaInScope(schema string) bool {
	return schema == "" || schema == sqliteMainSchema
}

const sqliteOverviewQuery = `
SELECT sqlite_version(),
       IFNULL((SELECT file FROM pragma_database_list() WHERE name = 'main'), ''),
       (SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()),
       (SELECT query_only FROM pragma_query_only()),
       (SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\')`

func sqliteOverview(ctx context.Context, dsn string, o Options) (*Overview, error) {
	out := &Overview{Engine: EngineSQLite}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		rows, err := conn.Query(ctx, sqliteOverviewQuery)
		if err != nil {
			return fmt.Errorf("dbinspect: sqlite overview query: %w", err)
		}
		defer rows.Close()
		if !rows.Next() {
			return fmt.Errorf("dbinspect: sqlite overview query returned no rows: %w", rows.Err())
		}
		var queryOnly, tableCount int64
		if err := rows.Scan(&out.ServerVersion, &out.Database, &out.SizeBytes,
			&queryOnly, &tableCount); err != nil {
			return fmt.Errorf("dbinspect: scan sqlite overview: %w", err)
		}
		out.ReadOnlySession = queryOnly == 1
		out.Schemas = []SchemaSummary{{Name: sqliteMainSchema, TableCount: int(tableCount)}}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const sqliteTablesQuery = `
SELECT name, type FROM sqlite_schema
WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
ORDER BY name`

const sqliteColumnsQuery = `
SELECT name, IFNULL(type, ''), "notnull", IFNULL(dflt_value, ''), pk
FROM pragma_table_info(?)`

const sqliteIndexListQuery = `
SELECT il.name, il."unique", il.origin, IFNULL(m.sql, '')
FROM pragma_index_list(?) il
LEFT JOIN sqlite_schema m ON m.name = il.name AND m.type = 'index'
ORDER BY il.name`

func sqliteSchema(ctx context.Context, dsn, schema string, o Options) (*SchemaReport, error) {
	out := &SchemaReport{Engine: EngineSQLite}
	if !sqliteSchemaInScope(schema) {
		return out, nil
	}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		tables, err := collectSQLiteTables(ctx, conn)
		if err != nil {
			return err
		}
		for _, t := range tables {
			if err := attachSQLiteColumns(ctx, conn, t); err != nil {
				return err
			}
			if err := attachSQLiteIndexes(ctx, conn, t); err != nil {
				return err
			}
			out.Tables = append(out.Tables, *t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func collectSQLiteTables(ctx context.Context, conn Conn) ([]*Table, error) {
	rows, err := conn.Query(ctx, sqliteTablesQuery)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: sqlite tables query: %w", err)
	}
	defer rows.Close()
	var out []*Table
	for rows.Next() {
		t := &Table{Schema: sqliteMainSchema}
		if err := rows.Scan(&t.Name, &t.Kind); err != nil {
			return nil, fmt.Errorf("dbinspect: scan sqlite table: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func attachSQLiteColumns(ctx context.Context, conn Conn, t *Table) error {
	rows, err := conn.Query(ctx, sqliteColumnsQuery, t.Name)
	if err != nil {
		return fmt.Errorf("dbinspect: sqlite columns for %s: %w", t.Name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var col Column
		var notNull, pk int64
		if err := rows.Scan(&col.Name, &col.Type, &notNull, &col.Default, &pk); err != nil {
			return fmt.Errorf("dbinspect: scan sqlite column: %w", err)
		}
		col.Nullable = notNull == 0
		col.PrimaryKey = pk > 0
		t.Columns = append(t.Columns, col)
	}
	return rows.Err()
}

func attachSQLiteIndexes(ctx context.Context, conn Conn, t *Table) error {
	rows, err := conn.Query(ctx, sqliteIndexListQuery, t.Name)
	if err != nil {
		return fmt.Errorf("dbinspect: sqlite indexes for %s: %w", t.Name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var idx Index
		var unique int64
		var origin, sqlText string
		if err := rows.Scan(&idx.Name, &unique, &origin, &sqlText); err != nil {
			return fmt.Errorf("dbinspect: scan sqlite index: %w", err)
		}
		idx.Unique = unique == 1
		// origin: "c" = CREATE INDEX, "u" = UNIQUE constraint, "pk" = primary key.
		idx.Primary = origin == "pk"
		idx.Definition = sqlText
		if idx.Definition == "" {
			idx.Definition = "auto index (" + origin + ")"
		}
		t.Indexes = append(t.Indexes, idx)
	}
	return rows.Err()
}

const sqliteForeignKeysQuery = `
SELECT id, "table", "from", IFNULL("to", ''), on_delete, on_update
FROM pragma_foreign_key_list(?)
ORDER BY id, seq`

func sqliteRelations(ctx context.Context, dsn, schema string, o Options) (*RelationsReport, error) {
	out := &RelationsReport{Engine: EngineSQLite}
	if !sqliteSchemaInScope(schema) {
		return out, nil
	}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		tables, err := collectSQLiteTables(ctx, conn)
		if err != nil {
			return err
		}
		for _, t := range tables {
			if t.Kind != "table" {
				continue
			}
			fks, err := collectSQLiteForeignKeys(ctx, conn, t.Name)
			if err != nil {
				return err
			}
			out.ForeignKeys = append(out.ForeignKeys, fks...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// collectSQLiteForeignKeys reads one table's foreign keys; rows arrive one
// per column ordered by (id, seq), so consecutive rows with the same id
// fold into one composite key. SQLite FKs are unnamed — names are
// synthesized as <table>_fk_<id>.
func collectSQLiteForeignKeys(ctx context.Context, conn Conn, table string) ([]ForeignKey, error) {
	rows, err := conn.Query(ctx, sqliteForeignKeysQuery, table)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: sqlite foreign keys for %s: %w", table, err)
	}
	defer rows.Close()
	var out []ForeignKey
	lastID := int64(-1)
	for rows.Next() {
		var id int64
		var toTable, fromCol, toCol, onDelete, onUpdate string
		if err := rows.Scan(&id, &toTable, &fromCol, &toCol, &onDelete, &onUpdate); err != nil {
			return nil, fmt.Errorf("dbinspect: scan sqlite foreign key: %w", err)
		}
		if id != lastID {
			out = append(out, ForeignKey{
				Name:       fmt.Sprintf("%s_fk_%d", table, id),
				FromSchema: sqliteMainSchema,
				FromTable:  table,
				ToSchema:   sqliteMainSchema,
				ToTable:    toTable,
				OnDelete:   strings.ToLower(onDelete),
				OnUpdate:   strings.ToLower(onUpdate),
			})
			lastID = id
		}
		fk := &out[len(out)-1]
		fk.FromColumns = append(fk.FromColumns, fromCol)
		if toCol != "" {
			fk.ToColumns = append(fk.ToColumns, toCol)
		}
	}
	return out, rows.Err()
}

// sqliteDBStatQuery aggregates on-disk bytes per table from the dbstat
// virtual table. Not every build ships dbstat, so callers treat a query
// error as "sizes unavailable", not failure.
const sqliteDBStatQuery = `
SELECT name, CAST(SUM(pgsize) AS INTEGER) FROM dbstat GROUP BY name`

// sqliteStat1Query reads ANALYZE output when present; the first integer in
// stat is the approximate row count.
const sqliteStat1Query = `
SELECT tbl, CAST(stat AS TEXT) FROM sqlite_stat1`

func sqliteStats(ctx context.Context, dsn, schema string, o Options) (*StatsReport, error) {
	out := &StatsReport{Engine: EngineSQLite}
	if !sqliteSchemaInScope(schema) {
		return out, nil
	}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		tables, err := collectSQLiteTables(ctx, conn)
		if err != nil {
			return err
		}
		sizes := sqliteNameValues(ctx, conn, sqliteDBStatQuery, parseInt64)
		rowEstimates := sqliteNameValues(ctx, conn, sqliteStat1Query, parseStatRows)
		for _, t := range tables {
			if t.Kind != "table" {
				continue
			}
			out.Tables = append(out.Tables, TableStats{
				Schema:     sqliteMainSchema,
				Name:       t.Name,
				LiveRows:   rowEstimates[t.Name],
				TotalBytes: sizes[t.Name],
			})
		}
		sort.SliceStable(out.Tables, func(i, j int) bool {
			return out.Tables[i].TotalBytes > out.Tables[j].TotalBytes
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sqliteNameValues runs an optional (name, value) query — dbstat or
// sqlite_stat1 — folding values per name with max. A query error returns an
// empty map: both sources are optional extras, absent on many databases.
func sqliteNameValues(ctx context.Context, conn Conn, query string, parse func(string) int64) map[string]int64 {
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return map[string]int64{}
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return out
		}
		if v := parse(value); v > out[name] {
			out[name] = v
		}
	}
	return out
}

func parseInt64(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseStatRows extracts the leading integer of an sqlite_stat1 stat value
// ("10000 12 3" → 10000).
func parseStatRows(stat string) int64 {
	first, _, _ := strings.Cut(strings.TrimSpace(stat), " ")
	return parseInt64(first)
}

const sqliteResolveTableQuery = `
SELECT name FROM sqlite_schema
WHERE type IN ('table', 'view') AND name = ?`

func sqliteSample(ctx context.Context, dsn, table string, limit int, o Options) (*SampleReport, error) {
	out := &SampleReport{Engine: EngineSQLite, Schema: sqliteMainSchema, Limit: limit}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		name, err := resolveSQLiteTable(ctx, conn, table)
		if err != nil {
			return err
		}
		out.Table = name
		// The identifier comes from sqlite_schema via resolveSQLiteTable,
		// then is quote-escaped; the limit is a bind parameter.
		query := "SELECT * FROM " + quoteIdent(name) + " LIMIT ?"
		rows, err := conn.Query(ctx, query, out.Limit)
		if err != nil {
			return fmt.Errorf("dbinspect: sample %s: %w", name, err)
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
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveSQLiteTable maps "table" or "main.table" to the catalog's own
// spelling via sqlite_schema.
func resolveSQLiteTable(ctx context.Context, conn Conn, table string) (string, error) {
	name := strings.TrimPrefix(table, sqliteMainSchema+".")
	rows, err := conn.Query(ctx, sqliteResolveTableQuery, name)
	if err != nil {
		return "", fmt.Errorf("dbinspect: resolve table %q: %w", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("dbinspect: resolve table %q: %w", table, err)
		}
		return "", fmt.Errorf("dbinspect: table %q not found", table)
	}
	var resolved string
	if err := rows.Scan(&resolved); err != nil {
		return "", fmt.Errorf("dbinspect: scan resolved table: %w", err)
	}
	return resolved, nil
}
