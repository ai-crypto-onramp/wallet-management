package deriver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
)

// Vectors below are derived from the canonical BIP-32 test seed
// 000102030405060708090a0b0c0d0e0f (https://github.com/bitcoin/bips/blob/master/bip-0032.mediawiki#test-vectors).
// The account-level xpubs and the derived addresses are deterministic and reproducible.

const (
	// m/44'/60'/0' account xpub derived from the BIP-32 test seed.
	evmXpub = "xpub6CeDpm2b5qtk96oy8yvM572W6cLZSvU5vnpKmKPypbfFwXo86SyT7VtfwWtMZAgZ5eKVMU9NnULt91HBFw9j62wJrcoc1ZRWiNvoorwBRXL"
	// m/84'/0'/0' account xpub (BIP-84 native SegWit) derived from the same seed.
	btcXpub = "xpub6C1HVMz946r433QEjZGpYYWYcspxXXBPys5PBGkmQboRXE6RLfFiStEkKbWKCZaPgDrzZh9nUEunxuiuy6MNdw23du2Ek7GoKYMJVH8eK5E"
	// A 32-byte ed25519 pubkey for Solana. The base58 encoding of 32 zero bytes
	// is "11111111111111111111111111111111" (32 ones); the hex form is 64 zeros.
	solHex    = "0000000000000000000000000000000000000000000000000000000000000000"
	solBase58 = "11111111111111111111111111111111"
)

func newCache() cache.Cache { return cache.NewMem() }

func TestParseXpub(t *testing.T) {
	t.Parallel()
	if _, err := ParseXpub("  "); err == nil {
		t.Error("expected error on empty xpub")
	}
	if v, err := ParseXpub("  abc  "); err != nil || v != "abc" {
		t.Errorf("expected trimmed abc, got %q err=%v", v, err)
	}
}

