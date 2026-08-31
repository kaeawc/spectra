package dbinspect

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeRedis struct {
	info         string
	config       map[string]string
	keys         []string // returned by Scan in batches
	types        map[string]string
	ttls         map[string]int64
	values       map[string]any
	typeCalls    int
	disconnected bool
}

func (f *fakeRedis) Info(context.Context, ...string) (string, error) { return f.info, nil }

func (f *fakeRedis) ConfigGet(context.Context, string) (map[string]string, error) {
	return f.config, nil
}

func (f *fakeRedis) Scan(_ context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	var matched []string
	for _, k := range f.keys {
		if match == "*" || strings.HasPrefix(k, strings.TrimSuffix(match, "*")) {
			matched = append(matched, k)
		}
	}
	start := int(cursor)
	if start >= len(matched) {
		return nil, 0, nil
	}
	end := start + int(count)
	if end >= len(matched) {
		return matched[start:], 0, nil
	}
	return matched[start:end], uint64(end), nil
}

func (f *fakeRedis) Type(_ context.Context, key string) (string, error) {
	f.typeCalls++
	if t, ok := f.types[key]; ok {
		return t, nil
	}
	return "none", nil
}

func (f *fakeRedis) TTLSeconds(_ context.Context, key string) (int64, error) {
	if ttl, ok := f.ttls[key]; ok {
		return ttl, nil
	}
	return -1, nil
}

func (f *fakeRedis) ReadValue(_ context.Context, key, _ string, _ int) (any, error) {
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("fakeRedis: no value for %s", key)
}

func (f *fakeRedis) Disconnect(context.Context) error {
	f.disconnected = true
	return nil
}

func redisOptions(f *fakeRedis) Options {
	return Options{ConnectRedis: func(context.Context, string) (RedisRunner, error) { return f, nil }}
}

const redisInfoFixture = `# Server
redis_version:7.4.1
role:master
# Clients
connected_clients:9
# Memory
used_memory:52428800
# Keyspace
db0:keys=120,expires=30,avg_ttl=60000
db2:keys=5,expires=0,avg_ttl=0`

func TestResolveEngineRedis(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{"redis://:pw@cache.internal:6379/0", "rediss://cache.internal:6380"} {
		if got := resolveEngine(dsn, Options{}); got != EngineRedis {
			t.Errorf("resolveEngine(%q) = %q, want redis", dsn, got)
		}
	}
}

func TestRedisOverview(t *testing.T) {
	t.Parallel()
	f := &fakeRedis{info: redisInfoFixture, config: map[string]string{"maxclients": "10000"}}
	got, err := CollectOverview(context.Background(), "redis://cache:6379/2", redisOptions(f))
	if err != nil {
		t.Fatalf("CollectOverview: %v", err)
	}
	if got.Engine != EngineRedis || got.ServerVersion != "7.4.1" || got.Database != "db2" {
		t.Errorf("unexpected overview: %+v", got)
	}
	if got.ReadOnlySession {
		t.Error("master role misreported as read-only replica")
	}
	if got.SizeBytes != 52428800 || got.Connections != 9 || got.MaxConnections != 10000 {
		t.Errorf("unexpected capacity fields: %+v", got)
	}
	if len(got.Schemas) != 2 || got.Schemas[0].Name != "db0" || got.Schemas[0].TableCount != 120 {
		t.Errorf("unexpected keyspace schemas: %+v", got.Schemas)
	}
	if !f.disconnected {
		t.Error("client not disconnected")
	}
}

func TestGeneralizeKeyPattern(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"session:1f9acd42e77b4a10", "session:*"},
		{"user:42:cart", "user:*:cart"},
		{"user:42:items:9", "user:*:items:*"},
		{"config", "config"},
		{"queue:emails", "queue:emails"},
		{"job:550e8400-e29b-41d4-a716-446655440000", "job:*"},
	}
	for _, tt := range tests {
		if got := generalizeKeyPattern(tt.in); got != tt.want {
			t.Errorf("generalizeKeyPattern(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRedisSchema(t *testing.T) {
	t.Parallel()
	f := &fakeRedis{
		keys:  []string{"session:aa11bb22", "session:cc33dd44", "user:1:cart", "user:2:cart", "session:ee55ff66", "config"},
		types: map[string]string{"session:aa11bb22": "hash", "user:1:cart": "list", "config": "string"},
	}
	got, err := CollectSchema(context.Background(), "redis://cache:6379", "", redisOptions(f))
	if err != nil {
		t.Fatalf("CollectSchema: %v", err)
	}
	if len(got.Tables) != 3 {
		t.Fatalf("expected 3 patterns, got %+v", got.Tables)
	}
	top := got.Tables[0]
	if top.Name != "session:*" || top.EstimatedRows != 3 || top.Kind != "hash" || top.Schema != "db0" {
		t.Errorf("unexpected top pattern: %+v", top)
	}
	if got.Tables[1].Name != "user:*:cart" || got.Tables[1].Kind != "list" {
		t.Errorf("unexpected second pattern: %+v", got.Tables[1])
	}
}

func TestRedisStats(t *testing.T) {
	t.Parallel()
	f := &fakeRedis{info: redisInfoFixture}
	got, err := CollectStats(context.Background(), "redis://cache:6379", "", redisOptions(f))
	if err != nil {
		t.Fatalf("CollectStats: %v", err)
	}
	if len(got.Tables) != 2 || got.Tables[0].Name != "db0" || got.Tables[0].LiveRows != 120 {
		t.Errorf("unexpected keyspace stats: %+v", got.Tables)
	}
}

func TestRedisRelationsEmpty(t *testing.T) {
	t.Parallel()
	got, err := CollectRelations(context.Background(), "redis://cache:6379", "", redisOptions(&fakeRedis{}))
	if err != nil || len(got.ForeignKeys) != 0 {
		t.Errorf("expected empty redis relations, got %+v, %v", got, err)
	}
}

func TestRedisSamplePattern(t *testing.T) {
	t.Parallel()
	f := &fakeRedis{
		keys:   []string{"session:a", "session:b"},
		types:  map[string]string{"session:a": "hash", "session:b": "hash"},
		ttls:   map[string]int64{"session:a": 3600, "session:b": -1},
		values: map[string]any{"session:a": map[string]string{"user": "ada"}, "session:b": map[string]string{"user": "lin"}},
	}
	got, err := SampleTable(context.Background(), "redis://cache:6379", "session:*", 0, redisOptions(f))
	if err != nil {
		t.Fatalf("SampleTable: %v", err)
	}
	if strings.Join(got.Columns, ",") != "key,type,ttl_seconds,value" {
		t.Fatalf("unexpected columns: %v", got.Columns)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %+v", got.Rows)
	}
	if got.Rows[0][0] != "session:a" || got.Rows[0][1] != "hash" || got.Rows[0][2] != int64(3600) {
		t.Errorf("unexpected row: %+v", got.Rows[0])
	}
	if got.Rows[1][2] != nil {
		t.Errorf("no-expiry TTL should be nil: %+v", got.Rows[1])
	}
}

func TestRedisSampleMissingKey(t *testing.T) {
	t.Parallel()
	f := &fakeRedis{}
	if _, err := SampleTable(context.Background(), "redis://cache:6379", "nope", 5, redisOptions(f)); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
	if _, err := SampleTable(context.Background(), "redis://cache:6379", "nope:*", 5, redisOptions(f)); err == nil ||
		!strings.Contains(err.Error(), "no keys match") {
		t.Errorf("expected no-match error, got %v", err)
	}
}
