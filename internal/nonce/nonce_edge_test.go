package nonce

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

// contendedLocker holds the lock for a configurable duration before allowing
// the next Acquire to succeed. This exercises the retry loop in ReserveNonce.
type contendedLocker struct {
	mu       sync.Mutex
	holds    map[string]chan struct{}
	released map[string]bool
}

func newContendedLocker() *contendedLocker {
	return &contendedLocker{holds: map[string]chan struct{}{}, released: map[string]bool{}}
}

// preHold makes Acquire return ok=false once, then releases, so the retry
// loop in ReserveNonce spins once and then succeeds.
func (l *contendedLocker) preHold(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holds[name] = make(chan struct{})
}

func (l *contendedLocker) release(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ch, ok := l.holds[name]; ok {
		close(ch)
		delete(l.holds, name)
	}
}

func (l *contendedLocker) Acquire(_ context.Context, name string, _ time.Duration) (string, bool, error) {
	l.mu.Lock()
	ch, held := l.holds[name]
	l.mu.Unlock()
	if held {
		// Wait until released; meanwhile report "not acquired" by returning
		// ok=false once, then succeed on the next attempt. We block here so
		// the retry loop observes contention.
		<-ch
		return "", false, nil
	}
	return "tok-" + name, true, nil
}

func (l *contendedLocker) Release(_ context.Context, name, _ string) error { return nil }

func TestReserveNonceRetriesOnContention(t *testing.T) {
	st := memstore.New()
	lk := newContendedLocker()
	svc := NewService(st, lk)
	ctx := context.Background()
	wID := uuid.New()

	// Pre-hold the nonce lock so the first Acquire returns ok=false; the retry
	// loop should spin and succeed once we release.
	lk.preHold(lockName(wID, wallet.ChainEthereum))
	go func() {
		time.Sleep(20 * time.Millisecond)
		lk.release(lockName(wID, wallet.ChainEthereum))
	}()

	n, err := svc.ReserveNonce(ctx, wID, wallet.ChainEthereum)
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected first reserved nonce 0, got %d", n)
	}
}

func lockName(wID uuid.UUID, chain wallet.Chain) string {
	return "nonce:lock:" + wID.String() + ":" + string(chain)
}

// errLocker always returns an error on Acquire.
type errLocker struct{ err error }

func (l *errLocker) Acquire(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, l.err
}

func (l *errLocker) Release(_ context.Context, _, _ string) error { return nil }

func TestReserveNonceAcquireError(t *testing.T) {
	st := memstore.New()
	lk := &errLocker{err: errors.New("redis down")}
	svc := NewService(st, lk)
	_, err := svc.ReserveNonce(context.Background(), uuid.New(), wallet.ChainPolygon)
	if err == nil {
		t.Fatal("expected error when Acquire fails")
	}
}

// alwaysFailLocker never yields the lock, so ReserveNonce exhausts its retries
// and returns the contention error.
type alwaysFailLocker struct{}

func (alwaysFailLocker) Acquire(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, nil
}

func (alwaysFailLocker) Release(_ context.Context, _, _ string) error { return nil }

func TestReserveNonceContentionExhausted(t *testing.T) {
	st := memstore.New()
	svc := NewService(st, alwaysFailLocker{})
	_, err := svc.ReserveNonce(context.Background(), uuid.New(), wallet.ChainEthereum)
	if err == nil {
		t.Fatal("expected contention error when lock never acquired")
	}
}

// errStore wraps memstore.Store to inject an IncrementPendingNonce failure.
type errStore struct {
	*memstore.Store
	incrementErr error
}

func (s *errStore) IncrementPendingNonce(ctx context.Context, wID uuid.UUID, chain string) (int64, int, error) {
	if s.incrementErr != nil {
		return 0, 0, s.incrementErr
	}
	return s.Store.IncrementPendingNonce(ctx, wID, chain)
}

func TestReserveNonceIncrementError(t *testing.T) {
	st := memstore.New()
	es := &errStore{Store: st, incrementErr: errors.New("db down")}
	svc := NewService(es, lock.NewMemLocker())
	_, err := svc.ReserveNonce(context.Background(), uuid.New(), wallet.ChainEthereum)
	if err == nil {
		t.Fatal("expected error when IncrementPendingNonce fails")
	}
}

func TestCommitNonceRollbackAndGet(t *testing.T) {
	st := memstore.New()
	svc := NewService(st, lock.NewMemLocker())
	ctx := context.Background()
	wID := uuid.New()

	// Commit before any reservation — AdvanceBroadcastNonce is a no-op-safe
	// call that initializes the row.
	if err := svc.CommitNonce(ctx, wID, wallet.ChainArbitrum, 5); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, wID, wallet.ChainArbitrum)
	if err != nil {
		t.Fatal(err)
	}
	if got.BroadcastNonce < 6 {
		t.Errorf("expected broadcast >= 6, got %d", got.BroadcastNonce)
	}

	// Rollback is a gap-safe no-op and must never error.
	if err := svc.RollbackNonce(ctx, wID, wallet.ChainBitcoin, 42); err != nil {
		t.Errorf("RollbackNonce returned error: %v", err)
	}
}