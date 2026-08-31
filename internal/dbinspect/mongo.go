package dbinspect

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The mongodb engine maps document-database concepts onto the relational
// report types: databases play the role of schemas, collections of tables,
// and index specifications of index definitions. Foreign keys don't exist,
// so relations reports are honestly empty. Every command issued is a read:
// buildInfo, listDatabases, dbStats, serverStatus, listCollections,
// listIndexes, collStats, and bounded find.

// maxMongoDatabases bounds the per-database fan-out when a deployment has
// many databases and the DSN names none.
const maxMongoDatabases = 25

// withMongo opens a bounded client session, runs fn, and disconnects.
func withMongo(ctx context.Context, dsn string, o Options, fn func(ctx context.Context, client MongoRunner) error) error {
	o = withDefaults(o)
	if o.ConnectMongo == nil {
		o.ConnectMongo = ConnectMongo
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	client, err := o.ConnectMongo(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = client.Disconnect(ctx) }()
	return fn(ctx, client)
}

// mongoDatabases returns the databases in scope: the schema filter, else
// the DSN's database, else every listable database (capped).
func mongoDatabases(ctx context.Context, client MongoRunner, dsn, schema string) ([]string, error) {
	if schema != "" {
		return []string{schema}, nil
	}
	if db := mongoDatabaseFromDSN(dsn); db != "" {
		return []string{db}, nil
	}
	names, err := client.ListDatabaseNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: mongo list databases: %w", err)
	}
	if len(names) > maxMongoDatabases {
		names = names[:maxMongoDatabases]
	}
	return names, nil
}

