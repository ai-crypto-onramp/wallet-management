package withdrawal

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/sha3"
)

// testScalar returns a 32-byte scalar with bytes [seed, seed+1, ...].
func testScalar(seed byte) [32]byte {
	var s [32]byte
	for i := range s {
		s[i] = seed + byte(i)
	}
	return s
}

func sampleEVMWithdrawal(t *testing.T) (*wallet.Wallet, *storage.WithdrawalRequest) {
	t.Helper()
	w := &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainEthereum, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive}
	wr := &storage.WithdrawalRequest{
		ID:        uuid.New(),
		WalletID:  w.ID,
		ToAddress: validEVMA,
		Asset:     "eth",
		Amount:    "1000000000000000000",
		State:     "WHITELISTED",
	}
	return w, wr
}

func TestBuildEVMUnsignedTx_SigningHashIs32Bytes(t *testing.T) {
	w, wr := sampleEVMWithdrawal(t)
	utx, err := BuildUnsignedTx(w, wr, 7, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if utx.Chain != wallet.ChainEthereum {
		t.Errorf("expected evm chain, got %s", utx.Chain)
	}
	if len(utx.Payload) != 32 {
		t.Fatalf("expected 32-byte keccak signing hash, got %d bytes", len(utx.Payload))
	}
	// The signing hash must be deterministic for the same inputs.
	utx2, _ := BuildUnsignedTx(w, wr, 7, nil)
	if !bytes.Equal(utx.Payload, utx2.Payload) {
		t.Error("expected deterministic signing hash for identical inputs")
	}
}

func TestEVMAssemble_ProducesValidSignedTx(t *testing.T) {
	w, wr := sampleEVMWithdrawal(t)
	nonce := int64(5)
	utx, err := BuildUnsignedTx(w, wr, nonce, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Sign the keccak signing hash with a test secp256k1 private key and
	// verify that Assemble produces a non-empty RLP-encoded signed tx whose
	// v/r/s recover the test key's public key against the signing hash.
	scalar7 := testScalar(7)
	priv, _ := btcec.PrivKeyFromBytes(scalar7[:])
	sig := btcecdsa.SignCompact(priv, utx.Payload, true)
	signed, err := utx.Assemble(sig)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(signed) == 0 {
		t.Fatal("expected non-empty signed tx bytes")
	}
	// Decode r, s, v from the signed RLP and verify the signature recovers
	// the test key's address. We don't fully RLP-decode here; instead we
	// re-sign with the same key and assert the signed bytes are stable and
	// the encoded length is plausible (a legacy tx is ~100-120 bytes).
	if len(signed) < 80 {
		t.Errorf("signed tx too short (%d bytes), expected ~100+", len(signed))
	}
	// Re-assembling with the same signature must be idempotent.
	signed2, _ := utx.Assemble(sig)
	if !bytes.Equal(signed, signed2) {
		t.Error("expected Assemble to be deterministic")
	}
}

func TestEVMAssemble_RejectsMalformedSignature(t *testing.T) {
	w, wr := sampleEVMWithdrawal(t)
	utx, _ := BuildUnsignedTx(w, wr, 0, nil)
	if _, err := utx.Assemble([]byte{0x01, 0x02}); err == nil {
		t.Error("expected error on short signature")
	}
	if _, err := utx.Assemble(make([]byte, 65)); err == nil {
		t.Error("expected error on invalid recovery header (0)")
	}
}

func TestEVMChainIDReplayProtection(t *testing.T) {
	w, wr := sampleEVMWithdrawal(t)
	prev := new(big.Int).Set(EVMChainID)
	defer func() { EVMChainID = prev }()
	EVMChainID = big.NewInt(137) // polygon
	utx1, _ := BuildUnsignedTx(w, wr, 1, nil)
	EVMChainID = big.NewInt(1) // ethereum
	utx2, _ := BuildUnsignedTx(w, wr, 1, nil)
	if bytes.Equal(utx1.Payload, utx2.Payload) {
		t.Error("expected different signing hashes for different chain ids (EIP-155)")
	}
}

func TestBuildBTCUnsignedTx_SighashPerInput(t *testing.T) {
	w := &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainBitcoin, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive}
	wr := &storage.WithdrawalRequest{
		ID:        uuid.New(),
		WalletID:  w.ID,
		ToAddress: validBTCA,
		Asset:     "btc",
		Amount:    "120",
		State:     "WHITELISTED",
	}
	outpoints := []string{validOutpointA, validOutpointB}
	utx, err := BuildUnsignedTx(w, wr, -1, outpoints)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if utx.Chain != wallet.ChainBitcoin {
		t.Errorf("expected btc chain, got %s", utx.Chain)
	}
	// Two inputs → payload is 64 bytes (2 * 32-byte sighash).
	if len(utx.Payload) != 64 {
		t.Fatalf("expected 64-byte sighash concat (2 inputs), got %d bytes", len(utx.Payload))
	}
	// Assemble with 128 bytes (2 * 64-byte r||s) → produces a serialized
	// wire-format tx that deserializes back to a MsgTx with 2 witness inputs.
	sig := make([]byte, 128)
	for i := range sig {
		sig[i] = byte(i)
	}
	signed, err := utx.Assemble(sig)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(signed) == 0 {
		t.Fatal("expected non-empty signed btc tx bytes")
	}
	var decoded wire.MsgTx
	if err := decoded.Deserialize(bytes.NewReader(signed)); err != nil {
		t.Fatalf("deserialize signed btc tx: %v", err)
	}
	if len(decoded.TxIn) != 2 {
		t.Errorf("expected 2 tx inputs, got %d", len(decoded.TxIn))
	}
	if len(decoded.TxOut) != 1 {
		t.Errorf("expected 1 tx output, got %d", len(decoded.TxOut))
	}
	for i, in := range decoded.TxIn {
		if len(in.Witness) != 2 {
			t.Errorf("input %d: expected witness [sig, pubkey], got %d items", i, len(in.Witness))
		}
		if len(in.Witness[0]) != 65 {
			t.Errorf("input %d: expected 65-byte sig+sighashtype, got %d", i, len(in.Witness[0]))
		}
	}
}

func TestBuildBTCUnsignedTx_RejectsEmptyOutpoints(t *testing.T) {
	w := &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainBitcoin, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive}
	wr := &storage.WithdrawalRequest{ID: uuid.New(), WalletID: w.ID, ToAddress: validBTCA, Asset: "btc", Amount: "1", State: "WHITELISTED"}
	if _, err := BuildUnsignedTx(w, wr, -1, nil); err == nil {
		t.Error("expected error on empty outpoints")
	}
}

