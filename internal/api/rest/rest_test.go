package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/deriver"
	"github.com/ai-crypto-onramp/wallet-management/internal/funding"
	"github.com/ai-crypto-onramp/wallet-management/internal/grpcclient"
	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/nonce"
	"github.com/ai-crypto-onramp/wallet-management/internal/policy"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/utxo"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/ai-crypto-onramp/wallet-management/internal/withdrawal"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
)

const (
	evmXpub = "xpub6CeDpm2b5qtk96oy8yvM572W6cLZSvU5vnpKmKPypbfFwXo86SyT7VtfwWtMZAgZ5eKVMU9NnULt91HBFw9j62wJrcoc1ZRWiNvoorwBRXL"
	btcXpub = "xpub6C1HVMz946r433QEjZGpYYWYcspxXXBPys5PBGkmQboRXE6RLfFiStEkKbWKCZaPgDrzZh9nUEunxuiuy6MNdw23du2Ek7GoKYMJVH8eK5E"
)

func newDeps(t *testing.T) (Deps, *memstore.Store) {
	t.Helper()
	st := memstore.New()
	cfg := config.Config{ConfirmationsEVM: 12, ConfirmationsBTC: 6, KeyCoolingPeriod: time.Hour, DefaultRotationDays: 7, HotWalletMinBalanceUSD: 1000}
	c := cache.NewMem()
	evm, _ := deriver.NewEVM(evmXpub, c, time.Hour)
	sol, _ := deriver.NewSolana("11111111111111111111111111111111", c, time.Hour)
	btc, _ := deriver.NewBTC(btcXpub, &chaincfg.MainNetParams, c, time.Hour)
	reg := deriver.NewRegistry(evm, sol, btc)
	em := audit.NoopEmitter{}
	wsvc := wallet.NewService(st, reg, lock.NewMemLocker(), em, cfg)
	bal := balance.NewService(st, em, cfg)
	fsvc := funding.NewService(st, bal, &noopTreasury{}, em, cfg)
	ns := nonce.NewService(st, lock.NewMemLocker())
	us := utxo.NewService(st)
	pc := &policy.MockClient{}
	signer := &grpcclient.MockMPCSigner{}
	gw := &grpcclient.MockGatewayClient{}
	kr := &staticKeyResolver{keyID: "k1"}
	wsvc2 := withdrawal.NewService(st, wsvc, ns, us, pc, signer, gw, kr, em)
	return Deps{Wallets: wsvc, Balances: bal, Funding: fsvc, Withdrawal: wsvc2}, st
}

type staticKeyResolver struct{ keyID string }

func (s *staticKeyResolver) ResolveActiveKeyID(_ context.Context, _ uuid.UUID) (string, error) {
	return s.keyID, nil
}

type noopTreasury struct{}

func (noopTreasury) RequestFunding(_ context.Context, _ *funding.TreasuryRequest) error { return nil }

func doRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode error: %v body=%s", err, rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestPostWalletCreated(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var w wallet.Wallet
	decode(t, rec, &w)
	if w.Chain != wallet.ChainEthereum || w.State != wallet.WalletStateActive {
		t.Errorf("unexpected wallet: %+v", w)
	}
}

func TestPostWalletBadInput(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "cardano", "type": "hot", "label": "w"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPostWalletBadJSON(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/wallets", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad json, got %d", rec.Code)
	}
}

func TestGetWallet(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	create := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	var w wallet.Wallet
	decode(t, create, &w)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+w.ID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got wallet.Wallet
	decode(t, rec, &got)
	if got.ID != w.ID {
		t.Error("id mismatch")
	}
}

func TestGetWalletNotFound(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetWalletBadID(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/not-a-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad uuid, got %d", rec.Code)
	}
}

func TestListWallets(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	_ = doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "a"})
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []*wallet.Wallet
	decode(t, rec, &out)
	if len(out) != 1 {
		t.Errorf("expected 1 wallet, got %d", len(out))
	}
}

func TestGetAddressesDeriveTrue(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	create := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	var w wallet.Wallet
	decode(t, create, &w)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+w.ID.String()+"/addresses?derive=true", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var addr wallet.Address
	decode(t, rec, &addr)
	if addr.WalletID != w.ID || addr.State != wallet.AddressStateActive {
		t.Errorf("unexpected address: %+v", addr)
	}
}