func TestRegistryFor(t *testing.T) {
	t.Parallel()
	evm, err := NewEVM(evmXpub, newCache(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sol, err := NewSolana(solBase58, newCache(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	btc, err := NewBTC(btcXpub, &chaincfg.MainNetParams, newCache(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(evm, sol, btc)
	for _, c := range []Chain{ChainEthereum, ChainPolygon, ChainArbitrum, ChainBase, ChainOptimism} {
		d, err := reg.For(c)
		if err != nil {
			t.Errorf("For(%s) err: %v", c, err)
		}
		if d == nil {
			t.Errorf("expected deriver for %s", c)
		}
	}
	if _, err := reg.For(Chain("cardano")); err == nil {
		t.Error("expected error for unsupported chain")
	}
}

func TestNewEVMEmpty(t *testing.T) {
	if _, err := NewEVM("", newCache(), time.Minute); err == nil {
		t.Error("expected error on empty xpub")
	}
}

func TestEVMDeriveKnownVectors(t *testing.T) {
	t.Parallel()
	d, err := NewEVM(evmXpub, newCache(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want := []string{
		"0x022b971dFF0C43305e691DEd7a14367AF19D6407",
		"0xbb7A182240010703dc81D6b1EFf630CA02a169FD",
		"0xECf722a6a8EE18F5A9D3C00D168be3D0d068732b",
	}
	for i, w := range want {
		got, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, i, 0)
		if err != nil {
			t.Fatalf("index %d: %v", i, err)
		}
		if got.Address != w {
			t.Errorf("EVM[%d]: expected %s got %s", i, w, got.Address)
		}
		wantPath := "m/44'/60'/0'/0/" + itoa(i)
		if got.DerivationPath != wantPath {
			t.Errorf("path: expected %s got %s", wantPath, got.DerivationPath)
		}
		if got.Index != i || got.Change != 0 {
			t.Errorf("index/change mismatch: %+v", got)
		}
		// EIP-55: re-applying the checksum to the address must be a no-op.
		if cs := eip55Checksum(got.Address); cs != got.Address {
			t.Errorf("EVM[%d]: checksum not canonical: %s vs %s", i, got.Address, cs)
		}
		// public-only: no private key bytes appear in result (just sanity check on Address string)
		if strings.Contains(got.Address, "priv") {
			t.Error("address contains suspicious substring")
		}
	}
}

func TestEIP55CanonicalVectors(t *testing.T) {
	t.Parallel()
	// Test vectors from EIP-55 itself.
	for _, want := range []string{
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
		"0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB",
		"0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
	} {
		if got := eip55Checksum(strings.ToLower(want)); got != want {
			t.Errorf("eip55Checksum: expected %s got %s", want, got)
		}
	}
}

func TestEVMCacheHit(t *testing.T) {
	d, err := NewEVM(evmXpub, newCache(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r1, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Address != r2.Address {
		t.Errorf("cache hit mismatch: %s vs %s", r1.Address, r2.Address)
	}
}

func TestEVMErrors(t *testing.T) {
	d, err := NewEVM(evmXpub, newCache(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := d.DeriveNext(ctx, uuid.New(), ChainEthereum, 1); err == nil {
		t.Error("expected error on non-zero change for EVM")
	}
	if _, err := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, 0, 0); err == nil {
		t.Error("expected error on non-EVM chain")
	}
	if _, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, 0, 1); err == nil {
		t.Error("expected error on change=1 for EVM")
	}
	if _, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, -1, 0); err == nil {
		t.Error("expected error on negative index")
	}
}

func TestSolanaDerive(t *testing.T) {
	t.Parallel()
	d, err := NewSolana(solBase58, newCache(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := d.DeriveAt(ctx, uuid.New(), ChainSolana, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Address != solBase58 {
		t.Errorf("expected %s got %s", solBase58, r.Address)
	}
	if r.DerivationPath != "m/44'/501'/0'/0'" {
		t.Errorf("unexpected path: %s", r.DerivationPath)
	}
	// re-derive returns the same single address
	r2, _ := d.DeriveNext(ctx, uuid.New(), ChainSolana, 0)
	if r2.Address != r.Address {
		t.Errorf("expected same solana address, got %s vs %s", r.Address, r2.Address)
	}
	// index/change ignored
	r3, _ := d.DeriveAt(ctx, uuid.New(), ChainSolana, 99, 1)
	if r3.Address != r.Address {
		t.Error("solana should ignore index/change")
	}
}

func TestSolanaHexInput(t *testing.T) {
	d, err := NewSolana(solHex, newCache(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := d.DeriveAt(context.Background(), uuid.New(), ChainSolana, 0, 0)
	if r.Address != solBase58 {
		t.Errorf("expected hex->base58 conversion to %s, got %s", solBase58, r.Address)
	}
}

func TestSolanaNonSolanaChain(t *testing.T) {
	d, err := NewSolana(solBase58, newCache(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeriveAt(context.Background(), uuid.New(), ChainEthereum, 0, 0); err == nil {
		t.Error("expected error on non-solana chain")
	}
}

func TestNewSolanaEmpty(t *testing.T) {
	if _, err := NewSolana("", newCache(), time.Minute); err == nil {
		t.Error("expected error on empty pubkey")
	}
}

func TestBTCDeriveKnownVectors(t *testing.T) {
	t.Parallel()
	d, err := NewBTC(btcXpub, &chaincfg.MainNetParams, newCache(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cases := []struct {
		change, index int
		want          string
	}{
		{0, 0, "bc1qpux3z758ulsxg69eptaakukraanqwtdxe5yy4c"},
		{0, 1, "bc1qytr8s7skf86x7ccl6wctal9hqrartu085r9mr5"},
		{1, 0, "bc1qcr25el7jpevd40duq22znc4hg3p5vs0y965ay8"},
		{1, 1, "bc1qstsv22jacyptlgle5rv7ew9ymht8v5a5fqt8ta"},
	}
	for _, c := range cases {
		r, err := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, c.index, c.change)
		if err != nil {
			t.Fatalf("BTC[%d/%d]: %v", c.change, c.index, err)
		}
		if r.Address != c.want {
			t.Errorf("BTC[%d/%d]: expected %s got %s", c.change, c.index, c.want, r.Address)
		}
		wantPath := "m/84'/0'/0'/" + itoa(c.change) + "/" + itoa(c.index)
		if r.DerivationPath != wantPath {
			t.Errorf("path: expected %s got %s", wantPath, r.DerivationPath)
		}
		if r.Change != c.change || r.Index != c.index {
			t.Errorf("change/index mismatch: %+v", r)
		}
	}
}

func TestBTCCacheHit(t *testing.T) {
	d, err := NewBTC(btcXpub, &chaincfg.MainNetParams, newCache(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r1, _ := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, 0, 0)
	r2, _ := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, 0, 0)
	if r1.Address != r2.Address {
		t.Errorf("cache hit mismatch: %s vs %s", r1.Address, r2.Address)
	}
}

func TestBTCErrors(t *testing.T) {
	d, err := NewBTC(btcXpub, &chaincfg.MainNetParams, newCache(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := d.DeriveNext(ctx, uuid.New(), ChainEthereum, 0); err == nil {
		t.Error("expected error on non-BTC chain")
	}
	if _, err := d.DeriveNext(ctx, uuid.New(), ChainBitcoin, 2); err == nil {
		t.Error("expected error on invalid change chain")
	}
	if _, err := d.DeriveNext(ctx, uuid.New(), ChainBitcoin, 0); err == nil {
		t.Error("expected error: DeriveNext requires index for BTC")
	}
	if _, err := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, -1, 0); err == nil {
		t.Error("expected error on negative index")
	}
	if _, err := d.DeriveAt(ctx, uuid.New(), ChainEthereum, 0, 0); err == nil {
		t.Error("expected error on non-BTC chain")
	}
	if _, err := d.DeriveAt(ctx, uuid.New(), ChainBitcoin, 0, 2); err == nil {
		t.Error("expected error on invalid change chain")
	}
}

func TestNewBTCEmpty(t *testing.T) {
	if _, err := NewBTC("", &chaincfg.MainNetParams, newCache(), time.Minute); err == nil {
		t.Error("expected error on empty xpub")
	}
}

func TestNewBTCDefaultNet(t *testing.T) {
	d, err := NewBTC(btcXpub, nil, newCache(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		t.Error("expected non-nil deriver with default net")
	}
}

// itoa is a tiny local int->string to avoid importing strconv everywhere.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestSecpDecompressInvalid(t *testing.T) {
	if _, err := decompressSecp256k1([]byte{0x02}); err == nil {
		t.Error("expected error on too-short compressed key")
	}
	// valid length but invalid prefix
	if _, err := decompressSecp256k1(make([]byte, 33)); err == nil {
		t.Error("expected error on invalid compressed key bytes")
	}
}

func TestChainIsEVM(t *testing.T) {
	for _, c := range []Chain{ChainEthereum, ChainPolygon, ChainArbitrum, ChainBase, ChainOptimism} {
		if !c.IsEVM() {
			t.Errorf("%s should be EVM", c)
		}
	}
	if ChainBitcoin.IsEVM() || ChainSolana.IsEVM() {
		t.Error("BTC/Solana should not be EVM")
	}
}