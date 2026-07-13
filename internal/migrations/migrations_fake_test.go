package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"database/sql/driver"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage/postgres"
)

// fakeDriver is a database/sql/driver.Driver and Connector backed by an
// in-memory store that records every executed statement and models the set of
// tables. It exists purely to exercise the Up/Down/RoundTrip/TablesExist code
// paths in migrations.go without a real Postgres.
type fakeDriver struct {
	mu     sync.Mutex
	stmts  []string
	tables map[string]bool
}

type fakeConn struct{ drv *fakeDriver }

type fakeStmt struct {
	drv *fakeDriver
	q   string
}

type fakeRows struct {
	tables []string
	idx    int
}

var fakeDrv = &fakeDriver{tables: map[string]bool{}}

func init() {
	sql.Register("fakemigrations", fakeDrv)
}

// Connect satisfies driver.Connector so this driver can be passed to sql.OpenDB.
func (d *fakeDriver) Connect(_ context.Context) (driver.Conn, error) {
	return &fakeConn{drv: d}, nil
}

func (d *fakeDriver) Driver() driver.Driver { return d }

func (d *fakeDriver) Open(_ string) (driver.Conn, error) {
	return &fakeConn{drv: d}, nil
}

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) {
	return &fakeStmt{drv: c.drv, q: q}, nil
}

func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) { return fakeTx{c}, nil }

type fakeTx struct{ c *fakeConn }

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(_ []driver.Value) (driver.Result, error) {
	s.drv.mu.Lock()
	defer s.drv.mu.Unlock()
	s.drv.stmts = append(s.drv.stmts, s.q)
	s.drv.apply(s.q)
	return driver.RowsAffected(0), nil
}

func (s *fakeStmt) Query(_ []driver.Value) (driver.Rows, error) {
	s.drv.mu.Lock()
	defer s.drv.mu.Unlock()
	var tables []string
	if strings.Contains(strings.ToUpper(s.q), "INFORMATION_SCHEMA.TABLES") {
		for t := range s.drv.tables {
			tables = append(tables, t)
		}
	}
	return &fakeRows{tables: tables}, nil
}

// apply parses the executed SQL and updates the fake table set so TablesExist
// reports the right tables after Up runs. We only model CREATE TABLE and
// DROP TABLE statements.
func (d *fakeDriver) apply(q string) {
	for _, stmt := range splitSQL(q) {
		u := strings.ToUpper(stmt)
		switch {
		case strings.Contains(u, "CREATE TABLE") && strings.Contains(u, "IF NOT EXISTS"):
			name := parseTableName(stmt)
			if name != "" {
				d.tables[name] = true
			}
		case strings.HasPrefix(u, "CREATE TABLE"):
			name := parseTableName(stmt)
			if name != "" {
				d.tables[name] = true
			}
		case strings.Contains(u, "DROP TABLE") && strings.Contains(u, "IF EXISTS"):
			for _, name := range parseDropTableNames(stmt) {
				delete(d.tables, name)
			}
		case strings.Contains(u, "CREATE EXTENSION") ||
			strings.Contains(u, "CREATE INDEX") ||
			strings.Contains(u, "CREATE UNIQUE INDEX"):
			// no-op for the fake model
		}
	}
}

func splitSQL(q string) []string {
	var out []string
	for _, s := range strings.Split(q, ";") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		var b strings.Builder
		for _, line := range strings.Split(s, "\n") {
			ln := strings.TrimSpace(line)
			if strings.HasPrefix(ln, "--") || ln == "" {
				continue
			}
			b.WriteString(line)
			b.WriteByte(' ')
		}
		out = append(out, strings.TrimSpace(b.String()))
	}
	return out
}

