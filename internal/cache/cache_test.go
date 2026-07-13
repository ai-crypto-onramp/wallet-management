package cache

import (
	"context"
	"testing"
	"time"
)

func TestMemCacheSetGet(t *testing.T) {
	t.Parallel()
	c := NewMem()
	ctx := context.Background()
	if _, ok, err := c.Get(ctx, "missing"); err != nil || ok {
		t.Errorf("expected miss no error, got ok=%v err=%v", ok, err)
	}
	if err := c.Set(ctx, "k", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c.Get(ctx, "k"); !ok || err != nil || v != "v" {
		t.Errorf("expected v ok nil, got %q %v %v", v, ok, err)
	}
}

func TestMemCacheExpiry(t *testing.T) {
	t.Parallel()
	c := NewMem()
	ctx := context.Background()
	if err := c.Set(ctx, "k", "v", 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok, _ := c.Get(ctx, "k"); ok {
		t.Error("expected entry to expire")
	}
}

func TestMemCacheDel(t *testing.T) {
	t.Parallel()
	c := NewMem()
	ctx := context.Background()
	_ = c.Set(ctx, "a", "1", time.Minute)
	_ = c.Set(ctx, "b", "2", time.Minute)
	if err := c.Del(ctx, "a", "b", "nonexistent"); err != nil {
		t.Errorf("Del returned error: %v", err)
	}
	if _, ok, _ := c.Get(ctx, "a"); ok {
		t.Error("a should be deleted")
	}
	if _, ok, _ := c.Get(ctx, "b"); ok {
		t.Error("b should be deleted")
	}
}

func TestMemCacheDelEmpty(t *testing.T) {
	t.Parallel()
	c := NewMem()
	if err := c.Del(context.Background()); err != nil {
		t.Errorf("Del with no keys returned error: %v", err)
	}
}