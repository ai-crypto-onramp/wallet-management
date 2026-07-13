//go:build integration

// Integration tests for the Postgres store. They require a running PostgreSQL
// (make docker-up) and are gated behind the "integration" build tag:
//
//	make test-integration
//
// TEST_DB_URL overrides the default local docker-compose connection string.
package postgres_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/domain"
	"github.com/ai-crypto-onramp/wallet-management/internal/migrations"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/postgres"
	"github.com/google/uuid"
)

var (
	setupOnce sync.Once
	sharedSt  *postgres.Store
	setupErr  error
)

func testStore(t *testing.T) *postgres.Store {
	t.Helper()
	setupOnce.Do(func() {
		url := os.Getenv("TEST_DB_URL")
		if url == "" {
			url = "postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable"
		}
		st, err := postgres.New(url)
		if err != nil {
			setupErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := st.DB().PingContext(ctx); err != nil {
			setupErr = err
			return
		}
		// Clean slate: down (ignore first-run errors) then up.
		_ = migrations.Down(ctx, st.DB())
		if err := migrations.Up(ctx, st.DB()); err != nil {
			setupErr = err
			return
		}
		sharedSt = st
	})
	if setupErr != nil {
		t.Fatalf("postgres unavailable: %v (start with `make docker-up`)", setupErr)
	}
	return sharedSt
}

func newWallet(t *testing.T, st *postgres.Store, chain domain.Chain, wtype domain.WalletType) *domain.Wallet {
	t.Helper()
	w := &domain.Wallet{
		ID:        uuid.New(),
		Chain:     chain,
		Type:      wtype,
		Label:     "itest-" + uuid.NewString()[:8],
		State:     domain.WalletStateActive,
		KeyID:     "key-" + uuid.NewString()[:8],
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateWallet(context.Background(), w); err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	return w
}

func TestMigrationRoundTrip(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := migrations.RoundTrip(ctx, st.DB()); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	missing, err := migrations.TablesExist(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing tables after round trip: %v", missing)
	}
}

func TestWalletCRUD(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	got, err := st.GetWallet(ctx, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Chain != w.Chain || got.Type != w.Type || got.State != domain.WalletStateActive {
		t.Errorf("wallet mismatch: %+v", got)
	}

	if err := st.UpdateWalletState(ctx, w.ID, domain.WalletStatePaused); err != nil {
		t.Fatalf("update state: %v", err)
	}
	got, _ = st.GetWallet(ctx, w.ID)
	if got.State != domain.WalletStatePaused {
		t.Errorf("expected paused, got %s", got.State)
	}

	list, err := st.ListWallets(ctx, string(domain.ChainEthereum), string(domain.WalletTypeHot), string(domain.WalletStatePaused))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, lw := range list {
		if lw.ID == w.ID {
			found = true
		}
	}
	if !found {
		t.Error("filtered list did not include wallet")
	}
}

func TestAddressLifecycleAndOneActiveIndex(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	idx, err := st.NextAddressIndex(ctx, string(domain.ChainEthereum), 0)
	if err != nil {
		t.Fatalf("next index: %v", err)
	}
	a := &domain.Address{
		ID: uuid.New(), WalletID: w.ID, Chain: w.Chain,
		Address: "0x" + uuid.NewString()[:8], DerivationPath: "m/44'/60'/0'/0/0",
		Index: idx, State: domain.AddressStateActive, CreatedAt: time.Now().UTC(),
	}
	if err := st.InsertAddress(ctx, a); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// The partial unique index must reject a second active address.
	dup := *a
	dup.ID = uuid.New()
	dup.Address = "0x" + uuid.NewString()[:8]
	dup.Index = idx + 1
	if err := st.InsertAddress(ctx, &dup); err == nil {
		t.Error("expected second active address to violate one-active-per-wallet index")
	}

	active, err := st.GetActiveAddress(ctx, w.ID)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active.ID != a.ID {
		t.Errorf("active mismatch: %s vs %s", active.ID, a.ID)
	}

	if err := st.IncrementReceiveCount(ctx, a.ID); err != nil {
		t.Fatalf("increment receives: %v", err)
	}
	if err := st.DeprecateAddress(ctx, a.ID); err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	got, err := st.GetAddress(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.AddressStateDeprecated || got.ReceiveCount != 1 {
		t.Errorf("expected deprecated with 1 receive, got %+v", got)
	}

	next, err := st.NextAddressIndex(ctx, string(domain.ChainEthereum), 0)
	if err != nil {
		t.Fatal(err)
	}
	if next <= idx {
		t.Errorf("index not monotonic: %d then %d", idx, next)
	}
}

func TestBalanceUpsertAndEventDedup(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	b := &storage.Balance{WalletID: w.ID, Asset: "ETH", Confirmed: "1.5", Pending: "0.5", Locked: "0", LastBlockSeen: 100}
	if err := st.UpsertBalance(ctx, b); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	b.Confirmed = "2.0"
	if err := st.UpsertBalance(ctx, b); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err := st.GetBalance(ctx, w.ID, "ETH")
	if err != nil {
		t.Fatal(err)
	}
	// numeric(38,18) round-trips with trailing zeros; compare numerically.
	if v, err := strconv.ParseFloat(got.Confirmed, 64); err != nil || v != 2.0 {
		t.Errorf("expected confirmed 2.0, got %s (%v)", got.Confirmed, err)
	}

	ev := &storage.BalanceEvent{ID: uuid.New(), WalletID: w.ID, Asset: "ETH", BlockHeight: 101, EventID: "ev-1"}
	if err := st.RecordBalanceEvent(ctx, ev); err != nil {
		t.Fatalf("record event: %v", err)
	}
	dup := &storage.BalanceEvent{ID: uuid.New(), WalletID: w.ID, Asset: "ETH", BlockHeight: 101, EventID: "ev-1"}
	if err := st.RecordBalanceEvent(ctx, dup); !errors.Is(err, storage.ErrDuplicateEvent) {
		t.Errorf("expected ErrDuplicateEvent, got %v", err)
	}
}

func TestUTXOFlow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainBitcoin, domain.WalletTypeHot)

	op1 := uuid.NewString() + ":0"
	op2 := uuid.NewString() + ":1"
	for _, op := range []string{op1, op2} {
		u := &storage.UTXO{Outpoint: op, WalletID: w.ID, Value: "0.1", ScriptType: "p2wpkh", Confirmations: 6, LockState: "free"}
		if err := st.InsertUTXO(ctx, u); err != nil {
			t.Fatalf("insert utxo: %v", err)
		}
	}
	free, err := st.ListFreeUTXOs(ctx, w.ID)
	if err != nil || len(free) != 2 {
		t.Fatalf("expected 2 free, got %d (%v)", len(free), err)
	}

	if err := st.LockUTXOs(ctx, []string{op1}); err != nil {
		t.Fatalf("lock: %v", err)
	}
	free, _ = st.ListFreeUTXOs(ctx, w.ID)
	if len(free) != 1 {
		t.Fatalf("expected 1 free after lock, got %d", len(free))
	}

	if err := st.MarkUTXOsSpent(ctx, []string{op1}, "txhash-1"); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if err := st.RestoreUTXOs(ctx, []string{op1}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	free, _ = st.ListFreeUTXOs(ctx, w.ID)
	if len(free) != 2 {
		t.Fatalf("expected 2 free after reorg restore, got %d", len(free))
	}

	if err := st.PruneUTXOs(ctx, []string{op1, op2}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	free, _ = st.ListFreeUTXOs(ctx, w.ID)
	if len(free) != 0 {
		t.Fatalf("expected 0 free after prune, got %d", len(free))
	}
}

func TestNonceIncrementConcurrent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	if err := st.UpsertNonce(ctx, &storage.Nonce{WalletID: w.ID, Chain: "ethereum"}); err != nil {
		t.Fatalf("seed nonce: %v", err)
	}

	const workers = 10
	results := make(chan int64, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Retry on serialization conflicts: concurrency control is the
			// point of the test, and callers hold a Redis lock in production.
			for {
				n, _, err := st.IncrementPendingNonce(ctx, w.ID, "ethereum")
				if err == nil {
					results <- n
					return
				}
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := map[int64]bool{}
	for n := range results {
		if seen[n] {
			t.Errorf("duplicate nonce %d", n)
		}
		seen[n] = true
	}
	if len(seen) != workers {
		t.Fatalf("expected %d distinct nonces, got %d", workers, len(seen))
	}

	// Committing nonce N records N+1 as the next expected broadcast nonce.
	if err := st.AdvanceBroadcastNonce(ctx, w.ID, "ethereum", 3); err != nil {
		t.Fatalf("advance broadcast: %v", err)
	}
	got, err := st.GetNonce(ctx, w.ID, "ethereum")
	if err != nil {
		t.Fatal(err)
	}
	if got.BroadcastNonce != 4 {
		t.Errorf("expected broadcast nonce 4, got %d", got.BroadcastNonce)
	}
}

func TestWithdrawalInflightDedupIndex(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	mk := func() *storage.WithdrawalRequest {
		return &storage.WithdrawalRequest{
			ID: uuid.New(), WalletID: w.ID, ToAddress: "0xdead", Asset: "ETH",
			Amount: "1", State: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	first := mk()
	if err := st.CreateWithdrawal(ctx, first); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.CreateWithdrawal(ctx, mk()); err == nil {
		t.Error("expected in-flight dedup index to reject duplicate withdrawal")
	}

	// Once the first reaches a terminal state, a new identical request is allowed.
	if err := st.UpdateWithdrawalState(ctx, first.ID, "failed", "test", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWithdrawal(ctx, mk()); err != nil {
		t.Errorf("expected new withdrawal after terminal state, got %v", err)
	}
}

func TestFundingOpenDedupIndex(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	mk := func() *storage.FundingRequest {
		return &storage.FundingRequest{
			ID: uuid.New(), WalletID: w.ID, Asset: "ETH", Amount: "100",
			State: "requested", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	first := mk()
	if err := st.CreateFundingRequest(ctx, first); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.CreateFundingRequest(ctx, mk()); err == nil {
		t.Error("expected open-funding dedup index to reject duplicate request")
	}

	open, err := st.GetOpenFundingRequest(ctx, w.ID, "ETH")
	if err != nil || open.ID != first.ID {
		t.Fatalf("open request mismatch: %v %v", open, err)
	}

	if err := st.UpdateFundingState(ctx, first.ID, "settled", "batch-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateFundingRequest(ctx, mk()); err != nil {
		t.Errorf("expected new request after settle, got %v", err)
	}
}

func TestKeyMappingRotation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	if err := st.BindKeyMapping(ctx, &storage.KeyMapping{
		WalletID: w.ID, KeyID: "key-old", ActiveFrom: time.Now().UTC(),
		RotationState: "current", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if err := st.RotateKeyMapping(ctx, w.ID, "key-new", time.Hour); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	keys, err := st.ResolveActiveKey(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, k := range keys {
		states[k.KeyID] = k.RotationState
	}
	if states["key-new"] != "current" || states["key-old"] != "cooling" {
		t.Errorf("expected new=current old=cooling, got %v", states)
	}

	// A cooling window already in the past expires on the next sweep. Negative
	// rather than zero so client/DB clock skew cannot leave active_to > now().
	if err := st.RotateKeyMapping(ctx, w.ID, "key-newer", -time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpireCooling(ctx); err != nil {
		t.Fatal(err)
	}
	keys, _ = st.ResolveActiveKey(ctx, w.ID)
	for _, k := range keys {
		if k.KeyID == "key-new" && k.RotationState == "cooling" {
			t.Error("expected key-new cooling mapping to be expired")
		}
	}
}

func TestAuditOutbox(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := newWallet(t, st, domain.ChainEthereum, domain.WalletTypeHot)

	seq1, err := st.NextAuditSeq(ctx, w.ID)
	if err != nil {
		t.Fatalf("next seq: %v", err)
	}
	seq2, err := st.NextAuditSeq(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seq2 <= seq1 {
		t.Errorf("audit seq not monotonic: %d then %d", seq1, seq2)
	}

	ev := &storage.AuditOutboxEvent{
		ID: uuid.New(), EventID: uuid.New(), WalletID: &w.ID,
		EventType: "wallet.created", Payload: []byte(`{"v":1}`), Seq: seq1, CreatedAt: time.Now().UTC(),
	}
	if err := st.AppendAuditEvent(ctx, ev); err != nil {
		t.Fatalf("append: %v", err)
	}

	// event_id is unique — redelivery inserts must be rejected.
	dup := *ev
	dup.ID = uuid.New()
	dup.Seq = seq2
	if err := st.AppendAuditEvent(ctx, &dup); err == nil {
		t.Error("expected duplicate event_id to be rejected")
	}

	undelivered, err := st.ListUndeliveredAuditEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range undelivered {
		if e.EventID == ev.EventID {
			found = true
		}
	}
	if !found {
		t.Fatal("appended event not listed as undelivered")
	}

	if err := st.MarkAuditDelivered(ctx, ev.ID); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	undelivered, _ = st.ListUndeliveredAuditEvents(ctx, 100)
	for _, e := range undelivered {
		if e.EventID == ev.EventID {
			t.Error("delivered event still listed as undelivered")
		}
	}
}

func TestInTxRollback(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := &domain.Wallet{
		ID: uuid.New(), Chain: domain.ChainEthereum, Type: domain.WalletTypeHot,
		Label: "rollback", State: domain.WalletStateActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	sentinel := errors.New("boom")
	err := st.InTx(ctx, func(ctx context.Context) error {
		if err := st.CreateWallet(ctx, w); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if _, err := st.GetWallet(ctx, w.ID); err == nil {
		t.Error("expected wallet insert to be rolled back")
	}
}
