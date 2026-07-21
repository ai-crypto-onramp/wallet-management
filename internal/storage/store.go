// Package storage defines the persistence interface for wallet-management.
// The interface is implemented by both a Postgres backend and an in-memory
// backend used by unit tests so that `go test ./...` passes without docker.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/domain"
	"github.com/google/uuid"
)

// Sentinel errors for idempotency / dedup paths.
var (
	ErrDuplicateEvent      = errors.New("duplicate balance event")
	ErrDuplicateWithdrawal = errors.New("duplicate in-flight withdrawal")
	ErrDuplicateFunding    = errors.New("duplicate open funding request")
	ErrDuplicateAudit      = errors.New("duplicate audit event")
)

// WithdrawalState enumerates the lifecycle states of a WithdrawalRequest.
type WithdrawalState string

const (
	WithdrawalStatePending     WithdrawalState = "PENDING"
	WithdrawalStateWhitelisted WithdrawalState = "WHITELISTED"
	WithdrawalStateSigned      WithdrawalState = "SIGNED"
	WithdrawalStateBroadcast   WithdrawalState = "BROADCAST"
	WithdrawalStateConfirmed   WithdrawalState = "CONFIRMED"
	WithdrawalStateFailed      WithdrawalState = "FAILED"
)

// FundingState enumerates the lifecycle states of a FundingRequest.
type FundingState string

const (
	FundingStateRequested FundingState = "REQUESTED"
	FundingStateApproved  FundingState = "APPROVED"
	FundingStateSettled   FundingState = "SETTLED"
	FundingStateRejected  FundingState = "REJECTED"
)

// RotationState enumerates the key rotation states of a KeyMapping.
type RotationState string

const (
	RotationStateCurrent RotationState = "CURRENT"
	RotationStateCooling RotationState = "COOLING"
	RotationStateRetired RotationState = "RETIRED"
)

// UTXOLockState enumerates the lock states of a UTXO.
type UTXOLockState string

const (
	UTXOLockStateFree   UTXOLockState = "FREE"
	UTXOLockStateLocked UTXOLockState = "LOCKED"
	UTXOLockStateSpent  UTXOLockState = "SPENT"
)

// ScriptType is the BTC script type of a UTXO.
type ScriptType string

const (
	ScriptTypeP2WPKH ScriptType = "P2WPKH"
)

// TxRunner executes fn within a serializable DB transaction.
type TxRunner interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Balance is the per-(wallet,asset) balance row.
type Balance struct {
	WalletID      uuid.UUID
	Asset         string
	Confirmed     string
	Pending       string
	Locked        string
	LastBlockSeen int64
	UpdatedAt     time.Time
}

// UTXO is a single BTC unspent transaction output.
type UTXO struct {
	Outpoint      string
	WalletID      uuid.UUID
	Value         string
	ScriptType    string
	Confirmations int
	LockState     string
	LockedAt      *time.Time
	SpentAt       *time.Time
	TxHash        string
	UpdatedAt     time.Time
}

// Nonce is the per-(wallet,chain) EVM nonce counter.
type Nonce struct {
	WalletID       uuid.UUID
	Chain          string
	PendingNonce   int64
	BroadcastNonce int64
	Version        int
	UpdatedAt      time.Time
}