func parseTableName(stmt string) string {
	u := strings.ToUpper(stmt)
	idx := strings.Index(u, "CREATE TABLE")
	rest := strings.TrimSpace(stmt[idx+len("CREATE TABLE"):])
	rest = strings.TrimPrefix(rest, "IF NOT EXISTS")
	rest = strings.TrimSpace(rest)
	end := len(rest)
	for i, c := range rest {
		if c == '(' || c == ' ' || c == '\t' || c == '\n' {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Trim(rest[:end], `"`))
}

func parseDropTableNames(stmt string) []string {
	u := strings.ToUpper(stmt)
	idx := strings.Index(u, "DROP TABLE")
	rest := strings.TrimSpace(stmt[idx+len("DROP TABLE"):])
	rest = strings.TrimPrefix(rest, "IF EXISTS")
	rest = strings.TrimSpace(rest)
	var names []string
	for _, p := range strings.Split(rest, ",") {
		name := strings.TrimSpace(p)
		end := len(name)
		for i, c := range name {
			if c == ' ' || c == '\t' || c == '\n' || c == '(' {
				end = i
				break
			}
		}
		name = strings.Trim(name[:end], `"`)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (r *fakeRows) Columns() []string { return []string{"table_name"} }

func (r *fakeRows) Close() error { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.tables) {
		return io.EOF
	}
	dest[0] = r.tables[r.idx]
	r.idx++
	return nil
}

func fakeDB(t *testing.T) *sql.DB {
	t.Helper()
	fakeDrv.mu.Lock()
	fakeDrv.stmts = nil
	fakeDrv.tables = map[string]bool{}
	fakeDrv.mu.Unlock()
	return sql.OpenDB(fakeDrv)
}

func TestUpAppliesAllUpMigrations(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	if err := Up(context.Background(), db); err != nil {
		t.Fatalf("Up: %v", err)
	}
	fakeDrv.mu.Lock()
	defer fakeDrv.mu.Unlock()
	for _, table := range []string{"wallets", "addresses", "balances", "utxos", "nonces", "withdrawal_requests", "key_mappings", "funding_requests", "audit_outbox", "audit_seq", "balance_events"} {
		if !fakeDrv.tables[table] {
			t.Errorf("expected table %s to be created by Up", table)
		}
	}
}

func TestDownDropsAllTables(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	if err := Up(context.Background(), db); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := Down(context.Background(), db); err != nil {
		t.Fatalf("Down: %v", err)
	}
	fakeDrv.mu.Lock()
	defer fakeDrv.mu.Unlock()
	for table := range fakeDrv.tables {
		t.Errorf("expected all tables dropped after Down, but %s remains", table)
	}
}

func TestRoundTripUpDownUp(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	if err := RoundTrip(context.Background(), db); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	fakeDrv.mu.Lock()
	defer fakeDrv.mu.Unlock()
	if len(fakeDrv.tables) == 0 {
		t.Error("expected tables to exist after round-trip")
	}
}

func TestTablesExistAllPresent(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	if err := Up(context.Background(), db); err != nil {
		t.Fatalf("Up: %v", err)
	}
	missing, err := TablesExist(context.Background(), db)
	if err != nil {
		t.Fatalf("TablesExist: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("expected no missing tables, got %v", missing)
	}
}

func TestTablesExistReportsMissing(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS wallets (id UUID PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	missing, err := TablesExist(context.Background(), db)
	if err != nil {
		t.Fatalf("TablesExist: %v", err)
	}
	if len(missing) == 0 {
		t.Error("expected some missing tables, got none")
	}
}

func TestUpIdempotent(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	if err := Up(context.Background(), db); err != nil {
		t.Fatalf("Up #1: %v", err)
	}
	if err := Up(context.Background(), db); err != nil {
		t.Fatalf("Up #2 (idempotent): %v", err)
	}
}

func TestDownIdempotent(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	if err := Down(context.Background(), db); err != nil {
		t.Fatalf("Down on empty DB: %v", err)
	}
}

func TestUpErrorPropagates(t *testing.T) {
	db := sql.OpenDB(&errDriver{})
	defer func() { _ = db.Close() }()
	if err := Up(context.Background(), db); err == nil {
		t.Error("expected Up to surface exec error")
	}
}

func TestDownErrorPropagates(t *testing.T) {
	db := sql.OpenDB(&errDriver{})
	defer func() { _ = db.Close() }()
	if err := Down(context.Background(), db); err == nil {
		t.Error("expected Down to surface exec error")
	}
}

func TestApplySchema(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	st := postgres.NewFromDB(db)
	if err := ApplySchema(context.Background(), st, `CREATE TABLE IF NOT EXISTS wallets (id UUID PRIMARY KEY)`); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	fakeDrv.mu.Lock()
	defer fakeDrv.mu.Unlock()
	if !fakeDrv.tables["wallets"] {
		t.Error("expected wallets table after ApplySchema")
	}
}

func TestApplySchemaError(t *testing.T) {
	db := sql.OpenDB(&errDriver{})
	defer func() { _ = db.Close() }()
	st := postgres.NewFromDB(db)
	if err := ApplySchema(context.Background(), st, `CREATE TABLE x()`); err == nil {
		t.Error("expected ApplySchema to surface exec error")
	}
}

func TestEnsureSchema(t *testing.T) {
	db := fakeDB(t)
	defer func() { _ = db.Close() }()
	st := postgres.NewFromDB(db)
	if err := EnsureSchema(context.Background(), st); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	fakeDrv.mu.Lock()
	defer fakeDrv.mu.Unlock()
	for _, table := range []string{"wallets", "addresses", "balances"} {
		if !fakeDrv.tables[table] {
			t.Errorf("expected %s after EnsureSchema", table)
		}
	}
}

func TestEnsureSchemaError(t *testing.T) {
	db := sql.OpenDB(&errDriver{})
	defer func() { _ = db.Close() }()
	st := postgres.NewFromDB(db)
	if err := EnsureSchema(context.Background(), st); err == nil {
		t.Error("expected EnsureSchema to surface exec error")
	}
}

type errDriver struct{}

func (d errDriver) Connect(_ context.Context) (driver.Conn, error) { return errConn{}, nil }
func (d errDriver) Driver() driver.Driver                        { return d }
func (errDriver) Open(_ string) (driver.Conn, error)             { return errConn{}, nil }

type errConn struct{}

func (errConn) Prepare(_ string) (driver.Stmt, error) { return errStmt{}, nil }
func (errConn) Close() error                          { return nil }
func (errConn) Begin() (driver.Tx, error)             { return errTx{}, nil }

type errTx struct{}

func (errTx) Commit() error   { return nil }
func (errTx) Rollback() error { return nil }

type errStmt struct{}

func (errStmt) Close() error  { return nil }
func (errStmt) NumInput() int { return -1 }
func (errStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return nil, fmt.Errorf("forced exec error")
}
func (errStmt) Query(_ []driver.Value) (driver.Rows, error) {
	return nil, fmt.Errorf("forced query error")
}