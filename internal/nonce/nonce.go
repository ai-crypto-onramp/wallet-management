// Package nonce implements EVM per-wallet nonce coordination with a Redis-backed
// lock and gap-safe pre-allocation/rollback.
package nonce

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

// Service manages EVM nonces per (wallet, chain).
type Service struct {
	Store  storage.Store
	Locker lock.Locker
}

// NewService constructs a NonceService.
func NewService(st storage.Store, lk lock.Locker) *Service {
	return &Service{Store: st, Locker: lk}
}

// ReserveNonce acquires a Redis lock, increments the pending nonce, and returns
// the reserved value. The lock is released before returning so subsequent
// callers are not blocked; the pending_nonce counter itself serializes gaps.
// If the lock is contended it briefly retries so concurrent reservations all
// succeed (the underlying pending_nonce counter guarantees distinct values).
func (s *Service) ReserveNonce(ctx context.Context, walletID uuid.UUID, chain wallet.Chain) (int64, error) {
	lockName := fmt.Sprintf("nonce:lock:%s:%s", walletID, chain)
	const retries = 50
	const sleep = 5 * time.Millisecond
	for i := 0; i < retries; i++ {
		token, ok, err := s.Locker.Acquire(ctx, lockName, 5*time.Second)
		if err != nil {
			return 0, fmt.Errorf("acquire nonce lock: %w", err)
		}
		if !ok {
			time.Sleep(sleep)
			continue
		}
		val, _, err := s.Store.IncrementPendingNonce(ctx, walletID, string(chain))
		_ = s.Locker.Release(ctx, lockName, token)
		if err != nil {
			return 0, fmt.Errorf("increment pending nonce: %w", err)
		}
		return val, nil
	}
	return 0, fmt.Errorf("nonce lock contention for %s/%s", walletID, chain)
}

// CommitNonce advances the broadcast nonce to nonce+1.
func (s *Service) CommitNonce(ctx context.Context, walletID uuid.UUID, chain wallet.Chain, nonce int64) error {
	return s.Store.AdvanceBroadcastNonce(ctx, walletID, string(chain), nonce)
}

// RollbackNonce releases a reserved nonce back to the available pool. It is
// gap-safe: pending_nonce is only decremented back to `nonce` when the
// current pending_nonce equals `nonce+1` (i.e. this was the most recently
// reserved nonce and no higher nonce has been reserved in the meantime). If
// a higher nonce was already reserved, the rollback is a no-op and the gap
// will be filled by the chain's mempool replacement policy.
func (s *Service) RollbackNonce(ctx context.Context, walletID uuid.UUID, chain wallet.Chain, nonce int64) error {
	_, err := s.Store.RollbackPendingNonce(ctx, walletID, string(chain), nonce)
	return err
}

// Get returns the current nonce row.
func (s *Service) Get(ctx context.Context, walletID uuid.UUID, chain wallet.Chain) (*storage.Nonce, error) {
	return s.Store.GetNonce(ctx, walletID, string(chain))
}
