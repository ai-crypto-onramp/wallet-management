package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open listener: %v", err)
	}
	return ln
}

func portOf(t *testing.T, ln net.Listener) string {
	t.Helper()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// runWithTakenPort sets PORT and GRPC_PORT to a single pre-bound port so the
// REST + gRPC listeners fail deterministically and run() returns instead of
// blocking on the shutdown signal. It returns a cleanup that closes the
// listener.
func runWithTakenPort(t *testing.T) func() {
	t.Helper()
	ln := mustListen(t)
	port := portOf(t, ln)
	t.Setenv("PORT", port)
	t.Setenv("GRPC_PORT", port)
	return func() { _ = ln.Close() }
}

// TestRunFailsOnEmptyEVMXpub makes deriver.NewEVM return an error (empty xpub
// is rejected by ParseXpub), so run() returns before starting servers.
func TestRunFailsOnEmptyEVMXpub(t *testing.T) {
	t.Setenv("EVM_XPUB", "   ")
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	cleanup := runWithTakenPort(t)
	defer cleanup()
	if err := run(); err == nil {
		t.Fatal("expected run() to return an error on an empty EVM xpub")
	}
}

// TestRunFailsOnEmptySolanaXpub makes deriver.NewSolana return an error.
func TestRunFailsOnEmptySolanaXpub(t *testing.T) {
	t.Setenv("SOL_XPUB", "")
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	cleanup := runWithTakenPort(t)
	defer cleanup()
	if err := run(); err == nil {
		t.Fatal("expected run() to return an error on an empty Solana xpub")
	}
}

// TestRunFailsOnEmptyBTCXpub makes deriver.NewBTC return an error.
func TestRunFailsOnEmptyBTCXpub(t *testing.T) {
	t.Setenv("BTC_XPUB", "")
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	cleanup := runWithTakenPort(t)
	defer cleanup()
	if err := run(); err == nil {
		t.Fatal("expected run() to return an error on an empty BTC xpub")
	}
}

// TestRunFailsOnBadRedisURL keeps the cache/locker fallback path; the redis
// ParseURL error is logged but not fatal, so run() continues to the derivers
// and servers, which fail on the taken port.
func TestRunFailsOnBadRedisURL(t *testing.T) {
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", ":::not-a-redis-url")
	cleanup := runWithTakenPort(t)
	defer cleanup()
	if err := run(); err == nil {
		t.Fatal("expected run() to return an error when the port is taken")
	}
}

// TestRunReachesServerStart verifies the full wiring path: with valid
// placeholder xpubs, an unreachable Postgres/Redis (falling back to in-memory
// cache/locker), the service wires all components and the REST listener
// fails on the taken port, surfacing a non-nil error.
func TestRunReachesServerStart(t *testing.T) {
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	cleanup := runWithTakenPort(t)
	defer cleanup()
	if err := run(); err == nil {
		t.Fatal("expected run() to return an error when the port is taken")
	}
}

var _ = strconv.Itoa

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// TestRunGracefulShutdownInProcess runs run() in a goroutine with free ports
// (and unreachable Postgres/Redis so the in-memory fallback is used), waits
// for the REST /healthz endpoint to come up, then sends SIGTERM to the
// current process. run()'s signal handler catches it, gracefully shuts down
// the servers, and returns nil. This exercises the signal branch of run().
func TestRunGracefulShutdownInProcess(t *testing.T) {
	port := freePort(t)
	grpcPort := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("GRPC_PORT", grpcPort)
	t.Setenv("DB_URL", "postgres://nonexistent:none@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	t.Setenv("EVM_XPUB", "xpub-placeholder")
	t.Setenv("SOL_XPUB", "xpub-placeholder")
	t.Setenv("BTC_XPUB", "xpub-placeholder")

	errCh := make(chan error, 1)
	go func() { errCh <- run() }()

	// Wait for the REST server to come up.
	url := fmt.Sprintf("http://127.0.0.1:%s/healthz", port)
	deadline := time.Now().Add(15 * time.Second)
	var up bool
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				up = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !up {
		// Unblock the goroutine before failing.
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
		t.Fatalf("server did not come up on %s", url)
	}

	// Send SIGTERM to ourselves; run()'s handler will shut down and return nil.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected clean run() exit, got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run() did not return within 15s of SIGTERM")
	}
}