package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/domain"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/google/uuid"
)

func ctx() context.Context { return context.Background() }

func TestWalletCRUD(t *testing.T) {
	s := New()
	w := &domain.Wallet{ID: uuid.New(), Chain: domain.ChainEthereum, Type: domain.WalletTypeHot, Label: "w1", State: domain.WalletStateActive, KeyID: "k1"}
	if err := s.CreateWallet(ctx(), w); err != nil {
		t.Fatal(err)
	}
	// duplicate
	if err := s.CreateWallet(ctx(), &domain.Wallet{ID: w.ID, Chain: domain.ChainBitcoin, Type: domain.WalletTypeHot, Label: "dup"}); err == nil {
		t.Error("expected duplicate wallet error")
	}
	got, err := s.GetWallet(ctx(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "w1" {
		t.Errorf("expected w1, got %s", got.Label)
	}
	// not found
	if _, err := s.GetWallet(ctx(), uuid.New()); err == nil {
		t.Error("expected not found error")
	}
	if err := s.UpdateWalletState(ctx(), w.ID, domain.WalletStatePaused); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetWallet(ctx(), w.ID)
	if got.State != domain.WalletStatePaused {
		t.Errorf("expected paused, got %s", got.State)
	}
	if err := s.UpdateWalletState(ctx(), uuid.New(), domain.WalletStateActive); err == nil {
		t.Error("expected not found on update missing wallet")
	}
}

func TestListWalletsFilters(t *testing.T) {
	s := New()
	w1 := &domain.Wallet{ID: uuid.New(), Chain: domain.ChainEthereum, Type: domain.WalletTypeHot, Label: "a", State: domain.WalletStateActive}
	w2 := &domain.Wallet{ID: uuid.New(), Chain: domain.ChainBitcoin, Type: domain.WalletTypeCold, Label: "b", State: domain.WalletStateRetired}
	for _, w := range []*domain.Wallet{w1, w2} {
		if err := s.CreateWallet(ctx(), w); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListWallets(ctx(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 wallets, got %d", len(all))
	}
	eth, _ := s.ListWallets(ctx(), "ethereum", "", "")
	if len(eth) != 1 || eth[0].ID != w1.ID {
		t.Errorf("chain filter wrong: %+v", eth)
	}
	cold, _ := s.ListWallets(ctx(), "", "COLD", "")
	if len(cold) != 1 || cold[0].ID != w2.ID {
		t.Errorf("type filter wrong: %+v", cold)
	}
	retired, _ := s.ListWallets(ctx(), "", "", "RETIRED")
	if len(retired) != 1 || retired[0].ID != w2.ID {
		t.Errorf("state filter wrong: %+v", retired)
	}
	none, _ := s.ListWallets(ctx(), "ethereum", "COLD", "")
	if len(none) != 0 {
		t.Errorf("expected 0, got %d", len(none))
	}
}

func TestAddressOps(t *testing.T) {
	s := New()
	wID := uuid.New()
	a1 := &domain.Address{ID: uuid.New(), WalletID: wID, Chain: domain.ChainEthereum, Address: "0x1", Index: 0, Change: 0, State: domain.AddressStateActive}
	if err := s.InsertAddress(ctx(), a1); err != nil {
		t.Fatal(err)
	}
	// second active for same wallet -> error
	if err := s.InsertAddress(ctx(), &domain.Address{ID: uuid.New(), WalletID: wID, Address: "0x2", State: domain.AddressStateActive}); err == nil {
		t.Error("expected duplicate active address error")
	}
	// deprecated can coexist
	a2 := &domain.Address{ID: uuid.New(), WalletID: wID, Chain: domain.ChainEthereum, Address: "0xold", Index: 1, State: domain.AddressStateDeprecated}
	if err := s.InsertAddress(ctx(), a2); err != nil {
		t.Fatal(err)
	}
	// duplicate id
	if err := s.InsertAddress(ctx(), &domain.Address{ID: a1.ID, WalletID: wID}); err == nil {
		t.Error("expected duplicate id error")
	}
	got, err := s.GetActiveAddress(ctx(), wID)
	if err != nil || got.ID != a1.ID {
		t.Errorf("active address mismatch: %v %v", got, err)
	}
	if err := s.DeprecateAddress(ctx(), a1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetActiveAddress(ctx(), wID); err == nil {
		t.Error("expected no active after deprecate")
	}
	if err := s.DeprecateAddress(ctx(), uuid.New()); err == nil {
		t.Error("expected not found on deprecate missing")
	}
	if _, err := s.GetAddress(ctx(), uuid.New()); err == nil {
		t.Error("expected not found on get missing address")
	}
	list, _ := s.ListAddresses(ctx(), wID)
	if len(list) != 2 {
		t.Errorf("expected 2 addresses, got %d", len(list))
	}
	idx, _ := s.NextAddressIndex(ctx(), string(domain.ChainEthereum), 0)
	if idx != 2 {
		t.Errorf("expected next index 2, got %d", idx)
	}
	if err := s.IncrementReceiveCount(ctx(), a2.ID); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetAddress(ctx(), a2.ID)
	if got2.ReceiveCount != 1 {
		t.Errorf("expected receive count 1, got %d", got2.ReceiveCount)
	}
	if err := s.IncrementReceiveCount(ctx(), uuid.New()); err == nil {
		t.Error("expected not found on increment missing")
	}
}

func TestBalanceOps(t *testing.T) {
	s := New()
	wID := uuid.New()
	b := &storage.Balance{WalletID: wID, Asset: "eth", Confirmed: "100", Pending: "50", Locked: "10"}
	if err := s.UpsertBalance(ctx(), b); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetBalance(ctx(), wID, "eth")
	if got.Confirmed != "100" {
		t.Errorf("expected 100, got %s", got.Confirmed)
	}
	if _, err := s.GetBalance(ctx(), uuid.New(), "eth"); err == nil {
		t.Error("expected not found on get balance")
	}
	list, _ := s.ListBalances(ctx(), wID)
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}
	// add second asset
	_ = s.UpsertBalance(ctx(), &storage.Balance{WalletID: wID, Asset: "usdc", Confirmed: "200"})
	list, _ = s.ListBalances(ctx(), wID)
	if len(list) != 2 {
		t.Errorf("expected 2 assets, got %d", len(list))
	}
	if list[0].Asset != "eth" || list[1].Asset != "usdc" {
		t.Errorf("expected sorted, got %s %s", list[0].Asset, list[1].Asset)
	}
}

func TestBalanceEventDedup(t *testing.T) {
	s := New()
	wID := uuid.New()
	e := &storage.BalanceEvent{ID: uuid.New(), WalletID: wID, Asset: "eth", BlockHeight: 10, EventID: "ev1"}
	if err := s.RecordBalanceEvent(ctx(), e); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBalanceEvent(ctx(), &storage.BalanceEvent{ID: uuid.New(), WalletID: wID, Asset: "eth", BlockHeight: 10, EventID: "ev1"}); !errors.Is(err, storage.ErrDuplicateEvent) {
		t.Errorf("expected ErrDuplicateEvent, got %v", err)
	}
}

func TestUTXOOps(t *testing.T) {
	s := New()
	wID := uuid.New()
	u1 := &storage.UTXO{Outpoint: "op1", WalletID: wID, Value: "100", LockState: "FREE"}
	u2 := &storage.UTXO{Outpoint: "op2", WalletID: wID, Value: "200", LockState: "FREE"}
	for _, u := range []*storage.UTXO{u1, u2} {
		if err := s.InsertUTXO(ctx(), u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertUTXO(ctx(), &storage.UTXO{Outpoint: "op1"}); err == nil {
		t.Error("expected duplicate utxo error")
	}
	free, _ := s.ListFreeUTXOs(ctx(), wID)
	if len(free) != 2 {
		t.Fatalf("expected 2 free, got %d", len(free))
	}
	if err := s.LockUTXOs(ctx(), []string{"op1", "op2"}); err != nil {
		t.Fatal(err)
	}
	free, _ = s.ListFreeUTXOs(ctx(), wID)
	if len(free) != 0 {
		t.Errorf("expected 0 free after lock, got %d", len(free))
	}
	// locking already-locked fails
	if err := s.LockUTXOs(ctx(), []string{"op1"}); err == nil {
		t.Error("expected lock failure on locked utxo")
	}
	// locking nonexistent fails
	if err := s.LockUTXOs(ctx(), []string{"opX"}); err == nil {
		t.Error("expected lock failure on missing utxo")
	}
	if err := s.MarkUTXOsSpent(ctx(), []string{"op1", "op2"}, "txhash1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUTXOsSpent(ctx(), []string{"opX"}, "h"); err == nil {
		t.Error("expected error marking missing utxo spent")
	}
	if err := s.RestoreUTXOs(ctx(), []string{"op1", "op2"}); err != nil {
		t.Fatal(err)
	}
	free, _ = s.ListFreeUTXOs(ctx(), wID)
	if len(free) != 2 {
		t.Errorf("expected 2 free after restore, got %d", len(free))
	}
	if err := s.RestoreUTXOs(ctx(), []string{"opX"}); err == nil {
		t.Error("expected error restoring missing utxo")
	}
	if err := s.PruneUTXOs(ctx(), []string{"op1"}); err != nil {
		t.Fatal(err)
	}
	free, _ = s.ListFreeUTXOs(ctx(), wID)
	if len(free) != 1 || free[0].Outpoint != "op2" {
		t.Errorf("expected only op2 left, got %+v", free)
	}
}

func TestNonceOps(t *testing.T) {
	s := New()
	wID := uuid.New()
	// GetNonce returns zero-value when absent
	n, _ := s.GetNonce(ctx(), wID, "ethereum")
	if n.PendingNonce != 0 || n.BroadcastNonce != 0 {
		t.Errorf("expected zero nonce, got %+v", n)
	}
	v1, ver1, err := s.IncrementPendingNonce(ctx(), wID, "ethereum")
	if err != nil {
		t.Fatal(err)
	}
	if v1 != 0 || ver1 != 1 {
		t.Errorf("expected 0/1, got %d/%d", v1, ver1)
	}
	v2, ver2, _ := s.IncrementPendingNonce(ctx(), wID, "ethereum")
	if v2 != 1 || ver2 != 2 {
		t.Errorf("expected 1/2, got %d/%d", v2, ver2)
	}
	if err := s.AdvanceBroadcastNonce(ctx(), wID, "ethereum", 1); err != nil {
		t.Fatal(err)
	}
	n, _ = s.GetNonce(ctx(), wID, "ethereum")
	if n.BroadcastNonce != 2 {
		t.Errorf("expected broadcast 2, got %d", n.BroadcastNonce)
	}
	// advance with smaller value is a no-op
	_ = s.AdvanceBroadcastNonce(ctx(), wID, "ethereum", 0)
	n, _ = s.GetNonce(ctx(), wID, "ethereum")
	if n.BroadcastNonce != 2 {
		t.Errorf("broadcast should remain 2, got %d", n.BroadcastNonce)
	}
	if err := s.UpsertNonce(ctx(), &storage.Nonce{WalletID: wID, Chain: "polygon", PendingNonce: 5, BroadcastNonce: 3, Version: 1}); err != nil {
		t.Fatal(err)
	}
	n, _ = s.GetNonce(ctx(), wID, "polygon")
	if n.PendingNonce != 5 {
		t.Errorf("expected 5, got %d", n.PendingNonce)
	}
}

func TestWithdrawalOps(t *testing.T) {
	s := New()
	wr := &storage.WithdrawalRequest{ID: uuid.New(), WalletID: uuid.New(), ToAddress: "0xto", Asset: "eth", Amount: "10", State: "PENDING"}
	if err := s.CreateWithdrawal(ctx(), wr); err != nil {
		t.Fatal(err)
	}
	// duplicate inflight
	if err := s.CreateWithdrawal(ctx(), &storage.WithdrawalRequest{ID: uuid.New(), WalletID: wr.WalletID, ToAddress: "0xto", Asset: "eth", Amount: "10"}); !errors.Is(err, storage.ErrDuplicateWithdrawal) {
		t.Errorf("expected ErrDuplicateWithdrawal, got %v", err)
	}
	if err := s.CreateWithdrawal(ctx(), &storage.WithdrawalRequest{ID: wr.ID}); err == nil {
		t.Error("expected duplicate id error")
	}
	got, _ := s.GetWithdrawal(ctx(), wr.ID)
	if got.State != "PENDING" {
		t.Errorf("expected pending, got %s", got.State)
	}
	if _, err := s.GetWithdrawal(ctx(), uuid.New()); err == nil {
		t.Error("expected not found")
	}
	if err := s.UpdateWithdrawalState(ctx(), wr.ID, "WHITELISTED", "", "", "dec1"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetWithdrawal(ctx(), wr.ID)
	if got.State != "WHITELISTED" || got.PolicyDecisionID != "dec1" {
		t.Errorf("mismatch: %+v", got)
	}
	if err := s.UpdateWithdrawalState(ctx(), wr.ID, "CONFIRMED", "r", "txh", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetWithdrawal(ctx(), wr.ID)
	if got.State != "CONFIRMED" || got.TxHash != "txh" || got.FailureReason != "r" {
		t.Errorf("mismatch: %+v", got)
	}
	// inflight dedup should be released now
	if err := s.CreateWithdrawal(ctx(), &storage.WithdrawalRequest{ID: uuid.New(), WalletID: wr.WalletID, ToAddress: "0xto", Asset: "eth", Amount: "10"}); err != nil {
		t.Errorf("expected re-create after terminal, got %v", err)
	}
	if err := s.UpdateWithdrawalState(ctx(), uuid.New(), "x", "", "", ""); err == nil {
		t.Error("expected not found on update missing withdrawal")
	}
	if err := s.UpdateWithdrawalNonce(ctx(), wr.ID, 7); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetWithdrawal(ctx(), wr.ID)
	if got.NonceValue == nil || *got.NonceValue != 7 {
		t.Error("nonce value not set")
	}
	if err := s.UpdateWithdrawalNonce(ctx(), uuid.New(), 1); err == nil {
		t.Error("expected not found on update missing withdrawal nonce")
	}
}

func TestKeyMappingOps(t *testing.T) {
	s := New()
	wID := uuid.New()
	if err := s.BindKeyMapping(ctx(), &storage.KeyMapping{WalletID: wID, KeyID: "k1", RotationState: "CURRENT", ActiveFrom: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// second current fails
	if err := s.BindKeyMapping(ctx(), &storage.KeyMapping{WalletID: wID, KeyID: "k2", RotationState: "CURRENT"}); err == nil {
		t.Error("expected duplicate current error")
	}
	// cooling is allowed
	if err := s.BindKeyMapping(ctx(), &storage.KeyMapping{WalletID: wID, KeyID: "k1old", RotationState: "COOLING"}); err != nil {
		t.Fatal(err)
	}
	ms, err := s.ResolveActiveKey(ctx(), wID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Errorf("expected 2 active, got %d", len(ms))
	}
	// no mappings
	if _, err := s.ResolveActiveKey(ctx(), uuid.New()); err == nil {
		t.Error("expected not found")
	}
	// rotate: k1 -> cooling, k1new -> current
	if err := s.RotateKeyMapping(ctx(), wID, "k1new", time.Hour); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.ResolveActiveKey(ctx(), wID)
	var hasCurrent, hasCooling bool
	for _, m := range ms {
		if m.KeyID == "k1new" && m.RotationState == "CURRENT" {
			hasCurrent = true
		}
		if m.KeyID == "k1" && m.RotationState == "COOLING" {
			hasCooling = true
		}
	}
	if !hasCurrent || !hasCooling {
		t.Errorf("rotation state wrong: %+v", ms)
	}
	// rotate to an existing key promotes it
	if err := s.RotateKeyMapping(ctx(), wID, "k1new", time.Hour); err != nil {
		t.Fatal(err)
	}
	// expire cooling (set activeTo in past)
	for _, m := range ms {
		if m.RotationState == "COOLING" {
			past := time.Now().Add(-time.Hour)
			m.ActiveTo = &past
		}
	}
	if err := s.ExpireCooling(ctx()); err != nil {
		t.Fatal(err)
	}
	// rotate again with negative cooling so activeTo is in the past, then expire
	if err := s.RotateKeyMapping(ctx(), wID, "k1new2", -time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireCooling(ctx()); err != nil {
		t.Fatal(err)
	}
}

func TestFundingRequestOps(t *testing.T) {
	s := New()
	wID := uuid.New()
	f1 := &storage.FundingRequest{ID: uuid.New(), WalletID: wID, Asset: "usdc", State: "REQUESTED"}
	if err := s.CreateFundingRequest(ctx(), f1); err != nil {
		t.Fatal(err)
	}
	// duplicate open
	if err := s.CreateFundingRequest(ctx(), &storage.FundingRequest{ID: uuid.New(), WalletID: wID, Asset: "usdc", State: "REQUESTED"}); !errors.Is(err, storage.ErrDuplicateFunding) {
		t.Errorf("expected ErrDuplicateFunding, got %v", err)
	}
	// different asset ok
	if err := s.CreateFundingRequest(ctx(), &storage.FundingRequest{ID: uuid.New(), WalletID: wID, Asset: "eth", State: "REQUESTED"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetOpenFundingRequest(ctx(), wID, "usdc")
	if got.ID != f1.ID {
		t.Error("open funding request mismatch")
	}
	if _, err := s.GetOpenFundingRequest(ctx(), wID, "btc"); err == nil {
		t.Error("expected not found on missing open request")
	}
	if err := s.UpdateFundingState(ctx(), f1.ID, "APPROVED", "batch1"); err != nil {
		t.Fatal(err)
	}
	// now no open 'requested' for usdc
	if _, err := s.GetOpenFundingRequest(ctx(), wID, "usdc"); err == nil {
		t.Error("expected no open after approved")
	}
	if err := s.UpdateFundingState(ctx(), uuid.New(), "APPROVED", ""); err == nil {
		t.Error("expected not found on update missing funding")
	}
}

func TestAuditOutboxOps(t *testing.T) {
	s := New()
	wID := uuid.New()
	e1 := &storage.AuditOutboxEvent{ID: uuid.New(), EventID: uuid.New(), WalletID: &wID, EventType: "x", Seq: 1}
	e2 := &storage.AuditOutboxEvent{ID: uuid.New(), EventID: uuid.New(), WalletID: &wID, EventType: "y", Seq: 2}
	if err := s.AppendAuditEvent(ctx(), e1); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAuditEvent(ctx(), e2); err != nil {
		t.Fatal(err)
	}
	// duplicate event id
	if err := s.AppendAuditEvent(ctx(), &storage.AuditOutboxEvent{ID: uuid.New(), EventID: e1.EventID}); !errors.Is(err, storage.ErrDuplicateAudit) {
		t.Errorf("expected ErrDuplicateAudit, got %v", err)
	}
	list, _ := s.ListUndeliveredAuditEvents(ctx(), 10)
	if len(list) != 2 || list[0].Seq != 1 || list[1].Seq != 2 {
		t.Errorf("expected ordered list, got %+v", list)
	}
	// limit
	lim, _ := s.ListUndeliveredAuditEvents(ctx(), 1)
	if len(lim) != 1 || lim[0].Seq != 1 {
		t.Errorf("limit wrong: %+v", lim)
	}
	if err := s.MarkAuditDelivered(ctx(), e1.ID); err != nil {
		t.Fatal(err)
	}
	undel, _ := s.ListUndeliveredAuditEvents(ctx(), 10)
	if len(undel) != 1 || undel[0].ID != e2.ID {
		t.Errorf("expected only e2 undelivered, got %+v", undel)
	}
	if err := s.MarkAuditDelivered(ctx(), uuid.New()); err == nil {
		t.Error("expected not found on mark missing")
	}
}

func TestNextAuditSeq(t *testing.T) {
	s := New()
	wID := uuid.New()
	s1, _ := s.NextAuditSeq(ctx(), wID)
	s2, _ := s.NextAuditSeq(ctx(), wID)
	if s1 != 1 || s2 != 2 {
		t.Errorf("expected 1,2 got %d,%d", s1, s2)
	}
	// different wallet independent
	s3, _ := s.NextAuditSeq(ctx(), uuid.New())
	if s3 != 1 {
		t.Errorf("expected 1 for new wallet, got %d", s3)
	}
}

func TestInTxRollback(t *testing.T) {
	s := New()
	called := false
	err := s.InTx(ctx(), func(ctx context.Context) error {
		called = true
		return errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Errorf("expected boom error, got %v", err)
	}
	if !called {
		t.Error("fn was not called")
	}
}

func TestApplyMigrationsNoop(t *testing.T) {
	s := New()
	if err := s.ApplyMigrations(ctx(), "anything"); err != nil {
		t.Errorf("ApplyMigrations should be noop, got %v", err)
	}
	if s.DB() != nil {
		t.Error("DB() should be nil for memstore")
	}
}