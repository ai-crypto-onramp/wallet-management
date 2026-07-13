package lock

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func newRedisLocker(t *testing.T) (*RedisLocker, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return NewRedisLocker(c), c, mr
}

func TestRedisLockerAcquireRelease(t *testing.T) {
	l, _, _ := newRedisLocker(t)
	ctx := context.Background()
	name := "lock:" + uuid.NewString()

	token, ok, err := l.Acquire(ctx, name, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if token == "" {
		t.Error("expected non-empty token")
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
	l, _, _ := newRedisLocker(t)
	ctx := context.Background()
	name := "lock:" + uuid.NewString()

	if _, ok, err := l.Acquire(ctx, name, time.Minute); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if err := l.Release(ctx, name, "not-the-token"); err == nil {
		t.Error("expected release with wrong token to fail")
	}
	if _, ok, _ := l.Acquire(ctx, name, time.Minute); ok {
		t.Error("lock was released despite wrong token")
	}
}

func TestRedisLockerTTLExpiryUnit(t *testing.T) {
	l, _, mr := newRedisLocker(t)
	ctx := context.Background()
	name := "lock:" + uuid.NewString()

	if _, ok, err := l.Acquire(ctx, name, 200*time.Millisecond); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	mr.FastForward(300 * time.Millisecond)
	if _, ok, err := l.Acquire(ctx, name, time.Minute); err != nil || !ok {
		t.Fatalf("expected reacquire after TTL expiry: ok=%v err=%v", ok, err)
	}
}

func TestRedisLockerReleaseMissingNil(t *testing.T) {
	l, _, _ := newRedisLocker(t)
	ctx := context.Background()
	name := "lock:" + uuid.NewString()
	// Releasing a lock that was never acquired returns nil (redis.Nil path).
	if err := l.Release(ctx, name, "tok"); err != nil {
		t.Errorf("expected nil on missing lock, got %v", err)
	}
}

func TestRedisLockerAcquireError(t *testing.T) {
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	l := NewRedisLocker(c)
	// Close the client to force a connection error on Acquire.
	_ = c.Close()
	_, ok, err := l.Acquire(context.Background(), "x", time.Second)
	if ok {
		t.Error("expected ok=false on connection error")
	}
	if err == nil {
		t.Error("expected non-nil error on connection error")
	}
}

func TestRedisLockerReleaseGetError(t *testing.T) {
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	l := NewRedisLocker(c)
	ctx := context.Background()
	name := "lock:" + uuid.NewString()
	if _, ok, err := l.Acquire(ctx, name, time.Minute); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	// Close the client to force a Get error on Release (not redis.Nil).
	_ = c.Close()
	if err := l.Release(ctx, name, "tok"); err == nil {
		t.Error("expected error on Release with closed client")
	}
}

func TestNewRedisLocker(t *testing.T) {
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	l := NewRedisLocker(c)
	if l == nil {
		t.Fatal("expected non-nil locker")
	}
}