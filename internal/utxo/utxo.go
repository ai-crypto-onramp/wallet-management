// Package utxo implements BTC UTXO set management: selection, locking, spending,
// and reorg restoration.
package utxo

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/google/uuid"
)

// Service manages the BTC UTXO set per wallet.
type Service struct {
	Store storage.Store
}

// NewService constructs a UTXOService.
func NewService(st storage.Store) *Service {
	return &Service{Store: st}
}

// SelectForAmount greedily selects free UTXOs whose total value >= amount
// (minor units), atomically marks them locked, and returns the selected
// outpoints and their total. Returns an error if insufficient funds.
func (s *Service) SelectForAmount(ctx context.Context, walletID uuid.UUID, amount int64) ([]string, int64, error) {
	free, err := s.Store.ListFreeUTXOs(ctx, walletID)
	if err != nil {
		return nil, 0, err
	}
	// greedy largest-first for fewer inputs
	sort.Slice(free, func(i, j int) bool {
		return parseVal(free[i].Value) > parseVal(free[j].Value)
	})
	var selected []string
	var total int64
	for _, u := range free {
		if total >= amount {
			break
		}
		selected = append(selected, u.Outpoint)
		total += parseVal(u.Value)
	}
	if total < amount {
		return nil, 0, fmt.Errorf("insufficient funds: need %d have %d", amount, total)
	}
	if err := s.Store.LockUTXOs(ctx, selected); err != nil {
		return nil, 0, fmt.Errorf("lock utxos: %w", err)
	}
	return selected, total, nil
}

// Unlock releases locked UTXOs back to free.
func (s *Service) Unlock(ctx context.Context, outpoints []string) error {
	return s.Store.RestoreUTXOs(ctx, outpoints)
}

// MarkSpent marks the given outpoints as spent with the broadcast tx hash.
func (s *Service) MarkSpent(ctx context.Context, outpoints []string, txHash string) error {
	return s.Store.MarkUTXOsSpent(ctx, outpoints, txHash)
}

// RestoreOnReorg flips spent UTXOs back to free.
func (s *Service) RestoreOnReorg(ctx context.Context, outpoints []string) error {
	return s.Store.RestoreUTXOs(ctx, outpoints)
}

// PruneFinalized deletes/archives finalized spent UTXOs.
func (s *Service) PruneFinalized(ctx context.Context, outpoints []string) error {
	return s.Store.PruneUTXOs(ctx, outpoints)
}

// TrackUTXO inserts a new UTXO into the set.
func (s *Service) TrackUTXO(ctx context.Context, u *storage.UTXO) error {
	return s.Store.InsertUTXO(ctx, u)
}

func parseVal(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// SelectMutex is a per-wallet selection mutex used by tests to assert
// correctness under concurrency; production relies on DB row locks.
type SelectMutex struct {
	mu sync.Mutex
}

// Lock acquires the mutex.
func (m *SelectMutex) Lock()   { m.mu.Lock() }

// Unlock releases the mutex.
func (m *SelectMutex) Unlock() { m.mu.Unlock() }