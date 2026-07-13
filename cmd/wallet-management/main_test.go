package main

import (
	"testing"
)

// TestRunFailsWithoutConfig exercises the run() wiring path. Without a real
// Postgres or valid xpubs, run() returns an error (from deriver.NewEVM when
// the placeholder xpub fails to parse, or from a downstream dependency). The
// important behavior is that run() does not block or panic.
func TestRunFailsWithoutConfig(t *testing.T) {
	// Force the EVM deriver to receive an invalid xpub so run() returns an
	// error early (after the lazy postgres open and the redis fallback).
	t.Setenv("EVM_XPUB", "")
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", "")
	if err := run(); err == nil {
		t.Fatal("expected run() to return an error without valid config")
	}
}

func TestEnvOr(t *testing.T) {
	if v := envOr("WALLET_MGMT_TEST_ENV_OR_KEY", "default"); v != "default" {
		t.Errorf("expected default, got %s", v)
	}
	t.Setenv("WALLET_MGMT_TEST_ENV_OR_KEY", "set")
	if v := envOr("WALLET_MGMT_TEST_ENV_OR_KEY", "default"); v != "set" {
		t.Errorf("expected set, got %s", v)
	}
}