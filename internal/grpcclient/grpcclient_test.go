package grpcclient

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestMockMPCSignerDefault(t *testing.T) {
	s := &MockMPCSigner{}
	resp, err := s.Sign(context.Background(), &SignRequest{KeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Signature) != "mock-sig" || resp.SignerID != "mock" {
		t.Errorf("unexpected default: %+v", resp)
	}
}

func TestMockMPCSignerCustomFn(t *testing.T) {
	s := &MockMPCSigner{SignFn: func(ctx context.Context, req *SignRequest) (*SignResponse, error) {
		if req.KeyID != "k1" {
			t.Errorf("unexpected keyID: %s", req.KeyID)
		}
		return &SignResponse{Signature: []byte("custom"), SignerID: "s1"}, nil
	}}
	resp, _ := s.Sign(context.Background(), &SignRequest{KeyID: "k1"})
	if string(resp.Signature) != "custom" {
		t.Errorf("expected custom sig, got %s", resp.Signature)
	}
}

func TestMockMPCSignerError(t *testing.T) {
	s := &MockMPCSigner{SignFn: func(ctx context.Context, req *SignRequest) (*SignResponse, error) {
		return nil, errors.New("signing failed")
	}}
	if _, err := s.Sign(context.Background(), &SignRequest{}); err == nil {
		t.Error("expected error")
	}
}

func TestMockGatewayDefault(t *testing.T) {
	g := &MockGatewayClient{}
	resp, err := g.BroadcastTx(context.Background(), &BroadcastRequest{Chain: "ethereum", WalletID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TxHash != "0xmocktx" {
		t.Errorf("unexpected default tx hash: %s", resp.TxHash)
	}
}

func TestMockGatewayCustomFn(t *testing.T) {
	g := &MockGatewayClient{BroadcastFn: func(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error) {
		return &BroadcastResponse{TxHash: "0xreal"}, nil
	}}
	resp, _ := g.BroadcastTx(context.Background(), &BroadcastRequest{})
	if resp.TxHash != "0xreal" {
		t.Errorf("expected 0xreal, got %s", resp.TxHash)
	}
}

func TestMockGatewayError(t *testing.T) {
	g := &MockGatewayClient{BroadcastFn: func(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error) {
		return nil, errors.New("gateway down")
	}}
	if _, err := g.BroadcastTx(context.Background(), &BroadcastRequest{}); err == nil {
		t.Error("expected error")
	}
}