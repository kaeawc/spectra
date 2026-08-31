package dbinspect

import (
	"context"
	"strings"
)

// resolveEngine picks the engine for a DSN: an explicit Options.Engine wins,
// then the URL scheme, then the go-sql-driver "user@tcp(host)/db" form.
// Everything else — libpq keyword form, bare PG* env fallback — is postgres.
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
	return EnginePostgres
}

// connectorFor fills Options.Connect with the engine's built-in connector
// when the caller didn't inject one.
func connectorFor(o Options, engine Engine) Options {
	if o.Connect != nil {
		return o
	}
	if engine == EngineMySQL {
		o.Connect = ConnectMySQL
	} else {
		o.Connect = ConnectPostgres
	}
	return o
}

// CollectOverview returns server, database, and session facts plus a count
// of tables per user schema.
func CollectOverview(ctx context.Context, dsn string, o Options) (*Overview, error) {
	engine := resolveEngine(dsn, o)
	o = connectorFor(o, engine)
	if engine == EngineMySQL {
		return mysqlOverview(ctx, dsn, o)
	}
	return postgresOverview(ctx, dsn, o)
}

// CollectSchema returns every user relation with its columns and indexes.
// Pass schema to limit scope to one schema, or "" for all user schemas.
func CollectSchema(ctx context.Context, dsn, schema string, o Options) (*SchemaReport, error) {
	engine := resolveEngine(dsn, o)
	o = connectorFor(o, engine)
	if engine == EngineMySQL {
		return mysqlSchema(ctx, dsn, schema, o)
	}
	return postgresSchema(ctx, dsn, schema, o)
}

// CollectRelations returns every foreign-key relationship whose referencing
// table is in scope. Pass schema to limit scope, or "" for all user schemas.
func CollectRelations(ctx context.Context, dsn, schema string, o Options) (*RelationsReport, error) {
	engine := resolveEngine(dsn, o)
	o = connectorFor(o, engine)
	if engine == EngineMySQL {
		return mysqlRelations(ctx, dsn, schema, o)
	}
	return postgresRelations(ctx, dsn, schema, o)
}

// CollectStats returns per-table health, largest tables first. Row counts
// are engine estimates — no COUNT(*) is issued.
func CollectStats(ctx context.Context, dsn, schema string, o Options) (*StatsReport, error) {
	engine := resolveEngine(dsn, o)
	o = connectorFor(o, engine)
	if engine == EngineMySQL {
		return mysqlStats(ctx, dsn, schema, o)
	}
	return postgresStats(ctx, dsn, schema, o)
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
	engine := resolveEngine(dsn, o)
	o = connectorFor(o, engine)
	if engine == EngineMySQL {
		return mysqlSample(ctx, dsn, table, limit, o)
	}
	return postgresSample(ctx, dsn, table, limit, o)
}
