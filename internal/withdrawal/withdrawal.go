// Package withdrawal implements the end-to-end withdrawal flow: whitelist
// check, nonce/UTXO coordination, MPC signing, and broadcast.
package withdrawal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/audit"
	"github.com/ai-crypto-onramp/wallet-management/internal/grpcclient"
	"github.com/ai-crypto-onramp/wallet-management/internal/nonce"
	"github.com/ai-crypto-onramp/wallet-management/internal/policy"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/utxo"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/google/uuid"
)

// CreateRequest is the REST payload for POST /v1/withdrawals.
type CreateRequest struct {
	WalletID  uuid.UUID `json:"wallet_id"`
	ToAddress string    `json:"to_address"`
	Asset     string    `json:"asset"`
	Amount    string    `json:"amount"`
}

// Service implements the withdrawal saga.
type Service struct {
	Store     storage.Store
	Wallets   *wallet.Service
	Nonces    *nonce.Service
	UTXOs     *utxo.Service
	Policy    policy.Client
	Signer    grpcclient.MPCSigner
	Gateway   grpcclient.GatewayClient
	KeyLookup KeyResolver
	Audit     audit.Emitter
}

// KeyResolver resolves a wallet's active key_id (wired to keymapping.Service).
type KeyResolver interface {
	ResolveActiveKeyID(ctx context.Context, walletID uuid.UUID) (string, error)
}

// NewService constructs a withdrawal Service.
func NewService(st storage.Store, ws *wallet.Service, ns *nonce.Service, us *utxo.Service, pc policy.Client, signer grpcclient.MPCSigner, gw grpcclient.GatewayClient, kr KeyResolver, em audit.Emitter) *Service {
	return &Service{Store: st, Wallets: ws, Nonces: ns, UTXOs: us, Policy: pc, Signer: signer, Gateway: gw, KeyLookup: kr, Audit: em}
}

// Create inserts a pending withdrawal and synchronously checks the whitelist.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*storage.WithdrawalRequest, error) {
	w, err := s.Store.GetWallet(ctx, req.WalletID)
	if err != nil {
		return nil, fmt.Errorf("get wallet: %w", err)
	}
	if w.State == wallet.WalletStateRetired {
		return nil, wallet.ErrWalletRetired
	}
	if w.State == wallet.WalletStatePaused {
		return nil, errors.New("wallet is paused")
	}
	now := time.Now()
	id, _ := uuid.NewV7()
	wr := &storage.WithdrawalRequest{
		ID:        id,
		WalletID:  req.WalletID,
		ToAddress: req.ToAddress,
		Asset:     req.Asset,
		Amount:    req.Amount,
		State:     string(storage.WithdrawalStatePending),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.Store.CreateWithdrawal(ctx, wr); err != nil {
		return nil, fmt.Errorf("create withdrawal: %w", err)
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "withdrawal.created",
			WalletID:  &req.WalletID,
			Payload:   map[string]any{"id": wr.ID, "to": req.ToAddress, "asset": req.Asset, "amount": req.Amount},
		})
	}
	// synchronous whitelist check
	dec, err := s.Policy.CheckWhitelist(ctx, &policy.CheckRequest{
		WalletID:  req.WalletID.String(),
		ToAddress: req.ToAddress,
		Asset:     req.Asset,
		Amount:    req.Amount,
	})
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "withdrawal.whitelist_checked",
			WalletID:  &req.WalletID,
			Payload:   map[string]any{"id": wr.ID, "decision_id": decisionID(dec, err)},
		})
	}
	if err != nil || dec == nil || !dec.Approved {
		reason := "policy_error"
		decisionID := ""
		if dec != nil {
			if dec.Reason != "" {
				reason = dec.Reason
			}
			decisionID = dec.DecisionID
		}
		_ = s.Store.UpdateWithdrawalState(ctx, wr.ID, string(storage.WithdrawalStateFailed), reason, "", decisionID)
		wr.State = string(storage.WithdrawalStateFailed)
		wr.FailureReason = reason
		wr.PolicyDecisionID = decisionID
		return wr, nil
	}
	if err := s.Store.UpdateWithdrawalState(ctx, wr.ID, string(storage.WithdrawalStateWhitelisted), "", "", dec.DecisionID); err != nil {
		return nil, err
	}
	wr.State = string(storage.WithdrawalStateWhitelisted)
	wr.PolicyDecisionID = dec.DecisionID
	return wr, nil
}

