package dbinspect

import (
	"context"
	"strings"
	"testing"
)

func TestResolveEngine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		dsn  string
		opts Options
		want Engine
	}{
		{"mysql url", "mysql://app:pw@db:3306/orders", Options{}, EngineMySQL},
		{"driver form", "app:pw@tcp(db:3306)/orders", Options{}, EngineMySQL},
		{"postgres url", "postgres://app@db/orders", Options{}, EnginePostgres},
		{"keyword form", "host=db user=app dbname=orders", Options{}, EnginePostgres},
		{"empty falls back to postgres env", "", Options{}, EnginePostgres},
		{"explicit override wins", "postgres://app@db/orders", Options{Engine: EngineMySQL}, EngineMySQL},
	}
	for _, tt := range tests {
		if got := resolveEngine(tt.dsn, tt.opts); got != tt.want {
			t.Errorf("%s: resolveEngine(%q) = %q, want %q", tt.name, tt.dsn, got, tt.want)
		}
	}
}

func TestMySQLDriverDSN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"url with port and params", "mysql://app:pw@db.internal:3307/orders?tls=true",
			"app:pw@tcp(db.internal:3307)/orders?tls=true", false},
		{"url default port", "mysql://app@db.internal/orders",
			"app@tcp(db.internal:3306)/orders", false},
		{"native form passthrough", "app:pw@tcp(db:3306)/orders",
			"app:pw@tcp(db:3306)/orders", false},
		{"wrong scheme", "postgres://app@db/orders", "", true},
	}
	for _, tt := range tests {
		got, err := mysqlDriverDSN(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got %q", tt.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s: mysqlDriverDSN(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestCollectOverviewMySQL(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"PROCESSLIST": {rows: [][]any{
			{"8.4.0", "orders", "app@%", int64(52428800), int64(4), 200},
		}},
		"@@session.transaction_read_only": {rows: [][]any{{int64(1)}}},
		"GROUP BY table_schema": {rows: [][]any{
			{"orders", 9},
		}},
	}}
	got, err := CollectOverview(context.Background(), "mysql://x/orders", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectOverview: %v", err)
	}
	if got.Engine != EngineMySQL || got.ServerVersion != "8.4.0" || got.Database != "orders" {
		t.Errorf("unexpected overview: %+v", got)
	}
	if !got.ReadOnlySession {
		t.Error("read-only session flag not set from @@transaction_read_only")
	}
	if got.Connections != 4 || got.MaxConnections != 200 || got.SizeBytes != 52428800 {
		t.Errorf("unexpected capacity fields: %+v", got)
	}
	if len(got.Schemas) != 1 || got.Schemas[0].TableCount != 9 {
		t.Errorf("unexpected schemas: %+v", got.Schemas)
	}
	if !conn.closed {
		t.Error("connection not closed")
	}
}

func TestCollectSchemaMySQL(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"CASE table_type": {rows: [][]any{
			{"orders", "invoices", "table", int64(1200), int64(4096000)},
		}},
		"information_schema.COLUMNS": {rows: [][]any{
			{"orders", "invoices", "id", "bigint unsigned", "NO", "", "PRI"},
			{"orders", "invoices", "customer_email", "varchar(255)", "YES", "", ""},
		}},
		"information_schema.STATISTICS": {rows: [][]any{
			{"orders", "invoices", "PRIMARY", "id", int64(0)},
			{"orders", "invoices", "idx_email", "customer_email", int64(1)},
		}},
	}}
	got, err := CollectSchema(context.Background(), "mysql://x/orders", "orders", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectSchema: %v", err)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(got.Tables))
	}
	tbl := got.Tables[0]
	if !tbl.Columns[0].PrimaryKey || tbl.Columns[0].Nullable {
		t.Errorf("unexpected id column: %+v", tbl.Columns[0])
	}
	if !tbl.Columns[1].Nullable || tbl.Columns[1].PrimaryKey {
		t.Errorf("unexpected email column: %+v", tbl.Columns[1])
	}
	if !tbl.Indexes[0].Primary || tbl.Indexes[0].Definition != "PRIMARY KEY (id)" {
		t.Errorf("unexpected primary index: %+v", tbl.Indexes[0])
	}
	if tbl.Indexes[1].Unique || tbl.Indexes[1].Definition != "INDEX idx_email (customer_email)" {
		t.Errorf("unexpected secondary index: %+v", tbl.Indexes[1])
	}
	for _, args := range conn.args {
		if len(args) != 2 || args[0] != "orders" || args[1] != "orders" {
			t.Errorf("schema filter not bound twice: %v", args)
		}
	}
}

