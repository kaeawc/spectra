package dbinspect

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/netstate"
)

func TestRedactDSN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"url with password", "postgres://app:s3cret@db.internal:5432/orders?sslmode=require",
			"postgres://app:[redacted]@db.internal:5432/orders?sslmode=require"},
		{"url without password", "postgres://app@db.internal/orders",
			"postgres://app@db.internal/orders"},
		{"keyword form", "host=db.internal user=app password=s3cret dbname=orders",
			"host=db.internal user=app password=[redacted] dbname=orders"},
		{"bare string", "s3cret", "[redacted]"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		if got := RedactDSN(tt.in); got != tt.want {
			t.Errorf("%s: RedactDSN(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestDiscoverConnections(t *testing.T) {
	t.Parallel()
	conns := []netstate.Connection{
		{PID: 101, Command: "orders-api", Proto: "tcp", LocalAddr: "127.0.0.1:52344", RemoteAddr: "10.0.0.5:5432", State: "established"},
		{PID: 102, Command: "reports", Proto: "tcp", LocalAddr: "[::1]:52345", RemoteAddr: "[::1]:3306", State: "established"},
		{PID: 103, Command: "browser", Proto: "tcp", LocalAddr: "127.0.0.1:52346", RemoteAddr: "142.250.1.1:443", State: "established"},
		{PID: 104, Command: "dns", Proto: "udp", LocalAddr: "127.0.0.1:5353", RemoteAddr: ""},
	}
	got := DiscoverConnections(conns)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %+v", len(got), got)
	}
	if got[0].Engine != EnginePostgres || got[0].PID != 101 || got[0].Port != 5432 {
		t.Errorf("unexpected postgres candidate: %+v", got[0])
	}
	if got[1].Engine != EngineMySQL || got[1].Port != 3306 {
		t.Errorf("unexpected mysql candidate: %+v", got[1])
	}
}

func TestDiscoverEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"DATABASE_URL": "postgres://app:s3cret@db.internal/orders",
		"PGPASSWORD":   "s3cret",
		"PGHOST":       "db.internal",
	}
	got := DiscoverEnv(func(k string) string { return env[k] })
	if len(got) != 3 {
		t.Fatalf("expected 3 hints, got %d: %+v", len(got), got)
	}
	byName := map[string]EnvHint{}
	for _, h := range got {
		byName[h.Name] = h
	}
	if h := byName["DATABASE_URL"]; h.Engine != EnginePostgres || strings.Contains(h.Value, "s3cret") {
		t.Errorf("DATABASE_URL hint not redacted or engine wrong: %+v", h)
	}
	if h := byName["PGPASSWORD"]; h.Value != "[redacted]" {
		t.Errorf("PGPASSWORD value leaked: %+v", h)
	}
	if h := byName["PGHOST"]; h.Value != "db.internal" {
		t.Errorf("PGHOST should keep its value: %+v", h)
	}
}

// fakeRows replays canned rows and assigns values by pointer type.
type fakeRows struct {
	cols   []string
	rows   [][]any
	cursor int
}

func (r *fakeRows) Next() bool {
	if r.cursor >= len(r.rows) {
		return false
	}
	r.cursor++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.cursor-1]
	if len(dest) != len(row) {
		return fmt.Errorf("scan: %d dests for %d values", len(dest), len(row))
	}
	for i, d := range dest {
		if err := assign(d, row[i]); err != nil {
			return fmt.Errorf("scan col %d: %w", i, err)
		}
	}
	return nil
}

func (r *fakeRows) Values() ([]any, error) { return r.rows[r.cursor-1], nil }
func (r *fakeRows) Columns() []string      { return r.cols }
func (r *fakeRows) Err() error             { return nil }
func (r *fakeRows) Close()                 {}

func assign(dest, v any) error {
	switch d := dest.(type) {
	case *string:
		*d = v.(string)
	case *bool:
		*d = v.(bool)
	case *int:
		*d = v.(int)
	case *int64:
		*d = v.(int64)
	case *[]string:
		*d = v.([]string)
	default:
		return fmt.Errorf("unsupported dest %T", dest)
	}
	return nil
}

// fakeConn returns canned rows for queries matched by substring.
type fakeConn struct {
	results map[string]*fakeRows
	queries []string
	args    [][]any
	closed  bool
}

func (c *fakeConn) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	c.queries = append(c.queries, sql)
	c.args = append(c.args, args)
	for key, rows := range c.results {
		if strings.Contains(sql, key) {
			copied := *rows
			copied.cursor = 0
			return &copied, nil
		}
	}
	return nil, fmt.Errorf("fakeConn: no canned result for query: %s", sql)
}

func (c *fakeConn) Close(context.Context) error {
	c.closed = true
	return nil
}

func fakeOptions(c *fakeConn) Options {
	return Options{Connect: func(context.Context, string) (Conn, error) { return c, nil }}
}

func TestCollectOverview(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"pg_database_size": {rows: [][]any{
			{"16.3", "orders", "app", true, int64(52428800), int64(7), 100},
		}},
		"GROUP BY n.nspname": {rows: [][]any{
			{"billing", 4},
			{"public", 12},
		}},
	}}
	got, err := CollectOverview(context.Background(), "postgres://x", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectOverview: %v", err)
	}
	if got.ServerVersion != "16.3" || got.Database != "orders" || !got.ReadOnlySession {
		t.Errorf("unexpected overview: %+v", got)
	}
	if got.Connections != 7 || got.MaxConnections != 100 || got.SizeBytes != 52428800 {
		t.Errorf("unexpected capacity fields: %+v", got)
	}
	if len(got.Schemas) != 2 || got.Schemas[1].TableCount != 12 {
		t.Errorf("unexpected schemas: %+v", got.Schemas)
	}
	if !conn.closed {
		t.Error("connection not closed")
	}
}