// ConstructAndSign reserves a nonce/UTXOs, builds the unsigned tx, and calls
// MPC signing. On success the withdrawal moves to 'signed'.
func (s *Service) ConstructAndSign(ctx context.Context, id uuid.UUID) error {
	wr, err := s.Store.GetWithdrawal(ctx, id)
	if err != nil {
		return err
	}
	if wr.State != string(storage.WithdrawalStateWhitelisted) {
		return fmt.Errorf("withdrawal not in whitelisted state (is %s)", wr.State)
	}
	w, err := s.Store.GetWallet(ctx, wr.WalletID)
	if err != nil {
		return err
	}
	keyID := ""
	if s.KeyLookup != nil {
		k, err := s.KeyLookup.ResolveActiveKeyID(ctx, wr.WalletID)
		if err != nil {
			return fmt.Errorf("resolve key: %w", err)
		}
		keyID = k
	}
	var reservedOutpoints []string
	var reservedNonce int64 = -1

	if w.Chain.IsEVM() {
		n, err := s.Nonces.ReserveNonce(ctx, wr.WalletID, w.Chain)
		if err != nil {
			return fmt.Errorf("reserve nonce: %w", err)
		}
		reservedNonce = n
		_ = s.Store.UpdateWithdrawalNonce(ctx, id, n)
	} else if w.Chain == wallet.ChainBitcoin {
		amountInt, _ := strconv.ParseInt(wr.Amount, 10, 64)
		ops, _, err := s.UTXOs.SelectForAmount(ctx, wr.WalletID, amountInt)
		if err != nil {
			_ = s.Nonces.RollbackNonce(ctx, wr.WalletID, w.Chain, reservedNonce)
			return fmt.Errorf("select utxos: %w", err)
		}
		reservedOutpoints = ops
		if err := s.Store.UpdateWithdrawalOutpoints(ctx, id, ops); err != nil {
			log.Printf("withdrawal %s: persist reserved outpoints: %v", id, err)
			s.rollback(ctx, w, reservedNonce, reservedOutpoints)
			return fmt.Errorf("persist outpoints: %w", err)
		}
	}

	unsigned, err := BuildUnsignedTx(w, wr, reservedNonce, reservedOutpoints)
	if err != nil {
		s.rollback(ctx, w, reservedNonce, reservedOutpoints)
		return fmt.Errorf("build unsigned tx: %w", err)
	}
	resp, err := s.Signer.Sign(ctx, &grpcclient.SignRequest{
		KeyID: keyID, TxBytes: unsigned.Payload, WalletID: wr.WalletID,
	})
	if err != nil {
		s.rollback(ctx, w, reservedNonce, reservedOutpoints)
		_ = s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateFailed), "sign_failed", "", "")
		return fmt.Errorf("sign: %w", err)
	}
	signedBytes, err := unsigned.Assemble(resp.Signature)
	if err != nil {
		s.rollback(ctx, w, reservedNonce, reservedOutpoints)
		_ = s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateFailed), "assemble_failed", "", "")
		return fmt.Errorf("assemble signed tx: %w", err)
	}
	if err := s.Store.UpdateWithdrawalSignedTx(ctx, id, signedBytes); err != nil {
		log.Printf("withdrawal %s: persist signed tx: %v", id, err)
		s.rollback(ctx, w, reservedNonce, reservedOutpoints)
		return fmt.Errorf("persist signed tx: %w", err)
	}
	if err := s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateSigned), "", "", ""); err != nil {
		return err
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "withdrawal.signed",
			WalletID:  &wr.WalletID,
			Payload:   map[string]any{"id": id, "nonce": reservedNonce, "utxos": reservedOutpoints, "signed_bytes_len": len(signedBytes)},
		})
	}
	return nil
}

