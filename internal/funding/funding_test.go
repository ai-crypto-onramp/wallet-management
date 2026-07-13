package funding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

type mockTreasury struct {
	calls   int
	fail    bool
	lastReq *TreasuryRequest
}

func (m *mockTreasury) RequestFunding(ctx context.Context, req *TreasuryRequest) error {
	m.calls++
	m.lastReq = req
	if m.fail {
		return errors.New("treasury unavailable")
	}
	return nil
}

func newSvc(t *testing.T, cfg config.Config) (*Service, *memstore.Store, *mockTreasury) {
	t.Helper()
	st := memstore.New()
	bal := balance.NewService(st, nil, config.Config{})
	tc := &mockTreasury{}
	return NewService(st, bal, tc, nil, cfg), st, tc
}

func seedWallet(t *testing.T, st *memstore.Store, wType wallet.WalletType, state wallet.WalletState, confirmed string) *wallet.Wallet {
	t.Helper()
	w := &wallet.Wallet{
		ID: uuid.New(), Chain: wallet.ChainEthereum, Type: wType, State: state,
		Label: "w", KeyID: "k", CustodianRef: "mpc",
	}
	if err := st.CreateWallet(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if confirmed != "" {
		_ = st.UpsertBalance(context.Background(), &storage.Balance{
			WalletID: w.ID, Asset: "usdc", Confirmed: confirmed,
		})
	}
	return w
}

func TestThresholdNotCrossedNoRequest(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, st, tc := newSvc(t, cfg)
	w := seedWallet(t, st, wallet.WalletTypeHot, wallet.WalletStateActive, "5000")
	if err := svc.EvaluateAndRequest(context.Background(), w.ID, "usdc"); err != nil {
		t.Fatal(err)
	}
	if tc.calls != 0 {
		t.Errorf("expected no treasury calls, got %d", tc.calls)
	}
}

func TestThresholdCrossedOneRequest(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, st, tc := newSvc(t, cfg)
	w := seedWallet(t, st, wallet.WalletTypeHot, wallet.WalletStateActive, "100")
	if err := svc.EvaluateAndRequest(context.Background(), w.ID, "usdc"); err != nil {
		t.Fatal(err)
	}
	if tc.calls != 1 {
		t.Fatalf("expected 1 treasury call, got %d", tc.calls)
	}
	// duplicate suppression: a second evaluate does not create another request
	if err := svc.EvaluateAndRequest(context.Background(), w.ID, "usdc"); err != nil {
		t.Fatal(err)
	}
	if tc.calls != 1 {
		t.Errorf("expected still 1 treasury call (idempotent), got %d", tc.calls)
	}
}

func TestNonHotWalletNoAutoRequest(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, st, tc := newSvc(t, cfg)
	w := seedWallet(t, st, wallet.WalletTypeCold, wallet.WalletStateActive, "10")
	if err := svc.EvaluateAndRequest(context.Background(), w.ID, "usdc"); err != nil {
		t.Fatal(err)
	}
	if tc.calls != 0 {
		t.Errorf("non-hot wallet should not auto-request, got %d calls", tc.calls)
	}
}

func TestEvaluateMissingWallet(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, _, _ := newSvc(t, cfg)
	if err := svc.EvaluateAndRequest(context.Background(), uuid.New(), "usdc"); err == nil {
		t.Error("expected error on missing wallet")
	}
}

func TestManualRequestWarmWallet(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, st, tc := newSvc(t, cfg)
	w := seedWallet(t, st, wallet.WalletTypeWarm, wallet.WalletStateActive, "99999")
	if err := svc.ManualRequest(context.Background(), w.ID, "usdc", "500", "ops topup"); err != nil {
		t.Fatal(err)
	}
	if tc.calls != 1 {
		t.Errorf("expected 1 treasury call for manual, got %d", tc.calls)
	}
	// duplicate while open
	if err := svc.ManualRequest(context.Background(), w.ID, "usdc", "500", "ops"); !errors.Is(err, storage.ErrDuplicateFunding) {
		t.Errorf("expected ErrDuplicateFunding, got %v", err)
	}
}

func TestManualRequestRetiredWallet(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, st, _ := newSvc(t, cfg)
	w := seedWallet(t, st, wallet.WalletTypeHot, wallet.WalletStateRetired, "")
	if err := svc.ManualRequest(context.Background(), w.ID, "usdc", "500", "x"); !errors.Is(err, wallet.ErrWalletRetired) {
		t.Errorf("expected ErrWalletRetired, got %v", err)
	}
}

func TestManualRequestMissingWallet(t *testing.T) {
	cfg := config.Config{}
	svc, _, _ := newSvc(t, cfg)
	if err := svc.ManualRequest(context.Background(), uuid.New(), "usdc", "500", "x"); err == nil {
		t.Error("expected error on missing wallet")
	}
}

func TestStateTransitions(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, st, tc := newSvc(t, cfg)
	w := seedWallet(t, st, wallet.WalletTypeHot, wallet.WalletStateActive, "0")
	_ = svc.EvaluateAndRequest(context.Background(), w.ID, "usdc")
	open, _ := st.GetOpenFundingRequest(context.Background(), w.ID, "usdc")
	if open.State != "requested" {
		t.Fatalf("expected requested, got %s", open.State)
	}
	if err := svc.MarkApproved(context.Background(), open.ID, "batch1"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetOpenFundingRequest(context.Background(), w.ID, "usdc")
	if got != nil {
		t.Error("expected no open request after approved")
	}
	if err := svc.MarkSettled(context.Background(), open.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRejected(context.Background(), open.ID, "nope"); err != nil {
		t.Fatal(err)
	}
	_ = tc
}

func TestMarkApprovedMissingRequest(t *testing.T) {
	cfg := config.Config{}
	svc, _, _ := newSvc(t, cfg)
	if err := svc.MarkApproved(context.Background(), uuid.New(), "b"); err == nil {
		t.Error("expected error on missing funding request")
	}
}

func TestTreasuryFailureLeavesRequested(t *testing.T) {
	cfg := config.Config{HotWalletMinBalanceUSD: 1000}
	svc, st, tc := newSvc(t, cfg)
	tc.fail = true
	w := seedWallet(t, st, wallet.WalletTypeHot, wallet.WalletStateActive, "10")
	if err := svc.EvaluateAndRequest(context.Background(), w.ID, "usdc"); err == nil {
		t.Error("expected treasury error to surface")
	}
	// request still in 'requested' state for retry
	open, err := st.GetOpenFundingRequest(context.Background(), w.ID, "usdc")
	if err != nil {
		t.Fatalf("expected open request after treasury failure: %v", err)
	}
	if open.State != "requested" {
		t.Errorf("expected requested, got %s", open.State)
	}
}

func TestHTTPTreasuryClientSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing Idempotency-Key header")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL)
	if err := c.RequestFunding(context.Background(), &TreasuryRequest{
		FundingRequestID: uuid.New(), WalletID: uuid.New(), Asset: "usdc", Amount: "100", IdempotencyKey: "k1", Reason: "x",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTreasuryClientFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad"))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL)
	err := c.RequestFunding(context.Background(), &TreasuryRequest{IdempotencyKey: "k"})
	if err == nil {
		t.Fatal("expected error on 400 response")
	}
}