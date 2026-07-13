// Command wallet-management is the main entrypoint for the wallet-management
// service. It wires the storage, derivers, services, REST + gRPC servers, and
// the audit outbox drainer.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/api/grpc"
	restapi "github.com/ai-crypto-onramp/wallet-management/internal/api/rest"
	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/deriver"
	"github.com/ai-crypto-onramp/wallet-management/internal/funding"
	grpcclient "github.com/ai-crypto-onramp/wallet-management/internal/grpcclient"
	"github.com/ai-crypto-onramp/wallet-management/internal/keymapping"
	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/migrations"
	"github.com/ai-crypto-onramp/wallet-management/internal/nonce"
	"github.com/ai-crypto-onramp/wallet-management/internal/policy"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/postgres"
	"github.com/ai-crypto-onramp/wallet-management/internal/utxo"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/ai-crypto-onramp/wallet-management/internal/withdrawal"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("wallet-management: %v", err)
	}
}

func run() error {
	cfg := config.FromEnv()

	st, err := postgres.New(cfg.DBURL)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := migrations.Up(ctx, st.DB()); err != nil {
		log.Printf("warn: migrations: %v (continuing)", err)
	}

	// Redis (optional; fall back to in-memory if unreachable).
	var c cache.Cache
	var lk lock.Locker
	rdb, redisErr := redis.ParseURL(cfg.RedisURL)
	if redisErr == nil {
		rc := redis.NewClient(rdb)
		if pingErr := rc.Ping(ctx).Err(); pingErr == nil {
			c = cache.NewRedisFromClient(rc, cfg.DerivationCacheTTL)
			lk = lock.NewRedisLocker(rc)
			log.Printf("connected to redis at %s", cfg.RedisURL)
		}
	}
	if c == nil {
		log.Printf("redis unavailable; using in-memory cache and locker")
		c = cache.NewMem()
		lk = lock.NewMemLocker()
	}

	// Derivers. In production the xpubs would be loaded from a secrets store
	// per chain; here we read from env so the binary is self-contained.
	evm, err := deriver.NewEVM(envOr("EVM_XPUB", "xpub-placeholder"), c, cfg.DerivationCacheTTL)
	if err != nil {
		return err
	}
	sol, err := deriver.NewSolana(envOr("SOL_XPUB", "xpub-placeholder"), c, cfg.DerivationCacheTTL)
	if err != nil {
		return err
	}
	btc, err := deriver.NewBTC(envOr("BTC_XPUB", "xpub-placeholder"), &chaincfg.MainNetParams, c, cfg.DerivationCacheTTL)
	if err != nil {
		return err
	}
	registry := deriver.NewRegistry(evm, sol, btc)

	emitter := audit.NewEmitter(st, nil)
	emitter.Start(ctx, 5*time.Second)
	defer emitter.Stop()

	walletSvc := wallet.NewService(st, registry, lk, emitter, cfg)
	balanceSvc := balance.NewService(st, emitter, cfg)
	utxoSvc := utxo.NewService(st)
	nonceSvc := nonce.NewService(st, lk)
	keymapSvc := keymapping.NewService(st, emitter, cfg)
	treasuryClient := funding.NewHTTPClient(cfg.TreasuryOrchestrationURL)
	fundingSvc := funding.NewService(st, balanceSvc, treasuryClient, emitter, cfg)
	policyClient := policy.NewHTTPClient(cfg.PolicyRiskEngineURL)
	signer := &grpcclient.MockMPCSigner{}
	gw := &grpcclient.MockGatewayClient{}

	withdrawalSvc := withdrawal.NewService(st, walletSvc, nonceSvc, utxoSvc, policyClient, signer, gw, keymapSvc, emitter)
	balanceSvc.UTXORestore = utxoSvc.RestoreOnReorg
	balanceSvc.OnConfirmedDecrease = func(walletID uuid.UUID, asset string) {
		go func() {
			fctx, fcancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer fcancel()
			if err := fundingSvc.EvaluateAndRequest(fctx, walletID, asset); err != nil {
				log.Printf("funding evaluation for wallet %s asset %s: %v", walletID, asset, err)
			}
		}()
	}

	restSrv := restapi.NewServer(":"+cfg.Port, restapi.Deps{
		Wallets:    walletSvc,
		Balances:   balanceSvc,
		Funding:    fundingSvc,
		Withdrawal: withdrawalSvc,
	})
	grpcSrv := grpcserver.NewServer(grpcserver.Deps{
		KeyMappings: keymapSvc,
		Balances:    balanceSvc,
		Withdrawals: withdrawalSvc,
	})

	errCh := make(chan error, 2)
	go func() { errCh <- restSrv.Start() }()
	go func() { errCh <- grpcSrv.Start(":" + cfg.GRPCPort) }()
	log.Printf("wallet-management REST on :%s, gRPC on :%s", cfg.Port, cfg.GRPCPort)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("received signal %s, shutting down", sig)
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		_ = restSrv.Shutdown(shutCtx)
		grpcSrv.Stop()
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}