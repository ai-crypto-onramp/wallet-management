package main

import (
	"net"
	"testing"
)

// TestRunFailsWithoutConfig exercises the run() wiring path up to server
// startup. The test holds a port itself and points PORT/GRPC_PORT at it so
// the listeners fail deterministically — otherwise run() would start
// successfully and block on the shutdown signal, hanging the test whenever
// the default ports happen to be free on the host.
func TestRunFailsWithoutConfig(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open listener: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("PORT", port)
	t.Setenv("GRPC_PORT", port)
	// Unreachable Postgres and Redis: migrations warn and continue, the cache
	// and locker fall back to in-memory — no dependence on local services.
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")

	if err := run(); err == nil {
		t.Fatal("expected run() to return an error when its port is taken")
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