// Broadcast submits the signed tx to the Blockchain Gateway.
func (s *Service) Broadcast(ctx context.Context, id uuid.UUID) error {
	wr, err := s.Store.GetWithdrawal(ctx, id)
	if err != nil {
		return err
	}
	if wr.State != string(storage.WithdrawalStateSigned) {
		return fmt.Errorf("withdrawal not signed (is %s)", wr.State)
	}
	w, err := s.Store.GetWallet(ctx, wr.WalletID)
	if err != nil {
		return err
	}
	if len(wr.SignedTxBytes) == 0 {
		return fmt.Errorf("withdrawal %s has no signed tx bytes", id)
	}
	resp, err := s.Gateway.BroadcastTx(ctx, &grpcclient.BroadcastRequest{
		Chain: string(w.Chain), TxBytes: wr.SignedTxBytes, WalletID: wr.WalletID,
	})
	if err != nil {
		// rollback reserved resources
		s.rollback(ctx, w, noncePtr(wr), wr.ReservedOutpoints)
		_ = s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateFailed), "broadcast_failed", "", "")
		return fmt.Errorf("broadcast: %w", err)
	}
	if w.Chain == wallet.ChainBitcoin {
		// mark the reserved UTXOs as spent with the broadcast tx hash
		if len(wr.ReservedOutpoints) > 0 {
			_ = s.UTXOs.MarkSpent(ctx, wr.ReservedOutpoints, resp.TxHash)
		}
	}
	if w.Chain.IsEVM() {
		if wr.NonceValue != nil {
			_ = s.Nonces.CommitNonce(ctx, wr.WalletID, w.Chain, *wr.NonceValue)
		}
	}
	if err := s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateBroadcast), "", resp.TxHash, ""); err != nil {
		return err
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "withdrawal.broadcast",
			WalletID:  &wr.WalletID,
			Payload:   map[string]any{"id": id, "tx_hash": resp.TxHash},
		})
	}
	return nil
}

// Confirm advances a broadcast withdrawal to confirmed on a Blockchain Gateway
// confirmation event.
func (s *Service) Confirm(ctx context.Context, id uuid.UUID, txHash string) error {
	wr, err := s.Store.GetWithdrawal(ctx, id)
	if err != nil {
		return err
	}
	if wr.State != string(storage.WithdrawalStateBroadcast) {
		return fmt.Errorf("withdrawal not broadcast (is %s)", wr.State)
	}
	if err := s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateConfirmed), "", txHash, ""); err != nil {
		return err
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "withdrawal.confirmed",
			WalletID:  &wr.WalletID,
			Payload:   map[string]any{"id": id, "tx_hash": txHash},
		})
	}
	return nil
}

// Fail marks a withdrawal as failed and rolls back reserved resources.
func (s *Service) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	wr, err := s.Store.GetWithdrawal(ctx, id)
	if err != nil {
		return err
	}
	w, err := s.Store.GetWallet(ctx, wr.WalletID)
	if err != nil {
		return err
	}
	s.rollback(ctx, w, noncePtr(wr), nil)
	if err := s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateFailed), reason, "", ""); err != nil {
		return err
	}
	if s.Audit != nil {
		_ = s.Audit.Emit(ctx, &audit.Event{
			EventType: "withdrawal.failed",
			WalletID:  &wr.WalletID,
			Payload:   map[string]any{"id": id, "reason": reason},
		})
	}
	return nil
}

// OnReorg handles a reorg by rolling back the withdrawal to broadcast (or
// failed) and restoring UTXOs.
func (s *Service) OnReorg(ctx context.Context, id uuid.UUID, outpoints []string) error {
	wr, err := s.Store.GetWithdrawal(ctx, id)
	if err != nil {
		return err
	}
	if err := s.UTXOs.RestoreOnReorg(ctx, outpoints); err != nil {
		return err
	}
	// demote confirmed->broadcast so the withdrawal can be re-broadcast
	if wr.State == string(storage.WithdrawalStateConfirmed) {
		return s.Store.UpdateWithdrawalState(ctx, id, string(storage.WithdrawalStateBroadcast), "reorg", "", "")
	}
	return nil
}

func (s *Service) rollback(ctx context.Context, w *wallet.Wallet, n int64, ops []string) {
	if n >= 0 && w.Chain.IsEVM() {
		_ = s.Nonces.RollbackNonce(ctx, w.ID, w.Chain, n)
	}
	if len(ops) > 0 {
		_ = s.UTXOs.Unlock(ctx, ops)
	}
}

func noncePtr(wr *storage.WithdrawalRequest) int64 {
	if wr.NonceValue == nil {
		return -1
	}
	return *wr.NonceValue
}

func decisionID(dec *policy.CheckResponse, err error) string {
	if err != nil {
		return "error"
	}
	if dec == nil {
		return "nil"
	}
	return dec.DecisionID
}
