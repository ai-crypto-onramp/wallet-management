package balance

// Load-test skeleton for the Stage 10 throughput targets: balance reads must
// sustain >= 2,000 ops/s. Run with `go test -bench . ./internal/balance`.

import (
	"context"
	"testing"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/google/uuid"
)

func BenchmarkGetBalances(b *testing.B) {
	svc, st := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	for _, asset := range []string{"ETH", "USDC", "USDT", "WBTC"} {
		if err := st.UpsertBalance(ctx, &storage.Balance{
			WalletID: wID, Asset: asset, Confirmed: "100", Pending: "1", Locked: "0",
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.GetBalances(ctx, wID); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetBalancesParallel(b *testing.B) {
	svc, st := newSvc(defaultCfg())
	ctx := context.Background()
	wID := uuid.New()
	if err := st.UpsertBalance(ctx, &storage.Balance{
		WalletID: wID, Asset: "ETH", Confirmed: "100", Pending: "1", Locked: "0",
	}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := svc.GetBalances(ctx, wID); err != nil {
				b.Fatal(err)
			}
		}
	})
}
