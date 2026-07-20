package walletpb

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// exerciseMessage runs the nil-safe generated getters against both non-nil and
// nil receivers (so both branches of the getters are covered), and the
// generated Reset/String/ProtoMessage/ProtoReflect/Descriptor methods against
// the non-nil receiver only (they panic on a nil receiver).
func exerciseMessage[T any](t *testing.T, newFn func() T, callGetters func(T), lifecycle func(T)) {
	t.Helper()
	m := newFn()
	callGetters(m)
	lifecycle(m)

	var zero T
	callGetters(zero)
}

func TestWalletMessagesAccessors(t *testing.T) {
	t.Run("ResolveKeyIDRequest", func(t *testing.T) {
		exerciseMessage(t,
			func() *ResolveKeyIDRequest { return &ResolveKeyIDRequest{WalletId: "w1"} },
			func(x *ResolveKeyIDRequest) {
				got := x.GetWalletId()
				if x != nil && got != "w1" {
					t.Errorf("GetWalletId=%s", got)
				}
			},
			func(x *ResolveKeyIDRequest) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
	t.Run("ResolveKeyIDResponse", func(t *testing.T) {
		exerciseMessage(t,
			func() *ResolveKeyIDResponse { return &ResolveKeyIDResponse{KeyIds: []string{"k1", "k2"}, CurrentKeyId: "k1"} },
			func(x *ResolveKeyIDResponse) {
				keys := x.GetKeyIds()
				cur := x.GetCurrentKeyId()
				if x != nil {
					if keys == nil || len(keys) != 2 {
						t.Errorf("GetKeyIds=%v", keys)
					}
					if cur != "k1" {
						t.Errorf("GetCurrentKeyId=%s", cur)
					}
				}
			},
			func(x *ResolveKeyIDResponse) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
	t.Run("OnConfirmationRequest", func(t *testing.T) {
		exerciseMessage(t,
			func() *OnConfirmationRequest {
				return &OnConfirmationRequest{
					TxHash: "0xh", WalletId: "w", Asset: "eth", Amount: "100",
					Confirmations: 3, BlockHeight: 42, EventId: "e1", IsFinalized: true, WithdrawalId: "wd1",
				}
			},
			func(x *OnConfirmationRequest) {
				txh, w, a, am := x.GetTxHash(), x.GetWalletId(), x.GetAsset(), x.GetAmount()
				conf, bh, eid, fin, wid := x.GetConfirmations(), x.GetBlockHeight(), x.GetEventId(), x.GetIsFinalized(), x.GetWithdrawalId()
				if x != nil {
					if txh != "0xh" || w != "w" || a != "eth" || am != "100" {
						t.Errorf("gets: tx=%s w=%s a=%s am=%s", txh, w, a, am)
					}
					if conf != 3 || bh != 42 || eid != "e1" || !fin || wid != "wd1" {
						t.Errorf("gets: conf=%d bh=%d eid=%s fin=%v wid=%s", conf, bh, eid, fin, wid)
					}
				}
			},
			func(x *OnConfirmationRequest) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
	t.Run("OnReorgRequest", func(t *testing.T) {
		exerciseMessage(t,
			func() *OnReorgRequest {
				return &OnReorgRequest{
					BlockHeight: 5, WalletId: "w", Asset: "btc", EventId: "e2", Outpoints: []string{"u1", "u2"}, WithdrawalId: "wd2",
				}
			},
			func(x *OnReorgRequest) {
				bh, w, a, eid, wid := x.GetBlockHeight(), x.GetWalletId(), x.GetAsset(), x.GetEventId(), x.GetWithdrawalId()
				outs := x.GetOutpoints()
				if x != nil {
					if bh != 5 || w != "w" || a != "btc" || eid != "e2" || wid != "wd2" {
						t.Errorf("gets: bh=%d w=%s a=%s eid=%s wid=%s", bh, w, a, eid, wid)
					}
					if len(outs) != 2 {
						t.Errorf("GetOutpoints=%v", outs)
					}
				}
			},
			func(x *OnReorgRequest) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
	t.Run("Empty", func(t *testing.T) {
		exerciseMessage(t,
			func() *Empty { return &Empty{} },
			func(x *Empty) {},
			func(x *Empty) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
}

func TestClientsMessagesAccessors(t *testing.T) {
	t.Run("SignRequest", func(t *testing.T) {
		exerciseMessage(t,
			func() *SignRequest { return &SignRequest{KeyId: "k1", TxBytes: []byte("tx"), WalletId: "w"} },
			func(x *SignRequest) {
				kid, tx, wid := x.GetKeyId(), x.GetTxBytes(), x.GetWalletId()
				if x != nil {
					if kid != "k1" || string(tx) != "tx" || wid != "w" {
						t.Errorf("gets: kid=%s tx=%s wid=%s", kid, tx, wid)
					}
				}
			},
			func(x *SignRequest) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
	t.Run("SignResponse", func(t *testing.T) {
		exerciseMessage(t,
			func() *SignResponse { return &SignResponse{Signature: []byte("sig"), SignerId: "node1"} },
			func(x *SignResponse) {
				sig, sid := x.GetSignature(), x.GetSignerId()
				if x != nil {
					if string(sig) != "sig" || sid != "node1" {
						t.Errorf("gets: sig=%s sid=%s", sig, sid)
					}
				}
			},
			func(x *SignResponse) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
	t.Run("BroadcastTxRequest", func(t *testing.T) {
		exerciseMessage(t,
			func() *BroadcastTxRequest { return &BroadcastTxRequest{Chain: "ethereum", TxBytes: []byte("tx"), WalletId: "w"} },
			func(x *BroadcastTxRequest) {
				ch, tx, wid := x.GetChain(), x.GetTxBytes(), x.GetWalletId()
				if x != nil {
					if ch != "ethereum" || string(tx) != "tx" || wid != "w" {
						t.Errorf("gets: ch=%s tx=%s wid=%s", ch, tx, wid)
					}
				}
			},
			func(x *BroadcastTxRequest) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
	t.Run("BroadcastTxResponse", func(t *testing.T) {
		exerciseMessage(t,
			func() *BroadcastTxResponse { return &BroadcastTxResponse{TxHash: "0xh"} },
			func(x *BroadcastTxResponse) {
				h := x.GetTxHash()
				if x != nil && h != "0xh" {
					t.Errorf("GetTxHash=%s", h)
				}
			},
			func(x *BroadcastTxResponse) {
				x.Reset()
				_ = x.String()
				x.ProtoMessage()
				_ = x.ProtoReflect()
				_, _ = x.Descriptor()
			},
		)
	})
}

// TestFileDescriptors exercises the package-level FileDescriptor vars and the
// GZIP raw-desc lazy initialization paths.
func TestFileDescriptors(t *testing.T) {
	if File_wallet_proto == nil {
		t.Error("File_wallet_proto is nil")
	}
	if File_clients_proto == nil {
		t.Error("File_clients_proto is nil")
	}
	file_wallet_proto_init()
	file_clients_proto_init()
	_ = file_wallet_proto_rawDescGZIP()
	_ = file_wallet_proto_rawDescGZIP()
	_ = file_clients_proto_rawDescGZIP()
	_ = file_clients_proto_rawDescGZIP()
}

func TestUnimplementedMPCSigningServiceServer(t *testing.T) {
	srv := UnimplementedMPCSigningServiceServer{}
	if _, err := srv.Sign(context.Background(), &SignRequest{}); err == nil {
		t.Error("expected Unimplemented error from Sign")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.Unimplemented {
		t.Errorf("expected Unimplemented code, got %v", err)
	}
	srv.mustEmbedUnimplementedMPCSigningServiceServer()
	srv.testEmbeddedByValue()
}

func TestUnimplementedGatewayServiceServer(t *testing.T) {
	srv := UnimplementedGatewayServiceServer{}
	if _, err := srv.BroadcastTx(context.Background(), &BroadcastTxRequest{}); err == nil {
		t.Error("expected Unimplemented error from BroadcastTx")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.Unimplemented {
		t.Errorf("expected Unimplemented code, got %v", err)
	}
	srv.mustEmbedUnimplementedGatewayServiceServer()
	srv.testEmbeddedByValue()
}

func TestUnimplementedWalletServiceServer(t *testing.T) {
	srv := UnimplementedWalletServiceServer{}
	ctx := context.Background()
	if _, err := srv.ResolveKeyID(ctx, &ResolveKeyIDRequest{}); err == nil {
		t.Error("expected Unimplemented from ResolveKeyID")
	}
	if _, err := srv.OnConfirmation(ctx, &OnConfirmationRequest{}); err == nil {
		t.Error("expected Unimplemented from OnConfirmation")
	}
	if _, err := srv.OnReorg(ctx, &OnReorgRequest{}); err == nil {
		t.Error("expected Unimplemented from OnReorg")
	}
	srv.mustEmbedUnimplementedWalletServiceServer()
	srv.testEmbeddedByValue()
}

type fakeMPCServer struct {
	UnimplementedMPCSigningServiceServer
	signErr error
}

func (f *fakeMPCServer) Sign(_ context.Context, req *SignRequest) (*SignResponse, error) {
	if f.signErr != nil {
		return nil, f.signErr
	}
	return &SignResponse{Signature: append([]byte("sig:"), req.KeyId...), SignerId: "node1"}, nil
}

type fakeGatewayServer struct {
	UnimplementedGatewayServiceServer
	broadcastErr error
}

func (f *fakeGatewayServer) BroadcastTx(_ context.Context, req *BroadcastTxRequest) (*BroadcastTxResponse, error) {
	if f.broadcastErr != nil {
		return nil, f.broadcastErr
	}
	return &BroadcastTxResponse{TxHash: "0x" + req.Chain}, nil
}

type fakeWalletServer struct {
	UnimplementedWalletServiceServer
	resolveErr error
	reorgErr   error
	confirmErr error
}

func (f *fakeWalletServer) ResolveKeyID(_ context.Context, _ *ResolveKeyIDRequest) (*ResolveKeyIDResponse, error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	return &ResolveKeyIDResponse{KeyIds: []string{"k1"}, CurrentKeyId: "k1"}, nil
}

func (f *fakeWalletServer) OnConfirmation(_ context.Context, _ *OnConfirmationRequest) (*Empty, error) {
	if f.confirmErr != nil {
		return nil, f.confirmErr
	}
	return &Empty{}, nil
}

func (f *fakeWalletServer) OnReorg(_ context.Context, _ *OnReorgRequest) (*Empty, error) {
	if f.reorgErr != nil {
		return nil, f.reorgErr
	}
	return &Empty{}, nil
}

func startBufconnServer(t *testing.T, register func(*grpc.Server)) (WalletServiceClient, MPCSigningServiceClient, GatewayServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	register(gs)
	go func() { _ = gs.Serve(lis) }()
	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	return NewWalletServiceClient(conn), NewMPCSigningServiceClient(conn), NewGatewayServiceClient(conn), func() {
		_ = conn.Close()
		gs.GracefulStop()
		_ = lis.Close()
	}
}

func TestMPCSigningServiceClientServer(t *testing.T) {
	wc, mc, gc, stop := startBufconnServer(t, func(gs *grpc.Server) {
		RegisterMPCSigningServiceServer(gs, &fakeMPCServer{})
		RegisterGatewayServiceServer(gs, &fakeGatewayServer{})
		RegisterWalletServiceServer(gs, &fakeWalletServer{})
	})
	defer stop()

	resp, err := mc.Sign(context.Background(), &SignRequest{KeyId: "k1", TxBytes: []byte("tx"), WalletId: "w"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if string(resp.Signature) != "sig:k1" || resp.SignerId != "node1" {
		t.Errorf("unexpected response: %+v", resp)
	}

	gresp, err := gc.BroadcastTx(context.Background(), &BroadcastTxRequest{Chain: "eth", TxBytes: []byte("tx"), WalletId: "w"})
	if err != nil {
		t.Fatalf("BroadcastTx: %v", err)
	}
	if gresp.TxHash != "0xeth" {
		t.Errorf("unexpected tx hash: %s", gresp.TxHash)
	}

	wresp, err := wc.ResolveKeyID(context.Background(), &ResolveKeyIDRequest{WalletId: "w"})
	if err != nil {
		t.Fatalf("ResolveKeyID: %v", err)
	}
	if wresp.CurrentKeyId != "k1" || len(wresp.KeyIds) != 1 {
		t.Errorf("unexpected resolve response: %+v", wresp)
	}

	if _, err := wc.OnConfirmation(context.Background(), &OnConfirmationRequest{WalletId: "w"}); err != nil {
		t.Fatalf("OnConfirmation: %v", err)
	}
	if _, err := wc.OnReorg(context.Background(), &OnReorgRequest{WalletId: "w"}); err != nil {
		t.Fatalf("OnReorg: %v", err)
	}
}

func TestClientServerErrors(t *testing.T) {
	wc, mc, gc, stop := startBufconnServer(t, func(gs *grpc.Server) {
		RegisterMPCSigningServiceServer(gs, &fakeMPCServer{signErr: errors.New("sign down")})
		RegisterGatewayServiceServer(gs, &fakeGatewayServer{broadcastErr: errors.New("gw down")})
		RegisterWalletServiceServer(gs, &fakeWalletServer{
			resolveErr: errors.New("resolve down"),
			confirmErr: errors.New("confirm down"),
			reorgErr:   errors.New("reorg down"),
		})
	})
	defer stop()

	if _, err := mc.Sign(context.Background(), &SignRequest{}); err == nil {
		t.Error("expected Sign error")
	}
	if _, err := gc.BroadcastTx(context.Background(), &BroadcastTxRequest{}); err == nil {
		t.Error("expected BroadcastTx error")
	}
	if _, err := wc.ResolveKeyID(context.Background(), &ResolveKeyIDRequest{}); err == nil {
		t.Error("expected ResolveKeyID error")
	}
	if _, err := wc.OnConfirmation(context.Background(), &OnConfirmationRequest{}); err == nil {
		t.Error("expected OnConfirmation error")
	}
	if _, err := wc.OnReorg(context.Background(), &OnReorgRequest{}); err == nil {
		t.Error("expected OnReorg error")
	}
}

// TestHandlersDirectly exercises the generated _WalletService_*_Handler and
// _MPCSigningService_Sign_Handler / _GatewayService_BroadcastTx_Handler
// functions directly so the no-interceptor and interceptor branches are both
// hit.
func TestHandlersDirectly(t *testing.T) {
	ctx := context.Background()

	if r, err := _WalletService_ResolveKeyID_Handler(&fakeWalletServer{}, ctx, decOK(), nil); err != nil {
		t.Fatalf("ResolveKeyID handler: %v", err)
	} else if _, ok := r.(*ResolveKeyIDResponse); !ok {
		t.Errorf("unexpected type %T", r)
	}
	if r, err := _WalletService_OnConfirmation_Handler(&fakeWalletServer{}, ctx, decOK(), nil); err != nil {
		t.Fatalf("OnConfirmation handler: %v", err)
	} else if _, ok := r.(*Empty); !ok {
		t.Errorf("unexpected type %T", r)
	}
	if r, err := _WalletService_OnReorg_Handler(&fakeWalletServer{}, ctx, decOK(), nil); err != nil {
		t.Fatalf("OnReorg handler: %v", err)
	} else if _, ok := r.(*Empty); !ok {
		t.Errorf("unexpected type %T", r)
	}

	ic := &capturingInterceptor{}
	if _, err := _WalletService_ResolveKeyID_Handler(&fakeWalletServer{}, ctx, decOK(), ic.interceptor); err != nil {
		t.Fatalf("ResolveKeyID handler with interceptor: %v", err)
	}
	if !strings.Contains(ic.last, "ResolveKeyID") {
		t.Errorf("interceptor not invoked for ResolveKeyID, got %s", ic.last)
	}
	ic.last = ""
	if _, err := _WalletService_OnConfirmation_Handler(&fakeWalletServer{}, ctx, decOK(), ic.interceptor); err != nil {
		t.Fatalf("OnConfirmation handler with interceptor: %v", err)
	}
	if !strings.Contains(ic.last, "OnConfirmation") {
		t.Errorf("interceptor not invoked for OnConfirmation, got %s", ic.last)
	}
	ic.last = ""
	if _, err := _WalletService_OnReorg_Handler(&fakeWalletServer{}, ctx, decOK(), ic.interceptor); err != nil {
		t.Fatalf("OnReorg handler with interceptor: %v", err)
	}
	if !strings.Contains(ic.last, "OnReorg") {
		t.Errorf("interceptor not invoked for OnReorg, got %s", ic.last)
	}

	if _, err := _WalletService_ResolveKeyID_Handler(&fakeWalletServer{}, ctx, decErr(), nil); err == nil {
		t.Error("expected decode error")
	}
	if _, err := _WalletService_OnConfirmation_Handler(&fakeWalletServer{}, ctx, decErr(), nil); err == nil {
		t.Error("expected decode error")
	}
	if _, err := _WalletService_OnReorg_Handler(&fakeWalletServer{}, ctx, decErr(), nil); err == nil {
		t.Error("expected decode error")
	}

	if r, err := _MPCSigningService_Sign_Handler(&fakeMPCServer{}, ctx, decOK(), nil); err != nil {
		t.Fatalf("Sign handler: %v", err)
	} else if _, ok := r.(*SignResponse); !ok {
		t.Errorf("unexpected type %T", r)
	}
	ic.last = ""
	if _, err := _MPCSigningService_Sign_Handler(&fakeMPCServer{}, ctx, decOK(), ic.interceptor); err != nil {
		t.Fatalf("Sign handler with interceptor: %v", err)
	}
	if !strings.Contains(ic.last, "Sign") {
		t.Errorf("interceptor not invoked for Sign, got %s", ic.last)
	}
	if _, err := _MPCSigningService_Sign_Handler(&fakeMPCServer{}, ctx, decErr(), nil); err == nil {
		t.Error("expected decode error")
	}

	if r, err := _GatewayService_BroadcastTx_Handler(&fakeGatewayServer{}, ctx, decOK(), nil); err != nil {
		t.Fatalf("BroadcastTx handler: %v", err)
	} else if _, ok := r.(*BroadcastTxResponse); !ok {
		t.Errorf("unexpected type %T", r)
	}
	ic.last = ""
	if _, err := _GatewayService_BroadcastTx_Handler(&fakeGatewayServer{}, ctx, decOK(), ic.interceptor); err != nil {
		t.Fatalf("BroadcastTx handler with interceptor: %v", err)
	}
	if !strings.Contains(ic.last, "BroadcastTx") {
		t.Errorf("interceptor not invoked for BroadcastTx, got %s", ic.last)
	}
	if _, err := _GatewayService_BroadcastTx_Handler(&fakeGatewayServer{}, ctx, decErr(), nil); err == nil {
		t.Error("expected decode error")
	}
}

type capturingInterceptor struct{ last string }

func (c *capturingInterceptor) interceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	c.last = info.FullMethod
	return handler(ctx, req)
}

func decOK() func(any) error { return func(any) error { return nil } }

func decErr() func(any) error { return func(any) error { return errors.New("forced decode error") } }

// TestRegisterServerByPointer exercises the pointer-embed branch of
// RegisterWalletServiceServer/RegisterMPCSigningServiceServer/RegisterGatewayServiceServer
// (the testEmbeddedByValue no-op path).
func TestRegisterServerByPointer(t *testing.T) {
	gs := grpc.NewServer()
	RegisterWalletServiceServer(gs, &fakeWalletServer{})
	RegisterMPCSigningServiceServer(gs, &fakeMPCServer{})
	RegisterGatewayServiceServer(gs, &fakeGatewayServer{})
}