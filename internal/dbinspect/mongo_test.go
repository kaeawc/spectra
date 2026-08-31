package dbinspect

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type fakeMongo struct {
	commands     map[string]map[string]any // "<db>/<command>" -> reply
	dbNames      []string
	collections  map[string][]map[string]any // db -> listCollections specs
	indexes      map[string][]map[string]any // "db.coll" -> index specs
	docs         map[string][]map[string]any // "db.coll" -> documents
	findLimit    int
	disconnected bool
}

func (f *fakeMongo) RunCommand(_ context.Context, db string, cmd any) (map[string]any, error) {
	doc, ok := cmd.(bson.D)
	if !ok || len(doc) == 0 {
		return nil, fmt.Errorf("fakeMongo: unexpected command shape %T", cmd)
	}
	reply, ok := f.commands[db+"/"+doc[0].Key]
	if !ok {
		return nil, fmt.Errorf("fakeMongo: no canned reply for %s/%s", db, doc[0].Key)
	}
	return reply, nil
}

func (f *fakeMongo) ListDatabaseNames(context.Context) ([]string, error) { return f.dbNames, nil }

func (f *fakeMongo) ListCollections(_ context.Context, db string) ([]map[string]any, error) {
	return f.collections[db], nil
}

func (f *fakeMongo) ListIndexes(_ context.Context, db, coll string) ([]map[string]any, error) {
	return f.indexes[db+"."+coll], nil
}

func (f *fakeMongo) Find(_ context.Context, db, coll string, limit int) ([]map[string]any, error) {
	f.findLimit = limit
	return f.docs[db+"."+coll], nil
}

func (f *fakeMongo) Disconnect(context.Context) error {
	f.disconnected = true
	return nil
}

func mongoOptions(f *fakeMongo) Options {
	return Options{ConnectMongo: func(context.Context, string) (MongoRunner, error) { return f, nil }}
}

func TestResolveEngineMongo(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"mongodb://app:pw@db.internal:27017/orders",
		"mongodb+srv://cluster0.example.mongodb.net/orders",
	} {
		if got := resolveEngine(dsn, Options{}); got != EngineMongo {
			t.Errorf("resolveEngine(%q) = %q, want mongodb", dsn, got)
		}
	}
}

func TestMongoDatabaseFromDSN(t *testing.T) {
	t.Parallel()
	tests := []struct{ dsn, want string }{
		{"mongodb://app@db:27017/orders?readPreference=secondary", "orders"},
		{"mongodb+srv://cluster.example.net/analytics", "analytics"},
		{"mongodb://db:27017", ""},
	}
	for _, tt := range tests {
		if got := mongoDatabaseFromDSN(tt.dsn); got != tt.want {
			t.Errorf("mongoDatabaseFromDSN(%q) = %q, want %q", tt.dsn, got, tt.want)
		}
	}
}

func TestMongoOverview(t *testing.T) {
	t.Parallel()
	f := &fakeMongo{commands: map[string]map[string]any{
		"admin/buildInfo":    {"version": "8.0.4"},
		"admin/serverStatus": {"connections": map[string]any{"current": int32(12), "available": int32(88)}},
		"orders/dbStats":     {"dataSize": int64(4096000), "indexSize": int64(1024000), "collections": int32(6)},
	}}
	got, err := CollectOverview(context.Background(), "mongodb://x/orders", mongoOptions(f))
	if err != nil {
		t.Fatalf("CollectOverview: %v", err)
	}
	if got.Engine != EngineMongo || got.ServerVersion != "8.0.4" || got.Database != "orders" {
		t.Errorf("unexpected overview: %+v", got)
	}
	if got.Connections != 12 || got.MaxConnections != 100 {
		t.Errorf("unexpected connections: %+v", got)
	}
	if got.SizeBytes != 5120000 {
		t.Errorf("size = %d, want dataSize+indexSize", got.SizeBytes)
	}
	if len(got.Schemas) != 1 || got.Schemas[0].TableCount != 6 {
		t.Errorf("unexpected schemas: %+v", got.Schemas)
	}
	if !f.disconnected {
		t.Error("client not disconnected")
	}
}

