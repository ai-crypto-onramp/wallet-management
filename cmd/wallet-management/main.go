// Command wallet-management is the main entrypoint for the wallet-management
// service. It wires the storage, derivers, services, REST + gRPC servers, and
// the audit outbox drainer.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/api/grpc"
	restapi "github.com/ai-crypto-onramp/wallet-management/internal/api/rest"
	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/cache"
	"github.com/ai-crypto-onramp/wallet-management/internal/clients"
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
	devMode := os.Getenv("DEV_MODE") == "1"

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

	c, lk := initCacheAndLocker(ctx, cfg, devMode)
	registry, err := buildDeriverRegistry(cfg, c, devMode)
	if err != nil {
		return err
	}

	auditSink := initAuditSink(devMode)
	emitter := audit.NewEmitter(st, auditSink)
	emitter.Start(ctx, 5*time.Second)
	defer emitter.Stop()
	if ks, ok := auditSink.(*audit.KafkaSink); ok {
		defer ks.Close()
	}

	walletSvc := wallet.NewService(st, registry, lk, emitter, cfg)
	balanceSvc := balance.NewService(st, emitter, cfg)
	utxoSvc := utxo.NewService(st)
	nonceSvc := nonce.NewService(st, lk)
	keymapSvc := keymapping.NewService(st, emitter, cfg)
	treasuryClient := funding.NewHTTPClient(cfg.TreasuryOrchestrationURL)
	fundingSvc := funding.NewService(st, balanceSvc, treasuryClient, emitter, cfg)
	policyClient := policy.NewHTTPClient(cfg.PolicyRiskEngineURL)

	signer, gw, err := initSignerAndGateway(cfg, devMode)
	if err != nil {
		return err
	}
	if cs, ok := signer.(*clients.MPCSigningClient); ok {
		defer cs.Close()
	}
	if cg, ok := gw.(*clients.GatewayClient); ok {
		defer cg.Close()
	}

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

func initCacheAndLocker(ctx context.Context, cfg config.Config, devMode bool) (cache.Cache, lock.Locker) {
	rdb, redisErr := redis.ParseURL(cfg.RedisURL)
	if redisErr == nil {
		rc := redis.NewClient(rdb)
		if pingErr := rc.Ping(ctx).Err(); pingErr == nil {
			c := cache.NewRedisFromClient(rc, cfg.DerivationCacheTTL)
			lk := lock.NewRedisLocker(rc)
			log.Printf("connected to redis at %s", cfg.RedisURL)
			return c, lk
		}
	}
	if !devMode {
		log.Printf("redis unavailable; using in-memory cache and locker (warning: not safe for production)")
	} else {
		log.Printf("DEV_MODE=1: redis unavailable; using in-memory cache and locker (NOT FOR PRODUCTION)")
	}
	return cache.NewMem(), lock.NewMemLocker()
}

func buildDeriverRegistry(cfg config.Config, c cache.Cache, devMode bool) (*deriver.Registry, error) {
	xpubEnv := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			if devMode {
				log.Printf("DEV_MODE=1: %s unset — using placeholder (NOT FOR PRODUCTION)", name)
				return "xpub-placeholder"
			}
			log.Fatalf("%s required in production mode; real secrets store integration not yet wired — set DEV_MODE=1 for local dev", name)
		}
		return v
	}
	evm, err := deriver.NewEVM(xpubEnv("EVM_XPUB"), c, cfg.DerivationCacheTTL)
	if err != nil {
		return nil, err
	}
	sol, err := deriver.NewSolana(xpubEnv("SOL_XPUB"), c, cfg.DerivationCacheTTL)
	if err != nil {
		return nil, err
	}
	btc, err := deriver.NewBTC(xpubEnv("BTC_XPUB"), &chaincfg.MainNetParams, c, cfg.DerivationCacheTTL)
	if err != nil {
		return nil, err
	}
	return deriver.NewRegistry(evm, sol, btc), nil
}

func initAuditSink(devMode bool) audit.Sink {
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		sink := audit.NewKafkaSink(splitCSV(brokers))
		log.Printf("audit sink: kafka (%s), topic %s", brokers, audit.AuditTopic)
		return sink
	}
	if devMode {
		log.Printf("warn: KAFKA_BROKERS unset and DEV_MODE=1; audit outbox will be drained but events dropped")
		return nil
	}
	log.Fatalf("KAFKA_BROKERS unset and DEV_MODE not set; cannot start audit producer")
	return nil
}

func initSignerAndGateway(cfg config.Config, devMode bool) (grpcclient.MPCSigner, grpcclient.GatewayClient, error) {
	if devMode {
		log.Printf("DEV_MODE=1: using MockMPCSigner / MockGatewayClient (NOT FOR PRODUCTION)")
		return &grpcclient.MockMPCSigner{}, &grpcclient.MockGatewayClient{}, nil
	}
	if cfg.MPCSigningURL == "" {
		log.Fatalf("MPC_SIGNING_URL required in production mode — set DEV_MODE=1 for local dev")
	}
	if cfg.BlockchainGatewayURL == "" {
		log.Fatalf("BLOCKCHAIN_GATEWAY_URL required in production mode — set DEV_MODE=1 for local dev")
	}
	realSigner, err := clients.NewMPCSigningClient(cfg.MPCSigningURL)
	if err != nil {
		return nil, nil, fmt.Errorf("dial mpc-signing-service %q: %w", cfg.MPCSigningURL, err)
	}
	realGw, err := clients.NewGatewayClient(cfg.BlockchainGatewayURL)
	if err != nil {
		realSigner.Close()
		return nil, nil, fmt.Errorf("dial blockchain-gateway %q: %w", cfg.BlockchainGatewayURL, err)
	}
	return realSigner, realGw, nil
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}