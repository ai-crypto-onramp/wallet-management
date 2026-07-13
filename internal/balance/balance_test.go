package balance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

func newSvc(cfg config.Config) (*Service, *memstore.Store) {
	st := memstore.New()
	return NewService(st, nil, cfg), st
}

func defaultCfg() config.Config {
	return config.Config{ConfirmationsEVM: 12, ConfirmationsBTC: 6}
}

func TestEVMConfirmationThreshold(t *testing.T) {
	svc, st := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	// below threshold -> pending
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "100", Confirmations: 5, BlockHeight: 1, EventID: "e1", Chain: wallet.ChainEthereum,
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := st.GetBalance(ctx, wID, "eth")
	if b.Pending != "100" || b.Confirmed != "0" {
		t.Errorf("expected pending=100 confirmed=0, got %+v", b)
	}
	// at threshold -> confirmed
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "50", Confirmations: 12, BlockHeight: 2, EventID: "e2", Chain: wallet.ChainEthereum,
	}); err != nil {
		t.Fatal(err)
	}
	b, _ = st.GetBalance(ctx, wID, "eth")
	if b.Confirmed != "50" || b.Pending != "100" {
		t.Errorf("expected confirmed=50 pending=100, got %+v", b)
	}
}

func TestBTCConfirmationThreshold(t *testing.T) {
	svc, _ := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "btc", Amount: "10", Confirmations: 5, BlockHeight: 1, EventID: "b1", Chain: wallet.ChainBitcoin,
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := svc.Store.GetBalance(ctx, wID, "btc")
	if b.Pending != "10" || b.Confirmed != "0" {
		t.Errorf("expected pending=10, got %+v", b)
	}
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "btc", Amount: "20", Confirmations: 6, BlockHeight: 2, EventID: "b2", Chain: wallet.ChainBitcoin,
	}); err != nil {
		t.Fatal(err)
	}
	b, _ = svc.Store.GetBalance(ctx, wID, "btc")
	if b.Confirmed != "20" || b.Pending != "10" {
		t.Errorf("expected confirmed=20 pending=10, got %+v", b)
	}
}

func TestSolanaFinalizedOnly(t *testing.T) {
	svc, _ := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	// not finalized -> pending
	_ = svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "sol", Amount: "5", Confirmations: 100, BlockHeight: 1, EventID: "s1", Chain: wallet.ChainSolana,
	})
	b, _ := svc.Store.GetBalance(ctx, wID, "sol")
	if b.Pending != "5" || b.Confirmed != "0" {
		t.Errorf("expected pending=5, got %+v", b)
	}
	// finalized -> confirmed
	_ = svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "sol", Amount: "7", Confirmations: 0, IsFinalized: true, BlockHeight: 2, EventID: "s2", Chain: wallet.ChainSolana,
	})
	b, _ = svc.Store.GetBalance(ctx, wID, "sol")
	if b.Confirmed != "7" || b.Pending != "5" {
		t.Errorf("expected confirmed=7 pending=5, got %+v", b)
	}
}

func TestReorgDemotion(t *testing.T) {
	svc, st := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	// build up confirmed balance
	_ = svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "100", Confirmations: 20, BlockHeight: 5, EventID: "r1", Chain: wallet.ChainEthereum,
	})
	b, _ := st.GetBalance(ctx, wID, "eth")
	if b.Confirmed != "100" {
		t.Fatalf("setup failed: confirmed=%s", b.Confirmed)
	}
	restored := false
	svc.UTXORestore = func(ctx context.Context, ops []string) error {
		restored = true
		if len(ops) != 1 || ops[0] != "utxo1" {
			t.Errorf("expected utxo1, got %+v", ops)
		}
		return nil
	}
	if err := svc.ApplyReorgEvent(ctx, &ReorgEvent{
		WalletID: wID, Asset: "eth", BlockHeight: 5, EventID: "reorg1", Outpoints: []string{"utxo1"},
	}); err != nil {
		t.Fatal(err)
	}
	b, _ = st.GetBalance(ctx, wID, "eth")
	if b.Confirmed != "0" || b.Pending != "100" {
		t.Errorf("expected demoted to pending, got confirmed=%s pending=%s", b.Confirmed, b.Pending)
	}
	if !restored {
		t.Error("UTXORestore callback not invoked")
	}
}

func TestReorgNoBalance(t *testing.T) {
	svc, _ := newSvc(defaultCfg())
	// reorg on a wallet with no balance row should not error
	if err := svc.ApplyReorgEvent(context.Background(), &ReorgEvent{WalletID: uuid.New(), Asset: "eth"}); err != nil {
		t.Errorf("expected nil on missing balance, got %v", err)
	}
}

func TestReorgWithoutRestoreCallback(t *testing.T) {
	svc, _ := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	_ = svc.Store.UpsertBalance(ctx, &storage.Balance{WalletID: wID, Asset: "eth", Confirmed: "50"})
	// no UTXORestore set, but outpoints provided — should still not panic
	if err := svc.ApplyReorgEvent(ctx, &ReorgEvent{WalletID: wID, Asset: "eth", Outpoints: []string{"x"}}); err != nil {
		t.Errorf("expected nil when no restore callback, got %v", err)
	}
}

