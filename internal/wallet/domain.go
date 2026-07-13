// Package wallet implements the wallet lifecycle service: creation, state
// transitions, and address derivation/rotation orchestration.
//
// Domain types (Wallet, Address, Chain, ...) live in internal/domain to avoid
// import cycles; this package re-exports them for convenience.
package wallet

import (
	"database/sql"
	"fmt"

	"github.com/ai-crypto-onramp/wallet-management/internal/domain"
)

// Re-export domain types so existing call sites compile unchanged.
type (
	Chain         = domain.Chain
	WalletType    = domain.WalletType
	WalletState   = domain.WalletState
	AddressState  = domain.AddressState
	Wallet        = domain.Wallet
	Address       = domain.Address
	CreateRequest = domain.CreateRequest
)

// Re-export constants.
const (
	ChainEthereum = domain.ChainEthereum
	ChainPolygon  = domain.ChainPolygon
	ChainArbitrum = domain.ChainArbitrum
	ChainBase     = domain.ChainBase
	ChainOptimism = domain.ChainOptimism
	ChainSolana   = domain.ChainSolana
	ChainBitcoin  = domain.ChainBitcoin

	WalletTypeHot  = domain.WalletTypeHot
	WalletTypeWarm = domain.WalletTypeWarm
	WalletTypeCold = domain.WalletTypeCold

	WalletStateActive  = domain.WalletStateActive
	WalletStatePaused  = domain.WalletStatePaused
	WalletStateRetired = domain.WalletStateRetired

	AddressStateActive     = domain.AddressStateActive
	AddressStateDeprecated = domain.AddressStateDeprecated
)

// Re-export functions and errors.
var (
	ValidChain         = domain.ValidChain
	ValidWalletType    = domain.ValidWalletType
	ValidWalletState   = domain.ValidWalletState
	ErrWalletRetired   = domain.ErrWalletRetired
	ErrWalletNotFound  = fmt.Errorf("wallet not found: %w", sql.ErrNoRows)
)