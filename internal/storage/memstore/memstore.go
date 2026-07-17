// Package memstore provides an in-memory implementation of storage.Store for
// unit tests. It uses a reentrant mutex to approximate serializable
// transactions, so methods that lock can be safely called from within InTx.
package memstore

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/domain"
	"github.com/google/uuid"
)

// Store is an in-memory implementation of storage.Store.
type Store struct {
	mu sync.Mutex

	wallets   map[uuid.UUID]*domain.Wallet
	addresses map[uuid.UUID]*domain.Address

	balances       map[string]*storage.Balance // key: walletID|asset
	balanceEvents  map[string]bool             // dedup key: walletID|asset|block|eventID
	utxos          map[string]*storage.UTXO
	nonces         map[string]*storage.Nonce
	withdrawals    map[uuid.UUID]*storage.WithdrawalRequest
	keyMappings    map[uuid.UUID][]*storage.KeyMapping
	fundingReq     map[uuid.UUID]*storage.FundingRequest
	auditOutbox    []*storage.AuditOutboxEvent
	auditSeqCounters map[uuid.UUID]int64

	// inflight withdrawal dedup: key walletID|to|amount|asset
	inflightWithdrawals map[string]bool

	// reentrant lock bookkeeping: per-goroutine depth.
	depthMu sync.Mutex
	depths  map[int64]int
}

// New returns a fresh in-memory store.
func New() *Store {
	return &Store{
		wallets:             make(map[uuid.UUID]*domain.Wallet),
		addresses:           make(map[uuid.UUID]*domain.Address),
		balances:            make(map[string]*storage.Balance),
		balanceEvents:       make(map[string]bool),
		utxos:               make(map[string]*storage.UTXO),
		nonces:              make(map[string]*storage.Nonce),
		withdrawals:         make(map[uuid.UUID]*storage.WithdrawalRequest),
		keyMappings:         make(map[uuid.UUID][]*storage.KeyMapping),
		fundingReq:          make(map[uuid.UUID]*storage.FundingRequest),
		auditSeqCounters:    make(map[uuid.UUID]int64),
		inflightWithdrawals: make(map[string]bool),
		depths:              make(map[int64]int),
	}
}

// DB returns nil; memstore has no DB.
func (s *Store) DB() *sql.DB { return nil }

// ApplyMigrations is a no-op for memstore.
func (s *Store) ApplyMigrations(_ context.Context, _ string) error { return nil }

// goID returns the current goroutine id.
func goID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// "goroutine 17 [...]"
	id := int64(0)
	i := len("goroutine ")
	for i < n && buf[i] >= '0' && buf[i] <= '9' {
		id = id*10 + int64(buf[i]-'0')
		i++
	}
	return id
}

// lock acquires the main mutex unless the current goroutine already holds it
// (reentrant). It returns a function to call to release (only when this call
// actually acquired the mutex).
func (s *Store) lock() func() {
	id := goID()
	s.depthMu.Lock()
	if s.depths[id] > 0 {
		s.depths[id]++
		s.depthMu.Unlock()
		return func() {
			s.depthMu.Lock()
			s.depths[id]--
			if s.depths[id] == 0 {
				delete(s.depths, id)
			}
			s.depthMu.Unlock()
		}
	}
	s.depthMu.Unlock()
	s.mu.Lock()
	s.depthMu.Lock()
	s.depths[id] = 1
	s.depthMu.Unlock()
	return func() {
		s.depthMu.Lock()
		s.depths[id]--
		if s.depths[id] == 0 {
			delete(s.depths, id)
		}
		s.depthMu.Unlock()
		s.mu.Unlock()
	}
}

// InTx executes fn with the store mutex held (reentrant), simulating a
// transaction. For the outermost (depth-1) transaction it snapshots the entire
// in-memory state and restores it on rollback, so a failed transaction leaves
// no visible mutations (matching the Postgres implementation's rollback
// semantics). Nested InTx calls reuse the outer snapshot.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	id := goID()
	s.depthMu.Lock()
	depth := s.depths[id]
	s.depthMu.Unlock()

	unlock := s.lock()
	defer unlock()

	if depth > 0 {
		// Nested call: outer tx owns the snapshot.
		return fn(ctx)
	}

	snap := s.snapshot()
	if err := fn(ctx); err != nil {
		s.restore(snap)
		return err
	}
	return nil
}

