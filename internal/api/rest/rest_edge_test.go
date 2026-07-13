package rest

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

// errStore wraps memstore.Store and injects failures on selected methods so
// the REST handlers' error branches can be exercised.
type errStore struct {
	*memstore.Store
	listWalletsErr     error
	listAddressesErr   error
	listBalancesErr    error
	getWalletErr       error
	insertAddressErr   error
	deprecateAddrErr   error
	nextAddrIndexErr   error
	getOpenFundingErr  error
	createFundingErr   error
	createWithdrawalErr error
	getWithdrawalErr   error
	updateWdStateErr   error
	createWalletErr    error
}

func (s *errStore) CreateWallet(ctx context.Context, w *wallet.Wallet) error {
	if s.createWalletErr != nil {
		return s.createWalletErr
	}
	return s.Store.CreateWallet(ctx, w)
}

func (s *errStore) ListWallets(ctx context.Context, chain, wtype, state string) ([]*wallet.Wallet, error) {
	if s.listWalletsErr != nil {
		return nil, s.listWalletsErr
	}
	return s.Store.ListWallets(ctx, chain, wtype, state)
}

func (s *errStore) ListAddresses(ctx context.Context, walletID uuid.UUID) ([]*wallet.Address, error) {
	if s.listAddressesErr != nil {
		return nil, s.listAddressesErr
	}
	return s.Store.ListAddresses(ctx, walletID)
}

func (s *errStore) ListBalances(ctx context.Context, walletID uuid.UUID) ([]*storage.Balance, error) {
	if s.listBalancesErr != nil {
		return nil, s.listBalancesErr
	}
	return s.Store.ListBalances(ctx, walletID)
}

func (s *errStore) GetWallet(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	if s.getWalletErr != nil {
		return nil, s.getWalletErr
	}
	return s.Store.GetWallet(ctx, id)
}

func (s *errStore) InsertAddress(ctx context.Context, a *wallet.Address) error {
	if s.insertAddressErr != nil {
		return s.insertAddressErr
	}
	return s.Store.InsertAddress(ctx, a)
}

func (s *errStore) DeprecateAddress(ctx context.Context, id uuid.UUID) error {
	if s.deprecateAddrErr != nil {
		return s.deprecateAddrErr
	}
	return s.Store.DeprecateAddress(ctx, id)
}

func (s *errStore) NextAddressIndex(ctx context.Context, chain string, change int) (int, error) {
	if s.nextAddrIndexErr != nil {
		return 0, s.nextAddrIndexErr
	}
	return s.Store.NextAddressIndex(ctx, chain, change)
}

func (s *errStore) GetOpenFundingRequest(ctx context.Context, walletID uuid.UUID, asset string) (*storage.FundingRequest, error) {
	if s.getOpenFundingErr != nil {
		return nil, s.getOpenFundingErr
	}
	return s.Store.GetOpenFundingRequest(ctx, walletID, asset)
}

func (s *errStore) CreateFundingRequest(ctx context.Context, f *storage.FundingRequest) error {
	if s.createFundingErr != nil {
		return s.createFundingErr
	}
	return s.Store.CreateFundingRequest(ctx, f)
}

func (s *errStore) CreateWithdrawal(ctx context.Context, w *storage.WithdrawalRequest) error {
	if s.createWithdrawalErr != nil {
		return s.createWithdrawalErr
	}
	return s.Store.CreateWithdrawal(ctx, w)
}

func (s *errStore) GetWithdrawal(ctx context.Context, id uuid.UUID) (*storage.WithdrawalRequest, error) {
	if s.getWithdrawalErr != nil {
		return nil, s.getWithdrawalErr
	}
	return s.Store.GetWithdrawal(ctx, id)
}

func (s *errStore) UpdateWithdrawalState(ctx context.Context, id uuid.UUID, state, reason, txHash, policyDecisionID string) error {
	if s.updateWdStateErr != nil {
		return s.updateWdStateErr
	}
	return s.Store.UpdateWithdrawalState(ctx, id, state, reason, txHash, policyDecisionID)
}

