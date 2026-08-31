package dbinspect

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRunner is the subset of go-redis the inspector needs. Redis's
// command surface doesn't fit the SQL-shaped Conn/Rows, so the redis
// engine gets its own narrow seam; tests inject a fake.
type RedisRunner interface {
	Info(ctx context.Context, sections ...string) (string, error)
	ConfigGet(ctx context.Context, param string) (map[string]string, error)
	Scan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error)
	Type(ctx context.Context, key string) (string, error)
	// TTLSeconds reports a key's TTL: -1 no expiry, -2 missing.
	TTLSeconds(ctx context.Context, key string) (int64, error)
	// ReadValue reads one key's value bounded by limit elements (or bytes,
	// for plain strings) using the read command appropriate for keyType.
	ReadValue(ctx context.Context, key, keyType string, limit int) (any, error)
	Disconnect(ctx context.Context) error
}

// RedisConnectFn opens a RedisRunner for a DSN. Nil selects ConnectRedis.
type RedisConnectFn func(ctx context.Context, dsn string) (RedisRunner, error)

// ConnectRedis opens go-redis read-only in spirit: redis has no read-only
// session mode outside replicas, so the guarantee is that this package
// only ever issues read commands — INFO, CONFIG GET, SCAN (never KEYS),
// TYPE, TTL, and bounded per-type reads. Timeouts are short, the pool is
// one connection, and the client is identifiable by name.
func ConnectRedis(ctx context.Context, dsn string) (RedisRunner, error) {
	opts, err := redis.ParseURL(dsn)
	if err != nil {
		return nil, fmt.Errorf("dbinspect: parse dsn %s: %w", RedactDSN(dsn), err)
	}
	opts.ClientName = "spectra-dbinspect"
	opts.DialTimeout = 5 * time.Second
	opts.ReadTimeout = 5 * time.Second
	opts.WriteTimeout = 5 * time.Second
	opts.PoolSize = 1
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("dbinspect: ping %s: %w", RedactDSN(dsn), err)
	}
	return redisRunner{client}, nil
}

type redisRunner struct{ client *redis.Client }

func (r redisRunner) Info(ctx context.Context, sections ...string) (string, error) {
	return r.client.Info(ctx, sections...).Result()
}

func (r redisRunner) ConfigGet(ctx context.Context, param string) (map[string]string, error) {
	return r.client.ConfigGet(ctx, param).Result()
}

func (r redisRunner) Scan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	return r.client.Scan(ctx, cursor, match, count).Result()
}

func (r redisRunner) Type(ctx context.Context, key string) (string, error) {
	return r.client.Type(ctx, key).Result()
}

func (r redisRunner) TTLSeconds(ctx context.Context, key string) (int64, error) {
	d, err := r.client.TTL(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return int64(d), nil // -1 no expiry, -2 missing, unscaled
	}
	return int64(d / time.Second), nil
}

// maxStringSampleBytes bounds plain-string reads so a huge value can't
// balloon a sample.
const maxStringSampleBytes = 4096

// ReadValue picks the bounded read command for the key's type. SMEMBERS
// and HGETALL are never issued — they are unbounded on large keys.
func (r redisRunner) ReadValue(ctx context.Context, key, keyType string, limit int) (any, error) {
	n := int64(limit)
	switch keyType {
	case "string":
		return r.client.GetRange(ctx, key, 0, maxStringSampleBytes-1).Result()
	case "list":
		return r.client.LRange(ctx, key, 0, n-1).Result()
	case "set":
		return r.client.SRandMemberN(ctx, key, n).Result()
	case "zset":
		return r.client.ZRangeWithScores(ctx, key, 0, n-1).Result()
	case "hash":
		return r.client.HRandFieldWithValues(ctx, key, int(n)).Result()
	case "stream":
		return r.client.XRangeN(ctx, key, "-", "+", n).Result()
	default:
		return fmt.Sprintf("(%s value not sampled)", keyType), nil
	}
}

func (r redisRunner) Disconnect(context.Context) error { return r.client.Close() }
