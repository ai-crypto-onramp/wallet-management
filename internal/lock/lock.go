// Package lock provides a distributed lock abstraction backed by Redis with an
// in-memory fallback for tests.
package lock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Locker acquires and releases a named mutex.
type Locker interface {
	Acquire(ctx context.Context, name string, ttl time.Duration) (string, bool, error)
	Release(ctx context.Context, name, token string) error
}

// RedisLocker uses SET NX PX for a simple distributed lock.
type RedisLocker struct {
	c *redis.Client
}

// NewRedisLocker wraps an existing redis client.
func NewRedisLocker(c *redis.Client) *RedisLocker { return &RedisLocker{c: c} }

func (l *RedisLocker) Acquire(ctx context.Context, name string, ttl time.Duration) (string, bool, error) {
	token := uuid.NewString()
	ok, err := l.c.SetNX(ctx, "lock:"+name, token, ttl).Result()
	if err != nil {
		return "", false, err
	}
	return token, ok, nil
}

func (l *RedisLocker) Release(ctx context.Context, name, token string) error {
	// Lua-free compare-and-delete via pipe; acceptable for low contention.
	cur, err := l.c.Get(ctx, "lock:"+name).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	if cur != token {
		return fmt.Errorf("lock token mismatch")
	}
	return l.c.Del(ctx, "lock:"+name).Err()
}

// MemLocker is a process-local mutex map for tests.
type MemLocker struct {
	mu    sync.Mutex
	locks map[string]string
}

// NewMemLocker returns a process-local locker.
func NewMemLocker() *MemLocker {
	return &MemLocker{locks: make(map[string]string)}
}

func (l *MemLocker) Acquire(_ context.Context, name string, _ time.Duration) (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.locks[name]; ok {
		return "", false, nil
	}
	token := uuid.NewString()
	l.locks[name] = token
	return token, true, nil
}

func (l *MemLocker) Release(_ context.Context, name, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.locks[name]; ok && cur == token {
		delete(l.locks, name)
	}
	return nil
}