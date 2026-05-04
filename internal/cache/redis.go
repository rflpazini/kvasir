// Package cache wraps the Redis client used for query caching, adapter health
// snapshots and per-adapter token-bucket rate limiting.
//
// The token bucket is implemented as an atomic Lua script to eliminate the
// classic INCR + EXPIRE race (a crash between the two leaves the key without
// a TTL, freezing the bucket).
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client is a thin wrapper around go-redis exposing kvasir-specific helpers.
type Client struct {
	rdb            *redis.Client
	tokenBucketLua *redis.Script
}

// Config carries the Redis connection settings.
type Config struct {
	Addr     string
	Password string
	DB       int
}

// New connects to Redis and pre-loads embedded Lua scripts.
func New(cfg Config) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	return &Client{
		rdb: rdb,
		// Atomic INCR + EXPIRE-on-first-hit. KEYS[1] is the bucket key;
		// ARGV[1] is the TTL in seconds.
		tokenBucketLua: redis.NewScript(`
local v = redis.call('INCR', KEYS[1])
if v == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return v
`),
	}
}

// Ping verifies the connection.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Close releases the underlying connection pool.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// IncrementBucket runs the atomic token-bucket script and returns the current
// counter value within the rolling window defined by ttl.
func (c *Client) IncrementBucket(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	res, err := c.tokenBucketLua.Run(ctx, c.rdb, []string{key}, int64(ttl.Seconds())).Result()
	if err != nil {
		return 0, err
	}
	v, ok := res.(int64)
	if !ok {
		return 0, errors.New("cache: unexpected token bucket return type")
	}
	return v, nil
}

// GetSearch returns the raw cached payload for a search key, or nil + redis.Nil
// when missing.
func (c *Client) GetSearch(ctx context.Context, key string) ([]byte, error) {
	return c.rdb.Get(ctx, key).Bytes()
}

// SetSearch stores a search payload with the given TTL.
func (c *Client) SetSearch(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, payload, ttl).Err()
}
