package wallet

import (
	"context"
	"database/sql"
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


// RotationPolicy decides when a receive address should be rotated and performs
// the rotation atomically under a per-wallet lock.
type RotationPolicy struct {
	Store    storage.Store
	Derivers *deriver.Registry
	Locker   lock.Locker
	Audit    audit.Emitter
	Config   config.Config
}

// NewRotationPolicy constructs a RotationPolicy.
func NewRotationPolicy(st storage.Store, reg *deriver.Registry, lk lock.Locker, em audit.Emitter, cfg config.Config) *RotationPolicy {
	return &RotationPolicy{Store: st, Derivers: reg, Locker: lk, Audit: em, Config: cfg}
}

// CurrentOrRotate returns the current active address; if it has aged out or
// exceeded the receive count, it rotates.
func (p *RotationPolicy) CurrentOrRotate(ctx context.Context, walletID uuid.UUID) (*Address, error) {
	w, err := p.Store.GetWallet(ctx, walletID)
	if err != nil {
		return nil, err
	}
	if w.State == WalletStateRetired {
		return nil, ErrWalletRetired
	}
	active, err := p.Store.GetActiveAddress(ctx, walletID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p.deriveNewLocked(ctx, w)
		}
		return nil, err
	}
	if p.shouldRotate(w, active) {
		return p.rotate(ctx, w, active)
	}
	return active, nil
}

// ForceRotate unconditionally deprecates the current address and derives a new one.
func (p *RotationPolicy) ForceRotate(ctx context.Context, walletID uuid.UUID) (*Address, error) {
	w, err := p.Store.GetWallet(ctx, walletID)
	if err != nil {
		return nil, err
	}
	if w.State == WalletStateRetired {
		return nil, ErrWalletRetired
	}
	active, err := p.Store.GetActiveAddress(ctx, walletID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if active != nil {
		return p.rotate(ctx, w, active)
	}
	// No active address: still acquire the per-wallet lock so concurrent
	// first-derivation attempts do not race on InsertAddress (active-uniqueness).
	lockName := "addr:" + w.ID.String()
	token, ok, err := p.Locker.Acquire(ctx, lockName, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("acquire rotation lock: %w", err)
	}
	if !ok {
		return nil, errors.New("rotation already in progress")
	}
	defer func() { _ = p.Locker.Release(ctx, lockName, token) }()
	return p.deriveNew(ctx, w)
}

func (p *RotationPolicy) shouldRotate(w *Wallet, a *Address) bool {
	days := p.Config.DefaultRotationDays
	if w.RotationDays != nil {
		days = *w.RotationDays
	}
	if days > 0 && time.Since(a.CreatedAt) > time.Duration(days)*24*time.Hour {
		return true
	}
	if w.RotationAfterReceives != nil && a.ReceiveCount >= *w.RotationAfterReceives {
		return true
	}
	return false
}

func (p *RotationPolicy) rotate(ctx context.Context, w *Wallet, old *Address) (*Address, error) {
	lockName := "addr:" + w.ID.String()
	token, ok, err := p.Locker.Acquire(ctx, lockName, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("acquire rotation lock: %w", err)
	}
	if !ok {
		return nil, errors.New("rotation already in progress")
	}
	defer func() { _ = p.Locker.Release(ctx, lockName, token) }()

	if err := p.Store.DeprecateAddress(ctx, old.ID); err != nil {
		return nil, fmt.Errorf("deprecate old address: %w", err)
	}
	addr, err := p.deriveNew(ctx, w)
	if err != nil {
		return nil, err
	}
	if p.Audit != nil {
		_ = p.Audit.Emit(ctx, &audit.Event{
			EventType: "wallet.address.rotated",
			WalletID:  &w.ID,
			Payload: map[string]any{
				"old_address": old.Address, "new_address": addr.Address, "index": addr.Index,
			},
		})
	}
	return addr, nil
}

func (p *RotationPolicy) deriveNew(ctx context.Context, w *Wallet) (*Address, error) {
	d, err := p.Derivers.For(deriver.Chain(w.Chain))
	if err != nil {
		return nil, err
	}
	change := 0
	if w.Chain == ChainBitcoin {
		change = 0 // receive chain
	}
	idx, err := p.Store.NextAddressIndex(ctx, string(w.Chain), change)
	if err != nil {
		return nil, fmt.Errorf("next index: %w", err)
	}
	// Solana only ever has index 0; if it already exists, return it.
	if w.Chain == ChainSolana {
		addrs, _ := p.Store.ListAddresses(ctx, w.ID)
		if len(addrs) > 0 {
			if err := p.Store.DeprecateAddress(ctx, addrs[0].ID); err != nil {
				return nil, err
			}
		}
	}
	res, err := d.DeriveAt(ctx, w.ID, deriver.Chain(w.Chain), idx, change)
	if err != nil {
		return nil, fmt.Errorf("derive: %w", err)
	}
	addr := &Address{
		ID:             uuid.New(),
		WalletID:       w.ID,
		Chain:          w.Chain,
		Address:        res.Address,
		DerivationPath: res.DerivationPath,
		Index:          res.Index,
		Change:         res.Change,
		State:          AddressStateActive,
		CreatedAt:      time.Now().UTC(),
	}
	if err := p.Store.InsertAddress(ctx, addr); err != nil {
		return nil, fmt.Errorf("insert address: %w", err)
	}
	if p.Audit != nil {
		_ = p.Audit.Emit(ctx, &audit.Event{
			EventType: "wallet.address.derived",
			WalletID:  &w.ID,
			Payload:   map[string]any{"address": addr.Address, "index": addr.Index, "path": addr.DerivationPath},
		})
	}
	return addr, nil
}

// deriveNewLocked acquires the per-wallet rotation lock before deriving a new
// active address, so concurrent first-derivation attempts on a fresh wallet do
// not race on the active-address uniqueness constraint. Losers spin-retry a few
// times and return the address created by the winner.
func (p *RotationPolicy) deriveNewLocked(ctx context.Context, w *Wallet) (*Address, error) {
	lockName := "addr:" + w.ID.String()
	const retries = 20
	const sleep = 10 * time.Millisecond
	for i := 0; i < retries; i++ {
		token, ok, err := p.Locker.Acquire(ctx, lockName, 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("acquire rotation lock: %w", err)
		}
		if !ok {
			// Another goroutine holds the lock; it may be creating the active
			// address. Re-check then back off.
			if active, err := p.Store.GetActiveAddress(ctx, w.ID); err == nil {
				return active, nil
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			time.Sleep(sleep)
			continue
		}
		// We hold the lock; re-check then derive.
		if active, err := p.Store.GetActiveAddress(ctx, w.ID); err == nil {
			_ = p.Locker.Release(ctx, lockName, token)
			return active, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			_ = p.Locker.Release(ctx, lockName, token)
			return nil, err
		}
		addr, err := p.deriveNew(ctx, w)
		_ = p.Locker.Release(ctx, lockName, token)
		return addr, err
	}
	return nil, errors.New("rotation already in progress")
}