// Package balance implements per-chain balance tracking with confirmation
// depth thresholds and reorg handling.
package balance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

// ConfirmationEvent is a balance confirmation event from the Blockchain Gateway.
type ConfirmationEvent struct {
	WalletID      uuid.UUID
	Asset         string
	Amount        string // positive for deposit
	Confirmations int
	BlockHeight   int64
	EventID       string
	IsFinalized   bool // Solana: true if finalized slot
	Chain         wallet.Chain
}

// ReorgEvent signals that a block has been reorged out.
type ReorgEvent struct {
	WalletID    uuid.UUID
	Asset       string
	BlockHeight int64
	EventID     string
	Outpoints   []string // BTC UTXOs to restore
}

// Service tracks balances per (wallet, asset) with confirmation thresholds.
type Service struct {
	Store  storage.Store
	Audit  audit.Emitter
	Config config.Config
	// UTXORestore is invoked on reorg to restore spent UTXOs (wired in Stage 5).
	UTXORestore func(ctx context.Context, outpoints []string) error
	// OnConfirmedDecrease is invoked after a confirmed balance decreases so the
	// funding service can evaluate a treasury top-up (wired in Stage 6). The
	// caller decides whether to run it asynchronously.
	OnConfirmedDecrease func(walletID uuid.UUID, asset string)
}

// NewService constructs a balance Service.
func NewService(st storage.Store, em audit.Emitter, cfg config.Config) *Service {
	return &Service{Store: st, Audit: em, Config: cfg}
}

// threshold returns the confirmation depth for a chain.
func (s *Service) threshold(chain wallet.Chain) int {
	switch chain {
	case wallet.ChainBitcoin:
		return s.Config.ConfirmationsBTC
	case wallet.ChainSolana:
		return 1 // finalized slot — represented as 1 confirmation event with IsFinalized
	default:
		if chain.IsEVM() {
			return s.Config.ConfirmationsEVM
		}
	}
	return s.Config.ConfirmationsEVM
}

// ApplyConfirmationEvent applies a deposit confirmation, moving value from
// pending to confirmed once the threshold is met. Idempotent on
// (wallet_id, asset, block_height, event_id).
func (s *Service) ApplyConfirmationEvent(ctx context.Context, ev *ConfirmationEvent) error {
	be := &storage.BalanceEvent{
		ID: uuid.New(), WalletID: ev.WalletID, Asset: ev.Asset,
		BlockHeight: ev.BlockHeight, EventID: ev.EventID,
	}
	if err := s.Store.RecordBalanceEvent(ctx, be); err != nil {
		if errors.Is(err, storage.ErrDuplicateEvent) {
			return nil // already applied
		}
		return err
	}

	// read-modify-write inside a transaction so concurrent events do not lose updates.
	err := s.Store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.Store.GetBalance(ctx, ev.WalletID, ev.Asset)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cur == nil {
			cur = &storage.Balance{WalletID: ev.WalletID, Asset: ev.Asset}
		}

		confirmed := parseDec(cur.Confirmed)
		pending := parseDec(cur.Pending)
		amount := parseDec(ev.Amount)

		if s.isConfirmed(ev) {
			confirmed = addDec(confirmed, amount)
		} else {
			pending = addDec(pending, amount)
		}

		upd := &storage.Balance{
			WalletID:      ev.WalletID,
			Asset:         ev.Asset,
			Confirmed:     formatDec(confirmed),
			Pending:       formatDec(pending),
			Locked:        cur.Locked,
			LastBlockSeen: max64(cur.LastBlockSeen, ev.BlockHeight),
			UpdatedAt:     time.Now(),
		}
		if err := s.Store.UpsertBalance(ctx, upd); err != nil {
			return err
		}
		if s.Audit != nil {
			_ = s.Audit.Emit(ctx, &audit.Event{
				EventType: "wallet.balance.updated",
				WalletID:  &ev.WalletID,
				Payload: map[string]any{
					"asset": ev.Asset, "confirmed": formatDec(confirmed), "pending": formatDec(pending),
					"block_height": ev.BlockHeight, "confirmations": ev.Confirmations,
				},
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	// A confirmed decrease (e.g. a settled withdrawal) may drop a hot wallet
	// below its funding threshold — let the funding service evaluate.
	if s.OnConfirmedDecrease != nil && s.isConfirmed(ev) && parseDec(ev.Amount) < 0 {
		s.OnConfirmedDecrease(ev.WalletID, ev.Asset)
	}
	return nil
}

// ApplyReorgEvent demotes confirmed value back to pending for the reorged block
// and restores any spent UTXOs.
func (s *Service) ApplyReorgEvent(ctx context.Context, ev *ReorgEvent) error {
	if err := s.Store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.Store.GetBalance(ctx, ev.WalletID, ev.Asset)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		confirmed := parseDec(cur.Confirmed)
		pending := parseDec(cur.Pending)
		pending = addDec(pending, confirmed)
		confirmed = 0
		upd := &storage.Balance{
			WalletID:      ev.WalletID,
			Asset:         ev.Asset,
			Confirmed:     formatDec(confirmed),
			Pending:       formatDec(pending),
			Locked:        cur.Locked,
			LastBlockSeen: cur.LastBlockSeen,
			UpdatedAt:     time.Now(),
		}
		return s.Store.UpsertBalance(ctx, upd)
	}); err != nil {
		return err
	}
	if s.UTXORestore != nil && len(ev.Outpoints) > 0 {
		if err := s.UTXORestore(ctx, ev.Outpoints); err != nil {
			return fmt.Errorf("restore utxos: %w", err)
		}
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "wallet.balance.updated",
			WalletID:  &ev.WalletID,
			Payload: map[string]any{
				"asset": ev.Asset, "reorg": true, "block_height": ev.BlockHeight,
			},
		})
	}
	return nil
}

