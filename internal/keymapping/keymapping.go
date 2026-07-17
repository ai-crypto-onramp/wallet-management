// Package keymapping implements the wallet-to-MPC key_id mapping with
// cooling-off key rotation.
package keymapping

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/google/uuid"
)

// Service manages wallet-to-key_id mappings.
type Service struct {
	Store  storage.Store
	Audit  audit.Emitter
	Config config.Config
}

// NewService constructs a KeyMappingService.
func NewService(st storage.Store, em audit.Emitter, cfg config.Config) *Service {
	return &Service{Store: st, Audit: em, Config: cfg}
}

// Bind associates a wallet with an MPC key_id at provisioning time.
func (s *Service) Bind(ctx context.Context, walletID uuid.UUID, keyID string) error {
	now := time.Now()
	return s.Store.BindKeyMapping(ctx, &storage.KeyMapping{
		WalletID:      walletID,
		KeyID:         keyID,
		ActiveFrom:    now,
		RotationState: string(storage.RotationStateCurrent),
		CreatedAt:     now,
	})
}

// Rotate initiates key rotation: the current mapping enters cooling-off and a
// new current mapping is created for newKeyID.
func (s *Service) Rotate(ctx context.Context, walletID uuid.UUID, newKeyID string) error {
	if err := s.Store.RotateKeyMapping(ctx, walletID, newKeyID, s.Config.KeyCoolingPeriod); err != nil {
		return err
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "key.rotated",
			WalletID:  &walletID,
			Payload:   map[string]any{"new_key_id": newKeyID, "cooling_period": s.Config.KeyCoolingPeriod.String()},
		})
	}
	return nil
}

// ResolveActive returns the current (and cooling) key mappings for a wallet.
func (s *Service) ResolveActive(ctx context.Context, walletID uuid.UUID) ([]*storage.KeyMapping, error) {
	mappings, err := s.Store.ResolveActiveKey(ctx, walletID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no active key for wallet %s: %w", walletID, err)
		}
		return nil, err
	}
	return mappings, nil
}

// ResolveActiveKeyID returns the single current key_id for a wallet, or the
// first available mapping during cooling-off. Satisfies withdrawal.KeyResolver.
func (s *Service) ResolveActiveKeyID(ctx context.Context, walletID uuid.UUID) (string, error) {
	ms, err := s.ResolveActive(ctx, walletID)
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return "", fmt.Errorf("no active key for wallet %s", walletID)
	}
	return ms[0].KeyID, nil
}

// ExpireCooling retires any cooling mappings whose cooling period has elapsed.
func (s *Service) ExpireCooling(ctx context.Context) error {
	return s.Store.ExpireCooling(ctx)
}