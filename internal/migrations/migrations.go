// Package migrations provides a tiny embed-based migration runner that applies
// .up.sql files in order. It is intentionally minimal so the service has no
// external migration tool dependency for tests.
package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage/postgres"
)

// Up runs all up migrations embedded in the migrations directory.
func Up(ctx context.Context, db *sql.DB) error {
	files := upFiles()
	for _, f := range files {
		ddl, ok := upMigrations[f]
		if !ok {
			continue
		}
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
	}
	return nil
}

// Down runs all down migrations in reverse order.
func Down(ctx context.Context, db *sql.DB) error {
	files := downFiles()
	for _, f := range files {
		ddl, ok := downMigrations[f]
		if !ok {
			continue
		}
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
	}
	return nil
}

// RoundTrip runs Up then Down then Up, returning an error if any step fails.
// It is used by the migration smoke test.
func RoundTrip(ctx context.Context, db *sql.DB) error {
	if err := Up(ctx, db); err != nil {
		return err
	}
	if err := Down(ctx, db); err != nil {
		return err
	}
	return Up(ctx, db)
}

// TablesExist checks that the expected tables exist in the current database.
func TablesExist(ctx context.Context, db *sql.DB) ([]string, error) {
	expected := []string{
		"wallets", "addresses", "balances", "utxos", "nonces",
		"withdrawal_requests", "key_mappings", "funding_requests",
		"audit_outbox", "audit_seq", "balance_events",
	}
	rows, err := db.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables WHERE table_schema='public'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	present := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		present[t] = true
	}
	var missing []string
	for _, e := range expected {
		if !present[e] {
			missing = append(missing, e)
		}
	}
	return missing, rows.Err()
}

// ApplySchema applies a schema.sql string via a postgres Store.
func ApplySchema(ctx context.Context, st *postgres.Store, ddl string) error {
	return st.ApplyMigrations(ctx, ddl)
}

// EnsureSchema applies the embedded up migrations to a postgres Store.
func EnsureSchema(ctx context.Context, st *postgres.Store) error {
	return Up(ctx, st.DB())
}

func upFiles() []string {
	var out []string
	for k := range upMigrations {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func downFiles() []string {
	var out []string
	for k := range downMigrations {
		out = append(out, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// EnsureFilePresent registers a migration programmatically (used by tests
// that embed the schema.sql directly).
func EnsureFilePresent(name, ddl string, isDown bool) {
	if isDown {
		downMigrations[name] = ddl
	} else {
		upMigrations[name] = ddl
	}
}

// StripComments removes SQL line comments for safer exec on some drivers.
func StripComments(ddl string) string {
	var b strings.Builder
	for _, line := range strings.Split(ddl, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

var upMigrations = map[string]string{}
var downMigrations = map[string]string{}