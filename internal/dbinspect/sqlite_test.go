package dbinspect

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteDriverDSN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"absolute url", "sqlite:///var/db/app.db",
			"file:/var/db/app.db?" + sqliteReadOnlyParams, false},
		{"relative url", "sqlite://app.db",
			"file:app.db?" + sqliteReadOnlyParams, false},
		{"file uri drops caller params", "file:/var/db/app.db?mode=rw",
			"file:/var/db/app.db?" + sqliteReadOnlyParams, false},
		{"bare path", "/var/db/app.sqlite3",
			"file:/var/db/app.sqlite3?" + sqliteReadOnlyParams, false},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		got, err := sqliteDriverDSN(tt.in)
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
			t.Errorf("%s: sqliteDriverDSN(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

// createSQLiteFixture writes a real database file: two tables, a composite
// of PK/nullable/default column shapes, an index, a view, a foreign key,
// rows, and ANALYZE output.
func createSQLiteFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`,
		`CREATE TABLE orders (
			id INTEGER PRIMARY KEY,
			customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
			note TEXT DEFAULT 'pending',
			payload BLOB
		)`,
		`CREATE INDEX idx_orders_customer ON orders(customer_id)`,
		`CREATE VIEW order_emails AS
			SELECT o.id, c.email FROM orders o JOIN customers c ON c.id = o.customer_id`,
		`INSERT INTO customers (id, email) VALUES (1, 'a@example.com'), (2, 'b@example.com')`,
		`INSERT INTO orders (id, customer_id, note, payload) VALUES
			(1, 1, 'first', X'DEAD'), (2, 2, NULL, NULL)`,
		`ANALYZE`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return path
}

func TestSQLiteOverviewEndToEnd(t *testing.T) {
	t.Parallel()
	path := createSQLiteFixture(t)
	got, err := CollectOverview(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("CollectOverview: %v", err)
	}
	if got.Engine != EngineSQLite || got.ServerVersion == "" {
		t.Errorf("unexpected overview basics: %+v", got)
	}
	if !got.ReadOnlySession {
		t.Error("query_only not reported for the session")
	}
	if got.SizeBytes <= 0 {
		t.Errorf("size not computed: %d", got.SizeBytes)
	}
	if len(got.Schemas) != 1 || got.Schemas[0].Name != "main" || got.Schemas[0].TableCount != 2 {
		t.Errorf("unexpected schemas: %+v", got.Schemas)
	}
}

func TestSQLiteSchemaEndToEnd(t *testing.T) {
	t.Parallel()
	path := createSQLiteFixture(t)
	got, err := CollectSchema(context.Background(), "sqlite://"+path, "", Options{})
	if err != nil {
		t.Fatalf("CollectSchema: %v", err)
	}
	byName := map[string]Table{}
	for _, tbl := range got.Tables {
		byName[tbl.Name] = tbl
	}
	if len(byName) != 3 {
		t.Fatalf("expected customers, orders, order_emails; got %+v", got.Tables)
	}
	orders := byName["orders"]
	if orders.Kind != "table" || len(orders.Columns) != 4 {
		t.Fatalf("unexpected orders table: %+v", orders)
	}
	// INTEGER PRIMARY KEY is a rowid alias; pragma_table_info reports it
	// notnull=0 even though it can never be NULL, so only pk is asserted.
	if !orders.Columns[0].PrimaryKey {
		t.Errorf("id column flags wrong: %+v", orders.Columns[0])
	}
	if orders.Columns[1].Nullable || orders.Columns[1].PrimaryKey {
		t.Errorf("customer_id column flags wrong: %+v", orders.Columns[1])
	}
	if orders.Columns[2].Default != "'pending'" || !orders.Columns[2].Nullable {
		t.Errorf("note column wrong: %+v", orders.Columns[2])
	}
	var idx *Index
	for i := range orders.Indexes {
		if orders.Indexes[i].Name == "idx_orders_customer" {
			idx = &orders.Indexes[i]
		}
	}
	if idx == nil || idx.Unique || !strings.Contains(idx.Definition, "CREATE INDEX") {
		t.Errorf("index missing or wrong: %+v", orders.Indexes)
	}
	if byName["order_emails"].Kind != "view" {
		t.Errorf("view not classified: %+v", byName["order_emails"])
	}
	if empty, err := CollectSchema(context.Background(), path, "other", Options{}); err != nil || len(empty.Tables) != 0 {
		t.Errorf("non-main schema filter should be empty: %+v, %v", empty, err)
	}
}

func TestSQLiteRelationsEndToEnd(t *testing.T) {
	t.Parallel()
	path := createSQLiteFixture(t)
	got, err := CollectRelations(context.Background(), path, "", Options{})
	if err != nil {
		t.Fatalf("CollectRelations: %v", err)
	}
	if len(got.ForeignKeys) != 1 {
		t.Fatalf("expected 1 foreign key, got %+v", got.ForeignKeys)
	}
	fk := got.ForeignKeys[0]
	if fk.FromTable != "orders" || fk.ToTable != "customers" || fk.OnDelete != "cascade" {
		t.Errorf("unexpected foreign key: %+v", fk)
	}
	if len(fk.FromColumns) != 1 || fk.FromColumns[0] != "customer_id" {
		t.Errorf("unexpected columns: %+v", fk)
	}
}

func TestSQLiteStatsEndToEnd(t *testing.T) {
	t.Parallel()
	path := createSQLiteFixture(t)
	got, err := CollectStats(context.Background(), path, "", Options{})
	if err != nil {
		t.Fatalf("CollectStats: %v", err)
	}
	rows := map[string]TableStats{}
	for _, s := range got.Tables {
		rows[s.Name] = s
	}
	if len(rows) != 2 {
		t.Fatalf("expected stats for 2 tables, got %+v", got.Tables)
	}
	// orders has an index, so ANALYZE recorded a row estimate for it.
	if rows["orders"].LiveRows != 2 {
		t.Errorf("orders row estimate = %d, want 2 (from sqlite_stat1)", rows["orders"].LiveRows)
	}
}

func TestSQLiteSampleEndToEnd(t *testing.T) {
	t.Parallel()
	path := createSQLiteFixture(t)
	got, err := SampleTable(context.Background(), path, "main.orders", 0, Options{})
	if err != nil {
		t.Fatalf("SampleTable: %v", err)
	}
	if got.Schema != "main" || got.Table != "orders" || got.Limit != 10 {
		t.Errorf("unexpected report basics: %+v", got)
	}
	if len(got.Columns) != 4 || got.Columns[3] != "payload" {
		t.Errorf("unexpected columns: %+v", got.Columns)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %+v", got.Rows)
	}
	if got.Rows[0][3] != `\xdead` {
		t.Errorf("blob not hex-rendered: %+v", got.Rows[0])
	}
	if got.Rows[1][2] != nil {
		t.Errorf("NULL not preserved: %+v", got.Rows[1])
	}
	if _, err := SampleTable(context.Background(), path, "nope", 5, Options{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestSQLiteSessionRefusesWrites(t *testing.T) {
	t.Parallel()
	path := createSQLiteFixture(t)
	conn, err := ConnectSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("ConnectSQLite: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	rows, err := conn.Query(context.Background(), "DELETE FROM orders")
	if err == nil {
		rows.Close()
		t.Fatal("write through read-only session unexpectedly succeeded")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "readonly") &&
		!strings.Contains(strings.ToLower(err.Error()), "read-only") {
		t.Errorf("expected readonly refusal, got: %v", err)
	}
}

func TestSQLiteConnectMissingFile(t *testing.T) {
	t.Parallel()
	_, err := ConnectSQLite(context.Background(), filepath.Join(t.TempDir(), "missing.db"))
	if err == nil {
		t.Fatal("expected error opening missing database file")
	}
}

func TestParseLSOFSQLiteFiles(t *testing.T) {
	t.Parallel()
	const out = `COMMAND   PID  USER   FD   TYPE  DEVICE  SIZE/OFF  NODE NAME
notes     314  alice  12u  REG   1,4     32768     99   /Users/alice/Library/Group Containers/notes.sqlite
notes     314  alice  13u  REG   1,4     8192      100  /Users/alice/Library/Group Containers/notes.sqlite-wal
chrome    512  alice  40u  REG   1,4     4096      101  /Users/alice/Library/Caches/History.db
chrome    512  alice  41u  IPv4  0xdead  0t0       TCP  1.2.3.4:443 (ESTABLISHED)
daemon    600  root   5u   REG   1,4     1024      102  /var/log/system.log`
	got := parseLSOFSQLiteFiles(out)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %+v", got)
	}
	if got[0].Path != "/Users/alice/Library/Group Containers/notes.sqlite" || got[0].PID != 314 {
		t.Errorf("space-containing path or sidecar dedup wrong: %+v", got[0])
	}
	if got[1].Command != "chrome" || got[1].Path != "/Users/alice/Library/Caches/History.db" {
		t.Errorf("unexpected second candidate: %+v", got[1])
	}
}
