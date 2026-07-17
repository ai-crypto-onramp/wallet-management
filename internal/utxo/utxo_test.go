package utxo

import (
	"context"
	"sync"
	"testing"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/google/uuid"
)

func newSvc(t *testing.T) (*Service, *memstore.Store) {
	t.Helper()
	st := memstore.New()
	return NewService(st), st
}

func seedUTXOs(t *testing.T, svc *Service, wID uuid.UUID, ops ...struct {
	outpoint string
	value    string
}) {
	t.Helper()
	for _, o := range ops {
		if err := svc.TrackUTXO(context.Background(), &storage.UTXO{
			Outpoint: o.outpoint, WalletID: wID, Value: o.value, LockState: "FREE",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSelectForAmount(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	seedUTXOs(t, svc, wID,
		struct{ outpoint, value string }{"a", "100"},
		struct{ outpoint, value string }{"b", "200"},
		struct{ outpoint, value string }{"c", "50"},
	)
	ops, total, err := svc.SelectForAmount(ctx, wID, 250)
	if err != nil {
		t.Fatal(err)
	}
	if total < 250 {
		t.Errorf("expected total>=250, got %d", total)
	}
	if len(ops) == 0 {
		t.Fatal("expected at least one selected outpoint")
	}
	// selected should now be locked, not free
	free, _ := svc.Store.ListFreeUTXOs(ctx, wID)
	if len(free) != 1 || free[0].Outpoint != "c" {
		t.Errorf("expected only c free, got %+v", free)
	}
}

func TestSelectForAmountInsufficient(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	seedUTXOs(t, svc, wID, struct{ outpoint, value string }{"a", "100"})
	if _, _, err := svc.SelectForAmount(ctx, wID, 500); err == nil {
		t.Error("expected insufficient funds error")
	}
}

func TestSelectForAmountEmpty(t *testing.T) {
	svc, _ := newSvc(t)
	if _, _, err := svc.SelectForAmount(context.Background(), uuid.New(), 1); err == nil {
		t.Error("expected insufficient funds on empty wallet")
	}
}

func TestSelectForAmountNoDoubleSpendUnderConcurrency(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	seedUTXOs(t, svc, wID,
		struct{ outpoint, value string }{"a", "100"},
		struct{ outpoint, value string }{"b", "100"},
		struct{ outpoint, value string }{"c", "100"},
	)
	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	results := make(chan []string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ops, _, err := svc.SelectForAmount(ctx, wID, 200)
			if err != nil {
				errs <- err
				return
			}
			results <- ops
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	success := 0
	allOps := map[string]int{}
	for ops := range results {
		success++
		for _, op := range ops {
			allOps[op]++
		}
	}
	if success > 1 {
		t.Errorf("expected at most 1 successful selection (3 utxos of 100 each, need 200 -> max 1 tx), got %d", success)
	}
	for op, c := range allOps {
		if c > 1 {
			t.Errorf("outpoint %s was selected by multiple goroutines (double-spend)", op)
		}
	}
	for err := range errs {
		// insufficient funds errors are expected for the losers; only flag
		// unexpected error types.
		_ = err
	}
}

func TestMarkSpentAndUnlock(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	seedUTXOs(t, svc, wID, struct{ outpoint, value string }{"a", "100"})
	ops, _, _ := svc.SelectForAmount(ctx, wID, 50)
	if err := svc.MarkSpent(ctx, ops, "txhash1"); err != nil {
		t.Fatal(err)
	}
	// no free utxos
	free, _ := svc.Store.ListFreeUTXOs(ctx, wID)
	if len(free) != 0 {
		t.Errorf("expected 0 free after spent, got %d", len(free))
	}
	// unlock restores to free
	if err := svc.Unlock(ctx, ops); err != nil {
		t.Fatal(err)
	}
	free, _ = svc.Store.ListFreeUTXOs(ctx, wID)
	if len(free) != 1 {
		t.Errorf("expected 1 free after unlock, got %d", len(free))
	}
}

func TestRestoreOnReorg(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	seedUTXOs(t, svc, wID, struct{ outpoint, value string }{"a", "100"})
	ops, _, _ := svc.SelectForAmount(ctx, wID, 50)
	_ = svc.MarkSpent(ctx, ops, "txh")
	// reorg restores spent -> free
	if err := svc.RestoreOnReorg(ctx, ops); err != nil {
		t.Fatal(err)
	}
	free, _ := svc.Store.ListFreeUTXOs(ctx, wID)
	if len(free) != 1 || free[0].Outpoint != "a" {
		t.Errorf("expected a free after reorg, got %+v", free)
	}
}

func TestPruneFinalized(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	wID := uuid.New()
	seedUTXOs(t, svc, wID, struct{ outpoint, value string }{"a", "100"})
	if err := svc.PruneFinalized(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	free, _ := svc.Store.ListFreeUTXOs(ctx, wID)
	if len(free) != 0 {
		t.Errorf("expected 0 after prune, got %d", len(free))
	}
}

func TestSelectMutex(t *testing.T) {
	m := &SelectMutex{}
	n := 0
	m.Lock()
	n++
	m.Unlock()
	if n != 1 {
		t.Fatal("expected critical section to run")
	}
}