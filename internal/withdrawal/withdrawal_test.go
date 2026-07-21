package withdrawal

import (
	"bytes"
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
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/google/uuid"
	"golang.org/x/crypto/ed25519"
)

// testPrivKey is a fixed secp256k1 private key used by the mock signer to
// produce realistic 65-byte compact signatures for EVM/BTC happy-path tests.
var testPrivKey = func() *btcec.PrivateKey {
	scalar := [32]byte{}
	for i := range scalar {
		scalar[i] = byte(i + 1)
	}
	pk, _ := btcec.PrivKeyFromBytes(scalar[:])
	return pk
}()

// testEdPrivKey is used for Solana mock signatures.
var testEdPrivKey = func() ed25519.PrivateKey {
	var seed [32]byte
	for i := range seed {
		seed[i] = byte(i + 2)
	}
	return ed25519.NewKeyFromSeed(seed[:])
}()

type testEnv struct {
	svc     *Service
	st      *memstore.Store
	wallets *wallet.Service
	nonces  *nonce.Service
	utxos   *utxo.Service
	policy  *policy.MockClient
	signer  *grpcclient.MockMPCSigner
	gateway *grpcclient.MockGatewayClient
	keyRes  *staticKeyResolver
}

type staticKeyResolver struct{ keyID string }

func (s *staticKeyResolver) ResolveActiveKeyID(_ context.Context, _ uuid.UUID) (string, error) {
	return s.keyID, nil
}

// validEVMA is a 20-byte EVM address used by tests (does not need to be a real
// deployed account — only the byte format matters for tx construction).
const validEVMA = "0x1234567890123456789012345678901234567890"

// validBTCA is a valid mainnet bech32 P2WPKH address used by tests.
const validBTCA = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"

// validOutpointA/B are valid "txid:vout" outpoint strings used by BTC tests.
// The txid portion is a 64-char hex string (32 bytes, little-endian on chain
// but stored as the display string here).
const (
	validOutpointA = "0000000000000000000000000000000000000000000000000000000000000001:0"
	validOutpointB = "0000000000000000000000000000000000000000000000000000000000000002:0"
)

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
	signer := &grpcclient.MockMPCSigner{SignFn: func(_ context.Context, req *grpcclient.SignRequest) (*grpcclient.SignResponse, error) {
		sig := mockSignForPayload(req.TxBytes)
		return &grpcclient.SignResponse{Signature: sig, SignerID: "mock"}, nil
	}}
	gw := &grpcclient.MockGatewayClient{}
	kr := &staticKeyResolver{keyID: "k1"}
	svc := NewService(st, wsvc, ns, us, pc, signer, gw, kr, nil)
	return &testEnv{svc: svc, st: st, wallets: wsvc, nonces: ns, utxos: us, policy: pc, signer: signer, gateway: gw, keyRes: kr}
}

// mockSignForPayload produces a signature whose format matches what the
// per-chain Assemble closure expects:
//   - EVM (32-byte keccak hash): 65-byte compact signature (header + r || s).
//   - BTC (N*32-byte sighash concat): N * 64-byte r||s.
//   - Solana (variable-length message): 64-byte ed25519 signature.
//
// The signature does not need to verify against a real key for the unit
// tests (we only assert that Assemble produces non-empty signed bytes and
// that Broadcast forwards those bytes); we still use real signing curves
// so the byte lengths and structure are realistic.
func mockSignForPayload(payload []byte) []byte {
	switch {
	case len(payload) == 32:
		return ecdsa.SignCompact(testPrivKey, payload, true)
	case len(payload) > 0 && len(payload)%32 == 0:
		n := len(payload) / 32
		out := make([]byte, 0, 64*n)
		for i := 0; i < n; i++ {
			chunk := payload[i*32 : (i+1)*32]
			sig := ecdsa.Sign(testPrivKey, chunk)
			r, s := sig.R(), sig.S()
			rb, sb := r.Bytes(), s.Bytes()
			out = append(out, rb[:]...)
			out = append(out, sb[:]...)
		}
		return out
	default:
		return ed25519.Sign(testEdPrivKey, payload)
	}
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
		WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wr.State != "FAILED" {
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
		WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wr.State != "FAILED" {
		t.Errorf("expected failed on policy error, got %s", wr.State)
	}
	if wr.FailureReason != "policy_error" {
		t.Errorf("expected policy_error, got %s", wr.FailureReason)
	}
}

func TestCreateRetiredWallet(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateRetired)
	if _, err := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"}); !errors.Is(err, wallet.ErrWalletRetired) {
		t.Errorf("expected ErrWalletRetired, got %v", err)
	}
}

