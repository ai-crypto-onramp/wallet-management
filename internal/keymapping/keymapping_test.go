package keymapping

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/google/uuid"
)

func newSvc(cfg config.Config) (*Service, *memstore.Store) {
	st := memstore.New()
	return NewService(st, nil, cfg), st
}

func cfg(cooling time.Duration) config.Config {
	return config.Config{KeyCoolingPeriod: cooling}
}

func TestBind(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	ctx := context.Background()
	wID := uuid.New()
	if err := svc.Bind(ctx, wID, "k1"); err != nil {
		t.Fatal(err)
	}
	ms, err := svc.ResolveActive(ctx, wID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].KeyID != "k1" || ms[0].RotationState != "current" {
		t.Errorf("unexpected mappings: %+v", ms)
	}
	// duplicate current bind fails
	if err := svc.Bind(ctx, wID, "k2"); err == nil {
		t.Error("expected error on duplicate current bind")
	}
}

func TestResolveActiveNoMapping(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	if _, err := svc.ResolveActive(context.Background(), uuid.New()); err == nil {
		t.Error("expected error when no active mapping")
	}
}

func TestResolveActiveKeyIDNoMapping(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	if _, err := svc.ResolveActiveKeyID(context.Background(), uuid.New()); err == nil {
		t.Error("expected error when no active key")
	}
}

func TestRotateCoolingThenRetired(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	ctx := context.Background()
	wID := uuid.New()
	_ = svc.Bind(ctx, wID, "k1")
	if err := svc.Rotate(ctx, wID, "k2"); err != nil {
		t.Fatal(err)
	}
	ms, _ := svc.ResolveActive(ctx, wID)
	if len(ms) != 2 {
		t.Fatalf("expected 2 active mappings during cooling, got %d", len(ms))
	}
	var current, cooling *storage.KeyMapping
	for _, m := range ms {
		mm := m
		if m.RotationState == "current" {
			current = mm
		}
		if m.RotationState == "cooling" {
			cooling = mm
		}
	}
	if current == nil || current.KeyID != "k2" {
		t.Errorf("expected current=k2, got %+v", current)
	}
	if cooling == nil || cooling.KeyID != "k1" {
		t.Errorf("expected cooling=k1, got %+v", cooling)
	}
	if cooling.ActiveTo == nil {
		t.Error("expected cooling mapping to have active_to set")
	}
}

func TestResolveActiveAfterCoolingExpired(t *testing.T) {
	svc, _ := newSvc(cfg(-time.Hour)) // already expired cooling
	ctx := context.Background()
	wID := uuid.New()
	_ = svc.Bind(ctx, wID, "k1")
	_ = svc.Rotate(ctx, wID, "k2")
	// expire cooling -> k1 becomes retired
	if err := svc.ExpireCooling(ctx); err != nil {
		t.Fatal(err)
	}
	ms, _ := svc.ResolveActive(ctx, wID)
	if len(ms) != 1 || ms[0].KeyID != "k2" || ms[0].RotationState != "current" {
		t.Errorf("expected only k2 current after expiry, got %+v", ms)
	}
}

func TestResolveActiveKeyID(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	ctx := context.Background()
	wID := uuid.New()
	_ = svc.Bind(ctx, wID, "k1")
	id, err := svc.ResolveActiveKeyID(ctx, wID)
	if err != nil {
		t.Fatal(err)
	}
	if id != "k1" {
		t.Errorf("expected k1, got %s", id)
	}
	// during cooling returns the first (oldest by active_from) mapping
	_ = svc.Rotate(ctx, wID, "k2")
	id2, _ := svc.ResolveActiveKeyID(ctx, wID)
	if id2 == "" {
		t.Error("expected non-empty key id during cooling")
	}
}

func TestExpireCoolingNoOp(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	if err := svc.ExpireCooling(context.Background()); err != nil {
		t.Errorf("ExpireCooling should be safe on empty store: %v", err)
	}
}

func TestRotateNoPriorMapping(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	ctx := context.Background()
	wID := uuid.New()
	// rotating a wallet with no current mapping just inserts the new current
	if err := svc.Rotate(ctx, wID, "knew"); err != nil {
		t.Fatal(err)
	}
	ms, _ := svc.ResolveActive(ctx, wID)
	if len(ms) != 1 || ms[0].KeyID != "knew" {
		t.Errorf("expected knew current, got %+v", ms)
	}
}

func TestRotateExistingKey(t *testing.T) {
	svc, _ := newSvc(cfg(time.Hour))
	ctx := context.Background()
	wID := uuid.New()
	_ = svc.Bind(ctx, wID, "k1")
	// bind k2 as cooling first
	_ = svc.Store.BindKeyMapping(ctx, &storage.KeyMapping{WalletID: wID, KeyID: "k2", RotationState: "cooling", ActiveFrom: time.Now()})
	// rotate to k2 (existing) should promote it to current
	if err := svc.Rotate(ctx, wID, "k2"); err != nil {
		t.Fatal(err)
	}
	id, _ := svc.ResolveActiveKeyID(ctx, wID)
	if id == "" {
		t.Error("expected non-empty current after rotate-to-existing")
	}
}

var _ = errors.New