func TestMongoSchema(t *testing.T) {
	t.Parallel()
	f := &fakeMongo{
		collections: map[string][]map[string]any{
			"orders": {
				{"name": "invoices", "type": "collection"},
				{"name": "billing_view", "type": "view"},
				{"name": "system.views", "type": "collection"},
			},
		},
		indexes: map[string][]map[string]any{
			"orders.invoices": {
				{"name": "_id_", "key": map[string]any{"_id": int32(1)}},
				{"name": "customer_1", "key": map[string]any{"customer": int32(1)}, "unique": true},
			},
		},
	}
	got, err := CollectSchema(context.Background(), "mongodb://x/orders", "", mongoOptions(f))
	if err != nil {
		t.Fatalf("CollectSchema: %v", err)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("system.* not skipped or missing tables: %+v", got.Tables)
	}
	view := got.Tables[0]
	if view.Name != "billing_view" || view.Kind != "view" || len(view.Indexes) != 0 {
		t.Errorf("unexpected view entry: %+v", view)
	}
	coll := got.Tables[1]
	if coll.Kind != "collection" || len(coll.Indexes) != 2 {
		t.Fatalf("unexpected collection entry: %+v", coll)
	}
	if !coll.Indexes[0].Primary || coll.Indexes[0].Name != "_id_" {
		t.Errorf("_id_ index not marked primary: %+v", coll.Indexes[0])
	}
	if !coll.Indexes[1].Unique || !strings.Contains(coll.Indexes[1].Definition, `"customer":1`) {
		t.Errorf("unexpected secondary index: %+v", coll.Indexes[1])
	}
}

func TestMongoRelationsEmpty(t *testing.T) {
	t.Parallel()
	got, err := CollectRelations(context.Background(), "mongodb://x/orders", "", mongoOptions(&fakeMongo{}))
	if err != nil {
		t.Fatalf("CollectRelations: %v", err)
	}
	if got.Engine != EngineMongo || len(got.ForeignKeys) != 0 {
		t.Errorf("expected empty mongo relations, got %+v", got)
	}
}

func TestMongoStats(t *testing.T) {
	t.Parallel()
	f := &fakeMongo{
		collections: map[string][]map[string]any{
			"orders": {
				{"name": "invoices", "type": "collection"},
				{"name": "events", "type": "collection"},
				{"name": "billing_view", "type": "view"},
			},
		},
		commands: map[string]map[string]any{
			"orders/collStats": {"count": int64(1500), "storageSize": int64(2048000), "totalIndexSize": int64(512000)},
		},
	}
	got, err := CollectStats(context.Background(), "mongodb://x/orders", "", mongoOptions(f))
	if err != nil {
		t.Fatalf("CollectStats: %v", err)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("views not skipped: %+v", got.Tables)
	}
	if got.Tables[0].LiveRows != 1500 || got.Tables[0].TotalBytes != 2560000 {
		t.Errorf("unexpected stats row: %+v", got.Tables[0])
	}
}

func TestMongoSample(t *testing.T) {
	t.Parallel()
	f := &fakeMongo{
		collections: map[string][]map[string]any{
			"orders": {{"name": "invoices", "type": "collection"}},
		},
		docs: map[string][]map[string]any{
			"orders.invoices": {
				{"_id": "a1", "total": int64(42), "customer": "ada"},
				{"_id": "a2", "customer": "lin", "flagged": true},
			},
		},
	}
	got, err := SampleTable(context.Background(), "mongodb://x", "orders.invoices", 0, mongoOptions(f))
	if err != nil {
		t.Fatalf("SampleTable: %v", err)
	}
	if got.Schema != "orders" || got.Table != "invoices" || got.Limit != 10 || f.findLimit != 10 {
		t.Errorf("unexpected report basics: %+v (find limit %d)", got, f.findLimit)
	}
	wantCols := []string{"_id", "customer", "flagged", "total"}
	if strings.Join(got.Columns, ",") != strings.Join(wantCols, ",") {
		t.Fatalf("columns = %v, want %v (_id first, rest sorted)", got.Columns, wantCols)
	}
	if got.Rows[0][3] != int64(42) || got.Rows[0][2] != nil {
		t.Errorf("row 0 misaligned: %+v", got.Rows[0])
	}
	if got.Rows[1][1] != "lin" || got.Rows[1][3] != nil {
		t.Errorf("row 1 misaligned: %+v", got.Rows[1])
	}
}

func TestMongoSampleErrors(t *testing.T) {
	t.Parallel()
	f := &fakeMongo{collections: map[string][]map[string]any{"orders": {}}}
	if _, err := SampleTable(context.Background(), "mongodb://x", "invoices", 5, mongoOptions(f)); err == nil ||
		!strings.Contains(err.Error(), "needs a database") {
		t.Errorf("expected needs-a-database error, got %v", err)
	}
	if _, err := SampleTable(context.Background(), "mongodb://x/orders", "invoices", 5, mongoOptions(f)); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestNormalizeMongoValue(t *testing.T) {
	t.Parallel()
	doc := bson.M{
		"nested": bson.D{{Key: "a", Value: bson.A{bson.D{{Key: "b", Value: int32(1)}}}}},
	}
	got := normalizeMongoDoc(doc)
	nested, ok := got["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested bson.D not converted: %T", got["nested"])
	}
	arr, ok := nested["a"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("bson.A not converted: %+v", nested["a"])
	}
	if inner, ok := arr[0].(map[string]any); !ok || inner["b"] != int32(1) {
		t.Errorf("inner doc not converted: %+v", arr[0])
	}
}