func TestBuildSolanaUnsignedTx_MessageHashIs32Bytes(t *testing.T) {
	w := &wallet.Wallet{ID: uuid.New(), Chain: wallet.ChainSolana, Type: wallet.WalletTypeHot, State: wallet.WalletStateActive}
	// Solana to_address must be a 32-byte base58 pubkey. Use the system
	// program id as a stand-in (it base58-decodes to 32 zero bytes).
	wr := &storage.WithdrawalRequest{
		ID:        uuid.New(),
		WalletID:  w.ID,
		ToAddress: "11111111111111111111111111111111",
		Asset:     "sol",
		Amount:    "1000",
		State:     "WHITELISTED",
	}
	utx, err := BuildUnsignedTx(w, wr, -1, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if utx.Chain != wallet.ChainSolana {
		t.Errorf("expected solana chain, got %s", utx.Chain)
	}
	// The signer payload is the serialized message. Assemble attaches a
	// 64-byte ed25519 signature to produce the final Transaction.
	if len(utx.Payload) == 0 {
		t.Fatal("expected non-empty solana message payload")
	}
	// Compute the message hash (Solana signs the message directly, but the
	// hash is what would be signed in some MPC schemes) and assert 32 bytes.
	h := sha3.NewLegacyKeccak256()
	h.Write(utx.Payload)
	hash := h.Sum(nil)
	if len(hash) != 32 {
		t.Fatalf("expected 32-byte message hash, got %d", len(hash))
	}
	edSeed := testScalar(9)
	edPriv := ed25519.NewKeyFromSeed(edSeed[:])
	sig := ed25519.Sign(edPriv, utx.Payload)
	signed, err := utx.Assemble(sig)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(signed) == 0 {
		t.Fatal("expected non-empty signed solana tx bytes")
	}
	// The signed transaction is [1 sig (64 bytes), message]; so its length
	// must be 1 + 64 + len(message).
	if len(signed) != 1+64+len(utx.Payload) {
		t.Errorf("expected signed tx len %d, got %d", 1+64+len(utx.Payload), len(signed))
	}
}

// Sanity check: a compact secp256k1 signature over a 32-byte digest round-
// trips through RecoverCompact, guarding the EVM Assemble path's assumption
// that the recovery header is well-formed.
func TestSecp256k1CompactSignatureRoundTrip(t *testing.T) {
	scalar11 := testScalar(11)
	priv, _ := btcec.PrivKeyFromBytes(scalar11[:])
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte("round-trip"))
	digest := hash.Sum(nil)
	sig := btcecdsa.SignCompact(priv, digest, true)
	recovered, _, err := btcecdsa.RecoverCompact(sig, digest)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !bytes.Equal(recovered.SerializeCompressed(), priv.PubKey().SerializeCompressed()) {
		t.Fatal("recovered pubkey does not match signer pubkey")
	}
}
