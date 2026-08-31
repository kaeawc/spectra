package dbinspect

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// The redis engine maps a keyspace onto the relational report types:
// logical databases (db0..db15) play the role of schemas, and generalized
// key patterns ("session:*") of tables. Everything issued is a read: INFO,
// CONFIG GET, SCAN (never KEYS), TYPE, TTL, and bounded per-type value
// reads.

const (
	// redisScanKeyLimit bounds how many keys a schema scan samples.
	redisScanKeyLimit = 1000
	// redisScanBatch is the SCAN COUNT hint per round trip.
	redisScanBatch = 200
	// redisTypeProbeLimit bounds TYPE calls while classifying patterns.
	redisTypeProbeLimit = 50
	// redisSampleElements bounds elements read per sampled key.
	redisSampleElements = 10
)

// withRedis opens a bounded client session, runs fn, and disconnects.
func withRedis(ctx context.Context, dsn string, o Options, fn func(ctx context.Context, client RedisRunner) error) error {
	o = withDefaults(o)
	if o.ConnectRedis == nil {
		o.ConnectRedis = ConnectRedis
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	client, err := o.ConnectRedis(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = client.Disconnect(ctx) }()
	return fn(ctx, client)
}

// redisDBFromDSN names the logical database a redis:// URL selects.
func redisDBFromDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "db0"
	}
	if n := strings.TrimPrefix(u.Path, "/"); n != "" {
		if _, err := strconv.Atoi(n); err == nil {
			return "db" + n
		}
	}
	return "db0"
}

// parseRedisInfo flattens INFO output into key/value pairs, dropping the
// "# Section" headers.
func parseRedisInfo(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, value, ok := strings.Cut(line, ":"); ok {
			out[key] = value
		}
	}
	return out
}

