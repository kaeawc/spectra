package dbinspect

import (
	"context"
	"strings"
)

// engineOps binds one engine's connector and collectors. New engines add a
// registry entry rather than growing per-function switches.
type engineOps struct {
	connect   ConnectFn
	overview  func(ctx context.Context, dsn string, o Options) (*Overview, error)
	schema    func(ctx context.Context, dsn, schema string, o Options) (*SchemaReport, error)
	relations func(ctx context.Context, dsn, schema string, o Options) (*RelationsReport, error)
	stats     func(ctx context.Context, dsn, schema string, o Options) (*StatsReport, error)
	sample    func(ctx context.Context, dsn, table string, limit int, o Options) (*SampleReport, error)
}

func opsFor(engine Engine) engineOps {
	switch engine {
	case EngineMySQL:
		return engineOps{ConnectMySQL, mysqlOverview, mysqlSchema, mysqlRelations, mysqlStats, mysqlSample}
	case EngineSQLite:
		return engineOps{ConnectSQLite, sqliteOverview, sqliteSchema, sqliteRelations, sqliteStats, sqliteSample}
	case EngineMongo:
		// The mongodb engine connects through Options.ConnectMongo, not the
		// SQL-shaped ConnectFn, so connect stays nil here.
		return engineOps{nil, mongoOverview, mongoSchema, mongoRelations, mongoStats, mongoSample}
	default:
		return engineOps{ConnectPostgres, postgresOverview, postgresSchema, postgresRelations, postgresStats, postgresSample}
	}
}

// sqliteFileSuffixes mark a bare path as an SQLite database.
var sqliteFileSuffixes = []string{".db", ".sqlite", ".sqlite3", ".db3"}

// resolveEngine picks the engine for a DSN: an explicit Options.Engine wins,
// then the URL scheme, then the go-sql-driver "user@tcp(host)/db" form, then
// a bare path with an SQLite file suffix. Everything else — libpq keyword
// form, bare PG* env fallback — is postgres.
func resolveEngine(dsn string, o Options) Engine {
	if o.Engine != "" {
		return o.Engine
	}
	if e := engineFromScheme(dsn); e != "" {
		return e
	}
	if strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(") {
		return EngineMySQL
	}
	for _, suffix := range sqliteFileSuffixes {
		if strings.HasSuffix(dsn, suffix) {
			return EngineSQLite
		}
	}
	return EnginePostgres
}

// resolve picks the ops for a DSN and fills Options.Connect with the
// engine's built-in connector when the caller didn't inject one.
func resolve(dsn string, o Options) (engineOps, Options) {
	ops := opsFor(resolveEngine(dsn, o))
	if o.Connect == nil && ops.connect != nil {
		o.Connect = ops.connect
	}
	return ops, o
}

// CollectOverview returns server, database, and session facts plus a count
// of tables per user schema.
func CollectOverview(ctx context.Context, dsn string, o Options) (*Overview, error) {
	ops, o := resolve(dsn, o)
	return ops.overview(ctx, dsn, o)
}

// CollectSchema returns every user relation with its columns and indexes.
// Pass schema to limit scope to one schema, or "" for all user schemas.
func CollectSchema(ctx context.Context, dsn, schema string, o Options) (*SchemaReport, error) {
	ops, o := resolve(dsn, o)
	return ops.schema(ctx, dsn, schema, o)
}

// CollectRelations returns every foreign-key relationship whose referencing
// table is in scope. Pass schema to limit scope, or "" for all user schemas.
func CollectRelations(ctx context.Context, dsn, schema string, o Options) (*RelationsReport, error) {
	ops, o := resolve(dsn, o)
	return ops.relations(ctx, dsn, schema, o)
}

// CollectStats returns per-table health, largest tables first. Row counts
// are engine estimates — no COUNT(*) is issued.
func CollectStats(ctx context.Context, dsn, schema string, o Options) (*StatsReport, error) {
	ops, o := resolve(dsn, o)
	return ops.stats(ctx, dsn, schema, o)
}

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
	ops, o := resolve(dsn, o)
	return ops.sample(ctx, dsn, table, limit, o)
}