func TestGetAddressesList(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	create := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	var w wallet.Wallet
	decode(t, create, &w)
	// derive one first
	_ = doRequest(t, r, http.MethodPost, "/v1/wallets/"+w.ID.String()+"/addresses/derive", nil)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+w.ID.String()+"/addresses", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []*wallet.Address
	decode(t, rec, &out)
	if len(out) != 1 {
		t.Errorf("expected 1 address, got %d", len(out))
	}
}

func TestDeriveAddressEndpoint(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	create := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	var w wallet.Wallet
	decode(t, create, &w)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/"+w.ID.String()+"/addresses/derive", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var addr wallet.Address
	decode(t, rec, &addr)
	if addr.WalletID != w.ID {
		t.Error("wallet id mismatch")
	}
}

func TestGetBalances(t *testing.T) {
	deps, st := newDeps(t)
	r := NewRouter(deps)
	create := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	var w wallet.Wallet
	decode(t, create, &w)
	_ = st.UpsertBalance(context.Background(), &storage.Balance{WalletID: w.ID, Asset: "eth", Confirmed: "100", Pending: "10"})
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+w.ID.String()+"/balances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []balanceResp
	decode(t, rec, &out)
	if len(out) != 1 || out[0].Asset != "eth" || out[0].Confirmed != "100" {
		t.Errorf("unexpected balances: %+v", out)
	}
}

func TestCreateFundingRequest(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	create := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "warm", "label": "w"})
	var w wallet.Wallet
	decode(t, create, &w)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/"+w.ID.String()+"/funding-request", map[string]string{"asset": "usdc", "amount": "500", "reason": "ops"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	// duplicate
	rec2 := doRequest(t, r, http.MethodPost, "/v1/wallets/"+w.ID.String()+"/funding-request", map[string]string{"asset": "usdc", "amount": "500", "reason": "ops"})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate, got %d", rec2.Code)
	}
}

func TestCreateFundingRequestBadJSON(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	create := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	var w wallet.Wallet
	decode(t, create, &w)
	req := httptest.NewRequest(http.MethodPost, "/v1/wallets/"+w.ID.String()+"/funding-request", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad json, got %d", rec.Code)
	}
}

func TestCreateWithdrawal(t *testing.T) {
	deps, st := newDeps(t)
	r := NewRouter(deps)
	w := &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k1", CustodianRef: "mpc", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = st.CreateWallet(context.Background(), w)
	rec := doRequest(t, r, http.MethodPost, "/v1/withdrawals", map[string]string{
		"wallet_id": w.ID.String(), "to_address": "0xgood", "asset": "eth", "amount": "5",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var wr storage.WithdrawalRequest
	decode(t, rec, &wr)
	if wr.State != "whitelisted" {
		t.Errorf("expected whitelisted, got %s", wr.State)
	}
}

func TestCreateWithdrawalDuplicate(t *testing.T) {
	deps, st := newDeps(t)
	r := NewRouter(deps)
	w := &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k1", CustodianRef: "mpc", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = st.CreateWallet(context.Background(), w)
	body := map[string]string{"wallet_id": w.ID.String(), "to_address": "0x1", "asset": "eth", "amount": "5"}
	_ = doRequest(t, r, http.MethodPost, "/v1/withdrawals", body)
	rec := doRequest(t, r, http.MethodPost, "/v1/withdrawals", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate, got %d", rec.Code)
	}
}

func TestCreateWithdrawalBadWalletID(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/withdrawals", map[string]string{"wallet_id": "not-a-uuid", "to_address": "0x1", "asset": "eth", "amount": "1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad wallet id, got %d", rec.Code)
	}
}

func TestCreateWithdrawalBadJSON(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/withdrawals", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad json, got %d", rec.Code)
	}
}

func TestGetWithdrawal(t *testing.T) {
	deps, st := newDeps(t)
	r := NewRouter(deps)
	w := &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k1", CustodianRef: "mpc", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = st.CreateWallet(context.Background(), w)
	create := doRequest(t, r, http.MethodPost, "/v1/withdrawals", map[string]string{"wallet_id": w.ID.String(), "to_address": "0x1", "asset": "eth", "amount": "5"})
	var wr storage.WithdrawalRequest
	decode(t, create, &wr)
	rec := doRequest(t, r, http.MethodGet, "/v1/withdrawals/"+wr.ID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got storage.WithdrawalRequest
	decode(t, rec, &got)
	if got.ID != wr.ID {
		t.Error("id mismatch")
	}
}

func TestGetWithdrawalNotFound(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/withdrawals/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetWithdrawalBadID(t *testing.T) {
	deps, _ := newDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/withdrawals/not-a-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad uuid, got %d", rec.Code)
	}
}

var _ = errors.New