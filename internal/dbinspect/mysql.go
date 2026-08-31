package dbinspect

import (
	"context"
	"fmt"
	"strings"
)

// All MySQL catalog queries read information_schema only and use bind
// parameters for every caller-supplied filter. Identifiers interpolated
// into SQL (sampling) are resolved against information_schema.TABLES first
// and then backtick-escaped.

// mysqlSchemaFilter excludes the system schemas.
const mysqlSchemaFilter = `table_schema NOT IN ('mysql','information_schema','performance_schema','sys')`

const mysqlOverviewQuery = `
SELECT VERSION(), IFNULL(DATABASE(), ''), CURRENT_USER(),
       CAST(IFNULL((SELECT SUM(data_length + index_length)
                    FROM information_schema.TABLES
                    WHERE table_schema = DATABASE()), 0) AS SIGNED),
       (SELECT COUNT(*) FROM information_schema.PROCESSLIST),
       @@global.max_connections`

const mysqlSchemaSummaryQuery = `
SELECT table_schema, CAST(COUNT(*) AS SIGNED)
FROM information_schema.TABLES
WHERE table_type = 'BASE TABLE' AND ` + mysqlSchemaFilter + `
GROUP BY table_schema
ORDER BY table_schema`

func mysqlOverview(ctx context.Context, dsn string, o Options) (*Overview, error) {
	out := &Overview{Engine: EngineMySQL}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		if err := scanMySQLOverviewRow(ctx, conn, out); err != nil {
			return err
		}
		out.ReadOnlySession = mysqlSessionReadOnly(ctx, conn)
		schemas, err := collectMySQLSchemaSummaries(ctx, conn)
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

func scanMySQLOverviewRow(ctx context.Context, conn Conn, out *Overview) error {
	rows, err := conn.Query(ctx, mysqlOverviewQuery)
	if err != nil {
		return fmt.Errorf("dbinspect: mysql overview query: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return fmt.Errorf("dbinspect: mysql overview query returned no rows: %w", rows.Err())
	}
	var connections int64
	if err := rows.Scan(&out.ServerVersion, &out.Database, &out.User,
		&out.SizeBytes, &connections, &out.MaxConnections); err != nil {
		return fmt.Errorf("dbinspect: scan mysql overview: %w", err)
	}
	out.Connections = int(connections)
	return rows.Err()
}

// mysqlSessionReadOnly reads the session read-only flag; MySQL and MariaDB
// spell the variable differently, and failure to read it is not failure to
// be read-only (the connector enforced it), so errors report false only.
func mysqlSessionReadOnly(ctx context.Context, conn Conn) bool {
	for _, q := range []string{
		"SELECT @@session.transaction_read_only",
		"SELECT @@session.tx_read_only",
	} {
		rows, err := conn.Query(ctx, q)
		if err != nil {
			continue
		}
		var flag int64
		ok := rows.Next() && rows.Scan(&flag) == nil
		rows.Close()
		if ok {
			return flag == 1
		}
	}
	return false
}

func collectMySQLSchemaSummaries(ctx context.Context, conn Conn) ([]SchemaSummary, error) {
	rows, err := conn.Query(ctx, mysqlSchemaSummaryQuery)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: mysql schema summary query: %w", err)
	}
	defer rows.Close()
	var out []SchemaSummary
	for rows.Next() {
		var s SchemaSummary
		if err := rows.Scan(&s.Name, &s.TableCount); err != nil {
			return nil, fmt.Errorf("dbinspect: scan mysql schema summary: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const mysqlTablesQuery = `
SELECT table_schema, table_name,
       CASE table_type
            WHEN 'BASE TABLE' THEN 'table'
            WHEN 'VIEW' THEN 'view'
            ELSE LOWER(table_type)
       END,
       CAST(IFNULL(table_rows, 0) AS SIGNED),
       CAST(IFNULL(data_length + index_length, 0) AS SIGNED)
FROM information_schema.TABLES
WHERE ` + mysqlSchemaFilter + `
  AND (? = '' OR table_schema = ?)
ORDER BY table_schema, table_name`

const mysqlColumnsQuery = `
SELECT table_schema, table_name, column_name, column_type,
       is_nullable, IFNULL(column_default, ''), column_key
FROM information_schema.COLUMNS
WHERE ` + mysqlSchemaFilter + `
  AND (? = '' OR table_schema = ?)
ORDER BY table_schema, table_name, ordinal_position`

const mysqlIndexesQuery = `
SELECT table_schema, table_name, index_name,
       GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ', '),
       CAST(MIN(non_unique) AS SIGNED)
FROM information_schema.STATISTICS
WHERE ` + mysqlSchemaFilter + `
  AND (? = '' OR table_schema = ?)
GROUP BY table_schema, table_name, index_name
ORDER BY table_schema, table_name, index_name`

func mysqlSchema(ctx context.Context, dsn, schema string, o Options) (*SchemaReport, error) {
	out := &SchemaReport{Engine: EngineMySQL}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		tables, err := collectMySQLTables(ctx, conn, schema)
		if err != nil {
			return err
		}
		if err := attachMySQLColumns(ctx, conn, schema, tables); err != nil {
			return err
		}
		if err := attachMySQLIndexes(ctx, conn, schema, tables); err != nil {
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

func collectMySQLTables(ctx context.Context, conn Conn, schema string) (*tableSet, error) {
	rows, err := conn.Query(ctx, mysqlTablesQuery, schema, schema)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: mysql tables query: %w", err)
	}
	defer rows.Close()
	set := &tableSet{byName: map[string]*Table{}}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.Kind, &t.EstimatedRows, &t.TotalBytes); err != nil {
			return nil, fmt.Errorf("dbinspect: scan mysql table: %w", err)
		}
		set.byName[t.Schema+"."+t.Name] = &t
		set.ordered = append(set.ordered, &t)
	}
	return set, rows.Err()
}

func attachMySQLColumns(ctx context.Context, conn Conn, schema string, set *tableSet) error {
	rows, err := conn.Query(ctx, mysqlColumnsQuery, schema, schema)
	if err != nil {
		return fmt.Errorf("dbinspect: mysql columns query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tblSchema, tblName, nullable, key string
		var col Column
		if err := rows.Scan(&tblSchema, &tblName, &col.Name, &col.Type,
			&nullable, &col.Default, &key); err != nil {
			return fmt.Errorf("dbinspect: scan mysql column: %w", err)
		}
		col.Nullable = strings.EqualFold(nullable, "YES")
		col.PrimaryKey = key == "PRI"
		if t := set.byName[tblSchema+"."+tblName]; t != nil {
			t.Columns = append(t.Columns, col)
		}
	}
	return rows.Err()
}

func attachMySQLIndexes(ctx context.Context, conn Conn, schema string, set *tableSet) error {
	rows, err := conn.Query(ctx, mysqlIndexesQuery, schema, schema)
	if err != nil {
		return fmt.Errorf("dbinspect: mysql indexes query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tblSchema, tblName, columns string
		var nonUnique int64
		var idx Index
		if err := rows.Scan(&tblSchema, &tblName, &idx.Name, &columns, &nonUnique); err != nil {
			return fmt.Errorf("dbinspect: scan mysql index: %w", err)
		}
		idx.Unique = nonUnique == 0
		idx.Primary = idx.Name == "PRIMARY"
		idx.Definition = mysqlIndexDefinition(idx, columns)
		if t := set.byName[tblSchema+"."+tblName]; t != nil {
			t.Indexes = append(t.Indexes, idx)
		}
	}
	return rows.Err()
}

// mysqlIndexDefinition synthesizes a readable definition — information_schema
// has no equivalent of pg_get_indexdef.
func mysqlIndexDefinition(idx Index, columns string) string {
	switch {
	case idx.Primary:
		return "PRIMARY KEY (" + columns + ")"
	case idx.Unique:
		return "UNIQUE INDEX " + idx.Name + " (" + columns + ")"
	default:
		return "INDEX " + idx.Name + " (" + columns + ")"
	}
}

const mysqlForeignKeysQuery = `
SELECT rc.constraint_name, rc.constraint_schema, rc.table_name,
       GROUP_CONCAT(kcu.column_name ORDER BY kcu.ordinal_position SEPARATOR ','),
       rc.unique_constraint_schema, rc.referenced_table_name,
       GROUP_CONCAT(kcu.referenced_column_name ORDER BY kcu.ordinal_position SEPARATOR ','),
       rc.delete_rule, rc.update_rule
FROM information_schema.REFERENTIAL_CONSTRAINTS rc
JOIN information_schema.KEY_COLUMN_USAGE kcu
  ON kcu.constraint_schema = rc.constraint_schema
 AND kcu.constraint_name = rc.constraint_name
 AND kcu.table_name = rc.table_name
WHERE (? = '' OR rc.constraint_schema = ?)
GROUP BY rc.constraint_name, rc.constraint_schema, rc.table_name,
         rc.unique_constraint_schema, rc.referenced_table_name,
         rc.delete_rule, rc.update_rule
ORDER BY rc.constraint_schema, rc.table_name, rc.constraint_name`

func mysqlRelations(ctx context.Context, dsn, schema string, o Options) (*RelationsReport, error) {
	out := &RelationsReport{Engine: EngineMySQL}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		rows, err := conn.Query(ctx, mysqlForeignKeysQuery, schema, schema)
		if err != nil {
			return fmt.Errorf("dbinspect: mysql foreign keys query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var fk ForeignKey
			var fromCols, toCols, onDelete, onUpdate string
			if err := rows.Scan(&fk.Name, &fk.FromSchema, &fk.FromTable, &fromCols,
				&fk.ToSchema, &fk.ToTable, &toCols, &onDelete, &onUpdate); err != nil {
				return fmt.Errorf("dbinspect: scan mysql foreign key: %w", err)
			}
			fk.FromColumns = strings.Split(fromCols, ",")
			fk.ToColumns = strings.Split(toCols, ",")
			fk.OnDelete = strings.ToLower(onDelete)
			fk.OnUpdate = strings.ToLower(onUpdate)
			out.ForeignKeys = append(out.ForeignKeys, fk)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mysqlStatsQuery reads size and row estimates. Unlike pg_stat_user_tables
// there are no scan counters without performance_schema, so those stay zero.
const mysqlStatsQuery = `
SELECT table_schema, table_name,
       CAST(IFNULL(table_rows, 0) AS SIGNED),
       CAST(IFNULL(data_length + index_length, 0) AS SIGNED)
FROM information_schema.TABLES
WHERE table_type = 'BASE TABLE' AND ` + mysqlSchemaFilter + `
  AND (? = '' OR table_schema = ?)
ORDER BY (data_length + index_length) DESC, table_schema, table_name
LIMIT 500`

func mysqlStats(ctx context.Context, dsn, schema string, o Options) (*StatsReport, error) {
	out := &StatsReport{Engine: EngineMySQL}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		rows, err := conn.Query(ctx, mysqlStatsQuery, schema, schema)
		if err != nil {
			return fmt.Errorf("dbinspect: mysql table stats query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t TableStats
			if err := rows.Scan(&t.Schema, &t.Name, &t.LiveRows, &t.TotalBytes); err != nil {
				return fmt.Errorf("dbinspect: scan mysql table stats: %w", err)
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

func mysqlSample(ctx context.Context, dsn, table string, limit int, o Options) (*SampleReport, error) {
	out := &SampleReport{Engine: EngineMySQL, Limit: limit}
	err := withConn(ctx, dsn, o, func(ctx context.Context, conn Conn) error {
		schema, name, err := resolveMySQLTable(ctx, conn, table)
		if err != nil {
			return err
		}
		out.Schema, out.Table = schema, name
		return sampleMySQLRows(ctx, conn, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveMySQLTable maps "db.table" or "table" (current database) to the
// catalog's own spelling, so the sample query only interpolates names the
// server itself resolved.
func resolveMySQLTable(ctx context.Context, conn Conn, table string) (string, string, error) {
	query := `SELECT table_schema, table_name FROM information_schema.TABLES
WHERE table_schema = DATABASE() AND table_name = ?`
	args := []any{table}
	if schemaPart, namePart, ok := strings.Cut(table, "."); ok {
		query = `SELECT table_schema, table_name FROM information_schema.TABLES
WHERE table_schema = ? AND table_name = ?`
		args = []any{schemaPart, namePart}
	}
	rows, err := conn.Query(ctx, query, args...)
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

func sampleMySQLRows(ctx context.Context, conn Conn, out *SampleReport) error {
	// Identifiers come from information_schema via resolveMySQLTable, then
	// are backtick-escaped; the limit is a bind parameter.
	query := "SELECT * FROM " + quoteMySQLIdent(out.Schema) + "." + quoteMySQLIdent(out.Table) + " LIMIT ?"
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

// quoteMySQLIdent backtick-quotes an identifier, escaping embedded backticks.
func quoteMySQLIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
