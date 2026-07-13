package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedisCache(t *testing.T, defaultTTL time.Duration) (*RedisCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := NewRedis("redis://"+mr.Addr()+"/0", defaultTTL)
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	t.Cleanup(func() { _ = c.c.Close() })
	return c, mr
}

func TestNewRedisBadDSN(t *testing.T) {
	if _, err := NewRedis("://bad", time.Minute); err == nil {
		t.Error("expected error on invalid DSN")
	}
}

func TestNewRedisFromClient(t *testing.T) {
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	rc := NewRedisFromClient(c, time.Minute)
	if rc == nil {
		t.Fatal("expected non-nil cache")
	}
	if rc.ttl != time.Minute {
		t.Errorf("expected ttl 1m, got %v", rc.ttl)
	}
}

func TestRedisCacheMiss(t *testing.T) {
	c, _ := newRedisCache(t, time.Minute)
	v, ok, err := c.Get(context.Background(), "missing")
	if err != nil || ok {
		t.Errorf("expected miss/nil, got v=%q ok=%v err=%v", v, ok, err)
	}
}

func TestRedisCacheSetGetDelUnit(t *testing.T) {
	c, _ := newRedisCache(t, time.Minute)
	ctx := context.Background()
	if err := c.Set(ctx, "k", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	v, ok, err := c.Get(ctx, "k")
	if err != nil || !ok || v != "v" {
		t.Fatalf("expected v/true/nil, got %q %v %v", v, ok, err)
	}
	if err := c.Del(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get(ctx, "k"); ok {
		t.Error("expected miss after delete")
	}
}

func TestRedisCacheGetError(t *testing.T) {
	mr := miniredis.RunT(t)
	c, _ := NewRedis("redis://"+mr.Addr()+"/0", time.Minute)
	t.Cleanup(func() { _ = c.c.Close() })
	// Close underlying client to force a real error (not redis.Nil).
	_ = c.c.Close()
	_, ok, err := c.Get(context.Background(), "k")
	if ok {
		t.Error("expected ok=false on connection error")
	}
	if err == nil {
		t.Error("expected non-nil error on connection error")
	}
}

func TestRedisCacheSetDefaultTTL(t *testing.T) {
	c, _ := newRedisCache(t, 2*time.Second)
	ctx := context.Background()
	if err := c.Set(ctx, "k", "v", 0); err != nil {
		t.Fatal(err)
	}
	ttl, err := c.c.TTL(ctx, "k").Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > 2*time.Second {
		t.Errorf("expected default TTL ~2s, got %v", ttl)
	}
}

func TestRedisCacheSetNegativeTTL(t *testing.T) {
	c, _ := newRedisCache(t, time.Second)
	ctx := context.Background()
	if err := c.Set(ctx, "neg", "v", -1); err != nil {
		t.Fatal(err)
	}
	ttl, _ := c.c.TTL(ctx, "neg").Result()
	if ttl <= 0 {
		t.Errorf("expected fallback to default TTL on negative ttl, got %v", ttl)
	}
}

func TestRedisCacheExpiry(t *testing.T) {
	c, mr := newRedisCache(t, time.Minute)
	ctx := context.Background()
	if err := c.Set(ctx, "k", "v", 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(200 * time.Millisecond)
	if _, ok, _ := c.Get(ctx, "k"); ok {
		t.Error("expected miss after TTL expiry")
	}
}

func TestRedisCacheDelEmptyNoop(t *testing.T) {
	c, _ := newRedisCache(t, time.Minute)
	if err := c.Del(context.Background()); err != nil {
		t.Errorf("Del with no keys returned error: %v", err)
	}
}

func TestRedisCacheDelMultiple(t *testing.T) {
	c, _ := newRedisCache(t, time.Minute)
	ctx := context.Background()
	_ = c.Set(ctx, "a", "1", time.Minute)
	_ = c.Set(ctx, "b", "2", time.Minute)
	if err := c.Del(ctx, "a", "b", "nonexistent"); err != nil {
		t.Fatalf("Del: %v", err)
	}
	if _, ok, _ := c.Get(ctx, "a"); ok {
		t.Error("a should be deleted")
	}
	if _, ok, _ := c.Get(ctx, "b"); ok {
		t.Error("b should be deleted")
	}
}