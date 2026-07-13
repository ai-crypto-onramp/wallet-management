package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/lib/pq"
)

func openPostgres(t *testing.T) *sql.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping migration round-trip test in short mode")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping migration round-trip test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	// verify reachability
	if err := db.PingContext(context.Background()); err != nil {
		t.Skipf("postgres not reachable at %s: %v", dsn, err)
	}
	return db
}

// TestMigrationsRoundTrip runs up -> down -> up on a real Postgres reachable via
// DATABASE_URL and asserts that all expected tables exist after the final up.
func TestMigrationsRoundTrip(t *testing.T) {
	db := openPostgres(t)
	defer db.Close()
	ctx := context.Background()

	// drop any leftover wallet-management tables to start clean.
	if err := Down(ctx, db); err != nil {
		// ignore errors if tables don't exist yet
		t.Logf("initial down (cleanup) returned: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS wallets, addresses, balances, utxos, nonces, withdrawal_requests, key_mappings, funding_requests, audit_outbox, balance_events CASCADE`); err != nil {
		t.Fatalf("drop leftover tables: %v", err)
	}

	if err := RoundTrip(ctx, db); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}

	missing, err := TablesExist(ctx, db)
	if err != nil {
		t.Fatalf("TablesExist: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("expected all tables present, missing: %v", missing)
	}

	// final cleanup
	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS wallets, addresses, balances, utxos, nonces, withdrawal_requests, key_mappings, funding_requests, audit_outbox, balance_events CASCADE`)
}

func TestUpFilesSorted(t *testing.T) {
	files := upFiles()
	for i := 1; i < len(files); i++ {
		if files[i] < files[i-1] {
			t.Errorf("upFiles not sorted: %q before %q", files[i-1], files[i])
		}
	}
}

func TestDownFilesReverseSorted(t *testing.T) {
	files := downFiles()
	for i := 1; i < len(files); i++ {
		if files[i] > files[i-1] {
			t.Errorf("downFiles not reverse-sorted: %q before %q", files[i-1], files[i])
		}
	}
}

func TestUpAndDownFilesPopulated(t *testing.T) {
	up := upFiles()
	if len(up) == 0 {
		t.Error("expected at least one up migration")
	}
	down := downFiles()
	if len(down) == 0 {
		t.Error("expected at least one down migration")
	}
	// the embedded init migration must be present
	found := false
	for _, f := range up {
		if f == "0001_init_schema.up.sql" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 0001_init_schema.up.sql in upFiles")
	}
}

func TestEnsureFilePresent(t *testing.T) {
	EnsureFilePresent("999_test.up.sql", "SELECT 1;", false)
	if _, ok := upMigrations["999_test.up.sql"]; !ok {
		t.Error("EnsureFilePresent did not register up migration")
	}
	EnsureFilePresent("999_test.down.sql", "SELECT 1;", true)
	if _, ok := downMigrations["999_test.down.sql"]; !ok {
		t.Error("EnsureFilePresent did not register down migration")
	}
	delete(upMigrations, "999_test.up.sql")
	delete(downMigrations, "999_test.down.sql")
}

func TestStripComments(t *testing.T) {
	in := "-- a comment\nCREATE TABLE x();\n-- another\nSELECT 1;\n"
	out := StripComments(in)
	for _, line := range []string{"-- a comment", "-- another"} {
		if contains(out, line) {
			t.Errorf("expected comment %q to be stripped", line)
		}
	}
	if !contains(out, "CREATE TABLE x();") {
		t.Error("expected non-comment line to be preserved")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && containsHelper(s, sub)))
}

func containsHelper(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestPqDriverRegistered(t *testing.T) {
	// ensure the lib/pq driver is imported so tests that use it compile.
	_ = pq.Driver{}
	_ = fmt.Sprintf
}