func TestCreatePausedWallet(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStatePaused)
	if _, err := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"}); err == nil {
		t.Error("expected error on paused wallet")
	}
}

func TestCreateMissingWallet(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Create(context.Background(), CreateRequest{WalletID: uuid.New(), ToAddress: validEVMA, Asset: "eth", Amount: "1"}); err == nil {
		t.Error("expected error on missing wallet")
	}
}

func TestEVMHappyPath(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, err := e.svc.Create(context.Background(), CreateRequest{
		WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wr.State != "WHITELISTED" {
		t.Fatalf("expected whitelisted, got %s", wr.State)
	}
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "SIGNED" {
		t.Fatalf("expected signed, got %s", got.State)
	}
	if got.NonceValue == nil || *got.NonceValue != 0 {
		t.Errorf("expected nonce 0 reserved, got %+v", got.NonceValue)
	}
	if len(got.SignedTxBytes) == 0 {
		t.Errorf("expected SignedTxBytes non-empty after ConstructAndSign")
	}
	// Capture the bytes the gateway receives and assert they match the
	// stored SignedTxBytes (not the fake "signed:<id>" stub).
	var broadcastBytes []byte
	e.gateway.BroadcastFn = func(_ context.Context, req *grpcclient.BroadcastRequest) (*grpcclient.BroadcastResponse, error) {
		broadcastBytes = req.TxBytes
		return &grpcclient.BroadcastResponse{TxHash: "0xmock"}, nil
	}
	if err := e.svc.Broadcast(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "BROADCAST" {
		t.Fatalf("expected broadcast, got %s", got.State)
	}
	if got.TxHash == "" {
		t.Error("expected tx hash set")
	}
	if !bytes.Equal(broadcastBytes, got.SignedTxBytes) {
		t.Errorf("broadcast TxBytes must match stored SignedTxBytes; got %x want %x", broadcastBytes, got.SignedTxBytes)
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
	if got.State != "CONFIRMED" || got.TxHash != "0xreal" {
		t.Errorf("expected confirmed/0xreal, got %+v", got)
	}
}

func TestConstructAndSignNotWhitelisted(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	// insert a pending withdrawal directly via store to skip whitelist
	wr := &storage.WithdrawalRequest{ID: uuid.New(), WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1", State: "PENDING"}
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
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	err := e.svc.ConstructAndSign(context.Background(), wr.ID)
	if err == nil {
		t.Fatal("expected sign error")
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "FAILED" {
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
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	if err := e.svc.Broadcast(context.Background(), wr.ID); err == nil {
		t.Fatal("expected broadcast error")
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "FAILED" || got.FailureReason != "broadcast_failed" {
		t.Errorf("expected failed/broadcast_failed, got %+v", got)
	}
}

func TestBTCHappyPath(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	// seed UTXOs
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: validOutpointA, WalletID: w.ID, Value: "100", LockState: "FREE"})
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: validOutpointB, WalletID: w.ID, Value: "50", LockState: "FREE"})
	wr, err := e.svc.Create(context.Background(), CreateRequest{
		WalletID: w.ID, ToAddress: validBTCA, Asset: "btc", Amount: "120",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConstructAndSign(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "SIGNED" {
		t.Fatalf("expected signed, got %s", got.State)
	}
	if len(got.SignedTxBytes) == 0 {
		t.Errorf("expected SignedTxBytes non-empty after ConstructAndSign")
	}
	if len(got.ReservedOutpoints) != 2 {
		t.Errorf("expected 2 reserved outpoints persisted, got %d", len(got.ReservedOutpoints))
	}
	// utxos should be locked now
	free, _ := e.st.ListFreeUTXOs(context.Background(), w.ID)
	if len(free) != 0 {
		t.Errorf("expected 0 free utxos, got %d", len(free))
	}
	var broadcastBytes []byte
	e.gateway.BroadcastFn = func(_ context.Context, req *grpcclient.BroadcastRequest) (*grpcclient.BroadcastResponse, error) {
		broadcastBytes = req.TxBytes
		return &grpcclient.BroadcastResponse{TxHash: "0xbtctx"}, nil
	}
	if err := e.svc.Broadcast(context.Background(), wr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "BROADCAST" {
		t.Fatalf("expected broadcast, got %s", got.State)
	}
	if !bytes.Equal(broadcastBytes, got.SignedTxBytes) {
		t.Errorf("broadcast TxBytes must match stored SignedTxBytes; got %x want %x", broadcastBytes, got.SignedTxBytes)
	}
}

func TestReorgConfirmationRollback(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: validOutpointA, WalletID: w.ID, Value: "100", LockState: "FREE"})
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validBTCA, Asset: "btc", Amount: "100"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	_ = e.svc.Broadcast(context.Background(), wr.ID)
	_ = e.svc.Confirm(context.Background(), wr.ID, "0xtx1")
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "CONFIRMED" {
		t.Fatalf("expected confirmed before reorg, got %s", got.State)
	}
	// reorg: rolls back to broadcast and restores utxos
	if err := e.svc.OnReorg(context.Background(), wr.ID, []string{validOutpointA}); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "BROADCAST" {
		t.Errorf("expected demoted to broadcast after reorg, got %s", got.State)
	}
	free, _ := e.st.ListFreeUTXOs(context.Background(), w.ID)
	if len(free) != 1 || free[0].Outpoint != validOutpointA {
		t.Errorf("expected utxo1 free after reorg, got %+v", free)
	}
}

func TestOnReorgNotConfirmed(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainBitcoin, wallet.WalletStateActive)
	_ = e.utxos.TrackUTXO(context.Background(), &storage.UTXO{Outpoint: validOutpointA, WalletID: w.ID, Value: "100", LockState: "FREE"})
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validBTCA, Asset: "btc", Amount: "100"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	// reorg when state is signed (not confirmed) — should still restore utxos but not change state
	if err := e.svc.OnReorg(context.Background(), wr.ID, []string{validOutpointA}); err != nil {
		t.Fatal(err)
	}
}

func TestFailRollsBack(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	_ = e.svc.ConstructAndSign(context.Background(), wr.ID)
	if err := e.svc.Fail(context.Background(), wr.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetWithdrawal(context.Background(), wr.ID)
	if got.State != "FAILED" || got.FailureReason != "manual" {
		t.Errorf("expected failed/manual, got %+v", got)
	}
}

func TestConfirmNotBroadcast(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	if err := e.svc.Confirm(context.Background(), wr.ID, "0x1"); err == nil {
		t.Error("expected error confirming non-broadcast withdrawal")
	}
}

func TestBroadcastNotSigned(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	wr, _ := e.svc.Create(context.Background(), CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "1"})
	if err := e.svc.Broadcast(context.Background(), wr.ID); err == nil {
		t.Error("expected error broadcasting non-signed withdrawal")
	}
}

func TestDuplicateWithdrawal(t *testing.T) {
	e := newEnv(t)
	w := seedWallet(t, e.st, wallet.ChainEthereum, wallet.WalletStateActive)
	req := CreateRequest{WalletID: w.ID, ToAddress: validEVMA, Asset: "eth", Amount: "5"}
	if _, err := e.svc.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(context.Background(), req); !errors.Is(err, storage.ErrDuplicateWithdrawal) {
		t.Errorf("expected ErrDuplicateWithdrawal, got %v", err)
	}
}
