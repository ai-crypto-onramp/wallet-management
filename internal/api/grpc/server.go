// Package grpcserver implements the internal gRPC server for MPC Signing
// (ResolveKeyID) and Blockchain Gateway (OnConfirmation, OnReorg) callbacks.
//
// To avoid requiring protoc at build time, the server registers a custom JSON
// codec with google.golang.org/grpc so plain Go structs can be used as
// request/response messages without generated code.
package grpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/ai-crypto-onramp/wallet-management/internal/balance"
	"github.com/ai-crypto-onramp/wallet-management/internal/keymapping"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/ai-crypto-onramp/wallet-management/internal/withdrawal"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

func init() {
	encoding.RegisterCodec(jsonCodec{})
}

type jsonCodec struct{}

func (jsonCodec) Name() string { return "json" }
func (jsonCodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
func (jsonCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// ResolveKeyIDRequest is the MPC Signing Service lookup request.
type ResolveKeyIDRequest struct {
	WalletID string `json:"wallet_id"`
}

// ResolveKeyIDResponse returns the active key_id(s) for the wallet.
type ResolveKeyIDResponse struct {
	KeyIDs       []string `json:"key_ids"`
	CurrentKeyID string   `json:"current_key_id"`
}

// OnConfirmationRequest is a Blockchain Gateway confirmation callback.
type OnConfirmationRequest struct {
	TxHash        string `json:"tx_hash"`
	WalletID      string `json:"wallet_id"`
	Asset         string `json:"asset"`
	Amount        string `json:"amount"`
	Confirmations int    `json:"confirmations"`
	BlockHeight   int64  `json:"block_height"`
	EventID       string `json:"event_id"`
	IsFinalized   bool   `json:"is_finalized"`
	WithdrawalID  string `json:"withdrawal_id"`
}

// OnReorgRequest is a Blockchain Gateway reorg callback.
type OnReorgRequest struct {
	BlockHeight  int64    `json:"block_height"`
	WalletID     string   `json:"wallet_id"`
	Asset        string   `json:"asset"`
	EventID      string   `json:"event_id"`
	Outpoints    []string `json:"outpoints"`
	WithdrawalID string   `json:"withdrawal_id"`
}

// Empty is the empty response for void gRPC methods.
type Empty struct{}

// Deps bundles gRPC server dependencies.
type Deps struct {
	KeyMappings *keymapping.Service
	Balances    *balance.Service
	Withdrawals *withdrawal.Service
}

// Server is the wallet-management gRPC server.
type Server struct {
	GRPCServer  *grpc.Server
	KeyMappings *keymapping.Service
	Balances    *balance.Service
	Withdrawals *withdrawal.Service
	listener    net.Listener
}

// NewServer constructs a new gRPC server (not yet listening).
func NewServer(d Deps) *Server {
	gs := grpc.NewServer(grpc.ForceServerCodec(jsonCodec{}))
	s := &Server{
		GRPCServer:  gs,
		KeyMappings: d.KeyMappings,
		Balances:    d.Balances,
		Withdrawals: d.Withdrawals,
	}
	registerMethods(gs, s)
	reflection.Register(gs)
	return s
}

// Start binds to addr and blocks serving gRPC.
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = ln
	return s.GRPCServer.Serve(ln)
}

// Stop gracefully stops the gRPC server.
func (s *Server) Stop() {
	if s.GRPCServer != nil {
		s.GRPCServer.GracefulStop()
	}
}

type walletServer struct {
	srv *Server
}

// walletService is the empty interface used to satisfy grpc.RegisterService's
// HandlerType check (which requires an interface type). We register methods
// manually with custom handlers, so the handler type only needs to be a valid
// interface that the concrete handler implements.
type walletService interface{}

func registerMethods(gs *grpc.Server, s *Server) {
	desc := grpc.ServiceDesc{
		ServiceName: "wallet.WalletService",
		HandlerType: (*walletService)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "ResolveKeyID", Handler: resolveKeyIDHandler},
			{MethodName: "OnConfirmation", Handler: onConfirmationHandler},
			{MethodName: "OnReorg", Handler: onReorgHandler},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "wallet.proto",
	}
	gs.RegisterService(&desc, &walletServer{srv: s})
}

func resolveKeyIDHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*walletServer)
	var req ResolveKeyIDRequest
	if err := dec(&req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	wid, err := uuid.Parse(req.WalletID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid wallet_id: %v", err)
	}
	mappings, err := s.srv.KeyMappings.ResolveActive(ctx, wid)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve key: %v", err)
	}
	var keyIDs []string
	var current string
	for _, m := range mappings {
		keyIDs = append(keyIDs, m.KeyID)
		if m.RotationState == string(storage.RotationStateCurrent) {
			current = m.KeyID
		}
	}
	return &ResolveKeyIDResponse{KeyIDs: keyIDs, CurrentKeyID: current}, nil
}

func onConfirmationHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*walletServer)
	var req OnConfirmationRequest
	if err := dec(&req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	if req.WithdrawalID != "" {
		wid, err := uuid.Parse(req.WithdrawalID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid withdrawal id: %v", err)
		}
		if err := s.srv.Withdrawals.Confirm(ctx, wid, req.TxHash); err != nil {
			return nil, status.Errorf(codes.Internal, "confirm: %v", err)
		}
		return &Empty{}, nil
	}
	if req.WalletID != "" {
		wid, err := uuid.Parse(req.WalletID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid wallet id: %v", err)
		}
		ev := &balance.ConfirmationEvent{
			WalletID: wid, Asset: req.Asset, Amount: req.Amount,
			Confirmations: req.Confirmations, BlockHeight: req.BlockHeight,
			EventID: req.EventID, IsFinalized: req.IsFinalized,
			Chain: wallet.Chain(chainOfAsset(req.Asset)),
		}
		if err := s.srv.Balances.ApplyConfirmationEvent(ctx, ev); err != nil {
			return nil, status.Errorf(codes.Internal, "apply confirmation: %v", err)
		}
	}
	return &Empty{}, nil
}

func onReorgHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*walletServer)
	var req OnReorgRequest
	if err := dec(&req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	if req.WithdrawalID != "" {
		wid, err := uuid.Parse(req.WithdrawalID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid withdrawal id: %v", err)
		}
		if err := s.srv.Withdrawals.OnReorg(ctx, wid, req.Outpoints); err != nil {
			return nil, status.Errorf(codes.Internal, "withdrawal reorg: %v", err)
		}
	}
	if req.WalletID != "" {
		wid, err := uuid.Parse(req.WalletID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid wallet id: %v", err)
		}
		ev := &balance.ReorgEvent{
			WalletID: wid, Asset: req.Asset, BlockHeight: req.BlockHeight,
			EventID: req.EventID, Outpoints: req.Outpoints,
		}
		if err := s.srv.Balances.ApplyReorgEvent(ctx, ev); err != nil {
			return nil, status.Errorf(codes.Internal, "apply reorg: %v", err)
		}
	}
	return &Empty{}, nil
}

func chainOfAsset(asset string) string {
	switch asset {
	case "btc", "bitcoin":
		return "bitcoin"
	case "sol", "solana":
		return "solana"
	default:
		return "ethereum"
	}
}

var _ = io.EOF
var _ = fmt.Sprintf