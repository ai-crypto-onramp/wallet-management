package withdrawal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/grpcclient"
	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/nonce"
	"github.com/ai-crypto-onramp/wallet-management/internal/policy"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/utxo"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

type testEnv struct {
	svc      *Service
	st       *memstore.Store
	wallets  *wallet.Service
	nonces   *nonce.Service
	utxos    *utxo.Service
	policy   *policy.MockClient
	signer   *grpcclient.MockMPCSigner
	gateway  *grpcclient.MockGatewayClient
	keyRes   *staticKeyResolver
}

type staticKeyResolver struct{ keyID string }

func (s *staticKeyResolver) ResolveActiveKeyID(_ context.Context, _ uuid.UUID) (string, error) {
	return s.keyID, nil
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st := memstore.New()
	cfg := config.Config{ConfirmationsEVM: 12, ConfirmationsBTC: 6, KeyCoolingPeriod: time.Hour}
	wsvc := wallet.NewService(st, nil, lock.NewMemLocker(), nil, cfg)
	ns := nonce.NewService(st, lock.NewMemLocker())
	us := utxo.NewService(st)
	pc := &policy.MockClient{CheckFn: func(ctx context.Context, req *policy.CheckRequest) (*policy.CheckResponse, error) {
		return &policy.CheckResponse{Approved: true, DecisionID: "dec-" + req.ToAddress}, nil
	}}
	signer := &grpcclient.MockMPCSigner{}
	gw := &grpcclient.MockGatewayClient{}
	kr := &staticKeyResolver{keyID: "k1"}
	svc := NewService(st, wsvc, ns, us, pc, signer, gw, kr, nil)
	return &testEnv{svc: svc, st: st, wallets: wsvc, nonces: ns, utxos: us, policy: pc, signer: signer, gateway: gw, keyRes: kr}
}