// parseRedisKeyspace extracts per-database key counts from INFO's
// "dbN:keys=K,expires=E,avg_ttl=T" entries, sorted by database index.
func parseRedisKeyspace(info map[string]string) []SchemaSummary {
	var out []SchemaSummary
	for key, value := range info {
		if !strings.HasPrefix(key, "db") {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(key, "db")); err != nil {
			continue
		}
		out = append(out, SchemaSummary{Name: key, TableCount: redisKeyspaceKeys(value)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func redisKeyspaceKeys(value string) int {
	for _, field := range strings.Split(value, ",") {
		if rest, ok := strings.CutPrefix(field, "keys="); ok {
			n, _ := strconv.Atoi(rest)
			return n
		}
	}
	return 0
}

func redisInfoInt64(info map[string]string, key string) int64 {
	n, _ := strconv.ParseInt(info[key], 10, 64)
	return n
}

func redisOverview(ctx context.Context, dsn string, o Options) (*Overview, error) {
	out := &Overview{Engine: EngineRedis, Database: redisDBFromDSN(dsn)}
	err := withRedis(ctx, dsn, o, func(ctx context.Context, client RedisRunner) error {
		text, err := client.Info(ctx)
		if err != nil {
			return fmt.Errorf("dbinspect: redis info: %w", err)
		}
		info := parseRedisInfo(text)
		out.ServerVersion = info["redis_version"]
		if v := info["valkey_version"]; v != "" {
			out.ServerVersion = v + " (valkey)"
		}
		// Replicas are the one server-enforced read-only mode redis has.
		out.ReadOnlySession = info["role"] == "slave"
		out.SizeBytes = redisInfoInt64(info, "used_memory")
		out.Connections = int(redisInfoInt64(info, "connected_clients"))
		if cfg, err := client.ConfigGet(ctx, "maxclients"); err == nil {
			out.MaxConnections = int(redisInfoInt64(cfg, "maxclients"))
		}
		out.Schemas = parseRedisKeyspace(info)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// generalizeKeyPattern buckets a key into a template by replacing
// variable-looking ":"-separated segments — numbers, long hex, UUIDs —
// with "*": "session:1f9acd42" becomes "session:*".
func generalizeKeyPattern(key string) string {
	segments := strings.Split(key, ":")
	for i, seg := range segments {
		if isVariableSegment(seg) {
			segments[i] = "*"
		}
	}
	return strings.Join(segments, ":")
}

func isVariableSegment(seg string) bool {
	if seg == "" {
		return false
	}
	if _, err := strconv.ParseInt(seg, 10, 64); err == nil {
		return true
	}
	hexish := len(seg) >= 8
	for _, r := range seg {
		if !strings.ContainsRune("0123456789abcdefABCDEF-", r) {
			hexish = false
			break
		}
	}
	return hexish
}

func redisSchema(ctx context.Context, dsn, _ string, o Options) (*SchemaReport, error) {
	out := &SchemaReport{Engine: EngineRedis}
	db := redisDBFromDSN(dsn)
	err := withRedis(ctx, dsn, o, func(ctx context.Context, client RedisRunner) error {
		keys, err := scanRedisKeys(ctx, client, "*", redisScanKeyLimit)
		if err != nil {
			return err
		}
		out.Tables = redisPatternTables(ctx, client, db, keys)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanRedisKeys(ctx context.Context, client RedisRunner, match string, limit int) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		batch, next, err := client.Scan(ctx, cursor, match, redisScanBatch)
		if err != nil {
			return nil, fmt.Errorf("dbinspect: redis scan: %w", err)
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 || len(keys) >= limit {
			break
		}
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

// redisPatternTables buckets sampled keys into generalized patterns and
// classifies each bucket by the TYPE of its first key (bounded probes).
// EstimatedRows counts keys observed in the sample, not the keyspace.
func redisPatternTables(ctx context.Context, client RedisRunner, db string, keys []string) []Table {
	counts := map[string]int{}
	exemplar := map[string]string{}
	for _, key := range keys {
		pattern := generalizeKeyPattern(key)
		counts[pattern]++
		if _, ok := exemplar[pattern]; !ok {
			exemplar[pattern] = key
		}
	}
	patterns := make([]string, 0, len(counts))
	for p := range counts {
		patterns = append(patterns, p)
	}
	sort.Slice(patterns, func(i, j int) bool {
		if counts[patterns[i]] != counts[patterns[j]] {
			return counts[patterns[i]] > counts[patterns[j]]
		}
		return patterns[i] < patterns[j]
	})
	probes := 0
	tables := make([]Table, 0, len(patterns))
	for _, p := range patterns {
		t := Table{Schema: db, Name: p, EstimatedRows: int64(counts[p])}
		if probes < redisTypeProbeLimit {
			if kind, err := client.Type(ctx, exemplar[p]); err == nil {
				t.Kind = kind
			}
			probes++
		}
		tables = append(tables, t)
	}
	return tables
}

// redisRelations is honestly empty: a keyspace has no foreign keys.
func redisRelations(ctx context.Context, dsn, _ string, o Options) (*RelationsReport, error) {
	out := &RelationsReport{Engine: EngineRedis}
	err := withRedis(ctx, dsn, o, func(context.Context, RedisRunner) error { return nil })
	if err != nil {
		return nil, err
	}
	return out, nil
}

// redisStats reports the per-database keyspace counters from INFO — the
// zero-cost health primitive redis offers. Memory detail lives in
// overview.
func redisStats(ctx context.Context, dsn, _ string, o Options) (*StatsReport, error) {
	out := &StatsReport{Engine: EngineRedis}
	err := withRedis(ctx, dsn, o, func(ctx context.Context, client RedisRunner) error {
		text, err := client.Info(ctx, "keyspace")
		if err != nil {
			return fmt.Errorf("dbinspect: redis info keyspace: %w", err)
		}
		for _, s := range parseRedisKeyspace(parseRedisInfo(text)) {
			out.Tables = append(out.Tables, TableStats{
				Schema:   "keyspace",
				Name:     s.Name,
				LiveRows: int64(s.TableCount),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// redisSample reads up to limit keys matching a key or MATCH pattern, with
// each value read bounded per type. Key names and values may both be
// sensitive — callers gate this like every other sample.
func redisSample(ctx context.Context, dsn, table string, limit int, o Options) (*SampleReport, error) {
	out := &SampleReport{
		Engine:  EngineRedis,
		Schema:  redisDBFromDSN(dsn),
		Table:   table,
		Limit:   limit,
		Columns: []string{"key", "type", "ttl_seconds", "value"},
	}
	err := withRedis(ctx, dsn, o, func(ctx context.Context, client RedisRunner) error {
		keys, err := resolveRedisKeys(ctx, client, table, limit)
		if err != nil {
			return err
		}
		for _, key := range keys {
			out.Rows = append(out.Rows, redisSampleRow(ctx, client, key))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func resolveRedisKeys(ctx context.Context, client RedisRunner, table string, limit int) ([]string, error) {
	if strings.ContainsAny(table, "*?[") {
		keys, err := scanRedisKeys(ctx, client, table, limit)
		if err != nil {
			return nil, err
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("dbinspect: no keys match %q", table)
		}
		return keys, nil
	}
	kind, err := client.Type(ctx, table)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: resolve key %q: %w", table, err)
	}
	if kind == "none" {
		return nil, fmt.Errorf("dbinspect: key %q not found", table)
	}
	return []string{table}, nil
}

func redisSampleRow(ctx context.Context, client RedisRunner, key string) []any {
	kind, err := client.Type(ctx, key)
	if err != nil {
		return []any{key, "", nil, "(error: " + err.Error() + ")"}
	}
	ttl, err := client.TTLSeconds(ctx, key)
	var ttlValue any
	if err == nil && ttl >= 0 {
		ttlValue = ttl
	}
	value, err := client.ReadValue(ctx, key, kind, redisSampleElements)
	if err != nil {
		return []any{key, kind, ttlValue, "(error: " + err.Error() + ")"}
	}
	return []any{key, kind, ttlValue, displayValue(value)}
}
