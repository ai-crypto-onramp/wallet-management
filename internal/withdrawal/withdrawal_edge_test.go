package withdrawal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/policy"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

// errStore wraps memstore.Store and lets a test inject failures on specific
// methods. It is used to exercise the error branches of the withdrawal
// Service that the happy-path tests do not reach.
type errStore struct {
	*memstore.Store
	getWalletErr        error
	getWithdrawalErr    error
	updateWithdrawalErr error
	restoreUTXOsErr     error
}

func (s *errStore) GetWallet(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	if s.getWalletErr != nil {
		return nil, s.getWalletErr
	}
	return s.Store.GetWallet(ctx, id)
}

func (s *errStore) GetWithdrawal(ctx context.Context, id uuid.UUID) (*storage.WithdrawalRequest, error) {
	if s.getWithdrawalErr != nil {
		return nil, s.getWithdrawalErr
	}
	return s.Store.GetWithdrawal(ctx, id)
}

func (s *errStore) UpdateWithdrawalState(ctx context.Context, id uuid.UUID, state, reason, txHash, policyDecisionID string) error {
	if s.updateWithdrawalErr != nil {
		return s.updateWithdrawalErr
	}
	return s.Store.UpdateWithdrawalState(ctx, id, state, reason, txHash, policyDecisionID)
}

func (s *errStore) RestoreUTXOs(ctx context.Context, outpoints []string) error {
	if s.restoreUTXOsErr != nil {
		return s.restoreUTXOsErr
	}
	return s.Store.RestoreUTXOs(ctx, outpoints)
}

func newErrEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	return e
}

func TestConstructAndSignGetWithdrawalError(t *testing.T) {
	e := newErrEnv(t)
	e.svc.Store = &errStore{Store: e.st, getWithdrawalErr: errors.New("db down")}
	if err := e.svc.ConstructAndSign(context.Background(), uuid.New()); err == nil {
		t.Error("expected error when GetWithdrawal fails")
	}
}

func TestConstructAndSignKeyLookupError(t *testing.T) {
	e := newErrEnv(t)
	e.svc.KeyLookup = &errKeyResolver{err: errors.New("kms down")}
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err == nil {
		t.Error("expected error when key lookup fails")
	}
}

func TestConstructAndSignBTCUTXOSelectError(t *testing.T) {
	e := newErrEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	// No UTXOs seeded — SelectForAmount will return insufficient funds.
	wr := &storage.WithdrawalRequest{ID: uuid.New(), WalletID: w.ID, ToAddress: validBTCA, Asset: "btc", Amount: "999", State: "WHITELISTED"}
	if err := e.st.CreateWithdrawal(context.Background(), wr); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err == nil {
		t.Error("expected error on insufficient BTC UTXOs")
	}
}

func TestBroadcastGetWithdrawalError(t *testing.T) {
	e := newErrEnv(t)
	e.svc.Store = &errStore{Store: e.st, getWithdrawalErr: errors.New("db down")}
	if err := e.svc.Broadcast(context.Background(), uuid.New()); err == nil {
		t.Error("expected error when GetWithdrawal fails in Broadcast")
	}
}

func TestBroadcastGetWalletError(t *testing.T) {
	e := newErrEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	// Swap in a store whose GetWallet fails only after the withdrawal is fetched.
	e.svc.Store = &errStore{Store: e.st, getWalletErr: errors.New("db down")}
	if err := e.svc.Broadcast(context.Background(), wr.ID); err == nil {
		t.Error("expected error when GetWallet fails in Broadcast")
	}
}

func TestConfirmGetWithdrawalError(t *testing.T) {
	e := newErrEnv(t)
	e.svc.Store = &errStore{Store: e.st, getWithdrawalErr: errors.New("db down")}
	if err := e.svc.Confirm(context.Background(), uuid.New(), "0x1"); err == nil {
		t.Error("expected error when GetWithdrawal fails in Confirm")
	}
}

