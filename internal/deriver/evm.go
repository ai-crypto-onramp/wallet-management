package deriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/google/uuid"
	"golang.org/x/crypto/sha3"
)

// EVMDeriver derives EIP-55 checksummed Ethereum-style addresses from an
// account-level BIP-44 extended public key.
type EVMDeriver struct {
	xpub  string
	cache cache.Cache
	ttl   time.Duration
}

// NewEVM creates an EVM deriver from an account-level xpub (m/44'/60'/0').
func NewEVM(xpub string, c cache.Cache, ttl time.Duration) (*EVMDeriver, error) {
	x, err := ParseXpub(xpub)
	if err != nil {
		return nil, err
	}
	return &EVMDeriver{xpub: x, cache: c, ttl: ttl}, nil
}

// DeriveNext derives the next receive address (change=0).
func (d *EVMDeriver) DeriveNext(ctx context.Context, walletID uuid.UUID, chain Chain, change int) (Result, error) {
	if change != 0 {
		return Result{}, fmt.Errorf("EVM deriver does not support change chain")
	}
	return d.DeriveAt(ctx, walletID, chain, -1, change)
}

// DeriveAt derives the address at the given index; if index < 0 it is
// determined from the cache counter (caller should normally pass a known index).
func (d *EVMDeriver) DeriveAt(ctx context.Context, walletID uuid.UUID, chain Chain, index, change int) (Result, error) {
	if !chain.IsEVM() {
		return Result{}, fmt.Errorf("non-EVM chain %q passed to EVMDeriver", chain)
	}
	if change != 0 {
		return Result{}, fmt.Errorf("EVM change chain must be 0")
	}
	if index < 0 {
		return Result{}, fmt.Errorf("EVM index must be >= 0")
	}
	path := fmt.Sprintf("m/44'/60'/0'/0/%d", index)
	cacheKey := fmt.Sprintf("deriv:evm:%s:%d", d.xpub, index)
	if v, ok, err := d.cache.Get(ctx, cacheKey); err == nil && ok {
		return makeResult(walletID, v, path, index, change), nil
	}
	addr, err := d.deriveUncached(index)
	if err != nil {
		return Result{}, err
	}
	_ = d.cache.Set(ctx, cacheKey, addr, d.ttl)
	return makeResult(walletID, addr, path, index, change), nil
}

func makeResult(_ uuid.UUID, addr, path string, index, change int) Result {
	return Result{
		Address:        addr,
		DerivationPath: path,
		Index:          index,
		Change:         change,
	}
}

func (d *EVMDeriver) deriveUncached(index int) (string, error) {
	acc, err := hdkeychain.NewKeyFromString(d.xpub)
	if err != nil {
		return "", fmt.Errorf("parse xpub: %w", err)
	}
	external, err := acc.Derive(0)
	if err != nil {
		return "", fmt.Errorf("derive external chain: %w", err)
	}
	child, err := external.Derive(uint32(index))
	if err != nil {
		return "", fmt.Errorf("derive index %d: %w", index, err)
	}
	pub, err := child.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("ec pubkey: %w", err)
	}
	ecBytes := pub.SerializeCompressed()
	return evmAddressFromCompressedPubkey(ecBytes), nil
}

// evmAddressFromCompressedPubkey computes the EIP-55 checksummed address from a
// compressed secp256k1 public key: decompress, take Keccak-256 of the 64-byte
// uncompressed X||Y (dropping the 0x04 prefix), take the last 20 bytes, then
// apply the EIP-55 checksum.
func evmAddressFromCompressedPubkey(compressed []byte) string {
	if len(compressed) != 33 {
		panic("expected 33-byte compressed pubkey")
	}
	pub, err := decompressSecp256k1(compressed)
	if err != nil {
		panic(err)
	}
	// pub is 65 bytes: 0x04 || X(32) || Y(32). Keccak of X||Y.
	h := sha3.NewLegacyKeccak256()
	h.Write(pub[1:])
	digest := h.Sum(nil)
	addr := digest[12:] // last 20 bytes
	return eip55Checksum(hex.EncodeToString(addr))
}

// eip55Checksum applies the EIP-55 checksum capitalization to a 40-char
// lowercase hex address (without 0x prefix).
func eip55Checksum(addrLower string) string {
	addrLower = strings.ToLower(strings.TrimPrefix(addrLower, "0x"))
	h := sha256.Sum256([]byte(addrLower))
	hh := hex.EncodeToString(h[:])
	var out strings.Builder
	out.Grow(42)
	out.WriteString("0x")
	for i := 0; i < len(addrLower); i++ {
		c := addrLower[i]
		if c >= '0' && c <= '9' {
			out.WriteByte(c)
			continue
		}
		// EIP-55: capitalize if the i-th nibble of the hash is >= 8.
		if hh[i] >= '8' {
			out.WriteByte(c - 32) // lowercase letter -> uppercase
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}