// snapshot copies the entire mutable state. Caller must hold s.mu.
func (s *Store) snapshot() memSnapshot {
	snap := memSnapshot{
		wallets:             make(map[uuid.UUID]*domain.Wallet, len(s.wallets)),
		addresses:           make(map[uuid.UUID]*domain.Address, len(s.addresses)),
		balances:            make(map[string]*storage.Balance, len(s.balances)),
		balanceEvents:       make(map[string]bool, len(s.balanceEvents)),
		utxos:               make(map[string]*storage.UTXO, len(s.utxos)),
		nonces:              make(map[string]*storage.Nonce, len(s.nonces)),
		withdrawals:         make(map[uuid.UUID]*storage.WithdrawalRequest, len(s.withdrawals)),
		keyMappings:         make(map[uuid.UUID][]*storage.KeyMapping, len(s.keyMappings)),
		fundingReq:          make(map[uuid.UUID]*storage.FundingRequest, len(s.fundingReq)),
		auditOutbox:         append([]*storage.AuditOutboxEvent(nil), s.auditOutbox...),
		auditSeqCounters:   make(map[uuid.UUID]int64, len(s.auditSeqCounters)),
		inflightWithdrawals: make(map[string]bool, len(s.inflightWithdrawals)),
	}
	for k, v := range s.wallets {
		cp := *v
		snap.wallets[k] = &cp
	}
	for k, v := range s.addresses {
		cp := *v
		snap.addresses[k] = &cp
	}
	for k, v := range s.balances {
		cp := *v
		snap.balances[k] = &cp
	}
	for k, v := range s.balanceEvents {
		snap.balanceEvents[k] = v
	}
	for k, v := range s.utxos {
		cp := *v
		snap.utxos[k] = &cp
	}
	for k, v := range s.nonces {
		cp := *v
		snap.nonces[k] = &cp
	}
	for k, v := range s.withdrawals {
		cp := *v
		snap.withdrawals[k] = &cp
	}
	for k, v := range s.keyMappings {
		cp := make([]*storage.KeyMapping, len(v))
		for i, m := range v {
			mm := *m
			cp[i] = &mm
		}
		snap.keyMappings[k] = cp
	}
	for k, v := range s.fundingReq {
		cp := *v
		snap.fundingReq[k] = &cp
	}
	for i, e := range s.auditOutbox {
		cp := *e
		snap.auditOutbox[i] = &cp
	}
	for k, v := range s.auditSeqCounters {
		snap.auditSeqCounters[k] = v
	}
	for k, v := range s.inflightWithdrawals {
		snap.inflightWithdrawals[k] = v
	}
	return snap
}

// restore replaces the live state with the snapshot. Caller must hold s.mu.
func (s *Store) restore(snap memSnapshot) {
	s.wallets = snap.wallets
	s.addresses = snap.addresses
	s.balances = snap.balances
	s.balanceEvents = snap.balanceEvents
	s.utxos = snap.utxos
	s.nonces = snap.nonces
	s.withdrawals = snap.withdrawals
	s.keyMappings = snap.keyMappings
	s.fundingReq = snap.fundingReq
	s.auditOutbox = snap.auditOutbox
	s.auditSeqCounters = snap.auditSeqCounters
	s.inflightWithdrawals = snap.inflightWithdrawals
}

type memSnapshot struct {
	wallets             map[uuid.UUID]*domain.Wallet
	addresses           map[uuid.UUID]*domain.Address
	balances            map[string]*storage.Balance
	balanceEvents       map[string]bool
	utxos               map[string]*storage.UTXO
	nonces              map[string]*storage.Nonce
	withdrawals         map[uuid.UUID]*storage.WithdrawalRequest
	keyMappings         map[uuid.UUID][]*storage.KeyMapping
	fundingReq          map[uuid.UUID]*storage.FundingRequest
	auditOutbox         []*storage.AuditOutboxEvent
	auditSeqCounters    map[uuid.UUID]int64
	inflightWithdrawals map[string]bool
}

func balanceKey(wID uuid.UUID, asset string) string {
	return wID.String() + "|" + asset
}

func nonceKey(wID uuid.UUID, chain string) string {
	return wID.String() + "|" + chain
}

func (s *Store) CreateWallet(_ context.Context, w *domain.Wallet) error {
	defer s.lock()()
	if _, ok := s.wallets[w.ID]; ok {
		return fmt.Errorf("wallet already exists")
	}
	cp := *w
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	cp.UpdatedAt = cp.CreatedAt
	s.wallets[w.ID] = &cp
	return nil
}

