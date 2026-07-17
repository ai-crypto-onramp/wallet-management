// Package domain holds the shared domain types for wallet-management.
// It is imported by storage, wallet, audit, balance, etc. to avoid import
// cycles between them.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Chain is a supported blockchain identifier.
type Chain string

const (
	ChainEthereum Chain = "ethereum"
	ChainPolygon  Chain = "polygon"
	ChainArbitrum Chain = "arbitrum"
	ChainBase     Chain = "base"
	ChainOptimism Chain = "optimism"
	ChainSolana   Chain = "solana"
	ChainBitcoin  Chain = "bitcoin"
)

// IsEVM reports whether the chain uses the EVM derivation scheme.
func (c Chain) IsEVM() bool {
	switch c {
	case ChainEthereum, ChainPolygon, ChainArbitrum, ChainBase, ChainOptimism:
		return true
	}
	return false
}

// WalletType classifies the custody tier of a wallet.
type WalletType string

const (
	WalletTypeHot  WalletType = "HOT"
	WalletTypeWarm WalletType = "WARM"
	WalletTypeCold WalletType = "COLD"
)

// WalletState is the operational lifecycle state of a wallet.
type WalletState string

const (
	WalletStateActive  WalletState = "ACTIVE"
	WalletStatePaused  WalletState = "PAUSED"
	WalletStateRetired WalletState = "RETIRED"
)

// AddressState describes whether a derived address is eligible to receive funds.
type AddressState string

const (
	AddressStateActive     AddressState = "ACTIVE"
	AddressStateDeprecated AddressState = "DEPRECATED"
)

// Wallet is the custody inventory record.
type Wallet struct {
	ID                    uuid.UUID    `json:"id"`
	Chain                 Chain        `json:"chain"`
	Type                  WalletType   `json:"type"`
	Label                 string       `json:"label"`
	State                 WalletState  `json:"state"`
	KeyID                 string       `json:"key_id"`
	CustodianRef          string       `json:"custodian_ref"`
	RotationDays          *int         `json:"rotation_days,omitempty"`
	RotationAfterReceives *int         `json:"rotation_after_receives,omitempty"`
	CreatedAt             time.Time    `json:"created_at"`
	UpdatedAt             time.Time    `json:"updated_at"`
}

// Address is a derived receive address bound to a wallet.
type Address struct {
	ID             uuid.UUID    `json:"id"`
	WalletID       uuid.UUID    `json:"wallet_id"`
	Chain          Chain        `json:"chain"`
	Address        string       `json:"address"`
	DerivationPath string       `json:"derivation_path"`
	Index          int          `json:"index"`
	Change         int          `json:"change"`
	State          AddressState `json:"state"`
	ReceiveCount   int          `json:"receive_count"`
	CreatedAt      time.Time    `json:"created_at"`
}

// CreateRequest is the payload for creating a new wallet.
type CreateRequest struct {
	Chain Chain      `json:"chain"`
	Type  WalletType `json:"type"`
	Label string     `json:"label"`
	KeyID string     `json:"key_id,omitempty"`
}

// ValidChain reports whether c is a supported chain.
func ValidChain(c Chain) bool {
	switch c {
	case ChainEthereum, ChainPolygon, ChainArbitrum, ChainBase, ChainOptimism, ChainSolana, ChainBitcoin:
		return true
	}
	return false
}

// ValidWalletType reports whether t is a supported wallet type.
func ValidWalletType(t WalletType) bool {
	switch t {
	case WalletTypeHot, WalletTypeWarm, WalletTypeCold:
		return true
	}
	return false
}

// ValidWalletState reports whether s is a supported wallet state.
func ValidWalletState(s WalletState) bool {
	switch s {
	case WalletStateActive, WalletStatePaused, WalletStateRetired:
		return true
	}
	return false
}

// ErrWalletRetired is returned when an operation is attempted on a retired wallet.
var ErrWalletRetired = errWalletRetired{}

type errWalletRetired struct{}

func (errWalletRetired) Error() string { return "wallet is retired" }