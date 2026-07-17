package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/grpcclient"
	"github.com/ai-crypto-onramp/wallet-management/internal/keymapping"
	"github.com/ai-crypto-onramp/wallet-management/internal/lock"
	"github.com/ai-crypto-onramp/wallet-management/internal/nonce"
	"github.com/ai-crypto-onramp/wallet-management/internal/policy"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/memstore"
	"github.com/ai-crypto-onramp/wallet-management/internal/utxo"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/ai-crypto-onramp/wallet-management/internal/withdrawal"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func newServer(t *testing.T) (*Server, *bufconn.Listener) {
	t.Helper()
	st := memstore.New()
	cfg := config.Config{ConfirmationsEVM: 12, ConfirmationsBTC: 6, KeyCoolingPeriod: time.Hour, DefaultRotationDays: 7}
	km := keymapping.NewService(st, nil, cfg)
	bal := balance.NewService(st, nil, cfg)
	wsvc := wallet.NewService(st, nil, lock.NewMemLocker(), nil, cfg)
	ns := nonce.NewService(st, lock.NewMemLocker())
	us := utxo.NewService(st)
	pc := &policy.MockClient{}
	signer := &grpcclient.MockMPCSigner{}
	gw := &grpcclient.MockGatewayClient{}
	kr := km // keymapping.Service satisfies KeyResolver
	wd := withdrawal.NewService(st, wsvc, ns, us, pc, signer, gw, kr, nil)
	srv := NewServer(Deps{KeyMappings: km, Balances: bal, Withdrawals: wd})
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.GRPCServer.Serve(lis) }()
	return srv, lis
}

func dial(t *testing.T, lis *bufconn.Listener) (*grpc.ClientConn, func()) {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{})),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conn, func() { _ = conn.Close() }
}

