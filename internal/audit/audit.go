// Package audit implements the outbox-pattern audit event emitter.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/google/uuid"
)

// Event is a domain event to be recorded in the audit log.
type Event struct {
	EventType string
	WalletID  *uuid.UUID
	Payload   map[string]any
}

// Emitter records and (asynchronously) delivers audit events.
type Emitter interface {
	Emit(ctx context.Context, e *Event) error
}

// NoopEmitter does nothing; used when audit is disabled.
type NoopEmitter struct{}

// Emit satisfies Emitter.
func (NoopEmitter) Emit(_ context.Context, _ *Event) error { return nil }

// Sink is the destination for delivered audit events (e.g. the Audit Event Log).
type Sink interface {
	Deliver(ctx context.Context, events []*storage.AuditOutboxEvent) error
}

// HTTPSink POSTs events as a JSON array to the audit event log URL.
type HTTPSink struct {
	URL    string
	Client doer
}

type doer interface {
	Do(req interface{}) (interface{}, error)
}

// EmitterService is the production Emitter: writes to the outbox within the
// caller's transaction, and drains the outbox via a background worker.
type EmitterService struct {
	Store storage.Store
	Sink  Sink

	mu      sync.Mutex
	stopped bool
	stopCh  chan struct{}
}

// NewEmitter constructs an EmitterService.
func NewEmitter(st storage.Store, sink Sink) *EmitterService {
	return &EmitterService{Store: st, Sink: sink, stopCh: make(chan struct{})}
}

// Emit persists the event to the audit outbox.
func (e *EmitterService) Emit(ctx context.Context, ev *Event) error {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return fmt.Errorf("marshal audit payload: %w", err)
	}
	var wID *uuid.UUID
	var seq int64
	if ev.WalletID != nil {
		wID = ev.WalletID
		seq, err = e.Store.NextAuditSeq(ctx, *wID)
		if err != nil {
			return fmt.Errorf("next audit seq: %w", err)
		}
	} else {
		seq = time.Now().UnixNano()
	}
	rowID, _ := uuid.NewV7()
	eventID, _ := uuid.NewV7()
	row := &storage.AuditOutboxEvent{
		ID:        rowID,
		EventID:   eventID,
		WalletID:  wID,
		EventType: ev.EventType,
		Payload:   payload,
		Seq:       seq,
		CreatedAt: time.Now(),
	}
	return e.Store.AppendAuditEvent(ctx, row)
}

// Start launches the background outbox drainer.
func (e *EmitterService) Start(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.stopCh:
				return
			case <-ticker.C:
				_ = e.Drain(ctx)
			}
		}
	}()
}

// Stop signals the background drainer to exit.
func (e *EmitterService) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.stopped {
		e.stopped = true
		close(e.stopCh)
	}
}

// Drain flushes pending outbox events to the sink once.
func (e *EmitterService) Drain(ctx context.Context) error {
	events, err := e.Store.ListUndeliveredAuditEvents(ctx, 100)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	if e.Sink != nil {
		if err := e.Sink.Deliver(ctx, events); err != nil {
			return err
		}
	}
	for _, ev := range events {
		if err := e.Store.MarkAuditDelivered(ctx, ev.ID); err != nil {
			return err
		}
	}
	return nil
}

// ChannelSink delivers events to a Go channel; useful for tests.
type ChannelSink struct {
	Ch chan<- *storage.AuditOutboxEvent
}

// Deliver sends each event to the channel.
func (c *ChannelSink) Deliver(_ context.Context, events []*storage.AuditOutboxEvent) error {
	for _, ev := range events {
		c.Ch <- ev
	}
	return nil
}