func TestIdempotentEventApplication(t *testing.T) {
	svc, st := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	ev := &ConfirmationEvent{WalletID: wID, Asset: "eth", Amount: "100", Confirmations: 20, BlockHeight: 5, EventID: "dup1", Chain: wallet.ChainEthereum}
	if err := svc.ApplyConfirmationEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	// second application is a no-op (dedup)
	if err := svc.ApplyConfirmationEvent(ctx, ev); err != nil {
		t.Fatalf("dedup should not error: %v", err)
	}
	b, _ := st.GetBalance(ctx, wID, "eth")
	if b.Confirmed != "100" {
		t.Errorf("expected 100 (no double count), got %s", b.Confirmed)
	}
}

func TestGetBalances(t *testing.T) {
	svc, st := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	_ = st.UpsertBalance(ctx, &storage.Balance{WalletID: wID, Asset: "a", Confirmed: "1"})
	_ = st.UpsertBalance(ctx, &storage.Balance{WalletID: wID, Asset: "b", Confirmed: "2"})
	list, err := svc.GetBalances(ctx, wID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 balances, got %d", len(list))
	}
}

func TestAddLockedAndReleaseLocked(t *testing.T) {
	svc, st := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	_ = st.UpsertBalance(ctx, &storage.Balance{WalletID: wID, Asset: "eth", Confirmed: "100", Locked: "10"})
	if err := svc.AddLocked(ctx, wID, "eth", "30"); err != nil {
		t.Fatal(err)
	}
	b, _ := st.GetBalance(ctx, wID, "eth")
	if b.Locked != "40" {
		t.Errorf("expected locked=40, got %s", b.Locked)
	}
	if err := svc.ReleaseLocked(ctx, wID, "eth", "15"); err != nil {
		t.Fatal(err)
	}
	b, _ = st.GetBalance(ctx, wID, "eth")
	if b.Locked != "25" {
		t.Errorf("expected locked=25, got %s", b.Locked)
	}
	// release more than locked -> clamp to 0
	_ = svc.ReleaseLocked(ctx, wID, "eth", "999")
	b, _ = st.GetBalance(ctx, wID, "eth")
	if b.Locked != "0" {
		t.Errorf("expected clamped to 0, got %s", b.Locked)
	}
}

func TestAddLockedMissingBalance(t *testing.T) {
	svc, _ := newSvc(defaultCfg())
	if err := svc.AddLocked(context.Background(), uuid.New(), "eth", "10"); err == nil {
		t.Error("expected error on missing balance for AddLocked")
	}
	if err := svc.ReleaseLocked(context.Background(), uuid.New(), "eth", "10"); err == nil {
		t.Error("expected error on missing balance for ReleaseLocked")
	}
}

func TestThresholdUnknownChain(t *testing.T) {
	// unknown chain falls back to EVM threshold
	svc, _ := newSvc(defaultCfg())
	if svc.threshold("cardano") != defaultCfg().ConfirmationsEVM {
		t.Error("expected EVM fallback for unknown chain")
	}
}

func TestOnConfirmedDecreaseHook(t *testing.T) {
	svc, _ := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()

	var gotWallet uuid.UUID
	var gotAsset string
	calls := 0
	svc.OnConfirmedDecrease = func(walletID uuid.UUID, asset string) {
		gotWallet, gotAsset = walletID, asset
		calls++
	}

	// A confirmed deposit must NOT trigger the hook.
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "100", Confirmations: 12, BlockHeight: 1, EventID: "d1", Chain: wallet.ChainEthereum,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("hook fired on deposit: %d calls", calls)
	}

	// A pending (below-threshold) decrease must NOT trigger the hook.
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "-10", Confirmations: 3, BlockHeight: 2, EventID: "p1", Chain: wallet.ChainEthereum,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("hook fired on pending decrease: %d calls", calls)
	}

	// A confirmed decrease triggers the hook exactly once.
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "-50", Confirmations: 12, BlockHeight: 3, EventID: "w1", Chain: wallet.ChainEthereum,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || gotWallet != wID || gotAsset != "eth" {
		t.Fatalf("expected 1 call for (%s, eth), got %d (%s, %s)", wID, calls, gotWallet, gotAsset)
	}

	// A duplicate event is idempotent and must not re-trigger.
	if err := svc.ApplyConfirmationEvent(ctx, &ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "-50", Confirmations: 12, BlockHeight: 3, EventID: "w1", Chain: wallet.ChainEthereum,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("hook re-fired on duplicate event: %d calls", calls)
	}
}

func TestConcurrentEventApplication(t *testing.T) {
	svc, _ := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := &ConfirmationEvent{
				WalletID: wID, Asset: "eth", Amount: "1", Confirmations: 20,
				BlockHeight: int64(i), EventID: "ev" + itoa(i), Chain: wallet.ChainEthereum,
			}
			if err := svc.ApplyConfirmationEvent(ctx, ev); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent apply error: %v", err)
	}
	b, _ := svc.Store.GetBalance(ctx, wID, "eth")
	if b.Confirmed != "20" {
		t.Errorf("expected confirmed=20, got %s", b.Confirmed)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

var _ = errors.New