func TestCollectSchema(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"CASE c.relkind": {rows: [][]any{
			{"public", "orders", "table", int64(1500), int64(8192000)},
		}},
		"pg_attribute a": {rows: [][]any{
			{"public", "orders", "id", "bigint", false, "nextval('orders_id_seq')", true},
			{"public", "orders", "customer_email", "text", true, "", false},
		}},
		"pg_get_indexdef": {rows: [][]any{
			{"public", "orders", "orders_pkey", "CREATE UNIQUE INDEX orders_pkey ON public.orders USING btree (id)", true, true},
		}},
	}}
	got, err := CollectSchema(context.Background(), "postgres://x", "public", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectSchema: %v", err)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(got.Tables))
	}
	tbl := got.Tables[0]
	if tbl.EstimatedRows != 1500 || len(tbl.Columns) != 2 || len(tbl.Indexes) != 1 {
		t.Errorf("unexpected table: %+v", tbl)
	}
	if !tbl.Columns[0].PrimaryKey || tbl.Columns[1].Name != "customer_email" {
		t.Errorf("unexpected columns: %+v", tbl.Columns)
	}
	for _, args := range conn.args {
		if len(args) != 1 || args[0] != "public" {
			t.Errorf("schema filter not bound: %v", args)
		}
	}
}

func TestCollectRelations(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"pg_constraint": {rows: [][]any{
			{"orders_customer_fk", "public", "orders", []string{"customer_id"},
				"public", "customers", []string{"id"}, "c", "a"},
		}},
	}}
	got, err := CollectRelations(context.Background(), "postgres://x", "", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectRelations: %v", err)
	}
	if len(got.ForeignKeys) != 1 {
		t.Fatalf("expected 1 foreign key, got %d", len(got.ForeignKeys))
	}
	fk := got.ForeignKeys[0]
	if fk.OnDelete != "cascade" || fk.OnUpdate != "no action" {
		t.Errorf("action codes not expanded: %+v", fk)
	}
	if fk.FromColumns[0] != "customer_id" || fk.ToTable != "customers" {
		t.Errorf("unexpected foreign key: %+v", fk)
	}
}

func TestCollectStats(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"pg_stat_user_tables": {rows: [][]any{
			{"public", "orders", int64(12000), int64(340), int64(1500), int64(90),
				int64(8192000), "2026-08-29T02:11:00Z", ""},
		}},
	}}
	got, err := CollectStats(context.Background(), "postgres://x", "", fakeOptions(conn))
	if err != nil {
		t.Fatalf("CollectStats: %v", err)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(got.Tables))
	}
	if got.Tables[0].SeqScans != 12000 || got.Tables[0].LastVacuum != "2026-08-29T02:11:00Z" {
		t.Errorf("unexpected stats: %+v", got.Tables[0])
	}
}

func TestSampleTable(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	conn := &fakeConn{results: map[string]*fakeRows{
		"to_regclass": {rows: [][]any{{"public", `weird"name`}}},
		"SELECT * FROM": {
			cols: []string{"id", "payload", "created_at"},
			rows: [][]any{{int64(1), []byte{0xde, 0xad}, when}},
		},
	}}
	got, err := SampleTable(context.Background(), "postgres://x", `public.weird"name`, 0, fakeOptions(conn))
	if err != nil {
		t.Fatalf("SampleTable: %v", err)
	}
	if got.Limit != 10 {
		t.Errorf("default limit = %d, want 10", got.Limit)
	}
	if got.Schema != "public" || got.Table != `weird"name` {
		t.Errorf("unexpected resolved table: %+v", got)
	}
	if len(got.Rows) != 1 || got.Rows[0][1] != `\xdead` || got.Rows[0][2] != "2026-08-30T12:00:00Z" {
		t.Errorf("unexpected rows: %+v", got.Rows)
	}
	sampleSQL := conn.queries[len(conn.queries)-1]
	if !strings.Contains(sampleSQL, `"public"."weird""name"`) {
		t.Errorf("identifier not quote-escaped: %s", sampleSQL)
	}
	if !strings.Contains(sampleSQL, "LIMIT $1") {
		t.Errorf("limit not bound: %s", sampleSQL)
	}
}

func TestSampleTableCapsLimit(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"to_regclass":   {rows: [][]any{{"public", "orders"}}},
		"SELECT * FROM": {cols: []string{"id"}},
	}}
	got, err := SampleTable(context.Background(), "postgres://x", "orders", 99999, fakeOptions(conn))
	if err != nil {
		t.Fatalf("SampleTable: %v", err)
	}
	if got.Limit != maxSampleLimit {
		t.Errorf("limit = %d, want cap %d", got.Limit, maxSampleLimit)
	}
}

func TestSampleTableNotFound(t *testing.T) {
	t.Parallel()
	conn := &fakeConn{results: map[string]*fakeRows{
		"to_regclass": {},
	}}
	_, err := SampleTable(context.Background(), "postgres://x", "nope", 5, fakeOptions(conn))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}
