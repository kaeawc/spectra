package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/dbinspect"
	"github.com/kaeawc/spectra/internal/netstate"
)

type fakeDBInspector struct {
	overview   *dbinspect.Overview
	sample     *dbinspect.SampleReport
	sampledDSN string
}

func (f *fakeDBInspector) DiscoverDBEnv() []dbinspect.EnvHint {
	return []dbinspect.EnvHint{{Name: "DATABASE_URL", Value: "postgres://app:[redacted]@db/orders", Engine: dbinspect.EnginePostgres}}
}

func (f *fakeDBInspector) DiscoverSQLiteFiles() []dbinspect.FileCandidate {
	return []dbinspect.FileCandidate{{Engine: dbinspect.EngineSQLite, PID: 200, Command: "notes-app", Path: "/Users/x/Library/notes.sqlite"}}
}

func (f *fakeDBInspector) Overview(context.Context, string) (*dbinspect.Overview, error) {
	return f.overview, nil
}

func (f *fakeDBInspector) Schema(context.Context, string, string) (*dbinspect.SchemaReport, error) {
	return &dbinspect.SchemaReport{Engine: dbinspect.EnginePostgres}, nil
}

func (f *fakeDBInspector) Relations(context.Context, string, string) (*dbinspect.RelationsReport, error) {
	return &dbinspect.RelationsReport{Engine: dbinspect.EnginePostgres}, nil
}

func (f *fakeDBInspector) Stats(context.Context, string, string) (*dbinspect.StatsReport, error) {
	return &dbinspect.StatsReport{Engine: dbinspect.EnginePostgres}, nil
}

func (f *fakeDBInspector) Sample(_ context.Context, dsn, _ string, _ int) (*dbinspect.SampleReport, error) {
	f.sampledDSN = dsn
	return f.sample, nil
}

func newDBTestServer(db *fakeDBInspector) *Server {
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetCollectors(Collectors{
		DB: db,
		Network: &fakeNetworkCollector{conns: []netstate.Connection{
			{PID: 100, Command: "orders-api", Proto: "tcp", LocalAddr: "127.0.0.1:53000", RemoteAddr: "10.0.0.5:5432", State: "established"},
		}},
		Clock: fixedClock{t: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)},
	})
	return s
}

func TestToolDBDiscover(t *testing.T) {
	s := newDBTestServer(&fakeDBInspector{})
	result := s.toolDB(json.RawMessage(`{"operation": "discover"}`))
	if result.IsError {
		t.Fatalf("discover failed: %+v", result.Content)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "1 live database connection(s)") || !strings.Contains(text, "1 connection env var(s)") {
		t.Fatalf("unexpected discover summary: %s", text)
	}
	if !strings.Contains(text, "10.0.0.5:5432") || !strings.Contains(text, "[redacted]") {
		t.Fatalf("expected candidate and redacted env hint in raw payload: %s", text)
	}
}

func TestToolDBOverview(t *testing.T) {
	s := newDBTestServer(&fakeDBInspector{overview: &dbinspect.Overview{
		Engine:        dbinspect.EnginePostgres,
		ServerVersion: "16.3",
		Database:      "orders",
		Schemas:       []dbinspect.SchemaSummary{{Name: "public", TableCount: 12}},
	}})
	result := s.toolDB(json.RawMessage(`{"operation": "overview"}`))
	if result.IsError {
		t.Fatalf("overview failed: %+v", result.Content)
	}
	if !strings.Contains(result.Content[0].Text, `postgres 16.3 database \"orders\"`) &&
		!strings.Contains(result.Content[0].Text, `postgres 16.3 database "orders"`) {
		t.Fatalf("unexpected overview summary: %s", result.Content[0].Text)
	}
}

func TestToolDBSampleRequiresConfirmation(t *testing.T) {
	db := &fakeDBInspector{sample: &dbinspect.SampleReport{Schema: "public", Table: "orders"}}
	s := newDBTestServer(db)

	result := s.toolDB(json.RawMessage(`{"operation": "sample", "table": "orders"}`))
	if !result.IsError || !strings.Contains(result.Content[0].Text, "confirm_sensitive") {
		t.Fatalf("expected confirm_sensitive refusal, got: %+v", result.Content)
	}
	if db.sampledDSN != "" {
		t.Fatal("sample ran without confirmation")
	}

	result = s.toolDB(json.RawMessage(`{"operation": "sample", "table": "orders", "confirm_sensitive": true, "dsn": "postgres://x"}`))
	if result.IsError {
		t.Fatalf("confirmed sample failed: %+v", result.Content)
	}
	if db.sampledDSN != "postgres://x" {
		t.Fatalf("sample did not receive dsn: %q", db.sampledDSN)
	}
}

func TestToolDBUnknownOperation(t *testing.T) {
	s := newDBTestServer(&fakeDBInspector{})
	result := s.toolDB(json.RawMessage(`{"operation": "drop_tables"}`))
	if !result.IsError || !strings.Contains(result.Content[0].Text, "unknown db operation") {
		t.Fatalf("expected unknown-operation error, got: %+v", result.Content)
	}
}
