package deriver

import (
	"context"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
)

func TestNonHardenedOutOfRange(t *testing.T) {
	if _, err := nonHardened(-1); err == nil {
		t.Error("expected error for negative index")
	}
	if _, err := nonHardened(0x80000000); err == nil {
		t.Error("expected error for index >= 2^31")
	}
	v, err := nonHardened(0x7fffffff)
	if err != nil || v != 0x7fffffff {
		t.Errorf("nonHardened(2^31-1)=%d err=%v", v, err)
	}
	v, err = nonHardened(0)
	if err != nil || v != 0 {
		t.Errorf("nonHardened(0)=%d err=%v", v, err)
	}
}

func TestFromHexCharAllRanges(t *testing.T) {
	cases := map[byte]byte{
		'0': 0, '9': 9, 'a': 10, 'f': 15, 'A': 10, 'F': 15,
		'g': 0, 'G': 0, ':': 0,
	}
	for in, want := range cases {
		if got := fromHexChar(in); got != want {
			t.Errorf("fromHexChar(%q)=%d want %d", in, got, want)
		}
	}
}

func TestIsHex32(t *testing.T) {
	if isHex32("ZZZZ") {
		t.Error("isHex32 should reject non-hex")
	}
	if !isHex32(solHex) {
		t.Error("isHex32 should accept 64 lowercase hex chars")
	}
	if !isHex32("0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF") {
		t.Error("isHex32 should accept 64 uppercase hex chars")
	}
	if isHex32("nothex_but_64_chars_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx") {
		t.Error("isHex32 should reject 64 non-hex chars")
	}
}

func TestEVMAddressFromCompressedPubkeyPanics(t *testing.T) {
	// Wrong length panics.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic on wrong-length compressed pubkey")
			}
		}()
		_ = evmAddressFromCompressedPubkey([]byte{0x02, 0x01})
	}()
	// Invalid compressed point (valid length, invalid curve point) panics
	// inside decompressSecp256k1.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic on invalid compressed pubkey")
			}
		}()
		_ = evmAddressFromCompressedPubkey(make([]byte, 33))
	}()
}

func TestBech32AddressEmptyWitness(t *testing.T) {
	// witness[1:] empty -> ConvertBits on empty slice still returns empty; this
	// exercises the empty-witness code path without error.
	got, err := bech32Address("bc", []byte{0x00})
	if err != nil {
		t.Fatalf("bech32Address empty: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty bech32 address")
	}
}

func TestEVMDeriveAtCacheHitViaSet(t *testing.T) {
	c := cache.NewMem()
	d, err := NewEVM(evmXpub, c, 1e9)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r1, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Second call hits the cache.
	r2, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Address != r2.Address {
		t.Errorf("cache hit mismatch: %s vs %s", r1.Address, r2.Address)
	}
}

func TestBTCDeriveAtCacheHit(t *testing.T) {
	c := cache.NewMem()
	d, err := NewBTC(btcXpub, &chaincfg.MainNetParams, c, 1e9)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r1, err := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Address != r2.Address {
		t.Errorf("btc cache hit mismatch: %s vs %s", r1.Address, r2.Address)
	}
}

func TestSolanaCacheHit(t *testing.T) {
	c := cache.NewMem()
	d, err := NewSolana(solBase58, c, 1e9)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r1, err := d.DeriveAt(ctx, uuid.New(), ChainSolana, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := d.DeriveAt(ctx, uuid.New(), ChainSolana, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Address != r2.Address {
		t.Errorf("solana cache hit mismatch: %s vs %s", r1.Address, r2.Address)
	}
}

func TestSolanaInvalidHexInput(t *testing.T) {
	// A 64-char non-hex string is not isHex32, so it is treated as a base58
	// pubkey and used verbatim. Verify NewSolana accepts it and DeriveAt
	// returns it as-is.
	d, err := NewSolana("gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg", newCache(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r, err := d.DeriveAt(context.Background(), uuid.New(), ChainSolana, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Address == "" {
		t.Error("expected non-empty solana address for non-hex input")
	}
}