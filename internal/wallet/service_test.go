package wallet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/deriver"
	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
)

const (
	evmXpub = "xpub6CeDpm2b5qtk96oy8yvM572W6cLZSvU5vnpKmKPypbfFwXo86SyT7VtfwWtMZAgZ5eKVMU9NnULt91HBFw9j62wJrcoc1ZRWiNvoorwBRXL"
	btcXpub = "xpub6C1HVMz946r433QEjZGpYYWYcspxXXBPys5PBGkmQboRXE6RLfFiStEkKbWKCZaPgDrzZh9nUEunxuiuy6MNdw23du2Ek7GoKYMJVH8eK5E"
)

func newSvc(t *testing.T, cfg config.Config) (*Service, *memstore.Store) {
	t.Helper()
	st := memstore.New()
	c := cache.NewMem()
	evm, err := deriver.NewEVM(evmXpub, c, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sol, err := deriver.NewSolana("11111111111111111111111111111111", c, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	btc, err := deriver.NewBTC(btcXpub, &chaincfg.MainNetParams, c, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reg := deriver.NewRegistry(evm, sol, btc)
	lk := lock.NewMemLocker()
	em := audit.NoopEmitter{}
	return NewService(st, reg, lk, em, cfg), st
}

func defaultCfg() config.Config {
	return config.Config{DefaultRotationDays: 7}
}

func TestCreateWallet(t *testing.T) {
	svc, st := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, err := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "eth-hot"})
	if err != nil {
		t.Fatal(err)
	}
	if w.State != WalletStateActive || w.CustodianRef != "mpc" || w.KeyID == "" {
		t.Errorf("unexpected wallet: %+v", w)
	}
	got, _ := st.GetWallet(ctx, w.ID)
	if got.Label != "eth-hot" {
		t.Errorf("persisted label mismatch: %s", got.Label)
	}
}

func TestCreateWalletValidation(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	cases := []CreateRequest{
		{Chain: "cardano", Type: WalletTypeHot, Label: "x"},
		{Chain: ChainEthereum, Type: "quantum", Label: "x"},
		{Chain: ChainEthereum, Type: WalletTypeHot, Label: ""},
	}
	for i, req := range cases {
		if _, err := svc.Create(ctx, req); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestCreateWalletWithKeyID(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	w, err := svc.Create(context.Background(), CreateRequest{Chain: ChainBitcoin, Type: WalletTypeCold, Label: "btc", KeyID: "k-explicit"})
	if err != nil {
		t.Fatal(err)
	}
	if w.KeyID != "k-explicit" {
		t.Errorf("expected explicit key id, got %s", w.KeyID)
	}
}

func TestGetWalletNotFound(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	if _, err := svc.Get(context.Background(), uuid.New()); err == nil {
		t.Error("expected not found")
	}
}

func TestSetState(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, _ := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w"})
	if err := svc.SetState(ctx, w.ID, WalletStatePaused); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, w.ID)
	if got.State != WalletStatePaused {
		t.Errorf("expected paused, got %s", got.State)
	}
	// un-retire not allowed
	if err := svc.SetState(ctx, w.ID, WalletStateRetired); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetState(ctx, w.ID, WalletStateActive); err == nil {
		t.Error("expected error un-retiring wallet")
	}
	// invalid state
	if err := svc.SetState(ctx, w.ID, WalletState("zombie")); err == nil {
		t.Error("expected error on invalid state")
	}
	// missing wallet
	if err := svc.SetState(ctx, uuid.New(), WalletStateActive); err == nil {
		t.Error("expected not found on missing wallet")
	}
}

func TestListWallets(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	_, _ = svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "a"})
	_, _ = svc.Create(ctx, CreateRequest{Chain: ChainBitcoin, Type: WalletTypeCold, Label: "b"})
	all, err := svc.List(ctx, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2, got %d", len(all))
	}
	eth, _ := svc.List(ctx, "ethereum", "", "")
	if len(eth) != 1 {
		t.Errorf("expected 1 eth, got %d", len(eth))
	}
}

