package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/rflpazini/kvasir/internal/cache"
)

func newClient(t *testing.T) (*cache.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := cache.New(cache.Config{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func TestCache_PingOK(t *testing.T) {
	c, _ := newClient(t)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestCache_SetAndGetSearch(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()

	payload := []byte(`{"results":[]}`)
	if err := c.SetSearch(ctx, "search:v1:abc", payload, 5*time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := c.GetSearch(ctx, "search:v1:abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("roundtrip mismatch: got=%q want=%q", got, payload)
	}
}

func TestCache_GetSearchMissReturnsRedisNil(t *testing.T) {
	c, _ := newClient(t)
	_, err := c.GetSearch(context.Background(), "search:v1:never-set")
	if err == nil {
		t.Fatal("expected redis.Nil on miss, got nil error")
	}
}

func TestCache_TokenBucketIncrementsAndExpires(t *testing.T) {
	c, mr := newClient(t)
	ctx := context.Background()
	key := "ratelimit:adapter-x"
	ttl := 60 * time.Second

	// First call sets the key with TTL.
	v, err := c.IncrementBucket(ctx, key, ttl)
	if err != nil {
		t.Fatalf("incr 1: %v", err)
	}
	if v != 1 {
		t.Errorf("first increment = %d, want 1", v)
	}
	if !mr.Exists(key) {
		t.Fatal("key missing after first increment")
	}
	if got := mr.TTL(key); got <= 0 || got > ttl {
		t.Errorf("TTL = %v, want >0 and <=%v", got, ttl)
	}

	// Subsequent calls increment without resetting TTL.
	for i := 2; i <= 5; i++ {
		v, err := c.IncrementBucket(ctx, key, ttl)
		if err != nil {
			t.Fatalf("incr %d: %v", i, err)
		}
		if int(v) != i {
			t.Errorf("incr %d = %d", i, v)
		}
	}
}

func TestCache_TokenBucketResetsAfterExpiry(t *testing.T) {
	c, mr := newClient(t)
	ctx := context.Background()
	key := "ratelimit:adapter-y"
	ttl := 1 * time.Second

	if _, err := c.IncrementBucket(ctx, key, ttl); err != nil {
		t.Fatalf("incr: %v", err)
	}

	// Advance time past TTL using miniredis's clock.
	mr.FastForward(2 * time.Second)

	if mr.Exists(key) {
		t.Fatal("key still present after TTL expired")
	}

	// New increment after expiry must restart at 1 with a fresh TTL.
	v, err := c.IncrementBucket(ctx, key, ttl)
	if err != nil {
		t.Fatalf("incr after expiry: %v", err)
	}
	if v != 1 {
		t.Errorf("incr after expiry = %d, want 1", v)
	}
}

func TestCache_SetSearchTTLApplied(t *testing.T) {
	c, mr := newClient(t)
	ctx := context.Background()

	if err := c.SetSearch(ctx, "search:v1:k", []byte("payload"), 30*time.Second); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := mr.TTL("search:v1:k"); got != 30*time.Second {
		t.Errorf("TTL = %v, want 30s", got)
	}
}