func TestCollectRelationsMySQL(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"REFERENTIAL_CONSTRAINTS": {rows: [][]any{
			{"fk_invoice_customer", "orders", "invoices", "customer_id,region_id",
				"orders", "customers", "id,region_id", "CASCADE", "NO ACTION"},
		}},
	}}
	got, err := CollectRelations(context.Background(), "mysql://x/orders", "", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectRelations: %v", err)
	}
	if len(got.ForeignKeys) != 1 {
		t.Fatalf("expected 1 foreign key, got %d", len(got.ForeignKeys))
	}
	fk := got.ForeignKeys[0]
	if len(fk.FromColumns) != 2 || fk.FromColumns[1] != "region_id" {
		t.Errorf("composite key not split: %+v", fk)
	}
	if fk.OnDelete != "cascade" || fk.OnUpdate != "no action" {
		t.Errorf("actions not lowercased: %+v", fk)
	}
}

func TestCollectStatsMySQL(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"ORDER BY (data_length + index_length) DESC": {rows: [][]any{
			{"orders", "invoices", int64(1200), int64(4096000)},
		}},
	}}
	got, err := CollectStats(context.Background(), "mysql://x/orders", "", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectStats: %v", err)
	}
	if len(got.Tables) != 1 || got.Tables[0].LiveRows != 1200 || got.Tables[0].TotalBytes != 4096000 {
		t.Errorf("unexpected stats: %+v", got.Tables)
	}
}

func TestSampleTableMySQL(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"FROM information_schema.TABLES": {rows: [][]any{{"orders", "weird`name"}}},
		"SELECT * FROM `": {
			cols: []string{"id", "note"},
			rows: [][]any{{int64(7), "hello"}},
		},
	}}
	got, err := SampleTable(context.Background(), "mysql://x/orders", "orders.weird`name", 0, fakeOptions(conn))
	if err != nil {
		t.Fatalf("SampleTable: %v", err)
	}
	if got.Engine != EngineMySQL || got.Limit != 10 {
		t.Errorf("unexpected report basics: %+v", got)
	}
	if got.Schema != "orders" || got.Table != "weird`name" {
		t.Errorf("unexpected resolved table: %+v", got)
	}
	if len(got.Rows) != 1 || got.Rows[0][1] != "hello" {
		t.Errorf("unexpected rows: %+v", got.Rows)
	}
	resolveArgs := conn.args[0]
	if len(resolveArgs) != 2 || resolveArgs[0] != "orders" || resolveArgs[1] != "weird`name" {
		t.Errorf("qualified name not split into bind args: %v", resolveArgs)
	}
	sampleSQL := conn.queries[len(conn.queries)-1]
	if !strings.Contains(sampleSQL, "`orders`.`weird``name`") {
		t.Errorf("identifier not backtick-escaped: %s", sampleSQL)
	}
	if !strings.Contains(sampleSQL, "LIMIT ?") {
		t.Errorf("limit not bound: %s", sampleSQL)
	}
}

func TestQuoteMySQLIdent(t *testing.T) {
	t.Parallel()
	if got := quoteMySQLIdent("weird`name"); got != "`weird``name`" {
		t.Errorf("quoteMySQLIdent = %q", got)
	}
}