func TestDeriveAddressFirst(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, _ := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w"})
	addr, err := svc.DeriveAddress(ctx, w.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if addr.State != AddressStateActive || addr.WalletID != w.ID {
		t.Errorf("unexpected address: %+v", addr)
	}
	// second call without force returns same active address
	addr2, _ := svc.DeriveAddress(ctx, w.ID, false)
	if addr2.ID != addr.ID {
		t.Errorf("expected same active address, got different")
	}
}

func TestDeriveAddressRetiredRejected(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, _ := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w"})
	_ = svc.SetState(ctx, w.ID, WalletStateRetired)
	if _, err := svc.DeriveAddress(ctx, w.ID, false); !errors.Is(err, ErrWalletRetired) {
		t.Errorf("expected ErrWalletRetired, got %v", err)
	}
	if _, err := svc.DeriveAddress(ctx, w.ID, true); !errors.Is(err, ErrWalletRetired) {
		t.Errorf("expected ErrWalletRetired on force, got %v", err)
	}
}

func TestDeriveMissingWallet(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	if _, err := svc.DeriveAddress(context.Background(), uuid.New(), false); err == nil {
		t.Error("expected not found")
	}
	if _, err := svc.DeriveAddress(context.Background(), uuid.New(), true); err == nil {
		t.Error("expected not found on force")
	}
}

func TestForceRotate(t *testing.T) {
	svc, st := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, _ := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w"})
	a1, _ := svc.DeriveAddress(ctx, w.ID, false)
	a2, _ := svc.DeriveAddress(ctx, w.ID, true)
	if a1.ID == a2.ID {
		t.Error("force rotate should produce new address")
	}
	// old should be deprecated
	old, _ := st.GetAddress(ctx, a1.ID)
	if old.State != AddressStateDeprecated {
		t.Errorf("expected old deprecated, got %s", old.State)
	}
	// new is active
	cur, _ := st.GetActiveAddress(ctx, w.ID)
	if cur.ID != a2.ID {
		t.Error("new address should be active")
	}
}

func TestTimeBasedRotation(t *testing.T) {
	cfg := defaultCfg()
	svc, st := newSvc(t, cfg)
	ctx := context.Background()
	// Create a wallet directly in the store with rotation_days=1 and an
	// already-aged active address, so the next non-force derive rotates.
	rd := 1
	w := &Wallet{
		ID: uuid.New(), Chain: ChainEthereum, Type: WalletTypeHot, Label: "w",
		State: WalletStateActive, KeyID: "k", CustodianRef: "mpc",
		RotationDays: &rd,
		CreatedAt:    time.Now().Add(-48 * time.Hour),
		UpdatedAt:    time.Now(),
	}
	if err := st.CreateWallet(ctx, w); err != nil {
		t.Fatal(err)
	}
	old := &Address{
		ID: uuid.New(), WalletID: w.ID, Chain: ChainEthereum, Address: "0xold",
		Index: 0, Change: 0, State: AddressStateActive,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	}
	if err := st.InsertAddress(ctx, old); err != nil {
		t.Fatal(err)
	}
	a2, err := svc.DeriveAddress(ctx, w.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if a2.ID == old.ID {
		t.Error("time-based rotation should produce a new address")
	}
	gotOld, _ := st.GetAddress(ctx, old.ID)
	if gotOld.State != AddressStateDeprecated {
		t.Errorf("expected old deprecated, got %s", gotOld.State)
	}
}

func TestCountBasedRotation(t *testing.T) {
	cfg := defaultCfg()
	svc, st := newSvc(t, cfg)
	ctx := context.Background()
	rc := 2
	w := &Wallet{
		ID: uuid.New(), Chain: ChainEthereum, Type: WalletTypeHot, Label: "w",
		State: WalletStateActive, KeyID: "k", CustodianRef: "mpc",
		RotationAfterReceives: &rc,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.CreateWallet(ctx, w); err != nil {
		t.Fatal(err)
	}
	a1, err := svc.DeriveAddress(ctx, w.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.IncrementReceiveCount(ctx, a1.ID)
	same, _ := svc.DeriveAddress(ctx, w.ID, false)
	if same.ID != a1.ID {
		t.Error("should not rotate before reaching receive threshold")
	}
	_ = st.IncrementReceiveCount(ctx, a1.ID)
	a3, _ := svc.DeriveAddress(ctx, w.ID, false)
	if a3.ID == a1.ID {
		t.Error("should rotate after reaching receive threshold")
	}
}

func TestConcurrentDerive(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, _ := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w"})
	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	addrs := make(chan *Address, n)
	// Non-force derive on a fresh wallet: one goroutine creates the active
	// address under the per-wallet lock; the rest should observe it.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := svc.DeriveAddress(ctx, w.ID, false)
			if err != nil {
				errs <- err
				return
			}
			addrs <- a
		}()
	}
	wg.Wait()
	close(errs)
	close(addrs)
	for err := range errs {
		t.Fatalf("concurrent derive error: %v", err)
	}
	first := uuid.Nil
	for a := range addrs {
		if first == uuid.Nil {
			first = a.ID
		} else if a.ID != first {
			t.Errorf("expected all concurrent derives to return same address, got %s and %s", first, a.ID)
		}
	}
}

func TestConcurrentForceRotateDistinctWallets(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	addrs := make(chan *Address, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w"})
			if err != nil {
				errs <- err
				return
			}
			a, err := svc.DeriveAddress(ctx, w.ID, true)
			if err != nil {
				errs <- err
				return
			}
			addrs <- a
		}()
	}
	wg.Wait()
	close(errs)
	close(addrs)
	for err := range errs {
		t.Fatalf("concurrent force-rotate error: %v", err)
	}
	seen := map[uuid.UUID]bool{}
	for a := range addrs {
		if seen[a.ID] {
			t.Error("duplicate address id across distinct wallets")
		}
		seen[a.ID] = true
	}
}

func TestListAddresses(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, _ := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w"})
	_, _ = svc.DeriveAddress(ctx, w.ID, false)
	_, _ = svc.DeriveAddress(ctx, w.ID, true)
	list, err := svc.ListAddresses(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 addresses, got %d", len(list))
	}
}

// Two wallets on the same chain share that chain's xpub, so index allocation
// must be chain-global — per-wallet numbering would derive the same address
// for both wallets (regression: unique (chain, address) violation).
func TestTwoWalletsSameChainDeriveDistinctAddresses(t *testing.T) {
	svc, _ := newSvc(t, defaultCfg())
	ctx := context.Background()
	w1, err := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(ctx, CreateRequest{Chain: ChainEthereum, Type: WalletTypeHot, Label: "w2"})
	if err != nil {
		t.Fatal(err)
	}
	a1, err := svc.DeriveAddress(ctx, w1.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := svc.DeriveAddress(ctx, w2.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Address == a2.Address {
		t.Errorf("wallets sharing a chain xpub derived the same address %s", a1.Address)
	}
	if a1.Index == a2.Index {
		t.Errorf("expected distinct chain-global indexes, both got %d", a1.Index)
	}
}

func TestSolanaDeriveSingle(t *testing.T) {
	svc, st := newSvc(t, defaultCfg())
	ctx := context.Background()
	w, _ := svc.Create(ctx, CreateRequest{Chain: ChainSolana, Type: WalletTypeHot, Label: "sol"})
	a1, err := svc.DeriveAddress(ctx, w.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Index != 0 {
		t.Errorf("expected index 0, got %d", a1.Index)
	}
	// forcing rotate on solana deprecates the old and creates a new one (same address string, different id)
	a2, _ := svc.DeriveAddress(ctx, w.ID, true)
	if a2.ID == a1.ID {
		t.Error("force rotate on solana should create a new row")
	}
	list, _ := st.ListAddresses(ctx, w.ID)
	if len(list) != 2 {
		t.Errorf("expected 2 solana addresses, got %d", len(list))
	}
}