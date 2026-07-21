// Package funding implements treasury funding requests for hot wallets.
package funding

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TreasuryClient posts funding requests to Treasury Orchestration.
type TreasuryClient interface {
	RequestFunding(ctx context.Context, req *TreasuryRequest) error
}

// TreasuryRequest is the payload sent to Treasury Orchestration.
type TreasuryRequest struct {
	FundingRequestID uuid.UUID `json:"funding_request_id"`
	WalletID         uuid.UUID `json:"wallet_id"`
	Asset            string    `json:"asset"`
	Amount           string    `json:"amount"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Reason           string    `json:"reason"`
}

// HTTPTreasuryClient posts JSON to TREASURY_ORCHESTRATION_URL.
type HTTPTreasuryClient struct {
	URL    string
	Client *http.Client
}

// NewHTTPClient constructs an HTTP treasury client.
func NewHTTPClient(url string) *HTTPTreasuryClient {
	return &HTTPTreasuryClient{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

// RequestFunding POSTs the funding request with an idempotency key.
func (c *HTTPTreasuryClient) RequestFunding(ctx context.Context, req *TreasuryRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/funding-requests", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("treasury rejected funding request: %d %s", resp.StatusCode, string(b))
	}
	return nil
}

// Service handles hot-wallet funding requests.
type Service struct {
	Store    storage.Store
	Balances *balance.Service
	Treasury TreasuryClient
	Audit    audit.Emitter
	Config   config.Config
}

// NewService constructs a funding Service.
func NewService(st storage.Store, bal *balance.Service, tc TreasuryClient, em audit.Emitter, cfg config.Config) *Service {
	return &Service{Store: st, Balances: bal, Treasury: tc, Audit: em, Config: cfg}
}

// EvaluateAndRequest checks the hot wallet's confirmed balance and emits a
// funding request if it has dropped below the configured threshold.
func (s *Service) EvaluateAndRequest(ctx context.Context, walletID uuid.UUID, asset string) error {
	w, err := s.Store.GetWallet(ctx, walletID)
	if err != nil {
		return err
	}
	bal, err := s.Store.GetBalance(ctx, walletID, asset)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	confirmed := decimal.Zero
	if bal != nil {
		confirmed = parseDec(bal.Confirmed)
	}
	if w.Type != wallet.WalletTypeHot {
		return nil // only hot wallets auto-request
	}
	if confirmed.InexactFloat64() >= s.Config.HotWalletMinBalanceUSD {
		return nil
	}
	// idempotency: check for an existing open request
	if existing, err := s.Store.GetOpenFundingRequest(ctx, walletID, asset); err == nil && existing != nil {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.createRequest(ctx, w, asset, strconv.FormatInt(int64(s.Config.HotWalletMinBalanceUSD-confirmed.InexactFloat64()), 10), "auto:below_threshold")
}

// ManualRequest creates a funding request for any wallet (operator-initiated).
func (s *Service) ManualRequest(ctx context.Context, walletID uuid.UUID, asset, amount, reason string) error {
	w, err := s.Store.GetWallet(ctx, walletID)
	if err != nil {
		return err
	}
	if w.State == wallet.WalletStateRetired {
		return wallet.ErrWalletRetired
	}
	if existing, err := s.Store.GetOpenFundingRequest(ctx, walletID, asset); err == nil && existing != nil {
		return storage.ErrDuplicateFunding
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.createRequest(ctx, w, asset, amount, reason)
}

func (s *Service) createRequest(ctx context.Context, w *wallet.Wallet, asset, amount, reason string) error {
	id, _ := uuid.NewV7()
	now := time.Now()
	fr := &storage.FundingRequest{
		ID:        id,
		WalletID:  w.ID,
		Asset:     asset,
		Amount:    amount,
		State:     string(storage.FundingStateRequested),
		Reason:    reason,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.Store.CreateFundingRequest(ctx, fr); err != nil {
		return err
	}
	idem := fmt.Sprintf("fr:%s:%s:%s", w.ID, asset, id)
	if s.Treasury != nil {
		if err := s.Treasury.RequestFunding(ctx, &TreasuryRequest{
			FundingRequestID: id, WalletID: w.ID, Asset: asset, Amount: amount,
			IdempotencyKey: idem, Reason: reason,
		}); err != nil {
			// leave the request in 'requested' state for retry
			return fmt.Errorf("treasury request: %w", err)
		}
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "wallet.funding.requested",
			WalletID:  &w.ID,
			Payload:   map[string]any{"asset": asset, "amount": amount, "reason": reason, "id": id},
		})
	}
	return nil
}

// MarkApproved transitions a funding request to approved.
func (s *Service) MarkApproved(ctx context.Context, id uuid.UUID, treasuryBatchID string) error {
	return s.Store.UpdateFundingState(ctx, id, string(storage.FundingStateApproved), treasuryBatchID)
}

// MarkSettled transitions a funding request to settled.
func (s *Service) MarkSettled(ctx context.Context, id uuid.UUID) error {
	return s.Store.UpdateFundingState(ctx, id, string(storage.FundingStateSettled), "")
}

// MarkRejected transitions a funding request to rejected.
func (s *Service) MarkRejected(ctx context.Context, id uuid.UUID, reason string) error {
	_ = reason
	return s.Store.UpdateFundingState(ctx, id, string(storage.FundingStateRejected), "")
}

// parseDec parses a fixed-point decimal string into decimal.Decimal. Returns
// zero on empty/invalid input. Money is stored as NUMERIC(38,18) in Postgres
// and string in memstore; int64 parsing would overflow for ETH wei / large
// BTC satoshi balances.
func parseDec(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
