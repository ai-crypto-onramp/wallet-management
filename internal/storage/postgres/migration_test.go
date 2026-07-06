//go:build migration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// expectedTables lists every wallet-management table created by 0001_init_schema.
var expectedTables = []string{
	"chains",
	"wallets",
	"addresses",
	"balances",
	"utxos",
	"nonces",
	"withdrawal_requests",
	"key_mappings",
	"funding_requests",
}

// dbURL returns the connection string, defaulting to the docker-compose DSN.
func dbURL() string {
	if v := os.Getenv("DB_URL"); v != "" {
		return v
	}
	return "postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable"
}

func runMigrate(t *testing.T, args ...string) {
	t.Helper()
	migrateBin := os.Getenv("MIGRATE_BIN")
	if migrateBin == "" {
		migrateBin = "migrate"
	}
	migrationsDir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	cmdArgs := append([]string{"-path", migrationsDir, "-database", dbURL()}, args...)
	cmd := exec.Command(migrateBin, cmdArgs...)
	// golang-migrate prompts for confirmation when applying all down migrations;
	// answer "yes" automatically.
	cmd.Stdin = strings.NewReader("y\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("migrate %v failed: %v\n%s", args, err, out)
	}
}

func queryRow(ctx context.Context, dsn, query string, args ...any) *sql.Row {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return &sql.Row{}
	}
	defer db.Close()
	return db.QueryRowContext(ctx, query, args...)
}

func tableExists(t *testing.T, dsn, table string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var exists bool
	err := queryRow(ctx, dsn,
		`SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists)
	if err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

func assertAllTables(t *testing.T, dsn string, want bool) {
	t.Helper()
	for _, table := range expectedTables {
		got := tableExists(t, dsn, table)
		if got != want {
			t.Fatalf("table %s: want exists=%v got %v", table, want, got)
		}
	}
}

// TestMigrationRoundTrip runs up -> down -> up and asserts that every table
// is created, dropped, then created again. Requires PostgreSQL reachable at
// DB_URL (default: postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable)
// and the golang-migrate CLI on PATH.
func TestMigrationRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping migration round-trip in short mode")
	}
	dsn := dbURL()

	// Ensure clean slate before starting (ignore errors if already clean).
	runMigrate(t, "down")

	// Up: all tables must exist.
	runMigrate(t, "up")
	assertAllTables(t, dsn, true)

	// Down: all tables must be dropped.
	runMigrate(t, "down")
	assertAllTables(t, dsn, false)

	// Up again: all tables must exist (idempotent re-application).
	runMigrate(t, "up")
	assertAllTables(t, dsn, true)
}