// Package deriver implements deterministic, public-only BIP-44 address
// derivation for EVM chains, Solana, and Bitcoin.
//
// The derivers accept an extended public key (account-level xpub) as input so
// the service never holds private key material. The address/index derivation is
// deterministic given the xpub and the index.
//
// This package does NOT depend on the wallet package to avoid an import cycle;
// it uses plain Chain string types and its own Result struct. The wallet
// service converts Results into wallet.Address values.
package deriver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Chain is a supported blockchain identifier (mirrors wallet.Chain).
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

// Result is the output of a derivation.
type Result struct {
	Address        string
	DerivationPath string
	Index          int
	Change         int
}

// Deriver derives receive addresses for a given chain.
type Deriver interface {
	DeriveNext(ctx context.Context, walletID uuid.UUID, chain Chain, change int) (Result, error)
	DeriveAt(ctx context.Context, walletID uuid.UUID, chain Chain, index, change int) (Result, error)
}

// Registry routes a chain to the appropriate Deriver.
type Registry struct {
	evm    *EVMDeriver
	solana *SolanaDeriver
	btc    *BTCDeriver
}

// NewRegistry builds a Registry from the per-chain derivers.
func NewRegistry(evm *EVMDeriver, sol *SolanaDeriver, btc *BTCDeriver) *Registry {
	return &Registry{evm: evm, solana: sol, btc: btc}
}

// For returns the deriver for a chain.
func (r *Registry) For(chain Chain) (Deriver, error) {
	switch {
	case chain.IsEVM():
		return r.evm, nil
	case chain == ChainSolana:
		return r.solana, nil
	case chain == ChainBitcoin:
		return r.btc, nil
	}
	return nil, fmt.Errorf("no deriver for chain %q", chain)
}

// nonHardened converts a derivation index to uint32, rejecting values outside
// the non-hardened BIP-32 range [0, 2^31).
func nonHardened(n int) (uint32, error) {
	if n < 0 || n >= 0x80000000 {
		return 0, fmt.Errorf("derivation index %d out of non-hardened range", n)
	}
	return uint32(n), nil
}

// ParseXpub validates an xpub/zpub string is non-empty.
func ParseXpub(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty xpub")
	}
	return s, nil
}