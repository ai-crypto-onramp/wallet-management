//go:build integration

// Integration tests for the Redis-backed locker. They require a running Redis
// (make docker-up); TEST_REDIS_URL overrides the default connection string.
package lock

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func redisLocker(t *testing.T) *RedisLocker {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis unavailable: %v (start with `make docker-up`)", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return NewRedisLocker(c)
}

func TestRedisLockerMutualExclusion(t *testing.T) {
	l := redisLocker(t)
	ctx := context.Background()
	name := "itest:" + uuid.NewString()

	token, ok, err := l.Acquire(ctx, name, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, err := l.Acquire(ctx, name, time.Minute); err != nil || ok {
		t.Fatalf("second acquire should fail while held: ok=%v err=%v", ok, err)
	}
	if err := l.Release(ctx, name, token); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok, err := l.Acquire(ctx, name, time.Minute); err != nil || !ok {
		t.Fatalf("reacquire after release: ok=%v err=%v", ok, err)
	}
}

func TestRedisLockerWrongTokenRelease(t *testing.T) {
	l := redisLocker(t)
	ctx := context.Background()
	name := "itest:" + uuid.NewString()

	if _, ok, err := l.Acquire(ctx, name, time.Minute); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if err := l.Release(ctx, name, "not-the-token"); err == nil {
		t.Error("expected release with wrong token to fail")
	}
	// Lock must still be held.
	if _, ok, _ := l.Acquire(ctx, name, time.Minute); ok {
		t.Error("lock was released despite wrong token")
	}
}

func TestRedisLockerTTLExpiry(t *testing.T) {
	l := redisLocker(t)
	ctx := context.Background()
	name := "itest:" + uuid.NewString()

	if _, ok, err := l.Acquire(ctx, name, 200*time.Millisecond); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok, _ := l.Acquire(ctx, name, time.Minute); ok {
			return // lock expired and was reacquired
		}
		if time.Now().After(deadline) {
			t.Fatal("lock did not expire after TTL")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
