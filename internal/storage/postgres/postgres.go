// Package postgres implements storage.Store against PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/domain"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// Store implements storage.SQLStore using database/sql + lib/pq.
type Store struct {
	db *sql.DB
}

// New opens a new Postgres connection.
func New(dbURL string) (*Store, error) {
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	return &Store{db: db}, nil
}

// NewFromDB wraps an existing *sql.DB.
func NewFromDB(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying connection.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the underlying connection.
func (s *Store) Close() error { return s.db.Close() }

// ApplyMigrations executes a multi-statement DDL string.
func (s *Store) ApplyMigrations(ctx context.Context, ddl string) error {
	_, err := s.db.ExecContext(ctx, ddl)
	return err
}

// InTx runs fn within a serializable transaction.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	tctx := context.WithValue(ctx, txKey{}, tx)
	if err := fn(tctx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type txKey struct{}

func (s *Store) tx(ctx context.Context) *sql.Tx {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return nil
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if tx := s.tx(ctx); tx != nil {
		return tx.ExecContext(ctx, query, args...)
	}
	return s.db.ExecContext(ctx, query, args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if tx := s.tx(ctx); tx != nil {
		return tx.QueryRowContext(ctx, query, args...)
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if tx := s.tx(ctx); tx != nil {
		return tx.QueryContext(ctx, query, args...)
	}
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Store) CreateWallet(ctx context.Context, w *domain.Wallet) error {
	_, err := s.exec(ctx,
		`INSERT INTO wallets (id, chain, type, label, state, key_id, custodian_ref, rotation_days, rotation_after_receives, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		w.ID, string(w.Chain), string(w.Type), w.Label, string(w.State), w.KeyID, w.CustodianRef, w.RotationDays, w.RotationAfterReceives, w.CreatedAt, w.UpdatedAt)
	return err
}

func (s *Store) GetWallet(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	row := s.queryRow(ctx, `SELECT id, chain, type, label, state, key_id, custodian_ref, rotation_days, rotation_after_receives, created_at, updated_at FROM wallets WHERE id=$1`, id)
	w := &domain.Wallet{}
	var rotDays, rotRecv sql.NullInt64
	if err := row.Scan(&w.ID, &w.Chain, &w.Type, &w.Label, &w.State, &w.KeyID, &w.CustodianRef, &rotDays, &rotRecv, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	if rotDays.Valid {
		v := int(rotDays.Int64)
		w.RotationDays = &v
	}
	if rotRecv.Valid {
		v := int(rotRecv.Int64)
		w.RotationAfterReceives = &v
	}
	return w, nil
}

func (s *Store) UpdateWalletState(ctx context.Context, id uuid.UUID, state domain.WalletState) error {
	_, err := s.exec(ctx, `UPDATE wallets SET state=$2, updated_at=now() WHERE id=$1`, id, string(state))
	return err
}

func (s *Store) ListWallets(ctx context.Context, chainF, typeF, stateF string) ([]*domain.Wallet, error) {
	q := `SELECT id, chain, type, label, state, key_id, custodian_ref, rotation_days, rotation_after_receives, created_at, updated_at FROM wallets WHERE 1=1`
	var args []any
	n := 1
	if chainF != "" {
		q += fmt.Sprintf(" AND chain=$%d", n)
		args = append(args, chainF)
		n++
	}
	if typeF != "" {
		q += fmt.Sprintf(" AND type=$%d", n)
		args = append(args, typeF)
		n++
	}
	if stateF != "" {
		q += fmt.Sprintf(" AND state=$%d", n)
		args = append(args, stateF)
	}
	q += " ORDER BY created_at"
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*domain.Wallet
	for rows.Next() {
		w := &domain.Wallet{}
		var rotDays, rotRecv sql.NullInt64
		if err := rows.Scan(&w.ID, &w.Chain, &w.Type, &w.Label, &w.State, &w.KeyID, &w.CustodianRef, &rotDays, &rotRecv, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		if rotDays.Valid {
			v := int(rotDays.Int64)
			w.RotationDays = &v
		}
		if rotRecv.Valid {
			v := int(rotRecv.Int64)
			w.RotationAfterReceives = &v
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) InsertAddress(ctx context.Context, a *domain.Address) error {
	_, err := s.exec(ctx,
		`INSERT INTO addresses (id, wallet_id, chain, address, derivation_path, index, change, state, receive_count, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		a.ID, a.WalletID, string(a.Chain), a.Address, a.DerivationPath, a.Index, a.Change, string(a.State), a.ReceiveCount, a.CreatedAt)
	return err
}

func (s *Store) GetActiveAddress(ctx context.Context, walletID uuid.UUID) (*domain.Address, error) {
	row := s.queryRow(ctx, `SELECT id, wallet_id, chain, address, derivation_path, index, change, state, receive_count, created_at FROM addresses WHERE wallet_id=$1 AND state='active' LIMIT 1`, walletID)
	return scanAddress(row)
}

func (s *Store) GetAddress(ctx context.Context, id uuid.UUID) (*domain.Address, error) {
	row := s.queryRow(ctx, `SELECT id, wallet_id, chain, address, derivation_path, index, change, state, receive_count, created_at FROM addresses WHERE id=$1`, id)
	return scanAddress(row)
}

func scanAddress(row *sql.Row) (*domain.Address, error) {
	a := &domain.Address{}
	if err := row.Scan(&a.ID, &a.WalletID, &a.Chain, &a.Address, &a.DerivationPath, &a.Index, &a.Change, &a.State, &a.ReceiveCount, &a.CreatedAt); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Store) ListAddresses(ctx context.Context, walletID uuid.UUID) ([]*domain.Address, error) {
	rows, err := s.query(ctx, `SELECT id, wallet_id, chain, address, derivation_path, index, change, state, receive_count, created_at FROM addresses WHERE wallet_id=$1 ORDER BY change, index`, walletID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*domain.Address
	for rows.Next() {
		a := &domain.Address{}
		if err := rows.Scan(&a.ID, &a.WalletID, &a.Chain, &a.Address, &a.DerivationPath, &a.Index, &a.Change, &a.State, &a.ReceiveCount, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeprecateAddress(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx, `UPDATE addresses SET state='deprecated' WHERE id=$1`, id)
	return err
}

func (s *Store) NextAddressIndex(ctx context.Context, walletID uuid.UUID, change int) (int, error) {
	row := s.queryRow(ctx, `SELECT COALESCE(MAX(index), -1) + 1 FROM addresses WHERE wallet_id=$1 AND change=$2`, walletID, change)
	var idx int
	if err := row.Scan(&idx); err != nil {
		return 0, err
	}
	return idx, nil
}

func (s *Store) IncrementReceiveCount(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx, `UPDATE addresses SET receive_count = receive_count + 1 WHERE id=$1`, id)
	return err
}

func (s *Store) UpsertBalance(ctx context.Context, b *storage.Balance) error {
	_, err := s.exec(ctx,
		`INSERT INTO balances (wallet_id, asset, confirmed, pending, locked, last_block_seen, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,now())
		 ON CONFLICT (wallet_id, asset) DO UPDATE SET confirmed=EXCLUDED.confirmed, pending=EXCLUDED.pending, locked=EXCLUDED.locked, last_block_seen=EXCLUDED.last_block_seen, updated_at=now()`,
		b.WalletID, b.Asset, b.Confirmed, b.Pending, b.Locked, b.LastBlockSeen)
	return err
}

func (s *Store) GetBalance(ctx context.Context, walletID uuid.UUID, asset string) (*storage.Balance, error) {
	row := s.queryRow(ctx, `SELECT wallet_id, asset, confirmed, pending, locked, last_block_seen, updated_at FROM balances WHERE wallet_id=$1 AND asset=$2`, walletID, asset)
	return scanBalance(row)
}

func scanBalance(row *sql.Row) (*storage.Balance, error) {
	b := &storage.Balance{}
	if err := row.Scan(&b.WalletID, &b.Asset, &b.Confirmed, &b.Pending, &b.Locked, &b.LastBlockSeen, &b.UpdatedAt); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Store) ListBalances(ctx context.Context, walletID uuid.UUID) ([]*storage.Balance, error) {
	rows, err := s.query(ctx, `SELECT wallet_id, asset, confirmed, pending, locked, last_block_seen, updated_at FROM balances WHERE wallet_id=$1 ORDER BY asset`, walletID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*storage.Balance
	for rows.Next() {
		b := &storage.Balance{}
		if err := rows.Scan(&b.WalletID, &b.Asset, &b.Confirmed, &b.Pending, &b.Locked, &b.LastBlockSeen, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) RecordBalanceEvent(ctx context.Context, e *storage.BalanceEvent) error {
	_, err := s.exec(ctx,
		`INSERT INTO balance_events (id, wallet_id, asset, block_height, event_id) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		e.ID, e.WalletID, e.Asset, e.BlockHeight, e.EventID)
	if err != nil {
		return err
	}
	// check if it was a no-op (duplicate). We can't easily detect, so caller checks via RowCount in memstore.
	// For postgres, rely on the unique constraint — a duplicate insert returns no error due to ON CONFLICT DO NOTHING.
	// To signal duplicates, we do a prior check.
	var exists bool
	err = s.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM balance_events WHERE wallet_id=$1 AND asset=$2 AND block_height=$3 AND event_id=$4 AND id <> $5)`, e.WalletID, e.Asset, e.BlockHeight, e.EventID, e.ID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return storage.ErrDuplicateEvent
	}
	return nil
}

func (s *Store) InsertUTXO(ctx context.Context, u *storage.UTXO) error {
	_, err := s.exec(ctx,
		`INSERT INTO utxos (outpoint, wallet_id, value, script_type, confirmations, lock_state, locked_at, spent_at, tx_hash, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`,
		u.Outpoint, u.WalletID, u.Value, u.ScriptType, u.Confirmations, u.LockState, u.LockedAt, u.SpentAt, u.TxHash)
	return err
}

func (s *Store) ListFreeUTXOs(ctx context.Context, walletID uuid.UUID) ([]*storage.UTXO, error) {
	rows, err := s.query(ctx, `SELECT outpoint, wallet_id, value, script_type, confirmations, lock_state, locked_at, spent_at, tx_hash, updated_at FROM utxos WHERE wallet_id=$1 AND lock_state='free' ORDER BY outpoint`, walletID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*storage.UTXO
	for rows.Next() {
		u := &storage.UTXO{}
		if err := rows.Scan(&u.Outpoint, &u.WalletID, &u.Value, &u.ScriptType, &u.Confirmations, &u.LockState, &u.LockedAt, &u.SpentAt, &u.TxHash, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) LockUTXOs(ctx context.Context, outpoints []string) error {
	if len(outpoints) == 0 {
		return nil
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		for _, op := range outpoints {
			res, err := s.exec(ctx, `UPDATE utxos SET lock_state='locked', locked_at=now(), updated_at=now() WHERE outpoint=$1 AND lock_state='free'`, op)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("utxo not free: %s", op)
			}
		}
		return nil
	})
}

func (s *Store) MarkUTXOsSpent(ctx context.Context, outpoints []string, txHash string) error {
	if len(outpoints) == 0 {
		return nil
	}
	for _, op := range outpoints {
		if _, err := s.exec(ctx, `UPDATE utxos SET lock_state='spent', spent_at=now(), tx_hash=$2, updated_at=now() WHERE outpoint=$1`, op, txHash); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RestoreUTXOs(ctx context.Context, outpoints []string) error {
	if len(outpoints) == 0 {
		return nil
	}
	for _, op := range outpoints {
		if _, err := s.exec(ctx, `UPDATE utxos SET lock_state='free', spent_at=NULL, locked_at=NULL, tx_hash='', updated_at=now() WHERE outpoint=$1`, op); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) PruneUTXOs(ctx context.Context, outpoints []string) error {
	if len(outpoints) == 0 {
		return nil
	}
	for _, op := range outpoints {
		if _, err := s.exec(ctx, `DELETE FROM utxos WHERE outpoint=$1`, op); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetNonce(ctx context.Context, walletID uuid.UUID, chain string) (*storage.Nonce, error) {
	row := s.queryRow(ctx, `SELECT wallet_id, chain, pending_nonce, broadcast_nonce, version, updated_at FROM nonces WHERE wallet_id=$1 AND chain=$2`, walletID, chain)
	n := &storage.Nonce{}
	if err := row.Scan(&n.WalletID, &n.Chain, &n.PendingNonce, &n.BroadcastNonce, &n.Version, &n.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return &storage.Nonce{WalletID: walletID, Chain: chain}, nil
		}
		return nil, err
	}
	return n, nil
}

func (s *Store) UpsertNonce(ctx context.Context, n *storage.Nonce) error {
	_, err := s.exec(ctx,
		`INSERT INTO nonces (wallet_id, chain, pending_nonce, broadcast_nonce, version, updated_at)
		 VALUES ($1,$2,$3,$4,$5,now())
		 ON CONFLICT (wallet_id, chain) DO UPDATE SET pending_nonce=EXCLUDED.pending_nonce, broadcast_nonce=EXCLUDED.broadcast_nonce, version=EXCLUDED.version, updated_at=now()`,
		n.WalletID, n.Chain, n.PendingNonce, n.BroadcastNonce, n.Version)
	return err
}

func (s *Store) IncrementPendingNonce(ctx context.Context, walletID uuid.UUID, chain string) (int64, int, error) {
	var val int64
	var ver int
	err := s.InTx(ctx, func(ctx context.Context) error {
		row := s.queryRow(ctx, `SELECT pending_nonce, version FROM nonces WHERE wallet_id=$1 AND chain=$2 FOR UPDATE`, walletID, chain)
		var pn, v sql.NullInt64
		if err := row.Scan(&pn, &v); err != nil {
			if err == sql.ErrNoRows {
				// insert new
				val = 0
				ver = 1
				_, err := s.exec(ctx, `INSERT INTO nonces (wallet_id, chain, pending_nonce, broadcast_nonce, version, updated_at) VALUES ($1,$2,1,0,1,now())`, walletID, chain)
				return err
			}
			return err
		}
		val = pn.Int64
		ver = int(v.Int64) + 1
		_, err := s.exec(ctx, `UPDATE nonces SET pending_nonce=$3, version=$4, updated_at=now() WHERE wallet_id=$1 AND chain=$2`, walletID, chain, val+1, ver)
		return err
	})
	return val, ver, err
}

func (s *Store) AdvanceBroadcastNonce(ctx context.Context, walletID uuid.UUID, chain string, nonce int64) error {
	_, err := s.exec(ctx, `UPDATE nonces SET broadcast_nonce=GREATEST(broadcast_nonce, $3), updated_at=now() WHERE wallet_id=$1 AND chain=$2`, walletID, chain, nonce+1)
	return err
}

func (s *Store) CreateWithdrawal(ctx context.Context, w *storage.WithdrawalRequest) error {
	_, err := s.exec(ctx,
		`INSERT INTO withdrawal_requests (id, wallet_id, to_address, asset, amount, state, policy_decision_id, failure_reason, tx_hash, nonce_value, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		w.ID, w.WalletID, w.ToAddress, w.Asset, w.Amount, w.State, w.PolicyDecisionID, w.FailureReason, w.TxHash, w.NonceValue, w.CreatedAt, w.UpdatedAt)
	return err
}

func (s *Store) GetWithdrawal(ctx context.Context, id uuid.UUID) (*storage.WithdrawalRequest, error) {
	row := s.queryRow(ctx, `SELECT id, wallet_id, to_address, asset, amount, state, policy_decision_id, failure_reason, tx_hash, nonce_value, created_at, updated_at FROM withdrawal_requests WHERE id=$1`, id)
	w := &storage.WithdrawalRequest{}
	if err := row.Scan(&w.ID, &w.WalletID, &w.ToAddress, &w.Asset, &w.Amount, &w.State, &w.PolicyDecisionID, &w.FailureReason, &w.TxHash, &w.NonceValue, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Store) UpdateWithdrawalState(ctx context.Context, id uuid.UUID, state string, reason string, txHash string, policyDecisionID string) error {
	_, err := s.exec(ctx,
		`UPDATE withdrawal_requests SET state=$2, failure_reason=COALESCE(NULLIF($3,''), failure_reason), tx_hash=COALESCE(NULLIF($4,''), tx_hash), policy_decision_id=COALESCE(NULLIF($5,''), policy_decision_id), updated_at=now() WHERE id=$1`,
		id, state, reason, txHash, policyDecisionID)
	return err
}

func (s *Store) UpdateWithdrawalNonce(ctx context.Context, id uuid.UUID, nonce int64) error {
	_, err := s.exec(ctx, `UPDATE withdrawal_requests SET nonce_value=$2, updated_at=now() WHERE id=$1`, id, nonce)
	return err
}

func (s *Store) BindKeyMapping(ctx context.Context, m *storage.KeyMapping) error {
	_, err := s.exec(ctx,
		`INSERT INTO key_mappings (wallet_id, key_id, active_from, active_to, rotation_state, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		m.WalletID, m.KeyID, m.ActiveFrom, m.ActiveTo, m.RotationState, m.CreatedAt)
	return err
}

func (s *Store) ResolveActiveKey(ctx context.Context, walletID uuid.UUID) ([]*storage.KeyMapping, error) {
	rows, err := s.query(ctx, `SELECT wallet_id, key_id, active_from, active_to, rotation_state, created_at FROM key_mappings WHERE wallet_id=$1 AND rotation_state IN ('current','cooling') ORDER BY active_from`, walletID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*storage.KeyMapping
	for rows.Next() {
		m := &storage.KeyMapping{}
		if err := rows.Scan(&m.WalletID, &m.KeyID, &m.ActiveFrom, &m.ActiveTo, &m.RotationState, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, sql.ErrNoRows
	}
	return out, rows.Err()
}

func (s *Store) RotateKeyMapping(ctx context.Context, walletID uuid.UUID, newKeyID string, cooling time.Duration) error {
	activeTo := time.Now().Add(cooling)
	return s.InTx(ctx, func(ctx context.Context) error {
		_, err := s.exec(ctx, `UPDATE key_mappings SET rotation_state='cooling', active_to=$2 WHERE wallet_id=$1 AND rotation_state='current'`, walletID, activeTo)
		if err != nil {
			return err
		}
		_, err = s.exec(ctx,
			`INSERT INTO key_mappings (wallet_id, key_id, active_from, rotation_state, created_at)
			 VALUES ($1,$2,now(),'current',now())
			 ON CONFLICT (wallet_id, key_id) DO UPDATE SET rotation_state='current', active_from=now(), active_to=NULL`,
			walletID, newKeyID)
		return err
	})
}

func (s *Store) ExpireCooling(ctx context.Context) error {
	_, err := s.exec(ctx, `UPDATE key_mappings SET rotation_state='retired' WHERE rotation_state='cooling' AND active_to IS NOT NULL AND active_to < now()`)
	return err
}

func (s *Store) CreateFundingRequest(ctx context.Context, f *storage.FundingRequest) error {
	_, err := s.exec(ctx,
		`INSERT INTO funding_requests (id, wallet_id, asset, amount, state, treasury_batch_id, reason, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		f.ID, f.WalletID, f.Asset, f.Amount, f.State, f.TreasuryBatchID, f.Reason, f.CreatedAt, f.UpdatedAt)
	return err
}

func (s *Store) GetOpenFundingRequest(ctx context.Context, walletID uuid.UUID, asset string) (*storage.FundingRequest, error) {
	row := s.queryRow(ctx, `SELECT id, wallet_id, asset, amount, state, treasury_batch_id, reason, created_at, updated_at FROM funding_requests WHERE wallet_id=$1 AND asset=$2 AND state='requested' LIMIT 1`, walletID, asset)
	f := &storage.FundingRequest{}
	if err := row.Scan(&f.ID, &f.WalletID, &f.Asset, &f.Amount, &f.State, &f.TreasuryBatchID, &f.Reason, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Store) UpdateFundingState(ctx context.Context, id uuid.UUID, state string, treasuryBatchID string) error {
	_, err := s.exec(ctx, `UPDATE funding_requests SET state=$2, treasury_batch_id=COALESCE(NULLIF($3,''), treasury_batch_id), updated_at=now() WHERE id=$1`, id, state, treasuryBatchID)
	return err
}

func (s *Store) AppendAuditEvent(ctx context.Context, e *storage.AuditOutboxEvent) error {
	_, err := s.exec(ctx,
		`INSERT INTO audit_outbox (id, event_id, wallet_id, event_type, payload, seq, delivered, attempts, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,false,0,$7)`,
		e.ID, e.EventID, e.WalletID, e.EventType, e.Payload, e.Seq, e.CreatedAt)
	return err
}

func (s *Store) ListUndeliveredAuditEvents(ctx context.Context, limit int) ([]*storage.AuditOutboxEvent, error) {
	rows, err := s.query(ctx, `SELECT id, event_id, wallet_id, event_type, payload, seq, delivered, attempts, created_at, delivered_at FROM audit_outbox WHERE delivered=false ORDER BY seq LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*storage.AuditOutboxEvent
	for rows.Next() {
		e := &storage.AuditOutboxEvent{}
		if err := rows.Scan(&e.ID, &e.EventID, &e.WalletID, &e.EventType, &e.Payload, &e.Seq, &e.Delivered, &e.Attempts, &e.CreatedAt, &e.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) MarkAuditDelivered(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx, `UPDATE audit_outbox SET delivered=true, attempts=attempts+1, delivered_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) NextAuditSeq(ctx context.Context, walletID uuid.UUID) (int64, error) {
	// A dedicated counter row reserves the sequence atomically; MAX(seq)+1 over
	// audit_outbox would hand the same value to concurrent emitters.
	var seq int64
	err := s.queryRow(ctx,
		`INSERT INTO audit_seq (wallet_id, seq) VALUES ($1, 1)
		 ON CONFLICT (wallet_id) DO UPDATE SET seq = audit_seq.seq + 1
		 RETURNING seq`, walletID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return seq, nil
}