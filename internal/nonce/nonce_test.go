package nonce

import (
	"context"
	"sync"
	"testing"

	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

func newSvc(t *testing.T) (*Service, *memstore.Store) {
	t.Helper()
	st := memstore.New()
	lk := lock.NewMemLocker()
	return NewService(st, lk), st
}

func TestReserveNonceSequential(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	for i := 0; i < 5; i++ {
		n, err := svc.ReserveNonce(ctx, wID, wallet.ChainEthereum)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		if n != int64(i) {
			t.Errorf("expected %d, got %d", i, n)
		}
	}
	n, _ := svc.Get(ctx, wID, wallet.ChainEthereum)
	if n.PendingNonce != 5 || n.BroadcastNonce != 0 {
		t.Errorf("expected pending=5 broadcast=0, got %+v", n)
	}
}

func TestReserveNonceConcurrentDistinctNoGaps(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	const n = 10
	var wg sync.WaitGroup
	res := make(chan int64, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := svc.ReserveNonce(ctx, wID, wallet.ChainEthereum)
			if err != nil {
				errs <- err
				return
			}
			res <- v
		}()
	}
	wg.Wait()
	close(res)
	close(errs)
	for err := range errs {
		t.Fatalf("reserve error: %v", err)
	}
	seen := map[int64]bool{}
	var values []int64
	for v := range res {
		if seen[v] {
			t.Errorf("duplicate nonce reserved: %d", v)
		}
		seen[v] = true
		values = append(values, v)
	}
	if len(values) != n {
		t.Fatalf("expected %d nonces, got %d", n, len(values))
	}
	// all values 0..n-1 must be present (no gaps)
	for i := 0; i < n; i++ {
		if !seen[int64(i)] {
			t.Errorf("missing nonce %d in reserved set", i)
		}
	}
	got, _ := st.GetNonce(ctx, wID, "ethereum")
	if got.PendingNonce != int64(n) {
		t.Errorf("expected pending=%d, got %d", n, got.PendingNonce)
	}
}

func TestCommitNonce(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	n, _ := svc.ReserveNonce(ctx, wID, wallet.ChainEthereum)
	if err := svc.CommitNonce(ctx, wID, wallet.ChainEthereum, n); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetNonce(ctx, wID, "ethereum")
	if got.BroadcastNonce != n+1 {
		t.Errorf("expected broadcast=%d, got %d", n+1, got.BroadcastNonce)
	}
}

func TestRollbackNonceGapSafe(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	n0, _ := svc.ReserveNonce(ctx, wID, wallet.ChainEthereum) // 0
	n1, _ := svc.ReserveNonce(ctx, wID, wallet.ChainEthereum) // 1
	// rollback n1 — should not advance broadcast and should be gap-safe
	if err := svc.RollbackNonce(ctx, wID, wallet.ChainEthereum, n1); err != nil {
		t.Fatal(err)
	}
	// commit n0 only
	_ = svc.CommitNonce(ctx, wID, wallet.ChainEthereum, n0)
	got, _ := st.GetNonce(ctx, wID, "ethereum")
	if got.BroadcastNonce != 1 {
		t.Errorf("expected broadcast=1, got %d", got.BroadcastNonce)
	}
	if got.PendingNonce != 2 {
		t.Errorf("expected pending=2 (still incremented), got %d", got.PendingNonce)
	}
	// next reserve continues monotonically
	n2, _ := svc.ReserveNonce(ctx, wID, wallet.ChainEthereum)
	if n2 != 2 {
		t.Errorf("expected next reserve=2, got %d", n2)
	}
}

func TestGetNonceEmpty(t *testing.T) {
	svc, _ := newSvc(t)
	n, err := svc.Get(context.Background(), uuid.New(), wallet.ChainPolygon)
	if err != nil {
		t.Fatal(err)
	}
	if n.PendingNonce != 0 || n.BroadcastNonce != 0 {
		t.Errorf("expected zero, got %+v", n)
	}
}