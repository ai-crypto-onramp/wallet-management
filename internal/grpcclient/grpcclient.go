// Package grpcclient defines the gRPC client interfaces for MPC Signing and
// Blockchain Gateway integration. They are plain Go interfaces so the service
// can be tested with mocks without requiring protoc-generated stubs.
package grpcclient

import (
	"context"

	"github.com/google/uuid"
)

// SignRequest is sent to the MPC Signing Service.
type SignRequest struct {
	KeyID    string
	TxBytes  []byte
	WalletID uuid.UUID
}

// SignResponse contains the signature produced by the MPC threshold signing.
type SignResponse struct {
	Signature []byte
	SignerID  string
}

// MPCSigner is the gRPC client interface for mpc-signing-service.
type MPCSigner interface {
	Sign(ctx context.Context, req *SignRequest) (*SignResponse, error)
}

// BroadcastRequest is sent to the Blockchain Gateway.
type BroadcastRequest struct {
	Chain    string
	TxBytes  []byte
	WalletID uuid.UUID
}

// BroadcastResponse contains the on-chain tx hash.
type BroadcastResponse struct {
	TxHash string
}

// GatewayClient is the gRPC client interface for blockchain-gateway.
type GatewayClient interface {
	BroadcastTx(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error)
}

// MockMPCSigner is an in-memory MPCSigner for tests.
type MockMPCSigner struct {
	SignFn func(ctx context.Context, req *SignRequest) (*SignResponse, error)
}

// Sign delegates to the configured SignFn.
func (m *MockMPCSigner) Sign(ctx context.Context, req *SignRequest) (*SignResponse, error) {
	if m.SignFn != nil {
		return m.SignFn(ctx, req)
	}
	return &SignResponse{Signature: []byte("mock-sig"), SignerID: "mock"}, nil
}

// MockGatewayClient is an in-memory GatewayClient for tests.
type MockGatewayClient struct {
	BroadcastFn func(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error)
}

// BroadcastTx delegates to the configured BroadcastFn.
func (m *MockGatewayClient) BroadcastTx(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error) {
	if m.BroadcastFn != nil {
		return m.BroadcastFn(ctx, req)
	}
	return &BroadcastResponse{TxHash: "0xmocktx"}, nil
}