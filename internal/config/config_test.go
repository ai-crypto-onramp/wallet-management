package config

import (
	"testing"
	"time"
)

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("DB_URL", "")
	t.Setenv("DEFAULT_ADDRESS_ROTATION_DAYS", "")
	t.Setenv("CONFIRMATIONS_REQUIRED_EVM", "")
	t.Setenv("HOT_WALLET_MIN_BALANCE_USD", "")
	t.Setenv("DERIVATION_CACHE_TTL", "")
	t.Setenv("KEY_COOLING_PERIOD", "")
	t.Setenv("CONFIRMATIONS_REQUIRED_SOL", "")

	c := FromEnv()
	if c.Port != "8080" || c.GRPCPort != "9090" {
		t.Errorf("port defaults wrong: %+v", c)
	}
	if c.DefaultRotationDays != 7 {
		t.Errorf("expected rotation 7, got %d", c.DefaultRotationDays)
	}
	if c.ConfirmationsEVM != 12 || c.ConfirmationsBTC != 6 {
		t.Errorf("confirmation defaults wrong: %+v", c)
	}
	if c.HotWalletMinBalanceUSD != 50000 {
		t.Errorf("expected 50000, got %f", c.HotWalletMinBalanceUSD)
	}
	if c.DerivationCacheTTL != 5*time.Minute {
		t.Errorf("expected 5m, got %v", c.DerivationCacheTTL)
	}
	if c.KeyCoolingPeriod != 24*time.Hour {
		t.Errorf("expected 24h, got %v", c.KeyCoolingPeriod)
	}
	if !c.IsFinalized() {
		t.Error("expected finalized by default")
	}
}

func TestFromEnvOverrides(t *testing.T) {
	t.Setenv("PORT", "9999")
	t.Setenv("CONFIRMATIONS_REQUIRED_EVM", "33")
	t.Setenv("CONFIRMATIONS_REQUIRED_SOL", "CONFIRMED")
	t.Setenv("HOT_WALLET_MIN_BALANCE_USD", "1234.5")
	t.Setenv("DERIVATION_CACHE_TTL", "10s")
	t.Setenv("KEY_COOLING_PERIOD", "1h")

	c := FromEnv()
	if c.Port != "9999" {
		t.Errorf("port override failed: %s", c.Port)
	}
	if c.ConfirmationsEVM != 33 {
		t.Errorf("evm conf override failed: %d", c.ConfirmationsEVM)
	}
	if c.IsFinalized() {
		t.Error("expected non-finalized after override")
	}
	if c.HotWalletMinBalanceUSD != 1234.5 {
		t.Errorf("threshold override failed: %f", c.HotWalletMinBalanceUSD)
	}
	if c.DerivationCacheTTL != 10*time.Second {
		t.Errorf("ttl override failed: %v", c.DerivationCacheTTL)
	}
	if c.KeyCoolingPeriod != time.Hour {
		t.Errorf("cooling override failed: %v", c.KeyCoolingPeriod)
	}
}

func TestFromEnvInvalidFallback(t *testing.T) {
	t.Setenv("CONFIRMATIONS_REQUIRED_EVM", "not-a-number")
	t.Setenv("HOT_WALLET_MIN_BALANCE_USD", "bogus")
	t.Setenv("DERIVATION_CACHE_TTL", "bogus")
	c := FromEnv()
	if c.ConfirmationsEVM != 12 {
		t.Errorf("expected default fallback 12, got %d", c.ConfirmationsEVM)
	}
	if c.HotWalletMinBalanceUSD != 50000 {
		t.Errorf("expected default 50000, got %f", c.HotWalletMinBalanceUSD)
	}
	if c.DerivationCacheTTL != 5*time.Minute {
		t.Errorf("expected default 5m, got %v", c.DerivationCacheTTL)
	}
}