// WithdrawalRequest is an outbound withdrawal record.
type WithdrawalRequest struct {
	ID                uuid.UUID
	WalletID          uuid.UUID
	ToAddress         string
	Asset             string
	Amount            string
	State             string
	PolicyDecisionID  string
	FailureReason     string
	TxHash            string
	NonceValue        *int64
	ReservedOutpoints []string
	SignedTxBytes     []byte
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// KeyMapping binds a wallet to an MPC key_id with rotation state.
type KeyMapping struct {
	WalletID      uuid.UUID
	KeyID         string
	ActiveFrom    time.Time
	ActiveTo      *time.Time
	RotationState string
	CreatedAt     time.Time
}

// FundingRequest is a treasury funding request.
type FundingRequest struct {
	ID              uuid.UUID
	WalletID        uuid.UUID
	Asset           string
	Amount          string
	State           string
	TreasuryBatchID string
	Reason          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AuditOutboxEvent is a pending audit event row.
type AuditOutboxEvent struct {
	ID          uuid.UUID
	EventID     uuid.UUID
	WalletID    *uuid.UUID
	EventType   string
	Payload     []byte
	Seq         int64
	Delivered   bool
	Attempts    int
	CreatedAt   time.Time
	DeliveredAt *time.Time
}

// BalanceEvent is an idempotency record for a balance confirmation event.
type BalanceEvent struct {
	ID          uuid.UUID
	WalletID    uuid.UUID
	Asset       string
	BlockHeight int64
	EventID     string
}

// Store is the persistence boundary for the entire service.
type Store interface {
	TxRunner

	// Wallets
	CreateWallet(ctx context.Context, w *domain.Wallet) error
	GetWallet(ctx context.Context, id uuid.UUID) (*domain.Wallet, error)
	UpdateWalletState(ctx context.Context, id uuid.UUID, state domain.WalletState) error
	ListWallets(ctx context.Context, chain string, wtype string, state string) ([]*domain.Wallet, error)

	// Addresses
	InsertAddress(ctx context.Context, a *domain.Address) error
	GetActiveAddress(ctx context.Context, walletID uuid.UUID) (*domain.Address, error)
	ListAddresses(ctx context.Context, walletID uuid.UUID) ([]*domain.Address, error)
	DeprecateAddress(ctx context.Context, id uuid.UUID) error
	// NextAddressIndex allocates the next derivation index per (chain, change),
	// not per wallet: xpubs are provisioned per chain, so all wallets on a
	// chain share one keyspace and per-wallet numbering would derive duplicate
	// addresses (violating the (chain, address) unique constraint).
	NextAddressIndex(ctx context.Context, chain string, change int) (int, error)
	IncrementReceiveCount(ctx context.Context, id uuid.UUID) error
	GetAddress(ctx context.Context, id uuid.UUID) (*domain.Address, error)

	// Balances
	UpsertBalance(ctx context.Context, b *Balance) error
	GetBalance(ctx context.Context, walletID uuid.UUID, asset string) (*Balance, error)
	ListBalances(ctx context.Context, walletID uuid.UUID) ([]*Balance, error)
	RecordBalanceEvent(ctx context.Context, e *BalanceEvent) error

	// UTXOs
	InsertUTXO(ctx context.Context, u *UTXO) error
	ListFreeUTXOs(ctx context.Context, walletID uuid.UUID) ([]*UTXO, error)
	LockUTXOs(ctx context.Context, outpoints []string) error
	MarkUTXOsSpent(ctx context.Context, outpoints []string, txHash string) error
	RestoreUTXOs(ctx context.Context, outpoints []string) error
	PruneUTXOs(ctx context.Context, outpoints []string) error

	// Nonces
	GetNonce(ctx context.Context, walletID uuid.UUID, chain string) (*Nonce, error)
	UpsertNonce(ctx context.Context, n *Nonce) error
	IncrementPendingNonce(ctx context.Context, walletID uuid.UUID, chain string) (int64, int, error)
	AdvanceBroadcastNonce(ctx context.Context, walletID uuid.UUID, chain string, nonce int64) error
	// RollbackPendingNonce conditionally decrements pending_nonce back to
	// `to` only when the current pending_nonce equals `to+1` (i.e. the rolled-
	// back value was the most recently reserved). If a higher nonce has
	// already been reserved in the meantime, the rollback is a no-op: the
	// gap will be filled by the chain's mempool replacement policy. This
	// returns the number of rows affected (1 if rolled back, 0 otherwise).
	RollbackPendingNonce(ctx context.Context, walletID uuid.UUID, chain string, to int64) (int64, error)

	// Withdrawals
	CreateWithdrawal(ctx context.Context, w *WithdrawalRequest) error
	GetWithdrawal(ctx context.Context, id uuid.UUID) (*WithdrawalRequest, error)
	ListWithdrawals(ctx context.Context, walletID uuid.UUID, state string) ([]*WithdrawalRequest, error)
	UpdateWithdrawalState(ctx context.Context, id uuid.UUID, state string, reason string, txHash string, policyDecisionID string) error
	UpdateWithdrawalNonce(ctx context.Context, id uuid.UUID, nonce int64) error
	// UpdateWithdrawalOutpoints persists the reserved UTXO outpoints on the
	// withdrawal row so a restart can detect double-spends.
	UpdateWithdrawalOutpoints(ctx context.Context, id uuid.UUID, outpoints []string) error
	// UpdateWithdrawalSignedTx persists the assembled signed transaction
	// bytes produced by MPC signing so Broadcast can submit the real bytes.
	UpdateWithdrawalSignedTx(ctx context.Context, id uuid.UUID, txBytes []byte) error

	// Key mappings
	BindKeyMapping(ctx context.Context, m *KeyMapping) error
	ResolveActiveKey(ctx context.Context, walletID uuid.UUID) ([]*KeyMapping, error)
	RotateKeyMapping(ctx context.Context, walletID uuid.UUID, newKeyID string, coolingPeriod time.Duration) error
	ExpireCooling(ctx context.Context) error

	// Funding requests
	CreateFundingRequest(ctx context.Context, f *FundingRequest) error
	GetOpenFundingRequest(ctx context.Context, walletID uuid.UUID, asset string) (*FundingRequest, error)
	ListFundingRequests(ctx context.Context, walletID uuid.UUID, state string) ([]*FundingRequest, error)
	UpdateFundingState(ctx context.Context, id uuid.UUID, state string, treasuryBatchID string) error

	// Audit outbox
	AppendAuditEvent(ctx context.Context, e *AuditOutboxEvent) error
	ListUndeliveredAuditEvents(ctx context.Context, limit int) ([]*AuditOutboxEvent, error)
	MarkAuditDelivered(ctx context.Context, id uuid.UUID) error
	NextAuditSeq(ctx context.Context, walletID uuid.UUID) (int64, error)
}

// SQLStore extends Store with direct DB access for migration helpers.
type SQLStore interface {
	Store
	DB() *sql.DB
	ApplyMigrations(ctx context.Context, ddl string) error
}
