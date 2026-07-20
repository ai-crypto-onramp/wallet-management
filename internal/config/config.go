package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the wallet-management service.
type Config struct {
	Port                     string
	GRPCPort                 string
	DBURL                    string
	RedisURL                 string
	DefaultRotationDays      int
	ConfirmationsEVM         int
	ConfirmationsBTC         int
	ConfirmationsSOL         string
	MPCSigningURL            string
	BlockchainGatewayURL     string
	TreasuryOrchestrationURL string
	PolicyRiskEngineURL      string
	HotWalletMinBalanceUSD   float64
	DerivationCacheTTL       time.Duration
	LogLevel                 string
	KeyCoolingPeriod         time.Duration
}

// FromEnv reads configuration from environment variables with sane defaults.
func FromEnv() Config {
	return Config{
		Port:                     envOr("PORT", "8080"),
		GRPCPort:                 envOr("GRPC_PORT", "9090"),
		DBURL:                    envOr("DB_URL", "postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable"),
		RedisURL:                 envOr("REDIS_URL", "redis://localhost:6379/0"),
		DefaultRotationDays:      envInt("DEFAULT_ADDRESS_ROTATION_DAYS", 7),
		ConfirmationsEVM:         envInt("CONFIRMATIONS_REQUIRED_EVM", 12),
		ConfirmationsBTC:         envInt("CONFIRMATIONS_REQUIRED_BTC", 6),
		ConfirmationsSOL:         envOr("CONFIRMATIONS_REQUIRED_SOL", "finalized"),
		MPCSigningURL:            envOr("MPC_SIGNING_URL", "dns:///localhost:9091"),
		BlockchainGatewayURL:     envOr("BLOCKCHAIN_GATEWAY_URL", "dns:///localhost:9092"),
		TreasuryOrchestrationURL: envOr("TREASURY_ORCHESTRATION_URL", "http://localhost:8081"),
		PolicyRiskEngineURL:      envOr("POLICY_RISK_ENGINE_URL", "http://localhost:8082"),
		HotWalletMinBalanceUSD:   envFloat("HOT_WALLET_MIN_BALANCE_USD", 50000),
		DerivationCacheTTL:       envDuration("DERIVATION_CACHE_TTL", 5*time.Minute),
		LogLevel:                 envOr("LOG_LEVEL", "info"),
		KeyCoolingPeriod:         envDuration("KEY_COOLING_PERIOD", 24*time.Hour),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// IsFinalized reports whether Solana confirmation policy requires finalized slots.
func (c Config) IsFinalized() bool {
	return strings.EqualFold(c.ConfirmationsSOL, "finalized")
}