// newErrDeps builds Deps backed by an errStore so handlers can be driven into
// their error branches. It returns the store so a test can flip its error
// fields between requests.
func newErrDeps(t *testing.T) (Deps, *errStore) {
	t.Helper()
	st := &errStore{Store: memstore.New()}
	cfg := config.Config{ConfirmationsEVM: 12, ConfirmationsBTC: 6, KeyCoolingPeriod: 1e9, DefaultRotationDays: 7, HotWalletMinBalanceUSD: 1000}
	c := cache.NewMem()
	evm, _ := deriver.NewEVM(evmXpub, c, 1e9)
	sol, _ := deriver.NewSolana("11111111111111111111111111111111", c, 1e9)
	btc, _ := deriver.NewBTC(btcXpub, &chaincfg.MainNetParams, c, 1e9)
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

func TestListWalletsError(t *testing.T) {
	deps, st := newErrDeps(t)
	st.listWalletsErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetWalletInternalError(t *testing.T) {
	deps, st := newErrDeps(t)
	st.getWalletErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+uuid.New().String(), nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestGetAddressesListError(t *testing.T) {
	deps, st := newErrDeps(t)
	// First create a wallet using the real store, then inject the error.
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k", CustodianRef: "mpc"})
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k2", CustodianRef: "mpc"})
	st.listAddressesErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+wID.String()+"/addresses", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetAddressesDeriveError(t *testing.T) {
	deps, st := newErrDeps(t)
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k", CustodianRef: "mpc"})
	st.nextAddrIndexErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+wID.String()+"/addresses?derive=true", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeriveAddressEndpointError(t *testing.T) {
	deps, st := newErrDeps(t)
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k", CustodianRef: "mpc"})
	st.insertAddressErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/"+wID.String()+"/addresses/derive", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetBalancesError(t *testing.T) {
	deps, st := newErrDeps(t)
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k", CustodianRef: "mpc"})
	st.listBalancesErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/"+wID.String()+"/balances", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestGetBalancesBadID(t *testing.T) {
	deps, _ := newErrDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/not-a-uuid/balances", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGetAddressesBadID(t *testing.T) {
	deps, _ := newErrDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/wallets/not-a-uuid/addresses", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDeriveAddressBadID(t *testing.T) {
	deps, _ := newErrDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/not-a-uuid/addresses/derive", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateFundingRequestBadID(t *testing.T) {
	deps, _ := newErrDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/not-a-uuid/funding-request", map[string]string{"asset": "usdc", "amount": "1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateFundingRequestInternalError(t *testing.T) {
	deps, st := newErrDeps(t)
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeWarm, State: wallet.WalletStateActive, KeyID: "k", CustodianRef: "mpc"})
	st.createFundingErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/"+wID.String()+"/funding-request", map[string]string{"asset": "usdc", "amount": "500", "reason": "ops"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateFundingRequestRetiredConflict(t *testing.T) {
	deps, st := newErrDeps(t)
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeWarm, State: wallet.WalletStateRetired, KeyID: "k", CustodianRef: "mpc"})
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/"+wID.String()+"/funding-request", map[string]string{"asset": "usdc", "amount": "500", "reason": "ops"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on retired wallet, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateFundingRequestGetOpenError(t *testing.T) {
	deps, st := newErrDeps(t)
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeWarm, State: wallet.WalletStateActive, KeyID: "k", CustodianRef: "mpc"})
	st.getOpenFundingErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets/"+wID.String()+"/funding-request", map[string]string{"asset": "usdc", "amount": "500", "reason": "ops"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on GetOpenFundingRequest error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateWithdrawalInternalError(t *testing.T) {
	deps, st := newErrDeps(t)
	wID := uuid.New()
	_ = st.Store.CreateWallet(context.Background(), &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k", CustodianRef: "mpc"})
	st.updateWdStateErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/withdrawals", map[string]string{
		"wallet_id": wID.String(), "to_address": "0x1", "asset": "eth", "amount": "1",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetWithdrawalInternalError(t *testing.T) {
	deps, st := newErrDeps(t)
	st.getWithdrawalErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodGet, "/v1/withdrawals/"+uuid.New().String(), nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateWalletInternalError(t *testing.T) {
	deps, st := newErrDeps(t)
	st.createWalletErr = errors.New("db down")
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot", "label": "w"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on CreateWallet error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNewServerAndStartShutdown(t *testing.T) {
	deps, _ := newErrDeps(t)
	srv := NewServer("127.0.0.1:0", deps)
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
	// Start would block; instead exercise Shutdown with a short timeout.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func TestPostWalletMissingLabel(t *testing.T) {
	deps, _ := newErrDeps(t)
	r := NewRouter(deps)
	rec := doRequest(t, r, http.MethodPost, "/v1/wallets", map[string]string{"chain": "ethereum", "type": "hot"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing label, got %d", rec.Code)
	}
}

func TestPostWalletEmptyBody(t *testing.T) {
	deps, _ := newErrDeps(t)
	r := NewRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/wallets", bytes.NewReader([]byte("")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on empty body, got %d", rec.Code)
	}
}

var _ = errors.New