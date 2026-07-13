package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/google/uuid"
)

func TestNoopEmitter(t *testing.T) {
	if err := (NoopEmitter{}).Emit(context.Background(), &Event{EventType: "x"}); err != nil {
		t.Errorf("noop emitter should never error, got %v", err)
	}
}

func TestEmitWithinTxRollback(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	ctx := context.Background()
	wID := uuid.New()
	// emit inside a tx that rolls back -> no event persisted
	err := st.InTx(ctx, func(ctx context.Context) error {
		if err := em.Emit(ctx, &Event{EventType: "wallet.created", WalletID: &wID}); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected boom error")
	}
	undel, _ := st.ListUndeliveredAuditEvents(ctx, 10)
	if len(undel) != 0 {
		t.Errorf("expected 0 events after rollback, got %d", len(undel))
	}
}

func TestEmitWithinTxCommit(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	ctx := context.Background()
	wID := uuid.New()
	if err := st.InTx(ctx, func(ctx context.Context) error {
		return em.Emit(ctx, &Event{EventType: "wallet.created", WalletID: &wID})
	}); err != nil {
		t.Fatal(err)
	}
	undel, _ := st.ListUndeliveredAuditEvents(ctx, 10)
	if len(undel) != 1 {
		t.Errorf("expected 1 event after commit, got %d", len(undel))
	}
}

func TestEmitNoWalletID(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	if err := em.Emit(context.Background(), &Event{EventType: "system"}); err != nil {
		t.Fatal(err)
	}
	undel, _ := st.ListUndeliveredAuditEvents(context.Background(), 10)
	if len(undel) != 1 {
		t.Errorf("expected 1 event, got %d", len(undel))
	}
}

func TestEmitMarshalFailure(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	// a payload that cannot be marshaled (e.g. a channel) forces json.Marshal error
	if err := em.Emit(context.Background(), &Event{EventType: "bad", Payload: map[string]any{"ch": make(chan int)}}); err == nil {
		t.Error("expected marshal error")
	}
}

func TestDrainRetry(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	wID := uuid.New()
	em := NewEmitter(st, &failingSink{failN: 1})
	_ = em.Emit(ctx, &Event{EventType: "e1", WalletID: &wID})
	// first drain fails (sink returns error) -> events stay undelivered
	if err := em.Drain(ctx); err == nil {
		t.Error("expected first drain to fail")
	}
	undel, _ := st.ListUndeliveredAuditEvents(ctx, 10)
	if len(undel) != 1 {
		t.Fatalf("expected event to remain undelivered, got %d", len(undel))
	}
	// second drain succeeds
	if err := em.Drain(ctx); err != nil {
		t.Fatalf("expected second drain to succeed: %v", err)
	}
	undel, _ = st.ListUndeliveredAuditEvents(ctx, 10)
	if len(undel) != 0 {
		t.Errorf("expected 0 undelivered after retry, got %d", len(undel))
	}
}

func TestDrainEmpty(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	if err := em.Drain(context.Background()); err != nil {
		t.Errorf("drain of empty outbox should be nil, got %v", err)
	}
}

func TestDrainNoSink(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	wID := uuid.New()
	_ = em.Emit(context.Background(), &Event{EventType: "x", WalletID: &wID})
	// no sink set: drain should mark delivered without delivering
	if err := em.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	undel, _ := st.ListUndeliveredAuditEvents(context.Background(), 10)
	if len(undel) != 0 {
		t.Errorf("expected 0 undelivered, got %d", len(undel))
	}
}

func TestOrderingPerWallet(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	wID := uuid.New()
	em := NewEmitter(st, nil)
	for i := 0; i < 5; i++ {
		if err := em.Emit(ctx, &Event{EventType: "e", WalletID: &wID}); err != nil {
			t.Fatal(err)
		}
	}
	undel, _ := st.ListUndeliveredAuditEvents(ctx, 10)
	for i, ev := range undel {
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d: expected seq %d, got %d", i, i+1, ev.Seq)
		}
	}
}

func TestDedupByEventID(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	ctx := context.Background()
	wID := uuid.New()
	_ = em.Emit(ctx, &Event{EventType: "e1", WalletID: &wID})
	// the same Emit call always generates a new EventID (uuid.New), so to test
	// dedup we inject an event with a fixed EventID directly via the store.
	fixedEventID := uuid.New()
	_ = st.AppendAuditEvent(ctx, &storage.AuditOutboxEvent{
		ID: uuid.New(), EventID: fixedEventID, WalletID: &wID, EventType: "manual", Seq: 99,
	})
	if err := st.AppendAuditEvent(ctx, &storage.AuditOutboxEvent{
		ID: uuid.New(), EventID: fixedEventID, WalletID: &wID, EventType: "manual", Seq: 100,
	}); !errors.Is(err, storage.ErrDuplicateAudit) {
		t.Errorf("expected ErrDuplicateAudit, got %v", err)
	}
}

func TestChannelSink(t *testing.T) {
	ch := make(chan *storage.AuditOutboxEvent, 2)
	sink := &ChannelSink{Ch: ch}
	e1 := &storage.AuditOutboxEvent{ID: uuid.New(), EventType: "a"}
	e2 := &storage.AuditOutboxEvent{ID: uuid.New(), EventType: "b"}
	if err := sink.Deliver(context.Background(), []*storage.AuditOutboxEvent{e1, e2}); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got.ID != e1.ID {
		t.Errorf("expected e1 first, got %+v", got)
	}
	if got := <-ch; got.ID != e2.ID {
		t.Errorf("expected e2 second, got %+v", got)
	}
}

func TestStartStopBackground(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	wID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	em.Start(ctx, 10*time.Millisecond)
	// emit a few events
	for i := 0; i < 3; i++ {
		_ = em.Emit(ctx, &Event{EventType: "e", WalletID: &wID})
	}
	// stop the drainer
	em.Stop()
	cancel()
	// Stop is idempotent
	em.Stop()
}

func TestConcurrentEmitSeqUnique(t *testing.T) {
	st := memstore.New()
	em := NewEmitter(st, nil)
	ctx := context.Background()
	wID := uuid.New()
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := em.Emit(ctx, &Event{EventType: "e", WalletID: &wID}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("emit error: %v", err)
	}
	undel, _ := st.ListUndeliveredAuditEvents(ctx, 1000)
	if len(undel) != n {
		t.Errorf("expected %d events, got %d", n, len(undel))
	}
	seen := map[int64]bool{}
	for _, ev := range undel {
		if seen[ev.Seq] {
			t.Errorf("duplicate seq %d", ev.Seq)
		}
		seen[ev.Seq] = true
	}
}

type failingSink struct {
	failN int
	calls int
}

func (s *failingSink) Deliver(_ context.Context, _ []*storage.AuditOutboxEvent) error {
	s.calls++
	if s.calls <= s.failN {
		return errors.New("transient sink failure")
	}
	return nil
}