func seedWallet(t *testing.T, st *memstore.Store, chain wallet.Chain, state wallet.WalletState) *wallet.Wallet {
	t.Helper()
	w := &wallet.Wallet{
		ID: uuid.New(), Chain: chain, Type: wallet.WalletTypeHot, Label: "w",
		State: state, KeyID: "k1", CustodianRef: "mpc",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.CreateWallet(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCreateWhitelistReject(t *testing.T) {
	e := newEnv(t)
	e.policy.CheckFn = func(ctx context.Context, req *policy.CheckRequest) (*policy.CheckResponse, error) {
		return &policy.CheckResponse{Approved: false, DecisionID: "rej-1", Reason: "not_whitelisted"}, nil
	}
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, err := e.svc.Create(context.Background(), CreateRequest{
		WalletID: w.ID, ToAddress: "0xbad", Asset: "eth", Amount: "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wr.State != "failed" {
		t.Errorf("expected failed, got %s", wr.State)
	}
	if wr.FailureReason != "not_whitelisted" {
		t.Errorf("expected not_whitelisted, got %s", wr.FailureReason)
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.PolicyDecisionID != "rej-1" {
		t.Errorf("expected decision id persisted, got %s", got.PolicyDecisionID)
	}
}

func TestCreatePolicyError(t *testing.T) {
	e := newEnv(t)
	e.policy.CheckFn = func(ctx context.Context, req *policy.CheckRequest) (*policy.CheckResponse, error) {
		return nil, errors.New("policy engine down")
	}
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, err := e.svc.Create(context.Background(), CreateRequest{
		WalletID: w.ID, ToAddress: "0xbad", Asset: "eth", Amount: "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wr.State != "failed" {
		t.Errorf("expected failed on policy error, got %s", wr.State)
	}
	if wr.FailureReason != "policy_error" {
		t.Errorf("expected policy_error, got %s", wr.FailureReason)
	}
}

func TestCreateRetiredWallet(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateRetired)
	if _, err := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1"}); !errors.Is(err, wallet.ErrWalletRetired) {
		t.Errorf("expected ErrWalletRetired, got %v", err)
	}
}

func TestCreatePausedWallet(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStatePaused)
	if _, err := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1"}); err == nil {
		t.Error("expected error on paused wallet")
	}
}

func TestCreateMissingWallet(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Create(context.Background(), CreateRequest{WalletID: uuid.New(), ToAddress: "0x1", Asset: "eth", Amount: "1"}); err == nil {
		t.Error("expected error on missing wallet")
	}
}

func TestEVMHappyPath(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, err := e.svc.Create(context.Background(), CreateRequest{
		WalletID: w.ID, ToAddress: "0xgood", Asset: "eth", Amount: "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wr.State != "whitelisted" {
		t.Fatalf("expected whitelisted, got %s", wr.State)
	}
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "signed" {
		t.Fatalf("expected signed, got %s", got.State)
	}
	if got.NonceValue == nil || *got.NonceValue != 0 {
		t.Errorf("expected nonce 0 reserved, got %+v", got.NonceValue)
	}
	if err := e.svc.Broadcast(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "broadcast" {
		t.Fatalf("expected broadcast, got %s", got.State)
	}
	if got.TxHash == "" {
		t.Error("expected tx hash set")
	}
	// nonce committed
	n, _ := e.st.GetNonce(context.Background(), w.ID, "ethereum")
	if n.BroadcastNonce != 1 {
		t.Errorf("expected broadcast nonce 1, got %d", n.BroadcastNonce)
	}
	if err := e.svc.Confirm(context.Background(), wr.ID, "0xreal"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "confirmed" || got.TxHash != "0xreal" {
		t.Errorf("expected confirmed/0xreal, got %+v", got)
	}
}

func TestConstructAndSignNotWhitelisted(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	// insert a pending withdrawal directly via store to skip whitelist
	wr := &storage.WithdrawalRequest{ID: uuid.New(), WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1", State: "pending"}
	if err := e.st.CreateWithdrawal(context.Background(), wr); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err == nil {
		t.Error("expected error on non-whitelisted withdrawal")
	}
}

func TestSignFailureRollsBackNonce(t *testing.T) {
	e := newEnv(t)
	e.signer.SignFn = func(ctx context.Context, req *grpcclient.SignRequest) (*grpcclient.SignResponse, error) {
		return nil, errors.New("mpc signing failed")
	}
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1"})
	err := e.svc.ConstructAndSign(context.Background(), wr.ID)
	if err == nil {
		t.Fatal("expected sign error")
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "failed" {
		t.Errorf("expected failed after sign error, got %s", got.State)
	}
	if got.FailureReason != "sign_failed" {
		t.Errorf("expected sign_failed reason, got %s", got.FailureReason)
	}
}

func TestBroadcastFailureRollsBackNonce(t *testing.T) {
	e := newEnv(t)
	e.gateway.BroadcastFn = func(ctx context.Context, req *grpcclient.BroadcastRequest) (*grpcclient.BroadcastResponse, error) {
		return nil, errors.New("gateway rejected")
	}
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	if err := e.svc.Broadcast(context.Background(), wr.ID); err == nil {
		t.Fatal("expected broadcast error")
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "failed" || got.FailureReason != "broadcast_failed" {
		t.Errorf("expected failed/broadcast_failed, got %+v", got)
	}
}

func TestBTCHappyPath(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	// seed UTXOs
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: "utxo1", WalletID: w.ID, Value: "100", LockState: "free"})
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: "utxo2", WalletID: w.ID, Value: "50", LockState: "free"})
	wr, err := e.svc.Create(context.Background(), CreateRequest{
		WalletID: w.ID, ToAddress: "bc1qto", Asset: "btc", Amount: "120",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "signed" {
		t.Fatalf("expected signed, got %s", got.State)
	}
	// utxos should be locked now
	free, _ := e.st.ListFreeUTXOs(context.Background(), w.ID)
	if len(free) != 0 {
		t.Errorf("expected 0 free utxos, got %d", len(free))
	}
	if err := e.svc.Broadcast(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "broadcast" {
		t.Fatalf("expected broadcast, got %s", got.State)
	}
}

func TestReorgConfirmationRollback(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: "utxo1", WalletID: w.ID, Value: "100", LockState: "free"})
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "bc1qto", Asset: "btc", Amount: "100"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	_ = e.svc.Broadcast(context.Background(), wr.ID)
	_ = e.svc.Confirm(context.Background(), wr.ID, "0xtx1")
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "confirmed" {
		t.Fatalf("expected confirmed before reorg, got %s", got.State)
	}
	// reorg: rolls back to broadcast and restores utxos
	if err := e.svc.OnReorg(context.Background(), wr.ID, []string{"utxo1"}); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "broadcast" {
		t.Errorf("expected demoted to broadcast after reorg, got %s", got.State)
	}
	free, _ := e.st.ListFreeUTXOs(context.Background(), w.ID)
	if len(free) != 1 || free[0].Outpoint != "utxo1" {
		t.Errorf("expected utxo1 free after reorg, got %+v", free)
	}
}

func TestOnReorgNotConfirmed(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: "u1", WalletID: w.ID, Value: "100", LockState: "free"})
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "bc1q", Asset: "btc", Amount: "100"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	// reorg when state is signed (not confirmed) — should still restore utxos but not change state
	if err := e.svc.OnReorg(context.Background(), wr.ID, []string{"u1"}); err != nil {
		t.Fatal(err)
	}
}

func TestFailRollsBack(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	if err := e.svc.Fail(context.Background(), wr.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "failed" || got.FailureReason != "manual" {
		t.Errorf("expected failed/manual, got %+v", got)
	}
}

func TestConfirmNotBroadcast(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1"})
	if err := e.svc.Confirm(context.Background(), wr.ID, "0x1"); err == nil {
		t.Error("expected error confirming non-broadcast withdrawal")
	}
}

func TestBroadcastNotSigned(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "1"})
	if err := e.svc.Broadcast(context.Background(), wr.ID); err == nil {
		t.Error("expected error broadcasting non-signed withdrawal")
	}
}

func TestDuplicateWithdrawal(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	req := CreateRequest{WalletID: w.ID, ToAddress: "0x1", Asset: "eth", Amount: "5"}
	if _, err := e.svc.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(context.Background(), req); !errors.Is(err, storage.ErrDuplicateWithdrawal) {
		t.Errorf("expected ErrDuplicateWithdrawal, got %v", err)
	}
}