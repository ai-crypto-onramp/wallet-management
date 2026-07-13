package clients

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/ai-crypto-onramp/wallet-management/internal/grpcclient"
	walletpb "github.com/ai-crypto-onramp/wallet-management/internal/pb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeMPCServer is a minimal MPCSigningServiceServer for tests.
type fakeMPCServer struct {
	walletpb.UnimplementedMPCSigningServiceServer
	signFn func(context.Context, *walletpb.SignRequest) (*walletpb.SignResponse, error)
}

func (f *fakeMPCServer) Sign(ctx context.Context, req *walletpb.SignRequest) (*walletpb.SignResponse, error) {
	if f.signFn != nil {
		return f.signFn(ctx, req)
	}
	return &walletpb.SignResponse{Signature: []byte("signed:" + req.KeyId), SignerId: "mpc-node-1"}, nil
}

// fakeGatewayServer is a minimal GatewayServiceServer for tests.
type fakeGatewayServer struct {
	walletpb.UnimplementedGatewayServiceServer
	broadcastFn func(context.Context, *walletpb.BroadcastTxRequest) (*walletpb.BroadcastTxResponse, error)
}

func (f *fakeGatewayServer) BroadcastTx(ctx context.Context, req *walletpb.BroadcastTxRequest) (*walletpb.BroadcastTxResponse, error) {
	if f.broadcastFn != nil {
		return f.broadcastFn(ctx, req)
	}
	return &walletpb.BroadcastTxResponse{TxHash: "0x" + req.Chain}, nil
}

// startMPCServer starts an in-process gRPC server serving MPCSigningService and
// returns a client target, a grpc.DialOption whose context dialer routes to
// the in-process listener, and a cleanup func.
func startMPCServer(t *testing.T, srv walletpb.MPCSigningServiceServer) (string, grpc.DialOption, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	walletpb.RegisterMPCSigningServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	return "passthrough:///mpctest", grpc.WithContextDialer(dialer), func() {
		gs.GracefulStop()
		_ = lis.Close()
	}
}

// startGatewayServer starts an in-process gRPC server serving GatewayService.
func startGatewayServer(t *testing.T, srv walletpb.GatewayServiceServer) (string, grpc.DialOption, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	walletpb.RegisterGatewayServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	return "passthrough:///gwtest", grpc.WithContextDialer(dialer), func() {
		gs.GracefulStop()
		_ = lis.Close()
	}
}

func TestMPCSigningClientSignHappyPath(t *testing.T) {
	srv := &fakeMPCServer{}
	target, dialOpt, stop := startMPCServer(t, srv)
	defer stop()

	c, err := NewMPCSigningClient(target, WithGRPCDialOptions(dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	wID := uuid.New()
	resp, err := c.Sign(context.Background(), &grpcclient.SignRequest{
		KeyID: "k-active", TxBytes: []byte("unsigned-tx"), WalletID: wID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Signature) != "signed:k-active" || resp.SignerID != "mpc-node-1" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestMPCSigningClientSignError(t *testing.T) {
	srv := &fakeMPCServer{signFn: func(context.Context, *walletpb.SignRequest) (*walletpb.SignResponse, error) {
		return nil, errors.New("mpc signing unavailable")
	}}
	target, dialOpt, stop := startMPCServer(t, srv)
	defer stop()

	c, err := NewMPCSigningClient(target, WithGRPCDialOptions(dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err := c.Sign(context.Background(), &grpcclient.SignRequest{KeyID: "k1"}); err == nil {
		t.Error("expected error from mpc signing")
	}
}

func TestMPCSigningClientDialError(t *testing.T) {
	// An empty target with no dialer produces a resolver error synchronously.
	if _, err := NewMPCSigningClient(""); err == nil {
		t.Error("expected dial error for empty target")
	}
}

func TestGatewayClientBroadcastHappyPath(t *testing.T) {
	srv := &fakeGatewayServer{}
	target, dialOpt, stop := startGatewayServer(t, srv)
	defer stop()

	c, err := NewGatewayClient(target, WithGRPCDialOptions(dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	wID := uuid.New()
	resp, err := c.BroadcastTx(context.Background(), &grpcclient.BroadcastRequest{
		Chain: "ethereum", TxBytes: []byte("signed-tx"), WalletID: wID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TxHash != "0xethereum" {
		t.Errorf("unexpected tx hash: %s", resp.TxHash)
	}
}

func TestGatewayClientBroadcastError(t *testing.T) {
	srv := &fakeGatewayServer{broadcastFn: func(context.Context, *walletpb.BroadcastTxRequest) (*walletpb.BroadcastTxResponse, error) {
		return nil, errors.New("gateway rejected tx")
	}}
	target, dialOpt, stop := startGatewayServer(t, srv)
	defer stop()

	c, err := NewGatewayClient(target, WithGRPCDialOptions(dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err := c.BroadcastTx(context.Background(), &grpcclient.BroadcastRequest{Chain: "bitcoin"}); err == nil {
		t.Error("expected error from gateway broadcast")
	}
}

func TestGatewayClientDialError(t *testing.T) {
	if _, err := NewGatewayClient(""); err == nil {
		t.Error("expected dial error for empty target")
	}
}

func TestCloseIdempotent(t *testing.T) {
	srv := &fakeMPCServer{}
	target, dialOpt, stop := startMPCServer(t, srv)
	defer stop()

	c, err := NewMPCSigningClient(target, WithGRPCDialOptions(dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("first close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}
