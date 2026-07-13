// Package clients implements the real gRPC client wrappers for the
// downstream services wallet-management depends on:
//
//   - MPCSigningClient dials mpc-signing-service and implements grpcclient.MPCSigner.
//   - GatewayClient dials blockchain-gateway and implements grpcclient.GatewayClient.
//
// Both clients use the generated stubs in internal/pb (walletpb). They dial on
// construction and hold a single *grpc.ClientConn for the lifetime of the
// process. mTLS / custom dial options can be supplied via the Options struct.
package clients

import (
	"context"
	"fmt"
	"sync"

	"github.com/ai-crypto-onramp/wallet-management/internal/grpcclient"
	walletpb "github.com/ai-crypto-onramp/wallet-management/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DialOption is a functional option for configuring a gRPC client.
type DialOption func(*dialConfig)

type dialConfig struct {
	grpcOpts []grpc.DialOption
}

// WithGRPCDialOptions overrides the default (insecure) gRPC dial options. Use
// this to plug in TLS credentials, interceptors, etc.
func WithGRPCDialOptions(opts ...grpc.DialOption) DialOption {
	return func(c *dialConfig) {
		c.grpcOpts = opts
	}
}

func defaultDialConfig() dialConfig {
	return dialConfig{
		grpcOpts: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
	}
}

// MPCSigningClient is a real gRPC client for mpc-signing-service. It satisfies
// grpcclient.MPCSigner.
type MPCSigningClient struct {
	conn *grpc.ClientConn
	raw  walletpb.MPCSigningServiceClient
	mu   sync.Mutex
}

// NewMPCSigningClient dials mpc-signing-service at target (e.g.
// "dns:///localhost:9091") and returns a client implementing grpcclient.MPCSigner.
func NewMPCSigningClient(target string, opts ...DialOption) (*MPCSigningClient, error) {
	if target == "" {
		return nil, fmt.Errorf("dial mpc-signing-service: empty target")
	}
	cfg := defaultDialConfig()
	for _, o := range opts {
		o(&cfg)
	}
	conn, err := grpc.NewClient(target, cfg.grpcOpts...)
	if err != nil {
		return nil, fmt.Errorf("dial mpc-signing-service %q: %w", target, err)
	}
	return &MPCSigningClient{conn: conn, raw: walletpb.NewMPCSigningServiceClient(conn)}, nil
}

// Sign calls MPCSigningService.Sign over gRPC.
func (c *MPCSigningClient) Sign(ctx context.Context, req *grpcclient.SignRequest) (*grpcclient.SignResponse, error) {
	resp, err := c.raw.Sign(ctx, &walletpb.SignRequest{
		KeyId:    req.KeyID,
		TxBytes:  req.TxBytes,
		WalletId: req.WalletID.String(),
	})
	if err != nil {
		return nil, err
	}
	return &grpcclient.SignResponse{Signature: resp.Signature, SignerID: resp.SignerId}, nil
}

// Close releases the underlying gRPC connection. Safe to call multiple times.
func (c *MPCSigningClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// GatewayClient is a real gRPC client for blockchain-gateway. It satisfies
// grpcclient.GatewayClient.
type GatewayClient struct {
	conn *grpc.ClientConn
	raw  walletpb.GatewayServiceClient
	mu   sync.Mutex
}

// NewGatewayClient dials blockchain-gateway at target (e.g.
// "dns:///localhost:9092") and returns a client implementing grpcclient.GatewayClient.
func NewGatewayClient(target string, opts ...DialOption) (*GatewayClient, error) {
	if target == "" {
		return nil, fmt.Errorf("dial blockchain-gateway: empty target")
	}
	cfg := defaultDialConfig()
	for _, o := range opts {
		o(&cfg)
	}
	conn, err := grpc.NewClient(target, cfg.grpcOpts...)
	if err != nil {
		return nil, fmt.Errorf("dial blockchain-gateway %q: %w", target, err)
	}
	return &GatewayClient{conn: conn, raw: walletpb.NewGatewayServiceClient(conn)}, nil
}

// BroadcastTx calls GatewayService.BroadcastTx over gRPC.
func (c *GatewayClient) BroadcastTx(ctx context.Context, req *grpcclient.BroadcastRequest) (*grpcclient.BroadcastResponse, error) {
	resp, err := c.raw.BroadcastTx(ctx, &walletpb.BroadcastTxRequest{
		Chain:    req.Chain,
		TxBytes:  req.TxBytes,
		WalletId: req.WalletID.String(),
	})
	if err != nil {
		return nil, err
	}
	return &grpcclient.BroadcastResponse{TxHash: resp.TxHash}, nil
}

// Close releases the underlying gRPC connection. Safe to call multiple times.
func (c *GatewayClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// Compile-time interface conformance checks.
var (
	_ grpcclient.MPCSigner     = (*MPCSigningClient)(nil)
	_ grpcclient.GatewayClient = (*GatewayClient)(nil)
)