func mongoOverview(ctx context.Context, dsn string, o Options) (*Overview, error) {
	out := &Overview{Engine: EngineMongo, Database: mongoDatabaseFromDSN(dsn)}
	err := withMongo(ctx, dsn, o, func(ctx context.Context, client MongoRunner) error {
		if info, err := client.RunCommand(ctx, "admin", bson.D{{Key: "buildInfo", Value: 1}}); err == nil {
			out.ServerVersion, _ = info["version"].(string)
		}
		if status, err := client.RunCommand(ctx, "admin", bson.D{{Key: "serverStatus", Value: 1}}); err == nil {
			if conns, ok := status["connections"].(map[string]any); ok {
				out.Connections = int(mongoInt64(conns["current"]))
				out.MaxConnections = out.Connections + int(mongoInt64(conns["available"]))
			}
		}
		dbs, err := mongoDatabases(ctx, client, dsn, "")
		if err != nil {
			return err
		}
		for _, db := range dbs {
			stats, err := client.RunCommand(ctx, db, bson.D{{Key: "dbStats", Value: 1}})
			if err != nil {
				return fmt.Errorf("dbinspect: mongo dbStats %s: %w", db, err)
			}
			out.SizeBytes += mongoInt64(stats["dataSize"]) + mongoInt64(stats["indexSize"])
			out.Schemas = append(out.Schemas, SchemaSummary{
				Name:       db,
				TableCount: int(mongoInt64(stats["collections"])),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func mongoSchema(ctx context.Context, dsn, schema string, o Options) (*SchemaReport, error) {
	out := &SchemaReport{Engine: EngineMongo}
	err := withMongo(ctx, dsn, o, func(ctx context.Context, client MongoRunner) error {
		dbs, err := mongoDatabases(ctx, client, dsn, schema)
		if err != nil {
			return err
		}
		for _, db := range dbs {
			tables, err := collectMongoCollections(ctx, client, db)
			if err != nil {
				return err
			}
			out.Tables = append(out.Tables, tables...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func collectMongoCollections(ctx context.Context, client MongoRunner, db string) ([]Table, error) {
	specs, err := client.ListCollections(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: mongo list collections %s: %w", db, err)
	}
	var out []Table
	for _, spec := range specs {
		name, _ := spec["name"].(string)
		if name == "" || strings.HasPrefix(name, "system.") {
			continue
		}
		kind, _ := spec["type"].(string)
		if kind == "" {
			kind = "collection"
		}
		t := Table{Schema: db, Name: name, Kind: kind}
		if kind == "collection" {
			indexes, err := client.ListIndexes(ctx, db, name)
			if err != nil {
				return nil, fmt.Errorf("dbinspect: mongo list indexes %s.%s: %w", db, name, err)
			}
			t.Indexes = mongoIndexes(indexes)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func mongoIndexes(specs []map[string]any) []Index {
	var out []Index
	for _, spec := range specs {
		var idx Index
		idx.Name, _ = spec["name"].(string)
		idx.Unique, _ = spec["unique"].(bool)
		idx.Primary = idx.Name == "_id_"
		if key, err := json.Marshal(spec["key"]); err == nil {
			idx.Definition = string(key)
		}
		out = append(out, idx)
	}
	return out
}

// mongoRelations is honestly empty: MongoDB has no foreign keys, and
// application-level references ($lookup joins, manual refs) are not
// discoverable from the catalog.
func mongoRelations(ctx context.Context, dsn, _ string, o Options) (*RelationsReport, error) {
	out := &RelationsReport{Engine: EngineMongo}
	err := withMongo(ctx, dsn, o, func(context.Context, MongoRunner) error { return nil })
	if err != nil {
		return nil, err
	}
	return out, nil
}

func mongoStats(ctx context.Context, dsn, schema string, o Options) (*StatsReport, error) {
	out := &StatsReport{Engine: EngineMongo}
	err := withMongo(ctx, dsn, o, func(ctx context.Context, client MongoRunner) error {
		dbs, err := mongoDatabases(ctx, client, dsn, schema)
		if err != nil {
			return err
		}
		for _, db := range dbs {
			if err := collectMongoCollStats(ctx, client, db, out); err != nil {
				return err
			}
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

func collectMongoCollStats(ctx context.Context, client MongoRunner, db string, out *StatsReport) error {
	specs, err := client.ListCollections(ctx, db)
	if err != nil {
		return fmt.Errorf("dbinspect: mongo list collections %s: %w", db, err)
	}
	for _, spec := range specs {
		name, _ := spec["name"].(string)
		if name == "" || strings.HasPrefix(name, "system.") {
			continue
		}
		if kind, _ := spec["type"].(string); kind == "view" {
			continue
		}
		row := TableStats{Schema: db, Name: name}
		// count here is the collection's metadata estimate, not a scan.
		if stats, err := client.RunCommand(ctx, db, bson.D{{Key: "collStats", Value: name}}); err == nil {
			row.LiveRows = mongoInt64(stats["count"])
			row.TotalBytes = mongoInt64(stats["storageSize"]) + mongoInt64(stats["totalIndexSize"])
		}
		out.Tables = append(out.Tables, row)
	}
	return nil
}

func mongoSample(ctx context.Context, dsn, table string, limit int, o Options) (*SampleReport, error) {
	out := &SampleReport{Engine: EngineMongo, Limit: limit}
	err := withMongo(ctx, dsn, o, func(ctx context.Context, client MongoRunner) error {
		db, coll, err := resolveMongoCollection(ctx, client, dsn, table)
		if err != nil {
			return err
		}
		out.Schema, out.Table = db, coll
		docs, err := client.Find(ctx, db, coll, out.Limit)
		if err != nil {
			return fmt.Errorf("dbinspect: sample %s.%s: %w", db, coll, err)
		}
		out.Columns, out.Rows = mongoDocsToRows(docs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveMongoCollection maps "db.collection" or "collection" (DSN
// database) input onto a collection the catalog confirms exists.
func resolveMongoCollection(ctx context.Context, client MongoRunner, dsn, table string) (string, string, error) {
	db := mongoDatabaseFromDSN(dsn)
	name := table
	if dbPart, collPart, ok := strings.Cut(table, "."); ok {
		db, name = dbPart, collPart
	}
	if db == "" {
		return "", "", fmt.Errorf("dbinspect: collection %q needs a database: use db.collection or a DSN with a database", table)
	}
	specs, err := client.ListCollections(ctx, db)
	if err != nil {
		return "", "", fmt.Errorf("dbinspect: resolve collection %q: %w", table, err)
	}
	for _, spec := range specs {
		if got, _ := spec["name"].(string); got == name {
			return db, name, nil
		}
	}
	return "", "", fmt.Errorf("dbinspect: collection %q not found", table)
}

// mongoDocsToRows flattens documents into a column-aligned grid: columns
// are the sorted union of top-level field names (_id first), and fields a
// document lacks stay nil.
func mongoDocsToRows(docs []map[string]any) ([]string, [][]any) {
	fields := map[string]bool{}
	for _, doc := range docs {
		for k := range doc {
			fields[k] = true
		}
	}
	columns := make([]string, 0, len(fields))
	for k := range fields {
		if k != "_id" {
			columns = append(columns, k)
		}
	}
	sort.Strings(columns)
	if fields["_id"] {
		columns = append([]string{"_id"}, columns...)
	}
	rows := make([][]any, len(docs))
	for i, doc := range docs {
		row := make([]any, len(columns))
		for j, col := range columns {
			if v, ok := doc[col]; ok {
				row[j] = displayValue(v)
			}
		}
		rows[i] = row
	}
	return columns, rows
}

// mongoInt64 folds the numeric types bson decoding produces into int64.
func mongoInt64(v any) int64 {
	switch t := v.(type) {
	case int32:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case int:
		return int64(t)
	default:
		return 0
	}
}
