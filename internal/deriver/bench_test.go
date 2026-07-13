package deriver

// Load-test skeleton for the Stage 10 throughput targets: derivation must
// sustain >= 500 ops/s. Run with `go test -bench . ./internal/deriver`.

import (
	"context"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
)

func BenchmarkEVMDeriveWarmCache(b *testing.B) {
	d, err := NewEVM(evmXpub, newCache(), time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	wID := uuid.New()
	if _, err := d.DeriveAt(ctx, wID, ChainEthereum, 0, 0); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.DeriveAt(ctx, wID, ChainEthereum, 0, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEVMDeriveColdCache(b *testing.B) {
	d, err := NewEVM(evmXpub, newCache(), time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	wID := uuid.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Distinct indexes so every iteration takes the uncached path.
		if _, err := d.DeriveAt(ctx, wID, ChainEthereum, i, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBTCDerive(b *testing.B) {
	d, err := NewBTC(btcXpub, &chaincfg.MainNetParams, newCache(), time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	wID := uuid.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.DeriveAt(ctx, wID, ChainBitcoin, i, 0); err != nil {
			b.Fatal(err)
		}
	}
}
