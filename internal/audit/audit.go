// Package audit implements the outbox-pattern audit event emitter.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

const AuditTopic = "audit.v1"

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

// KafkaSink publishes each AuditOutboxEvent wrapped in the canonical audit.v1
// envelope (see .github/contracts/asyncapi/audit/v1/asyncapi.yaml) to the `audit.v1`
// Kafka topic. The envelope's `payload` field carries the event's raw
// `Payload` bytes; `payload_hash` is the SHA-256 of those bytes.
type KafkaSink struct {
	writer *kafka.Writer
}

// NewKafkaSink returns a KafkaSink targeting the given brokers (comma-
// separated). Topic is fixed to audit.v1.
func NewKafkaSink(brokers []string) *KafkaSink {
	if len(brokers) == 0 {
		return &KafkaSink{}
	}
	return &KafkaSink{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        AuditTopic,
			Balancer:     &kafka.LeastBytes{},
			BatchTimeout: 10 * time.Millisecond,
			RequiredAcks: kafka.RequireAll,
		},
	}
}

// Close flushes and closes the underlying writer.
func (s *KafkaSink) Close() error {
	if s.writer == nil {
		return nil
	}
	return s.writer.Close()
}

// Deliver wraps each event in the canonical envelope and publishes it.
func (s *KafkaSink) Deliver(ctx context.Context, events []*storage.AuditOutboxEvent) error {
	if s.writer == nil {
		return fmt.Errorf("audit kafka: not connected")
	}
	msgs := make([]kafka.Message, 0, len(events))
	for _, ev := range events {
		envelope, key, err := buildEnvelope(ev)
		if err != nil {
			return err
		}
		body, err := json.Marshal(envelope)
		if err != nil {
			return fmt.Errorf("audit kafka encode: %w", err)
		}
		msgs = append(msgs, kafka.Message{Key: []byte(key), Value: body})
	}
	if err := s.writer.WriteMessages(ctx, msgs...); err != nil {
		return fmt.Errorf("audit kafka publish: %w", err)
	}
	return nil
}

func buildEnvelope(ev *storage.AuditOutboxEvent) (map[string]any, string, error) {
	sum := sha256.Sum256(ev.Payload)
	payloadHash := "sha256:" + hex.EncodeToString(sum[:])
	targetID := ""
	if ev.WalletID != nil {
		targetID = ev.WalletID.String()
	}
	id := ev.EventID.String()
	if id == "" || id == uuid.Nil.String() {
		id = uuid.NewString()
	}
	var payload any
	if len(ev.Payload) > 0 {
		payload = json.RawMessage(ev.Payload)
	}
	envelope := map[string]any{
		"schema_version": "1",
		"id":              id,
		"ts":              ev.CreatedAt.UTC().Format(time.RFC3339Nano),
		"source_service":  "wallet-management",
		"actor_id":        "wallet-management",
		"action":          ev.EventType,
		"target_type":     "wallet",
		"target_id":       targetID,
		"payload_hash":    payloadHash,
		"payload":         payload,
	}
	return envelope, id, nil
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