func TestConfirmUpdateStateError(t *testing.T) {
	e := newErrEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	_ = e.svc.Broadcast(context.Background(), wr.ID)
	e.svc.Store = &errStore{Store: e.st, updateWithdrawalErr: errors.New("db down")}
	if err := e.svc.Confirm(context.Background(), wr.ID, "0xreal"); err == nil {
		t.Error("expected error when UpdateWithdrawalState fails in Confirm")
	}
}

func TestFailGetWithdrawalError(t *testing.T) {
	e := newErrEnv(t)
	e.svc.Store = &errStore{Store: e.st, getWithdrawalErr: errors.New("db down")}
	if err := e.svc.Fail(context.Background(), uuid.New(), "manual"); err == nil {
		t.Error("expected error when GetWithdrawal fails in Fail")
	}
}

func TestFailUpdateStateError(t *testing.T) {
	e := newErrEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	e.svc.Store = &errStore{Store: e.st, updateWithdrawalErr: errors.New("db down")}
	if err := e.svc.Fail(context.Background(), wr.ID, "manual"); err == nil {
		t.Error("expected error when UpdateWithdrawalState fails in Fail")
	}
}

func TestOnReorgGetWithdrawalError(t *testing.T) {
	e := newErrEnv(t)
	e.svc.Store = &errStore{Store: e.st, getWithdrawalErr: errors.New("db down")}
	if err := e.svc.OnReorg(context.Background(), uuid.New(), nil); err == nil {
		t.Error("expected error when GetWithdrawal fails in OnReorg")
	}
}

func TestOnReorgRestoreError(t *testing.T) {
	e := newErrEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: validOutpointA, WalletID: w.ID, Value: "100", LockState: "FREE"})
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validBTCA, Asset: "btc", Amount: "100"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	_ = e.svc.Broadcast(context.Background(), wr.ID)
	_ = e.svc.Confirm(context.Background(), wr.ID, "0xtx")
	// Point the UTXO service at a failing store so RestoreOnReorg errors.
	e.svc.UTXOs.Store = &errStore{Store: e.st, restoreUTXOsErr: errors.New("utxo restore down")}
	if err := e.svc.OnReorg(context.Background(), wr.ID, []string{validOutpointA}); err == nil {
		t.Error("expected error when RestoreUTXOs fails in OnReorg")
	}
}

func TestConstructAndSignGetWalletError(t *testing.T) {
	e := newErrEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	e.svc.Store = &errStore{Store: e.st, getWalletErr: errors.New("db down")}
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err == nil {
		t.Error("expected error when GetWallet fails in ConstructAndSign")
	}
}

func TestCreateUpdateWithdrawalStateError(t *testing.T) {
	e := newErrEnv(t)
	// Whitelisted path: after approval, UpdateWithdrawalState("WHITELISTED")
	// is called. Inject a failure there.
	e.svc.Store = &errStore{Store: e.st, updateWithdrawalErr: errors.New("db down")}
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	_, err := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	if err == nil {
		t.Error("expected error when UpdateWithdrawalState fails during Create whitelist path")
	}
}

func TestReserveNonceErrorInConstructAndSign(t *testing.T) {
	e := newErrEnv(t)
	e.svc.Nonces.Locker = &errLocker{}
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err == nil {
		t.Error("expected error when ReserveNonce fails in ConstructAndSign")
	}
}

func TestDecisionIDBranches(t *testing.T) {
	if got := decisionID(nil, errors.New("x")); got != "error" {
		t.Errorf("expected 'error', got %q", got)
	}
	if got := decisionID(nil, nil); got != "nil" {
		t.Errorf("expected 'nil', got %q", got)
	}
	if got := decisionID(&policy.CheckResponse{DecisionID: "d1"}, nil); got != "d1" {
		t.Errorf("expected 'd1', got %q", got)
	}
}

// errLocker always returns an error on Acquire.
type errLocker struct{}

func (errLocker) Acquire(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, errors.New("lock acquire failed")
}

func (errLocker) Release(_ context.Context, _, _ string) error { return nil }

// errKeyResolver always returns an error.
type errKeyResolver struct{ err error }

func (r *errKeyResolver) ResolveActiveKeyID(_ context.Context, _ uuid.UUID) (string, error) {
	return "", r.err
}
