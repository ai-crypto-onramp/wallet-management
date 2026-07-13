//go:build integration

// Integration tests for the Redis-backed cache. They require a running Redis
// (make docker-up); TEST_REDIS_URL overrides the default connection string.
package cache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func redisCache(t *testing.T) *RedisCache {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	c, err := NewRedis(url, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(context.Background(), "ping"); err != nil {
		t.Fatalf("redis unavailable: %v (start with `make docker-up`)", err)
	}
	return c
}

func TestRedisCacheSetGetDel(t *testing.T) {
	c := redisCache(t)
	ctx := context.Background()
	key := "itest:" + uuid.NewString()

	if _, ok, err := c.Get(ctx, key); err != nil || ok {
		t.Fatalf("expected miss: ok=%v err=%v", ok, err)
	}
	if err := c.Set(ctx, key, "value-1", time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, ok, err := c.Get(ctx, key)
	if err != nil || !ok || v != "value-1" {
		t.Fatalf("get: v=%q ok=%v err=%v", v, ok, err)
	}
	if err := c.Del(ctx, key); err != nil {
		t.Fatalf("del: %v", err)
	}
	if _, ok, _ := c.Get(ctx, key); ok {
		t.Error("expected miss after delete")
	}
}

func TestRedisCacheDefaultTTLAndExpiry(t *testing.T) {
	c := redisCache(t)
	ctx := context.Background()
	key := "itest:" + uuid.NewString()

	if err := c.Set(ctx, key, "short-lived", 200*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok, _ := c.Get(ctx, key); !ok {
			break // expired
		}
		if time.Now().After(deadline) {
			t.Fatal("key did not expire after TTL")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// ttl <= 0 falls back to the cache default rather than persisting forever.
	if err := c.Set(ctx, key, "default-ttl", 0); err != nil {
		t.Fatalf("set default ttl: %v", err)
	}
	ttl, err := c.c.TTL(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > time.Minute {
		t.Errorf("expected default TTL <= 1m, got %v", ttl)
	}
}
