package deriver

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
)

// BTCDeriver derives native SegWit (BIP-84) bech32 Bitcoin addresses from an
// account-level extended public key.
type BTCDeriver struct {
	xpub  string
	net   *chaincfg.Params
	cache cache.Cache
	ttl   time.Duration
}

// NewBTC creates a BTC deriver. For mainnet use &chaincfg.MainNetParams; for
// testnet use &chaincfg.TestNet3Params.
func NewBTC(xpub string, net *chaincfg.Params, c cache.Cache, ttl time.Duration) (*BTCDeriver, error) {
	x, err := ParseXpub(xpub)
	if err != nil {
		return nil, err
	}
	if net == nil {
		net = &chaincfg.MainNetParams
	}
	return &BTCDeriver{xpub: x, net: net, cache: c, ttl: ttl}, nil
}

// DeriveNext derives the next address for the given change chain (0=receive,1=change).
func (d *BTCDeriver) DeriveNext(ctx context.Context, walletID uuid.UUID, chain Chain, change int) (Result, error) {
	if chain != ChainBitcoin {
		return Result{}, fmt.Errorf("non-BTC chain %q passed to BTCDeriver", chain)
	}
	if change != 0 && change != 1 {
		return Result{}, fmt.Errorf("BTC change chain must be 0 or 1")
	}
	return Result{}, fmt.Errorf("DeriveNext requires an index for BTC; use DeriveAt")
}

// DeriveAt derives the bech32 native SegWit address at the given index/change.
func (d *BTCDeriver) DeriveAt(ctx context.Context, walletID uuid.UUID, chain Chain, index, change int) (Result, error) {
	if chain != ChainBitcoin {
		return Result{}, fmt.Errorf("non-BTC chain %q passed to BTCDeriver", chain)
	}
	if change != 0 && change != 1 {
		return Result{}, fmt.Errorf("BTC change chain must be 0 or 1")
	}
	if index < 0 {
		return Result{}, fmt.Errorf("BTC index must be >= 0")
	}
	path := fmt.Sprintf("m/84'/0'/0'/%d/%d", change, index)
	cacheKey := fmt.Sprintf("deriv:btc:%s:%d:%d", d.xpub, change, index)
	if v, ok, err := d.cache.Get(ctx, cacheKey); err == nil && ok {
		return d.makeAddr(walletID, v, path, index, change), nil
	}
	addr, err := d.deriveUncached(index, change)
	if err != nil {
		return Result{}, err
	}
	_ = d.cache.Set(ctx, cacheKey, addr, d.ttl)
	return d.makeAddr(walletID, addr, path, index, change), nil
}

func (d *BTCDeriver) makeAddr(_ uuid.UUID, addr, path string, index, change int) Result {
	return Result{
		Address:        addr,
		DerivationPath: path,
		Index:          index,
		Change:         change,
	}
}

func (d *BTCDeriver) deriveUncached(index, change int) (string, error) {
	acc, err := hdkeychain.NewKeyFromString(d.xpub)
	if err != nil {
		return "", fmt.Errorf("parse xpub: %w", err)
	}
	changeBranch, err := acc.Derive(uint32(change))
	if err != nil {
		return "", fmt.Errorf("derive change chain %d: %w", change, err)
	}
	child, err := changeBranch.Derive(uint32(index))
	if err != nil {
		return "", fmt.Errorf("derive index %d: %w", index, err)
	}
	pub, err := child.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("ec pubkey: %w", err)
	}
	witness := append([]byte{0x00}, btcutil.Hash160(pub.SerializeCompressed())...)
	return bech32Address("bc", witness)
}

// bech32Address converts a 21-byte witness program (version+20-byte hash) into
// a bech32 SegWit address with the given human-readable part.
func bech32Address(hrp string, witness []byte) (string, error) {
	conv, err := bech32.ConvertBits(witness[1:], 8, 5, true)
	if err != nil {
		return "", err
	}
	return bech32.Encode(hrp, append([]byte{witness[0]}, conv...))
}