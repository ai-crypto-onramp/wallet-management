// Package cache provides a Redis-backed cache with an in-memory fallback.
package cache

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is the abstraction used by derivers to memoize derived public keys.
type Cache interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
}

// RedisCache wraps go-redis.
type RedisCache struct {
	c   *redis.Client
	ttl time.Duration
}

// NewRedis creates a Redis-backed cache. dsn may be redis://host:port/db.
func NewRedis(dsn string, ttl time.Duration) (*RedisCache, error) {
	opts, err := redis.ParseURL(dsn)
	if err != nil {
		return nil, err
	}
	return &RedisCache{c: redis.NewClient(opts), ttl: ttl}, nil
}

// NewRedisFromClient wraps an existing redis client (useful for tests).
func NewRedisFromClient(c *redis.Client, ttl time.Duration) *RedisCache {
	return &RedisCache{c: c, ttl: ttl}
}

func (r *RedisCache) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := r.c.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (r *RedisCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = r.ttl
	}
	return r.c.Set(ctx, key, value, ttl).Err()
}

func (r *RedisCache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return r.c.Del(ctx, keys...).Err()
}

// MemCache is an in-memory cache used by tests when Redis is unavailable.
type MemCache struct {
	mu  sync.Mutex
	data map[string]memEntry
}

type memEntry struct {
	value string
	exp   time.Time
}

// NewMem returns a process-local in-memory cache.
func NewMem() *MemCache {
	return &MemCache{data: make(map[string]memEntry)}
}

func (m *MemCache) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[key]
	if !ok || time.Now().After(e.exp) {
		delete(m.data, key)
		return "", false, nil
	}
	return e.value, true, nil
}

func (m *MemCache) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = memEntry{value: value, exp: time.Now().Add(ttl)}
	return nil
}

func (m *MemCache) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}