func (s *Store) GetWallet(_ context.Context, id uuid.UUID) (*domain.Wallet, error) {
	defer s.lock()()
	w, ok := s.wallets[id]
	if !ok {
		return nil, fmt.Errorf("wallet not found: %w", sql.ErrNoRows)
	}
	cp := *w
	return &cp, nil
}

func (s *Store) UpdateWalletState(_ context.Context, id uuid.UUID, state domain.WalletState) error {
	defer s.lock()()
	w, ok := s.wallets[id]
	if !ok {
		return fmt.Errorf("wallet not found: %w", sql.ErrNoRows)
	}
	w.State = state
	w.UpdatedAt = time.Now()
	return nil
}

func (s *Store) ListWallets(_ context.Context, chainF, typeF, stateF string) ([]*domain.Wallet, error) {
	defer s.lock()()
	var out []*domain.Wallet
	for _, w := range s.wallets {
		if chainF != "" && string(w.Chain) != chainF {
			continue
		}
		if typeF != "" && string(w.Type) != typeF {
			continue
		}
		if stateF != "" && string(w.State) != stateF {
			continue
		}
		cp := *w
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

func (s *Store) InsertAddress(_ context.Context, a *domain.Address) error {
	defer s.lock()()
	if _, ok := s.addresses[a.ID]; ok {
		return fmt.Errorf("address exists")
	}
	// enforce one active per wallet
	if a.State == domain.AddressStateActive {
		for _, ex := range s.addresses {
			if ex.WalletID == a.WalletID && ex.State == domain.AddressStateActive {
				return fmt.Errorf("active address already exists for wallet")
			}
		}
	}
	cp := *a
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	s.addresses[a.ID] = &cp
	return nil
}

func (s *Store) GetActiveAddress(_ context.Context, walletID uuid.UUID) (*domain.Address, error) {
	defer s.lock()()
	for _, a := range s.addresses {
		if a.WalletID == walletID && a.State == domain.AddressStateActive {
			cp := *a
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("no active address: %w", sql.ErrNoRows)
}

func (s *Store) ListAddresses(_ context.Context, walletID uuid.UUID) ([]*domain.Address, error) {
	defer s.lock()()
	var out []*domain.Address
	for _, a := range s.addresses {
		if a.WalletID == walletID {
			cp := *a
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Change != out[j].Change {
			return out[i].Change < out[j].Change
		}
		return out[i].Index < out[j].Index
	})
	return out, nil
}

func (s *Store) DeprecateAddress(_ context.Context, id uuid.UUID) error {
	defer s.lock()()
	a, ok := s.addresses[id]
	if !ok {
		return fmt.Errorf("address not found: %w", sql.ErrNoRows)
	}
	a.State = domain.AddressStateDeprecated
	return nil
}

func (s *Store) GetAddress(_ context.Context, id uuid.UUID) (*domain.Address, error) {
	defer s.lock()()
	a, ok := s.addresses[id]
	if !ok {
		return nil, fmt.Errorf("address not found: %w", sql.ErrNoRows)
	}
	cp := *a
	return &cp, nil
}

func (s *Store) NextAddressIndex(_ context.Context, chain string, change int) (int, error) {
	defer s.lock()()
	maxIdx := -1
	for _, a := range s.addresses {
		if string(a.Chain) == chain && a.Change == change {
			if a.Index > maxIdx {
				maxIdx = a.Index
			}
		}
	}
	return maxIdx + 1, nil
}

func (s *Store) IncrementReceiveCount(_ context.Context, id uuid.UUID) error {
	defer s.lock()()
	a, ok := s.addresses[id]
	if !ok {
		return fmt.Errorf("address not found: %w", sql.ErrNoRows)
	}
	a.ReceiveCount++
	return nil
}

func (s *Store) UpsertBalance(_ context.Context, b *storage.Balance) error {
	defer s.lock()()
	key := balanceKey(b.WalletID, b.Asset)
	cp := *b
	cp.UpdatedAt = time.Now()
	s.balances[key] = &cp
	return nil
}

func (s *Store) GetBalance(_ context.Context, walletID uuid.UUID, asset string) (*storage.Balance, error) {
	defer s.lock()()
	b, ok := s.balances[balanceKey(walletID, asset)]
	if !ok {
		return nil, fmt.Errorf("balance not found: %w", sql.ErrNoRows)
	}
	cp := *b
	return &cp, nil
}

func (s *Store) ListBalances(_ context.Context, walletID uuid.UUID) ([]*storage.Balance, error) {
	defer s.lock()()
	var out []*storage.Balance
	prefix := walletID.String() + "|"
	for k, b := range s.balances {
		if strings.HasPrefix(k, prefix) {
			cp := *b
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Asset < out[j].Asset })
	return out, nil
}

func (s *Store) RecordBalanceEvent(_ context.Context, e *storage.BalanceEvent) error {
	defer s.lock()()
	key := e.WalletID.String() + "|" + e.Asset + "|" + fmt.Sprintf("%d", e.BlockHeight) + "|" + e.EventID
	if s.balanceEvents[key] {
		return storage.ErrDuplicateEvent
	}
	s.balanceEvents[key] = true
	return nil
}

func (s *Store) InsertUTXO(_ context.Context, u *storage.UTXO) error {
	defer s.lock()()
	if _, ok := s.utxos[u.Outpoint]; ok {
		return fmt.Errorf("utxo exists")
	}
	cp := *u
	cp.UpdatedAt = time.Now()
	s.utxos[u.Outpoint] = &cp
	return nil
}

func (s *Store) ListFreeUTXOs(_ context.Context, walletID uuid.UUID) ([]*storage.UTXO, error) {
	defer s.lock()()
	var out []*storage.UTXO
	for _, u := range s.utxos {
		if u.WalletID == walletID && u.LockState == string(storage.UTXOLockStateFree) {
			cp := *u
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Outpoint < out[j].Outpoint })
	return out, nil
}

func (s *Store) LockUTXOs(_ context.Context, outpoints []string) error {
	defer s.lock()()
	for _, op := range outpoints {
		u, ok := s.utxos[op]
		if !ok {
			return fmt.Errorf("utxo not found: %s", op)
		}
		if u.LockState != string(storage.UTXOLockStateFree) {
			return fmt.Errorf("utxo not free: %s state=%s", op, u.LockState)
		}
	}
	now := time.Now()
	for _, op := range outpoints {
		u := s.utxos[op]
		u.LockState = string(storage.UTXOLockStateLocked)
		u.LockedAt = &now
		u.UpdatedAt = now
	}
	return nil
}

func (s *Store) MarkUTXOsSpent(_ context.Context, outpoints []string, txHash string) error {
	defer s.lock()()
	now := time.Now()
	for _, op := range outpoints {
		u, ok := s.utxos[op]
		if !ok {
			return fmt.Errorf("utxo not found: %s", op)
		}
		u.LockState = string(storage.UTXOLockStateSpent)
		u.SpentAt = &now
		u.TxHash = txHash
		u.UpdatedAt = now
	}
	return nil
}

func (s *Store) RestoreUTXOs(_ context.Context, outpoints []string) error {
	defer s.lock()()
	now := time.Now()
	for _, op := range outpoints {
		u, ok := s.utxos[op]
		if !ok {
			return fmt.Errorf("utxo not found: %s", op)
		}
		u.LockState = string(storage.UTXOLockStateFree)
		u.SpentAt = nil
		u.LockedAt = nil
		u.TxHash = ""
		u.UpdatedAt = now
	}
	return nil
}

func (s *Store) PruneUTXOs(_ context.Context, outpoints []string) error {
	defer s.lock()()
	for _, op := range outpoints {
		delete(s.utxos, op)
	}
	return nil
}

func (s *Store) GetNonce(_ context.Context, walletID uuid.UUID, chain string) (*storage.Nonce, error) {
	defer s.lock()()
	n, ok := s.nonces[nonceKey(walletID, chain)]
	if !ok {
		return &storage.Nonce{WalletID: walletID, Chain: chain}, nil
	}
	cp := *n
	return &cp, nil
}

func (s *Store) UpsertNonce(_ context.Context, n *storage.Nonce) error {
	defer s.lock()()
	cp := *n
	cp.UpdatedAt = time.Now()
	s.nonces[nonceKey(n.WalletID, n.Chain)] = &cp
	return nil
}

func (s *Store) IncrementPendingNonce(_ context.Context, walletID uuid.UUID, chain string) (int64, int, error) {
	defer s.lock()()
	n, ok := s.nonces[nonceKey(walletID, chain)]
	if !ok {
		n = &storage.Nonce{WalletID: walletID, Chain: chain}
		s.nonces[nonceKey(walletID, chain)] = n
	}
	val := n.PendingNonce
	n.PendingNonce = val + 1
	n.Version++
	n.UpdatedAt = time.Now()
	return val, n.Version, nil
}

func (s *Store) AdvanceBroadcastNonce(_ context.Context, walletID uuid.UUID, chain string, nonce int64) error {
	defer s.lock()()
	n, ok := s.nonces[nonceKey(walletID, chain)]
	if !ok {
		n = &storage.Nonce{WalletID: walletID, Chain: chain}
		s.nonces[nonceKey(walletID, chain)] = n
	}
	if nonce+1 > n.BroadcastNonce {
		n.BroadcastNonce = nonce + 1
	}
	n.UpdatedAt = time.Now()
	return nil
}

func (s *Store) CreateWithdrawal(_ context.Context, w *storage.WithdrawalRequest) error {
	defer s.lock()()
	if _, ok := s.withdrawals[w.ID]; ok {
		return fmt.Errorf("withdrawal exists")
	}
	key := withdrawalDedupKey(w.WalletID, w.ToAddress, w.Amount, w.Asset)
	if s.inflightWithdrawals[key] {
		return storage.ErrDuplicateWithdrawal
	}
	s.inflightWithdrawals[key] = true
	cp := *w
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	cp.UpdatedAt = cp.CreatedAt
	s.withdrawals[w.ID] = &cp
	return nil
}

func (s *Store) GetWithdrawal(_ context.Context, id uuid.UUID) (*storage.WithdrawalRequest, error) {
	defer s.lock()()
	w, ok := s.withdrawals[id]
	if !ok {
		return nil, fmt.Errorf("withdrawal not found: %w", sql.ErrNoRows)
	}
	cp := *w
	return &cp, nil
}

func (s *Store) ListWithdrawals(_ context.Context, walletID uuid.UUID, stateF string) ([]*storage.WithdrawalRequest, error) {
	defer s.lock()()
	var out []*storage.WithdrawalRequest
	for _, w := range s.withdrawals {
		if walletID != uuid.Nil && w.WalletID != walletID {
			continue
		}
		if stateF != "" && w.State != stateF {
			continue
		}
		cp := *w
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) UpdateWithdrawalState(_ context.Context, id uuid.UUID, state string, reason string, txHash string, policyDecisionID string) error {
	defer s.lock()()
	w, ok := s.withdrawals[id]
	if !ok {
		return fmt.Errorf("withdrawal not found: %w", sql.ErrNoRows)
	}
	// release inflight dedup if transitioning to terminal
	if state == string(storage.WithdrawalStateConfirmed) || state == string(storage.WithdrawalStateFailed) {
		key := withdrawalDedupKey(w.WalletID, w.ToAddress, w.Amount, w.Asset)
		delete(s.inflightWithdrawals, key)
	}
	w.State = state
	if reason != "" {
		w.FailureReason = reason
	}
	if txHash != "" {
		w.TxHash = txHash
	}
	if policyDecisionID != "" {
		w.PolicyDecisionID = policyDecisionID
	}
	w.UpdatedAt = time.Now()
	return nil
}

func (s *Store) UpdateWithdrawalNonce(_ context.Context, id uuid.UUID, nonce int64) error {
	defer s.lock()()
	w, ok := s.withdrawals[id]
	if !ok {
		return fmt.Errorf("withdrawal not found: %w", sql.ErrNoRows)
	}
	w.NonceValue = &nonce
	w.UpdatedAt = time.Now()
	return nil
}

func (s *Store) BindKeyMapping(_ context.Context, m *storage.KeyMapping) error {
	defer s.lock()()
	for _, ex := range s.keyMappings[m.WalletID] {
		if ex.RotationState == string(storage.RotationStateCurrent) && m.RotationState == string(storage.RotationStateCurrent) {
			return fmt.Errorf("current key mapping already exists")
		}
	}
	cp := *m
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	if cp.ActiveFrom.IsZero() {
		cp.ActiveFrom = time.Now()
	}
	s.keyMappings[m.WalletID] = append(s.keyMappings[m.WalletID], &cp)
	return nil
}

func (s *Store) ResolveActiveKey(_ context.Context, walletID uuid.UUID) ([]*storage.KeyMapping, error) {
	defer s.lock()()
	var out []*storage.KeyMapping
	for _, m := range s.keyMappings[walletID] {
		if m.RotationState == string(storage.RotationStateCurrent) || m.RotationState == string(storage.RotationStateCooling) {
			cp := *m
			out = append(out, &cp)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no active key mapping: %w", sql.ErrNoRows)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ActiveFrom.Before(out[j].ActiveFrom) })
	return out, nil
}

func (s *Store) RotateKeyMapping(_ context.Context, walletID uuid.UUID, newKeyID string, cooling time.Duration) error {
	defer s.lock()()
	now := time.Now()
	activeTo := now.Add(cooling)
	for _, m := range s.keyMappings[walletID] {
		if m.RotationState == string(storage.RotationStateCurrent) {
			m.RotationState = string(storage.RotationStateCooling)
			m.ActiveTo = &activeTo
		}
	}
	for _, m := range s.keyMappings[walletID] {
		if m.KeyID == newKeyID {
			m.RotationState = string(storage.RotationStateCurrent)
			m.ActiveTo = nil
			m.ActiveFrom = now
			return nil
		}
	}
	s.keyMappings[walletID] = append(s.keyMappings[walletID], &storage.KeyMapping{
		WalletID:      walletID,
		KeyID:         newKeyID,
		ActiveFrom:    now,
		RotationState: string(storage.RotationStateCurrent),
		CreatedAt:     now,
	})
	return nil
}

func (s *Store) ExpireCooling(_ context.Context) error {
	defer s.lock()()
	now := time.Now()
	for _, mappings := range s.keyMappings {
		for _, m := range mappings {
			if m.RotationState == string(storage.RotationStateCooling) && m.ActiveTo != nil && now.After(*m.ActiveTo) {
				m.RotationState = string(storage.RotationStateRetired)
			}
		}
	}
	return nil
}

func (s *Store) CreateFundingRequest(_ context.Context, f *storage.FundingRequest) error {
	defer s.lock()()
	// idempotency: one open 'requested' per (wallet, asset)
	for _, ex := range s.fundingReq {
		if ex.WalletID == f.WalletID && ex.Asset == f.Asset && ex.State == string(storage.FundingStateRequested) {
			return storage.ErrDuplicateFunding
		}
	}
	cp := *f
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	cp.UpdatedAt = cp.CreatedAt
	s.fundingReq[f.ID] = &cp
	return nil
}

func (s *Store) GetOpenFundingRequest(_ context.Context, walletID uuid.UUID, asset string) (*storage.FundingRequest, error) {
	defer s.lock()()
	for _, f := range s.fundingReq {
		if f.WalletID == walletID && f.Asset == asset && f.State == string(storage.FundingStateRequested) {
			cp := *f
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("no open funding request: %w", sql.ErrNoRows)
}

func (s *Store) ListFundingRequests(_ context.Context, walletID uuid.UUID, stateF string) ([]*storage.FundingRequest, error) {
	defer s.lock()()
	var out []*storage.FundingRequest
	for _, f := range s.fundingReq {
		if walletID != uuid.Nil && f.WalletID != walletID {
			continue
		}
		if stateF != "" && f.State != stateF {
			continue
		}
		cp := *f
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) UpdateFundingState(_ context.Context, id uuid.UUID, state string, treasuryBatchID string) error {
	defer s.lock()()
	f, ok := s.fundingReq[id]
	if !ok {
		return fmt.Errorf("funding request not found: %w", sql.ErrNoRows)
	}
	f.State = state
	if treasuryBatchID != "" {
		f.TreasuryBatchID = treasuryBatchID
	}
	f.UpdatedAt = time.Now()
	return nil
}

func (s *Store) AppendAuditEvent(_ context.Context, e *storage.AuditOutboxEvent) error {
	defer s.lock()()
	for _, ex := range s.auditOutbox {
		if ex.EventID == e.EventID {
			return storage.ErrDuplicateAudit
		}
	}
	cp := *e
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	s.auditOutbox = append(s.auditOutbox, &cp)
	return nil
}

func (s *Store) ListUndeliveredAuditEvents(_ context.Context, limit int) ([]*storage.AuditOutboxEvent, error) {
	defer s.lock()()
	var out []*storage.AuditOutboxEvent
	for _, e := range s.auditOutbox {
		if !e.Delivered {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) MarkAuditDelivered(_ context.Context, id uuid.UUID) error {
	defer s.lock()()
	for _, e := range s.auditOutbox {
		if e.ID == id {
			e.Delivered = true
			e.Attempts++
			now := time.Now()
			e.DeliveredAt = &now
			return nil
		}
	}
	return fmt.Errorf("audit event not found: %w", sql.ErrNoRows)
}

func (s *Store) NextAuditSeq(_ context.Context, walletID uuid.UUID) (int64, error) {
	defer s.lock()()
	s.auditSeqCounters[walletID]++
	return s.auditSeqCounters[walletID], nil
}

func withdrawalDedupKey(wID uuid.UUID, to, amount, asset string) string {
	return wID.String() + "|" + to + "|" + amount + "|" + asset
}