func (s *Service) isConfirmed(ev *ConfirmationEvent) bool {
	if ev.Chain == wallet.ChainSolana {
		return ev.IsFinalized
	}
	return ev.Confirmations >= s.threshold(ev.Chain)
}

// GetBalances returns confirmed + pending balances per asset for a wallet.
func (s *Service) GetBalances(ctx context.Context, walletID uuid.UUID) ([]*storage.Balance, error) {
	return s.Store.ListBalances(ctx, walletID)
}

// AddLocked increases the locked portion of a (wallet, asset) balance.
func (s *Service) AddLocked(ctx context.Context, walletID uuid.UUID, asset, amount string) error {
	return s.Store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.Store.GetBalance(ctx, walletID, asset)
		if err != nil {
			return err
		}
		locked := addDec(parseDec(cur.Locked), parseDec(amount))
		return s.Store.UpsertBalance(ctx, &storage.Balance{
			WalletID: walletID, Asset: asset,
			Confirmed: cur.Confirmed, Pending: cur.Pending, Locked: formatDec(locked),
			LastBlockSeen: cur.LastBlockSeen, UpdatedAt: time.Now(),
		})
	})
}

// ReleaseLocked decreases the locked portion.
func (s *Service) ReleaseLocked(ctx context.Context, walletID uuid.UUID, asset, amount string) error {
	return s.Store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.Store.GetBalance(ctx, walletID, asset)
		if err != nil {
			return err
		}
		locked := subDec(parseDec(cur.Locked), parseDec(amount))
		if locked < 0 {
			locked = 0
		}
		return s.Store.UpsertBalance(ctx, &storage.Balance{
			WalletID: walletID, Asset: asset,
			Confirmed: cur.Confirmed, Pending: cur.Pending, Locked: formatDec(locked),
			LastBlockSeen: cur.LastBlockSeen, UpdatedAt: time.Now(),
		})
	})
}

// helpers using float64 is risky for money; we use string-based decimal math via
// strconv on fixed-point integers represented as integer strings.

func parseDec(s string) int64 {
	if s == "" {
		return 0
	}
	// interpret as integer minor units (caller is responsible for scale).
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func formatDec(n int64) string {
	return strconv.FormatInt(n, 10)
}

func addDec(a, b int64) int64 { return a + b }

func subDec(a, b int64) int64 {
	if b > a {
		return 0
	}
	return a - b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}