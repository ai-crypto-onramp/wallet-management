package deriver

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/google/uuid"
)

// SolanaDeriver derives a single base58 Solana address per wallet.
//
// Solana uses ed25519, which is not supported by BIP-32 secp256k1 derivation
// libraries. The MPC layer derives the hardened ed25519 keypair at path
// m/44'/501'/0' and provides the 32-byte public key to this service. We accept
// that public key either as a base58 string (the Solana address itself) or as
// a hex-encoded 32-byte pubkey.
//
// One address is derived per wallet; re-derivation returns the same address.
type SolanaDeriver struct {
	pubkeyBase58 string
	cache        cache.Cache
	ttl          time.Duration
}

// NewSolana creates a Solana deriver from the wallet's ed25519 public key.
// The input may be a base58-encoded pubkey (== Solana address) or a hex
// 32-byte pubkey string.
func NewSolana(pubkey string, c cache.Cache, ttl time.Duration) (*SolanaDeriver, error) {
	pubkey, err := ParseXpub(pubkey)
	if err != nil {
		return nil, err
	}
	// If the input is hex, convert to base58.
	addr := pubkey
	if isHex32(pubkey) {
		b := hexDecode32(pubkey)
		addr = base58.Encode(b)
	}
	return &SolanaDeriver{pubkeyBase58: addr, cache: c, ttl: ttl}, nil
}

// DeriveNext always returns the single Solana address for the wallet.
func (d *SolanaDeriver) DeriveNext(ctx context.Context, _ uuid.UUID, chain Chain, change int) (Result, error) {
	return d.DeriveAt(ctx, uuid.Nil, chain, 0, change)
}

// DeriveAt returns the Solana address. index and change are ignored.
func (d *SolanaDeriver) DeriveAt(ctx context.Context, _ uuid.UUID, chain Chain, _, _ int) (Result, error) {
	if chain != ChainSolana {
		return Result{}, fmt.Errorf("non-Solana chain %q passed to SolanaDeriver", chain)
	}
	cacheKey := "deriv:sol:" + d.pubkeyBase58
	if v, ok, err := d.cache.Get(ctx, cacheKey); err == nil && ok {
		return Result{Address: v, DerivationPath: "m/44'/501'/0'/0'", Index: 0, Change: 0}, nil
	}
	_ = d.cache.Set(ctx, cacheKey, d.pubkeyBase58, d.ttl)
	return Result{Address: d.pubkeyBase58, DerivationPath: "m/44'/501'/0'/0'", Index: 0, Change: 0}, nil
}

func isHex32(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func hexDecode32(s string) []byte {
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		hi := fromHexChar(s[2*i])
		lo := fromHexChar(s[2*i+1])
		out[i] = (hi << 4) | lo
	}
	return out
}

func fromHexChar(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}