func TestResolveKeyIDHappyPath(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	ctx := context.Background()
	wID := uuid.New()
	// bind a key mapping directly
	if err := srv.KeyMappings.Bind(ctx, wID, "k-active"); err != nil {
		t.Fatal(err)
	}
	conn, closeFn := dial(t, lis)
	defer closeFn()
	resp := &ResolveKeyIDResponse{}
	if err := grpcInvoke(ctx, conn, "ResolveKeyID", &ResolveKeyIDRequest{WalletID: wID.String()}, resp); err != nil {
		t.Fatal(err)
	}
	if resp.CurrentKeyID != "k-active" || len(resp.KeyIDs) != 1 || resp.KeyIDs[0] != "k-active" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestResolveKeyIDNotFound(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(context.Background(), conn, "ResolveKeyID", &ResolveKeyIDRequest{WalletID: uuid.New().String()}, &ResolveKeyIDResponse{}); err == nil {
		t.Error("expected error for unknown wallet")
	}
}

func TestResolveKeyIDBadWalletID(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(context.Background(), conn, "ResolveKeyID", &ResolveKeyIDRequest{WalletID: "not-a-uuid"}, &ResolveKeyIDResponse{}); err == nil {
		t.Error("expected error for bad wallet id")
	}
}

func TestOnConfirmationWithdrawalHappyPath(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	ctx := context.Background()
	// seed a wallet + withdrawal that is in 'broadcast' state
	wID := uuid.New()
	w := &wallet.Wallet{ID: wID, Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k1", CustodianRef: "mpc", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = srv.Withdrawals.Store.CreateWallet(ctx, w)
	wr := &storage.WithdrawalRequest{ID: uuid.New(), WalletID: wID, ToAddress: "0x1", Asset: "eth", Amount: "1", State: "BROADCAST"}
	_ = srv.Withdrawals.Store.CreateWithdrawal(ctx, wr)
	// advance to broadcast via update (CreateWithdrawal sets pending)
	_ = srv.Withdrawals.Store.UpdateWithdrawalState(ctx, wr.ID, "BROADCAST", "", "0xold", "")

	conn, closeFn := dial(t, lis)
	defer closeFn()
	resp := &Empty{}
	if err := grpcInvoke(ctx, conn, "OnConfirmation", &OnConfirmationRequest{
		WithdrawalID: wr.ID.String(), TxHash: "0xnew",
	}, resp); err != nil {
		t.Fatal(err)
	}
	got, _ := srv.Withdrawals.Store.GetWithdrawal(ctx, wr.ID)
	if got.State != "CONFIRMED" || got.TxHash != "0xnew" {
		t.Errorf("expected confirmed/0xnew, got %+v", got)
	}
}

func TestOnConfirmationBalanceHappyPath(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	ctx := context.Background()
	wID := uuid.New()
	resp := &Empty{}
	if err := grpcInvoke(ctx, conn1(t, lis), "OnConfirmation", &OnConfirmationRequest{
		WalletID: wID.String(), Asset: "eth", Amount: "100", Confirmations: 20, BlockHeight: 1, EventID: "ev1",
	}, resp); err != nil {
		t.Fatal(err)
	}
	b, err := srv.Balances.GetBalances(ctx, wID)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].Confirmed != "100" {
		t.Errorf("expected confirmed=100, got %+v", b)
	}
}

func TestOnConfirmationBadWalletID(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(context.Background(), conn, "OnConfirmation", &OnConfirmationRequest{WalletID: "bad"}, &Empty{}); err == nil {
		t.Error("expected error on bad wallet id")
	}
}

func TestOnConfirmationBadWithdrawalID(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(context.Background(), conn, "OnConfirmation", &OnConfirmationRequest{WithdrawalID: "bad"}, &Empty{}); err == nil {
		t.Error("expected error on bad withdrawal id")
	}
}

func TestOnReorgHappyPath(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	ctx := context.Background()
	wID := uuid.New()
	// seed a balance to demote
	_ = srv.Balances.ApplyConfirmationEvent(ctx, &balance.ConfirmationEvent{
		WalletID: wID, Asset: "eth", Amount: "100", Confirmations: 20, BlockHeight: 5, EventID: "e1", Chain: wallet.ChainEthereum,
	})
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(ctx, conn, "OnReorg", &OnReorgRequest{
		WalletID: wID.String(), Asset: "eth", BlockHeight: 5, EventID: "reorg1", Outpoints: []string{"u1"},
	}, &Empty{}); err != nil {
		t.Fatal(err)
	}
	b, _ := srv.Balances.GetBalances(ctx, wID)
	if len(b) != 1 || b[0].Confirmed != "0" || b[0].Pending != "100" {
		t.Errorf("expected demoted to pending, got %+v", b)
	}
}

func TestOnReorgWithdrawalRollback(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	ctx := context.Background()
	wID := uuid.New()
	w := &wallet.Wallet{ID: wID, Chain: wallet.ChainBitcoin, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive, KeyID: "k1", CustodianRef: "mpc", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = srv.Withdrawals.Store.CreateWallet(ctx, w)
	_ = srv.Withdrawals.UTXOs.TrackUTXO(ctx, &storage.UTXO{Outpoint: "u1", WalletID: wID, Value: "100", LockState: "FREE"})
	wr := &storage.WithdrawalRequest{ID: uuid.New(), WalletID: wID, ToAddress: "bc1q", Asset: "btc", Amount: "100", State: "BROADCAST"}
	_ = srv.Withdrawals.Store.CreateWithdrawal(ctx, wr)
	_ = srv.Withdrawals.Store.UpdateWithdrawalState(ctx, wr.ID, "CONFIRMED", "", "0xtx", "")
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(ctx, conn, "OnReorg", &OnReorgRequest{
		WithdrawalID: wr.ID.String(), Outpoints: []string{"u1"},
	}, &Empty{}); err != nil {
		t.Fatal(err)
	}
	got, _ := srv.Withdrawals.Store.GetWithdrawal(ctx, wr.ID)
	if got.State != "BROADCAST" {
		t.Errorf("expected demoted to broadcast after reorg, got %s", got.State)
	}
	free, _ := srv.Withdrawals.Store.ListFreeUTXOs(ctx, wID)
	if len(free) != 1 || free[0].Outpoint != "u1" {
		t.Errorf("expected u1 free after reorg, got %+v", free)
	}
}

func TestOnReorgBadWalletID(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(context.Background(), conn, "OnReorg", &OnReorgRequest{WalletID: "bad"}, &Empty{}); err == nil {
		t.Error("expected error on bad wallet id")
	}
}

func TestOnReorgBadWithdrawalID(t *testing.T) {
	srv, lis := newServer(t)
	defer srv.Stop()
	conn, closeFn := dial(t, lis)
	defer closeFn()
	if err := grpcInvoke(context.Background(), conn, "OnReorg", &OnReorgRequest{WithdrawalID: "bad"}, &Empty{}); err == nil {
		t.Error("expected error on bad withdrawal id")
	}
}

func TestChainOfAsset(t *testing.T) {
	cases := map[string]string{
		"btc":     "bitcoin",
		"bitcoin": "bitcoin",
		"sol":     "solana",
		"solana":  "solana",
		"eth":     "ethereum",
	}
	for in, want := range cases {
		if got := chainOfAsset(in); got != want {
			t.Errorf("chainOfAsset(%s)=%s, want %s", in, got, want)
		}
	}
}

// grpcInvoke is a tiny helper that invokes a unary method on the wallet service.
func grpcInvoke(ctx context.Context, conn *grpc.ClientConn, method string, req, resp any) error {
	return conn.Invoke(ctx, "/wallet.WalletService/"+method, req, resp)
}

// conn1 is a convenience helper that dials and returns the conn (for tests that
// only need a single call).
func conn1(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	conn, _ := dial(t, lis)
	return conn
}