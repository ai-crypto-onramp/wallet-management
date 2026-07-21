// Package rest implements the REST control-plane HTTP handlers for
// wallet-management.
package rest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/funding"
	"github.com/ai-crypto-onramp/wallet-management/internal/nonce"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/ai-crypto-onramp/wallet-management/internal/withdrawal"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// Deps bundles the service dependencies the REST handlers need.
type Deps struct {
	Wallets    *wallet.Service
	Balances   *balance.Service
	Funding    *funding.Service
	Withdrawal *withdrawal.Service
	Nonce      *nonce.Service
}

// NewRouter builds the chi router with all wallet-management endpoints.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", healthz)

	r.Route("/v1", func(r chi.Router) {
		r.Post("/wallets", createWallet(d))
		r.Get("/wallets/{id}", getWallet(d))
		r.Get("/wallets", listWallets(d))

		r.Get("/wallets/{id}/addresses", getAddresses(d))
		r.Post("/wallets/{id}/addresses/derive", deriveAddress(d))

		r.Get("/wallets/{id}/balances", getBalances(d))

		r.Post("/wallets/{id}/funding-request", createFundingRequest(d))
		r.Get("/wallets/{id}/funding-requests", listFundingRequests(d))

		r.Post("/wallets/{id}/nonce/allocate", allocateNonce(d))

		r.Post("/withdrawals", createWithdrawal(d))
		r.Get("/withdrawals", listWithdrawals(d))
		r.Get("/withdrawals/{id}", getWithdrawal(d))
	})
	return r
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createWalletReq struct {
	Chain string `json:"chain"`
	Type  string `json:"type"`
	Label string `json:"label"`
	KeyID string `json:"key_id"`
}

func createWallet(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createWalletReq
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		wl, err := d.Wallets.Create(r.Context(), wallet.CreateRequest{
			Chain: wallet.Chain(req.Chain), Type: wallet.WalletType(req.Type), Label: req.Label, KeyID: req.KeyID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, wl)
	}
}

func getWallet(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		wl, err := d.Wallets.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, errors.New("wallet not found"))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, wl)
	}
}

func listWallets(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		wallets, err := d.Wallets.List(r.Context(), q.Get("chain"), q.Get("type"), q.Get("state"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if wallets == nil {
			wallets = []*wallet.Wallet{}
		}
		writeJSON(w, http.StatusOK, wallets)
	}
}

func getAddresses(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		derive := r.URL.Query().Get("derive") == "true"
		if derive {
			addr, err := d.Wallets.DeriveAddress(r.Context(), id, false)
			if err != nil {
				if errors.Is(err, wallet.ErrWalletRetired) {
					writeError(w, http.StatusConflict, err)
					return
				}
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, addr)
			return
		}
		addrs, err := d.Wallets.ListAddresses(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if addrs == nil {
			addrs = []*wallet.Address{}
		}
		writeJSON(w, http.StatusOK, addrs)
	}
}

func deriveAddress(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		addr, err := d.Wallets.DeriveAddress(r.Context(), id, true)
		if err != nil {
			if errors.Is(err, wallet.ErrWalletRetired) {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, addr)
	}
}

type balanceResp struct {
	Asset     string `json:"asset"`
	Confirmed string `json:"confirmed"`
	Pending   string `json:"pending"`
	Locked    string `json:"locked"`
}

func getBalances(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		bals, err := d.Balances.GetBalances(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]balanceResp, 0, len(bals))
		for _, b := range bals {
			out = append(out, balanceResp{Asset: b.Asset, Confirmed: b.Confirmed, Pending: b.Pending, Locked: b.Locked})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type fundingReq struct {
	Asset  string `json:"asset"`
	Amount string `json:"amount"`
	Reason string `json:"reason"`
}

func createFundingRequest(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var req fundingReq
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := d.Funding.ManualRequest(r.Context(), id, req.Asset, req.Amount, req.Reason); err != nil {
			if errors.Is(err, storage.ErrDuplicateFunding) {
				writeError(w, http.StatusConflict, err)
				return
			}
			if errors.Is(err, wallet.ErrWalletRetired) {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"status": "requested"})
	}
}

type createWithdrawalReq struct {
	WalletID  string `json:"wallet_id"`
	ToAddress string `json:"to_address"`
	Asset     string `json:"asset"`
	Amount    string `json:"amount"`
}

func createWithdrawal(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createWithdrawalReq
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		wid, err := uuid.Parse(req.WalletID)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid wallet_id"))
			return
		}
		wr, err := d.Withdrawal.Create(r.Context(), withdrawal.CreateRequest{
			WalletID: wid, ToAddress: req.ToAddress, Asset: req.Asset, Amount: req.Amount,
		})
		if err != nil {
			if errors.Is(err, storage.ErrDuplicateWithdrawal) {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, wr)
	}
}

func getWithdrawal(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := uuid.Parse(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		wr, err := d.Withdrawal.Store.GetWithdrawal(r.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, errors.New("withdrawal not found"))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, wr)
	}
}

func listWithdrawals(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var walletID uuid.UUID
		if wids := q.Get("wallet_id"); wids != "" {
			id, err := uuid.Parse(wids)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid wallet_id"))
				return
			}
			walletID = id
		}
		wrs, err := d.Withdrawal.Store.ListWithdrawals(r.Context(), walletID, q.Get("state"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if wrs == nil {
			wrs = []*storage.WithdrawalRequest{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"withdrawals": wrs})
	}
}

func listFundingRequests(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state := r.URL.Query().Get("state")
		frs, err := d.Funding.Store.ListFundingRequests(r.Context(), id, state)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if frs == nil {
			frs = []*storage.FundingRequest{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"funding_requests": frs})
	}
}

type allocateNonceReq struct {
	Chain string `json:"chain"`
}

type allocateNonceResp struct {
	Nonce int64 `json:"nonce"`
}

// allocateNonce reserves the next pending nonce for the wallet on the
// given chain via nonce.Service.ReserveNonce. It is the endpoint called
// by the blockchain-gateway prepayment coordinator.
func allocateNonce(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if d.Nonce == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("nonce service unavailable"))
			return
		}
		var req allocateNonceReq
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Chain == "" {
			writeError(w, http.StatusBadRequest, errors.New("chain is required"))
			return
		}
		n, err := d.Nonce.ReserveNonce(r.Context(), id, wallet.Chain(req.Chain))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, allocateNonceResp{Nonce: n})
	}
}

// Server wraps the HTTP server.
type Server struct {
	httpSrv *http.Server
}

// NewServer constructs a new REST server.
func NewServer(addr string, d Deps) *Server {
	return &Server{
		httpSrv: &http.Server{
			Addr:              addr,
			Handler:           NewRouter(d),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Start blocks until the server exits.
func (s *Server) Start() error { return s.httpSrv.ListenAndServe() }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.httpSrv.Shutdown(ctx) }

func parseID(r *http.Request) (uuid.UUID, error) {
	idStr := chi.URLParam(r, "id")
	return uuid.Parse(idStr)
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	defer func() { _ = r.Body.Close() }()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
