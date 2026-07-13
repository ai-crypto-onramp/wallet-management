package lock

import (
	"context"
	"testing"
	"time"
)

func TestMemLockerAcquireRelease(t *testing.T) {
	t.Parallel()
	l := NewMemLocker()
	ctx := context.Background()
	token, ok, err := l.Acquire(ctx, "n1", time.Second)
	if err != nil || !ok {
		t.Fatalf("expected acquire ok, got ok=%v err=%v", ok, err)
	}
	if token == "" {
		t.Error("expected non-empty token")
	}
	// second acquire on same name fails
	if _, ok2, err := l.Acquire(ctx, "n1", time.Second); ok2 || err != nil {
		t.Errorf("expected re-acquire to fail, got ok=%v err=%v", ok2, err)
	}
	if err := l.Release(ctx, "n1", token); err != nil {
		t.Errorf("release returned error: %v", err)
	}
	// can re-acquire after release
	if _, ok3, err := l.Acquire(ctx, "n1", time.Second); !ok3 || err != nil {
		t.Errorf("expected re-acquire after release to succeed, got ok=%v err=%v", ok3, err)
	}
}

func TestMemLockerWrongTokenNoRelease(t *testing.T) {
	t.Parallel()
	l := NewMemLocker()
	ctx := context.Background()
	token, _, _ := l.Acquire(ctx, "n2", time.Second)
	// releasing with wrong token should not delete the lock
	_ = l.Release(ctx, "n2", "wrong-token")
	if _, ok, _ := l.Acquire(ctx, "n2", time.Second); ok {
		t.Error("lock should still be held after wrong-token release")
	}
	// proper release
	_ = l.Release(ctx, "n2", token)
	if _, ok, _ := l.Acquire(ctx, "n2", time.Second); !ok {
		t.Error("lock should be re-acquirable after proper release")
	}
}

func TestMemLockerReleaseMissingNoop(t *testing.T) {
	t.Parallel()
	l := NewMemLocker()
	if err := l.Release(context.Background(), "nonexistent", "tok"); err != nil {
		t.Errorf("release of missing lock should be noop, got %v", err)
	}
}