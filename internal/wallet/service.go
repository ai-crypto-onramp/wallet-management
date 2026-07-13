// Package wallet implements the wallet lifecycle service: creation, state
// transitions, and address derivation/rotation orchestration.
package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/deriver"
	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/google/uuid"
)

// Service orchestrates wallet lifecycle and address derivation.
type Service struct {
	Store     storage.Store
	Derivers  *deriver.Registry
	Locker    lock.Locker
	Audit     audit.Emitter
	Config    config.Config
	Rotation  *RotationPolicy
}

// NewService constructs a wallet Service.
func NewService(st storage.Store, reg *deriver.Registry, lk lock.Locker, em audit.Emitter, cfg config.Config) *Service {
	s := &Service{Store: st, Derivers: reg, Locker: lk, Audit: em, Config: cfg}
	s.Rotation = NewRotationPolicy(st, reg, lk, em, cfg)
	return s
}

// Create persists a new wallet in active state and binds a key_id placeholder.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Wallet, error) {
	if !ValidChain(req.Chain) {
		return nil, fmt.Errorf("unsupported chain %q", req.Chain)
	}
	if !ValidWalletType(req.Type) {
		return nil, fmt.Errorf("unsupported wallet type %q", req.Type)
	}
	if req.Label == "" {
		return nil, errors.New("label is required")
	}
	keyID := req.KeyID
	if keyID == "" {
		keyID = "pending:" + uuid.NewString()
	}
	now := time.Now().UTC()
	w := &Wallet{
		ID:           uuid.New(),
		Chain:        req.Chain,
		Type:         req.Type,
		Label:        req.Label,
		State:        WalletStateActive,
		KeyID:        keyID,
		CustodianRef: "mpc",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.Store.CreateWallet(ctx, w); err != nil {
		return nil, fmt.Errorf("create wallet: %w", err)
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "wallet.created",
			WalletID:  &w.ID,
			Payload: map[string]any{
				"chain": w.Chain, "type": w.Type, "label": w.Label, "key_id": w.KeyID,
			},
		})
	}
	return w, nil
}

// Get returns wallet metadata by id.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Wallet, error) {
	w, err := s.Store.GetWallet(ctx, id)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// SetState transitions a wallet between active/paused/retired.
func (s *Service) SetState(ctx context.Context, id uuid.UUID, state WalletState) error {
	if !ValidWalletState(state) {
		return fmt.Errorf("invalid state %q", state)
	}
	w, err := s.Store.GetWallet(ctx, id)
	if err != nil {
		return err
	}
	if w.State == WalletStateRetired && state != WalletStateRetired {
		return errors.New("cannot un-retire a wallet")
	}
	if err := s.Store.UpdateWalletState(ctx, id, state); err != nil {
		return err
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "wallet.state_changed",
			WalletID:  &id,
			Payload:   map[string]any{"from": w.State, "to": state},
		})
	}
	return nil
}

// List returns wallets filtered by chain/type/state (empty = any).
func (s *Service) List(ctx context.Context, chain, wtype, state string) ([]*Wallet, error) {
	return s.Store.ListWallets(ctx, chain, wtype, state)
}

// DeriveAddress derives (and persists) the next receive address for a wallet,
// honoring the rotation policy. If force is true, an on-demand rotation is
// performed.
func (s *Service) DeriveAddress(ctx context.Context, walletID uuid.UUID, force bool) (*Address, error) {
	if force {
		return s.Rotation.ForceRotate(ctx, walletID)
	}
	return s.Rotation.CurrentOrRotate(ctx, walletID)
}

// ListAddresses returns all addresses for a wallet.
func (s *Service) ListAddresses(ctx context.Context, walletID uuid.UUID) ([]*Address, error) {
	return s.Store.ListAddresses(ctx, walletID)
}