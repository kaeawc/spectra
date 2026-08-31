package dbinspect

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// MongoRunner is the subset of the mongo driver the inspector needs.
// MongoDB's command surface doesn't fit the SQL-shaped Conn/Rows, so the
// mongodb engine gets its own narrow seam; tests inject a fake.
type MongoRunner interface {
	// RunCommand runs one database command and returns its reply document.
	RunCommand(ctx context.Context, db string, cmd any) (map[string]any, error)
	ListDatabaseNames(ctx context.Context) ([]string, error)
	ListCollections(ctx context.Context, db string) ([]map[string]any, error)
	ListIndexes(ctx context.Context, db, coll string) ([]map[string]any, error)
	Find(ctx context.Context, db, coll string, limit int) ([]map[string]any, error)
	Disconnect(ctx context.Context) error
}

// MongoConnectFn opens a MongoRunner for a DSN. Nil selects ConnectMongo.
type MongoConnectFn func(ctx context.Context, dsn string) (MongoRunner, error)

// ConnectMongo opens the official driver read-only in spirit: document
// databases have no session read-only mode, so the guarantee is that this
// package only ever issues read commands. Reads prefer secondaries so
// inspection stays off the primary, timeouts are short, the pool is one
// connection, and the session is identifiable by appName.
func ConnectMongo(ctx context.Context, dsn string) (MongoRunner, error) {
	opts := options.Client().
		ApplyURI(dsn).
		SetAppName("spectra-dbinspect").
		SetReadPreference(readpref.SecondaryPreferred()).
		SetServerSelectionTimeout(5 * time.Second).
		SetConnectTimeout(5 * time.Second).
		SetTimeout(5 * time.Second).
		SetMaxPoolSize(1)
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: connect %s: %w", RedactDSN(dsn), err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("dbinspect: ping %s: %w", RedactDSN(dsn), err)
	}
	return mongoRunner{client}, nil
}

// mongoDatabaseFromDSN extracts the database named in a mongodb:// or
// mongodb+srv:// URI path, or "" when the URI names none.
func mongoDatabaseFromDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

type mongoRunner struct{ client *mongo.Client }

func (r mongoRunner) RunCommand(ctx context.Context, db string, cmd any) (map[string]any, error) {
	var out bson.M
	if err := r.client.Database(db).RunCommand(ctx, cmd).Decode(&out); err != nil {
		return nil, err
	}
	return normalizeMongoDoc(out), nil
}

func (r mongoRunner) ListDatabaseNames(ctx context.Context) ([]string, error) {
	return r.client.ListDatabaseNames(ctx, bson.D{})
}

func (r mongoRunner) ListCollections(ctx context.Context, db string) ([]map[string]any, error) {
	cursor, err := r.client.Database(db).ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	return decodeMongoCursor(ctx, cursor)
}

func (r mongoRunner) ListIndexes(ctx context.Context, db, coll string) ([]map[string]any, error) {
	cursor, err := r.client.Database(db).Collection(coll).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	return decodeMongoCursor(ctx, cursor)
}

func (r mongoRunner) Find(ctx context.Context, db, coll string, limit int) ([]map[string]any, error) {
	cursor, err := r.client.Database(db).Collection(coll).
		Find(ctx, bson.D{}, options.Find().SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	return decodeMongoCursor(ctx, cursor)
}

func (r mongoRunner) Disconnect(ctx context.Context) error { return r.client.Disconnect(ctx) }

func decodeMongoCursor(ctx context.Context, cursor *mongo.Cursor) ([]map[string]any, error) {
	var docs []bson.M
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(docs))
	for i, d := range docs {
		out[i] = normalizeMongoDoc(d)
	}
	return out, nil
}

// normalizeMongoDoc rewrites a decoded document into plain maps and
// slices: decoding into bson.M leaves *nested* documents as ordered bson.D
// pairs, which would leak driver types into reports and JSON output.
func normalizeMongoDoc(doc bson.M) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		out[k] = normalizeMongoValue(v)
	}
	return out
}

func normalizeMongoValue(v any) any {
	switch t := v.(type) {
	case bson.D:
		m := make(map[string]any, len(t))
		for _, e := range t {
			m[e.Key] = normalizeMongoValue(e.Value)
		}
		return m
	case bson.M:
		return normalizeMongoDoc(t)
	case bson.A:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeMongoValue(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeMongoValue(e)
		}
		return out
	default:
		return v
	}
}
